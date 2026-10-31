// Package discovery serves the auto-discovery module.
//
// IT IS ITS OWN HANDLER PACKAGE, unlike the data centre and address modules,
// because it shares neither the instance use case nor the instance row shape:
// a rule is not an instance, an account is not an instance, and a catalogue
// entry is not a row anywhere. Mounting it from the instance handler would have
// made that handler hold two use cases and a constructor with two parameters —
// a change to an existing signature rather than an addition.
package discovery

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// Handler serves the module's routes.
type Handler struct {
	uc *discoverybiz.Usecase
}

// NewHandler builds the handler.
func NewHandler(uc *discoverybiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the auto-discovery routes.
//
// Routes:
//
//	GET    /v1/cmdb/discovery/rules                          (any authed)
//	POST   /v1/cmdb/discovery/rules                          (any authed) — create
//	GET    /v1/cmdb/discovery/rules/{ref}                    (any authed)
//	PUT    /v1/cmdb/discovery/rules/{ref}                    (any authed)
//	DELETE /v1/cmdb/discovery/rules/{ref}                    (any authed)
//	GET    /v1/cmdb/discovery/rules/{ref}/accounts           (any authed)
//	PUT    /v1/cmdb/discovery/rules/{ref}/accounts           (any authed) — replace the set
//	GET    /v1/cmdb/discovery/accounts/{id}                  (any authed)
//	DELETE /v1/cmdb/discovery/accounts/{id}                  (any authed)
//	POST   /v1/cmdb/discovery/templates/import               (admin) — a template file
//	GET    /v1/cmdb/discovery/templates/export               (any authed)
//	GET    /v1/cmdb/discovery/target-types                   (any authed) — models with targets
//	GET    /v1/cmdb/discovery/providers                      (any authed)
//	GET    /v1/cmdb/discovery/catalog/http/{provider}/categories   (any authed)
//	GET    /v1/cmdb/discovery/catalog/http/{provider}/resources    (any authed)
//	GET    /v1/cmdb/discovery/catalog/http/{provider}/attributes   (any authed)
//	GET    /v1/cmdb/discovery/catalog/http/{provider}/mapping      (any authed)
//	GET    /v1/cmdb/discovery/catalog/snmp/attributes              (any authed)
//	GET    /v1/cmdb/discovery/catalog/components/{name}/attributes (any authed)
//	GET    /v1/cmdb/discovery/adc                            (any authed) — the pool; see http_adc.go
//
// THE TEMPLATE IMPORT IS ADMIN-GATED because it writes provider definitions
// across the deployment at once — it is the one route here whose effect is not
// scoped to what the caller made.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/cmdb/discovery/rules", h.listRules)
	r.Post("/v1/cmdb/discovery/rules", h.createRule)
	r.Get("/v1/cmdb/discovery/rules/{ref}", h.getRule)
	r.Put("/v1/cmdb/discovery/rules/{ref}", h.updateRule)
	r.Delete("/v1/cmdb/discovery/rules/{ref}", h.deleteRule)

	r.Get("/v1/cmdb/discovery/rules/{ref}/accounts", h.listAccounts)
	r.Put("/v1/cmdb/discovery/rules/{ref}/accounts", h.replaceAccounts)
	r.Get("/v1/cmdb/discovery/accounts/{id}", h.getAccount)
	r.Delete("/v1/cmdb/discovery/accounts/{id}", h.deleteAccount)

	r.Post("/v1/cmdb/discovery/templates/import", h.importTemplates)
	r.Get("/v1/cmdb/discovery/templates/export", h.exportTemplates)

	r.Get("/v1/cmdb/discovery/target-types", h.listTargetTypes)
	r.Get("/v1/cmdb/discovery/providers", h.providers)
	r.Get("/v1/cmdb/discovery/catalog/http/{provider}/categories", h.httpCategories)
	r.Get("/v1/cmdb/discovery/catalog/http/{provider}/resources", h.httpResources)
	r.Get("/v1/cmdb/discovery/catalog/http/{provider}/attributes", h.httpAttributes)
	r.Get("/v1/cmdb/discovery/catalog/http/{provider}/mapping", h.httpMapping)
	r.Get("/v1/cmdb/discovery/catalog/snmp/attributes", h.snmpAttributes)
	r.Get("/v1/cmdb/discovery/catalog/components/{name}/attributes", h.componentAttributes)

	h.registerTargets(r)
	h.registerADC(r)
}

// pageParams reads the paging query parameters the other CMDB list endpoints
// take, so a console page written against one of them works against this.
func pageParams(r *http.Request) (page, size int) {
	query := r.URL.Query()
	page = 1
	if raw := query.Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	if raw := query.Get("page_size"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			size = n
		}
	}
	return page, size
}

const maxBody = 1 << 20

