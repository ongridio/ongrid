// Package citype builds the HTTP routes for CMDB model configuration.
package citype

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// maxIDList bounds the `ids` query parameter. The parameter exists to fetch a
// handful of known models, so a large list indicates a caller bug rather than
// a legitimate request, and an unbounded IN clause would be a cheap way to
// make the database do work.
const maxIDList = 200

// roleAdmin mirrors iam/model.RoleAdmin without crossing the BC boundary
// (arch-lint forbids cmdb -> iam imports). Same constant and rationale as
// internal/manager/server/device.
const roleAdmin = "admin"

// Handler exposes the model-configuration routes under /v1/cmdb.
type Handler struct {
	uc *citypebiz.Usecase
}

// NewHandler builds the handler around the CI-type usecase.
func NewHandler(uc *citypebiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the model-configuration read routes on r.
//
// Routes:
//
//	GET /v1/cmdb/ci-types            (any authed) — list, optional ?name= and ?ids=
//	GET /v1/cmdb/ci-types/icons      (any authed) — id -> icon map
//	GET /v1/cmdb/ci-types/{ref}      (any authed) — one model, by numeric id or exact name
//
// Every route here is a READ. cantor gated writes on the `app admin` role and
// reads on nothing beyond authentication, and the port keeps that split: the
// model definitions are the vocabulary the rest of CMDB is built from, so
// hiding them from ordinary users would make the product unusable, while
// changing them is an administrative act.
//
// `/ci-types/icons` coexists with `/ci-types/{ref}`. chi gives the static
// segment priority over the parameter regardless of registration order
// (verified, not assumed), so `icons` is reachable and a CI type literally
// named "icons" would not be addressable by name — it remains reachable by id.
// The ordering below is for readability only.
func (h *Handler) Register(r chi.Router) {
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/builtin-models", h.ensureBuiltinModels)
	r.Get("/v1/cmdb/ci-types", h.list)
	r.Get("/v1/cmdb/ci-types/icons", h.icons)
	r.Get("/v1/cmdb/ci-types/{ref}", h.get)
	h.registerWrites(r)
	// Attribute management is its own sub-tree: the attribute dictionary is a
	// shared namespace, not a property of any one model, while the bindings and
	// form sections belong to a model and are addressed through it.
	h.registerAttributeRoutes(r)
	h.registerAttributeGroupRoutes(r)
	// Unique constraints and triggers are the other two per-model rule
	// families. Unlike the attribute routes, their reads are admin-gated too:
	// a trigger names the endpoints the product calls and the filters it
	// matches on, which is deployment configuration rather than something a
	// reader of the model needs.
	h.registerRuleRoutes(r)
	// Model relations are the schema the service tree and the resource
	// hierarchy are built on: an edge says which models may be connected, by
	// which relation type, with what cardinality.
	h.registerRelationRoutes(r)
	// Per-user preferences and the console's model groupings. The preferences
	// are deliberately NOT admin-gated — a subscription is one person's view of
	// the inventory — while the groupings are, because everyone sees them.
	h.registerPreferenceRoutes(r)
	h.registerLevelViews(r)

	// Service-tree definitions and saved searches. The definitions are also
	// read by the resource menu, which is why they are written here and
	// consumed there.
	h.registerServiceTreeRoutes(r)
}

// citTypeDTO is the wire shape of one CI model.
//
// It carries the derived fields cantor's console relied on (unique_key,
// show_name, parent_ids) so the SPA does not have to resolve attribute ids
// itself. `unique_key` is what cantor called the field in its LIST path; its
// detail path inconsistently called it `unique_name` for the same value. The
// port picks one name — `unique_key` — and uses it in both, because shipping
// two names for one field is how a frontend ends up branching on which
// endpoint it called.
type citTypeDTO struct {
	ID         model.ID `json:"id"`
	Name       string   `json:"name"`
	Alias      string   `json:"alias"`
	Enabled    bool     `json:"enabled"`
	IsAttached bool     `json:"is_attached"`

	// Icon is opaque user data: cantor stores an iconfont literal here (e.g.
	// "caise-jiaohuanji$$") and the SPA resolves it. Never reinterpreted.
	Icon string `json:"icon,omitempty"`

	// Order carries the column's own width rather than a widened int, so the
	// wire type cannot disagree with what is storable.
	Order            model.SmallInt `json:"order"`
	DefaultOrderAttr string         `json:"default_order_attr,omitempty"`
	UniqueKey        string         `json:"unique_key,omitempty"`
	ShowName         string         `json:"show_name,omitempty"`
	ParentIDs        []model.ID     `json:"parent_ids"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type listResponse struct {
	Items []citTypeDTO `json:"items"`
	Total int          `json:"total"`
}

func toDTO(v citypebiz.View) citTypeDTO {
	ct := v.Type
	icon := ""
	if ct.Icon != nil {
		icon = *ct.Icon
	}
	orderAttr := ""
	if ct.DefaultOrderAttr != nil {
		orderAttr = *ct.DefaultOrderAttr
	}
	parents := v.ParentIDs
	if parents == nil {
		// A JSON null here would make the SPA special-case the empty list.
		parents = []model.ID{}
	}
	dto := citTypeDTO{
		ID: ct.ID, Name: ct.Name, Alias: ct.Alias,
		Enabled: ct.Enabled, IsAttached: ct.IsAttached,
		Icon: icon, Order: ct.Order, DefaultOrderAttr: orderAttr,
		UniqueKey: v.UniqueKey, ShowName: v.ShowName, ParentIDs: parents,
	}
	if !ct.CreatedAt.IsZero() {
		dto.CreatedAt = ct.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !ct.UpdatedAt.IsZero() {
		dto.UpdatedAt = ct.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	views, err := h.uc.List(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]citTypeDTO, 0, len(views))
	for _, v := range views {
		items = append(items, toDTO(v))
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, Total: len(items)})
}

func (h *Handler) icons(w http.ResponseWriter, r *http.Request) {
	icons, err := h.uc.Icons(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	// Keys are stringified ids because JSON object keys are strings; emitting
	// numbers would be impossible and emitting a list would lose the lookup.
	out := make(map[string]string, len(icons))
	for id, icon := range icons {
		out[strconv.FormatInt(int64(id), 10)] = icon
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeErr(w, errs.ErrInvalid)
		return
	}

	// cantor exposed /ci_types/{type_id:int} and /ci_types/{type_name} as two
	// routes over one handler; the port keeps the dual addressing but resolves
	// it here, so the route table stays a single entry.
	if id, err := strconv.ParseInt(ref, 10, 32); err == nil {
		if id <= 0 {
			writeErr(w, errs.ErrInvalid)
			return
		}
		view, err := h.uc.Get(r.Context(), model.ID(id))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toDTO(*view))
		return
	}

	view, err := h.uc.GetByName(r.Context(), ref)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(*view))
}

// parseListFilter reads ?name= and ?ids= from the query string.
func parseListFilter(r *http.Request) (citypebiz.ListFilter, error) {
	q := r.URL.Query()
	var filter citypebiz.ListFilter
	filter.NameLike = strings.TrimSpace(q.Get("name"))

	raw := strings.TrimSpace(q.Get("ids"))
	if raw == "" {
		return filter, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxIDList {
		return citypebiz.ListFilter{}, errs.ErrInvalid
	}
	ids := make([]model.ID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil || n <= 0 {
			return citypebiz.ListFilter{}, errs.ErrInvalid
		}
		ids = append(ids, model.ID(n))
	}
	filter.IDs = ids
	return filter, nil
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	writeJSON(w, errs.HTTPStatus(err), errorBody{Error: err.Error(), Code: errCode(err)})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not_found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not_wired_yet"
	}
	return "internal"
}

// ensureBuiltinModels creates the models a built-in feature addresses by name.
//
// IT IS A SEPARATE ROUTE RATHER THAN A STARTUP STEP. Creating CI models during
// boot would put rows in a database from a path nobody chose and nobody can
// see, and the project's rule for schema work is that an operator runs it and
// can see what it did. The report says which models and attributes were created
// and which were already there, because a model that exists with the wrong
// attributes is the case worth looking at.
//
// IT NEVER MODIFIES ANYTHING. A model that exists is left exactly as it is,
// however it is configured; only models and dictionary entries that are ABSENT
// are written.
//
// ADMIN-GATED, like the model writes around it: this writes schema, and it
// writes it for the whole deployment rather than for the caller.
func (h *Handler) ensureBuiltinModels(w http.ResponseWriter, r *http.Request) {
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.EnsureBuiltinModels(r.Context(), citypebiz.BuiltinEnsureInput{
		Models: cibiz.DCIMBuiltinModels(),
		Actor:  actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