// --- rules ---

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) getRule(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.GetRule(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRuleInput(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.CreateRule(r.Context(), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRuleInput(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.UpdateRule(r.Context(), chi.URLParam(r, "ref"), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteRule(r.Context(), chi.URLParam(r, "ref"), actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ruleBody is a rule's write body.
//
// THE OPTIONALS ARE POINTERS so a caller's omission is distinguishable from an
// empty value: a rename that does not mention `option` leaves the icon alone,
// and one that sends `{}` clears it.
type ruleBody struct {
	Name string `json:"name"`

	Type         *string `json:"type"`
	IsPlugin     *bool   `json:"is_plugin"`
	PluginScript *string `json:"plugin_script"`
	UniqueKey    *string `json:"unique_key"`

	// Attributes is the field list, for a plugin rule. It is a LIST, which is
	// what the catalogue uses; cantor's column held a dict, and the reader
	// understands both.
	Attributes []discoverybiz.AttributeField `json:"attributes"`

	Option *map[string]any `json:"option"`
}

func decodeRuleInput(r *http.Request) (discoverybiz.RuleInput, error) {
	var body ruleBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return discoverybiz.RuleInput{}, errs.ErrInvalid
		}
		return discoverybiz.RuleInput{}, errs.ErrInvalid
	}
	in := discoverybiz.RuleInput{
		Name: body.Name, Type: body.Type, IsPlugin: body.IsPlugin,
		PluginScript: body.PluginScript, UniqueKey: body.UniqueKey,
		Attributes: body.Attributes,
	}
	if body.Option != nil {
		in.Option, in.SetOption = *body.Option, true
	}
	return in, nil
}

// --- accounts ---

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListAccounts(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetAccount(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteAccount(r.Context(), id, actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// accountListBody is the replace body: the rule's whole account set.
type accountListBody struct {
	Accounts []struct {
		ID     model.ID       `json:"id"`
		Name   string         `json:"name"`
		Config map[string]any `json:"config"`
	} `json:"accounts"`
}

func (h *Handler) replaceAccounts(w http.ResponseWriter, r *http.Request) {
	var body accountListBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	in := make([]discoverybiz.AccountInput, 0, len(body.Accounts))
	for _, item := range body.Accounts {
		in = append(in, discoverybiz.AccountInput{
			ID: item.ID, Name: item.Name, Config: item.Config,
		})
	}
	got, err := h.uc.UpsertAccounts(r.Context(), chi.URLParam(r, "ref"), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- templates ---

// templateBody is a template file: a list of rules.
type templateBody struct {
	Rules []ruleBody `json:"rules"`
}

func (h *Handler) importTemplates(w http.ResponseWriter, r *http.Request) {
	var body templateBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	in := make([]discoverybiz.RuleInput, 0, len(body.Rules))
	for _, rule := range body.Rules {
		item := discoverybiz.RuleInput{
			Name: rule.Name, Type: rule.Type, IsPlugin: rule.IsPlugin,
			PluginScript: rule.PluginScript, UniqueKey: rule.UniqueKey,
			Attributes: rule.Attributes,
		}
		if rule.Option != nil {
			item.Option, item.SetOption = *rule.Option, true
		}
		in = append(in, item)
	}
	got, err := h.uc.ImportTemplates(r.Context(), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// exportTemplates returns every rule in the import's shape.
//
// THE SAME SHAPE, so an export is an import: a template file is how a
// deployment moves its provider definitions between installations, and two
// shapes would make the round trip need a converter nobody maintains.
func (h *Handler) exportTemplates(w http.ResponseWriter, r *http.Request) {
	rules, err := h.uc.ListRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		entry := map[string]any{"name": rule.Name}
		if rule.Type != "" {
			entry["type"] = rule.Type
		}
		entry["is_inner"] = rule.IsInner
		entry["is_plugin"] = rule.IsPlugin
		if rule.UniqueKey != "" {
			entry["unique_key"] = rule.UniqueKey
		}
		if len(rule.Attributes) > 0 {
			entry["attributes"] = rule.Attributes
		}
		if len(rule.Option) > 0 {
			entry["option"] = rule.Option
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

// --- the catalogue ---

func (h *Handler) providers(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.Providers()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) httpCategories(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.HTTPCategories(chi.URLParam(r, "provider"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) httpResources(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.HTTPResources(chi.URLParam(r, "provider"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) httpAttributes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.HTTPAttributes(chi.URLParam(r, "provider"), r.URL.Query().Get("resource"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) httpMapping(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.HTTPMapping(chi.URLParam(r, "provider"), r.URL.Query().Get("resource"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) snmpAttributes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.SNMPAttributes()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) componentAttributes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ComponentAttributes(chi.URLParam(r, "name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- plumbing ---

// actorOf reads the caller identity. It is never taken from the request body.
func actorOf(r *http.Request) (discoverybiz.Actor, error) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		return discoverybiz.Actor{}, errs.ErrUnauthorized
	}
	if int64(t.UserID) > int64(maxModelID) {
		return discoverybiz.Actor{}, errs.ErrInvalid
	}
	return discoverybiz.Actor{
		UserID:  model.ID(t.UserID),
		Email:   t.Email,
		IsAdmin: t.IsSuperuser || t.Role == "admin",
	}, nil
}

// maxModelID is the largest value a CMDB id column holds — the signed 32-bit
// ceiling, because cantor declares them int(11).
const maxModelID = 2147483647

func pathID(r *http.Request, name string) (model.ID, error) {
	n, err := strconv.ParseInt(chi.URLParam(r, name), 10, 32)
	if err != nil || n <= 0 {
		return 0, errs.ErrInvalid
	}
	return model.ID(n), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeErr reports a failure as plain text with the status the error maps to —
// the shape every other CMDB handler uses.
func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.WriteHeader(errs.HTTPStatus(err))
	_, _ = w.Write([]byte(strings.TrimSpace(err.Error())))
}
