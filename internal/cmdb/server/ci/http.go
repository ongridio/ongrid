// Package ci builds the HTTP routes for CMDB instances — the 资源数据 pages: a
// model's instances, their attribute values, and the writes that change them.
package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// maxWriteBody bounds a write. An instance's attributes are a small document;
// a larger one is a caller bug or an attempt to make the server allocate.
const maxWriteBody = 256 << 10

// maxImportBody bounds a sheet. It is far larger than a write because one
// request carries every row, and it is still bounded because it is not larger:
// the row count has its own ceiling in the biz layer, and a request that
// exceeds this one is refused rather than truncated.
const maxImportBody = 16 << 20

// Handler exposes the instance routes under /v1/cmdb.
type Handler struct {
	uc *cibiz.Usecase
}

// NewHandler builds the handler.
func NewHandler(uc *cibiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the instance routes.
//
// Routes:
//
//	GET    /v1/cmdb/ci-types/{ref}/instances   (any authed) — a page of instances
//	GET    /v1/cmdb/instances/{id}             (any authed) — one instance
//	POST   /v1/cmdb/instances                  (any authed) — create
//	PUT    /v1/cmdb/instances                  (any authed) — create-or-replace
//	PUT    /v1/cmdb/instances/{id}             (any authed) — update
//	DELETE /v1/cmdb/instances/{id}             (any authed) — delete
//
// THE WRITES ARE NOT ADMIN-GATED, unlike the model-configuration ones, and the
// distinction is the product's: editing a MODEL changes the vocabulary everyone
// shares, while adding a HOST is the ordinary work the product exists for. cantor
// gated these on the caller's ADD/UPDATE/DELETE permission for the model, which
// is what the port will do when resource-level authorization lands — the hook is
// where the model is resolved.
func (h *Handler) Register(r chi.Router) {
	h.registerSearch(r)
	h.registerRelations(r)
	h.registerDCIM(r)
	h.registerIPAM(r)
	h.registerImport(r)
	r.Get("/v1/cmdb/ci-types/{ref}/instances", h.listInstances)
	r.Get("/v1/cmdb/instances/{id}", h.getInstance)
	r.Post("/v1/cmdb/instances", h.createInstance)
	r.Put("/v1/cmdb/instances", h.replaceInstance)
	r.Put("/v1/cmdb/instances/{id}", h.updateInstance)
	r.Delete("/v1/cmdb/instances/{id}", h.deleteInstance)
}

// instanceRequest is the write body.
//
// It is FLAT, like cantor's: the attribute values are the bulk of the payload
// and nesting them under a `values` key would make the common case the awkward
// one. The reserved keys are peeled off first and everything else is an
// attribute reference.
type instanceRequest struct {
	TypeRef string `json:"ci_type"`

	ExistPolicy       string `json:"exist_policy"`
	NoAttributePolicy string `json:"no_attribute_policy"`

	TicketID string `json:"ticket_id"`
	Origin   string `json:"origin"`
	Reason   string `json:"reason"`
}

// reservedKeys are the request fields that are NOT attribute values.
var reservedKeys = map[string]bool{
	"ci_type": true, "exist_policy": true, "no_attribute_policy": true,
	"ticket_id": true, "origin": true, "reason": true,
	"fields": true, "ret_key": true, "page": true, "count": true,
}

// decodeWrite reads a write body and splits it into the control fields and the
// attribute values.
//
// It decodes into a map rather than a struct because the attribute keys are
// only known once the model is resolved, and decoding twice would be the
// cheapest way to get this subtly wrong: a field that looks reserved in the
// first pass and not the second.
func decodeWrite(r *http.Request) (*instanceRequest, map[string]any, error) {
	var raw map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, nil, errs.ErrInvalid
	}

	req := &instanceRequest{}
	if v, ok := raw["ci_type"].(string); ok {
		req.TypeRef = strings.TrimSpace(v)
	}
	for key, dst := range map[string]*string{
		"exist_policy": &req.ExistPolicy, "no_attribute_policy": &req.NoAttributePolicy,
		"ticket_id": &req.TicketID, "origin": &req.Origin, "reason": &req.Reason,
	} {
		if v, ok := raw[key].(string); ok {
			*dst = strings.TrimSpace(v)
		}
	}

	values := make(map[string]any, len(raw))
	for k, v := range raw {
		// A leading underscore is cantor's convention for a field the CLIENT
		// does not own; the port keeps it, so a caller cannot smuggle a
		// server-side control through the attribute namespace.
		if reservedKeys[k] || strings.HasPrefix(k, "_") {
			continue
		}
		values[k] = normaliseJSONValue(v)
	}
	return req, values, nil
}

// normaliseJSONValue converts a decoded JSON number to a type the value layer
// accepts.
//
// The decoder is told to keep numbers exact so a 64-bit id survives; the value
// layer's own coercion handles json.Number. Strings and containers pass through
// untouched.
func normaliseJSONValue(v any) any {
	if n, ok := v.(json.Number); ok {
		return n
	}
	return v
}

// actor pulls the caller identity and the audit fields.
func actor(r *http.Request, req *instanceRequest) (cibiz.AuditInfo, error) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		return cibiz.AuditInfo{}, errs.ErrUnauthorized
	}
	if int64(t.UserID) > int64(maxModelID) {
		return cibiz.AuditInfo{}, fmt.Errorf("%w: user id does not fit the CMDB id range", errs.ErrInvalid)
	}
	origin := req.Origin
	if origin == "" {
		origin = "ui"
	}
	return cibiz.AuditInfo{
		Actor: model.ID(t.UserID), Origin: origin,
		TicketID: req.TicketID, Reason: req.Reason,
	}, nil
}

// maxModelID is the largest value a CMDB id column holds: cantor declares them
// int(11), so a user id that does not fit is reported rather than truncated.
const maxModelID = int32(2147483647)

func (h *Handler) createInstance(w http.ResponseWriter, r *http.Request) {
	req, values, err := decodeWrite(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	policy := cibiz.ExistPolicy(req.ExistPolicy)
	if policy == "" {
		// cantor's create default. A create that silently overwrote an existing
		// instance would be the wrong default for the one endpoint whose whole
		// job is to make a new thing.
		policy = cibiz.ExistReject
	}

	res, err := h.uc.Create(r.Context(), cibiz.WriteRequest{
		TypeRef: req.TypeRef, Values: values,
		ExistPolicy:       policy,
		NoAttributePolicy: cibiz.ExistPolicy(req.NoAttributePolicy),
		Audit:             audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, writeResponse(res))
}

// replaceInstance is the create-or-update form, which the console's save button
// uses: it does not know whether the record is new.
func (h *Handler) replaceInstance(w http.ResponseWriter, r *http.Request) {
	req, values, err := decodeWrite(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	policy := cibiz.ExistPolicy(req.ExistPolicy)
	if policy == "" {
		policy = cibiz.ExistReplace
	}

	res, err := h.uc.Create(r.Context(), cibiz.WriteRequest{
		TypeRef: req.TypeRef, Values: values,
		ExistPolicy:       policy,
		NoAttributePolicy: cibiz.ExistPolicy(req.NoAttributePolicy),
		Audit:             audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if !res.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, writeResponse(res))
}

func (h *Handler) updateInstance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	req, values, err := decodeWrite(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.uc.Update(r.Context(), id, cibiz.WriteRequest{
		TypeRef: req.TypeRef, Values: values,
		NoAttributePolicy: cibiz.ExistPolicy(req.NoAttributePolicy),
		Audit:             audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, writeResponse(res))
}

func writeResponse(res *cibiz.WriteResult) map[string]any {
	body := map[string]any{"ci_id": res.CIID, "created": res.Created}
	if len(res.Changes) > 0 {
		changes := make([]map[string]any, 0, len(res.Changes))
		for _, chg := range res.Changes {
			item := map[string]any{"attr_id": chg.AttrID}
			if chg.Old != nil {
				item["old"] = chg.Old
			}
			if chg.New != nil {
				item["new"] = chg.New
			}
			changes = append(changes, item)
		}
		body["changes"] = changes
	}
	return body
}

func (h *Handler) deleteInstance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- reads ---

// instanceDTO is one instance on the wire.
//
// `values` is keyed according to the request's ret_key, and holds no PASSWORD,
// LINK or FILE attribute — see Usecase.renderInstance, which is the single
// place that decision is made.
type instanceDTO struct {
	ID        model.ID       `json:"ci_id"`
	TypeID    model.ID       `json:"type_id"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	Values    map[string]any `json:"values"`
}

func toInstanceDTO(in cibiz.Instance) instanceDTO {
	dto := instanceDTO{ID: in.ID, TypeID: in.TypeID, Values: in.Values}
	if dto.Values == nil {
		dto.Values = map[string]any{}
	}
	if !in.CreatedAt.IsZero() {
		dto.CreatedAt = in.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !in.UpdatedAt.IsZero() {
		dto.UpdatedAt = in.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

type instanceListResponse struct {
	TypeID   model.ID      `json:"type_id"`
	Items    []instanceDTO `json:"items"`
	Total    int           `json:"total"`
	NumFound int64         `json:"numfound"`
	Page     int           `json:"page"`
}

func (h *Handler) listInstances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := positiveIntParam(q.Get("page"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	size, err := positiveIntParam(q.Get("count"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ListByType(r.Context(), chi.URLParam(r, "ref"), page, size,
		splitList(q.Get("fields")), cibiz.RetKey(q.Get("ret_key")))
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]instanceDTO, 0, len(got.Items))
	for _, item := range got.Items {
		items = append(items, toInstanceDTO(item))
	}
	writeJSON(w, http.StatusOK, instanceListResponse{
		TypeID: firstTypeID(items), Items: items,
		Total: len(items), NumFound: got.NumFound, Page: got.Page,
	})
}

// firstTypeID reports the model the page belongs to. The page's own type is on
// every row; cantor echoed it at the top level, and the console reads it there.
func firstTypeID(items []instanceDTO) model.ID {
	if len(items) == 0 {
		return 0
	}
	return items[0].TypeID
}

func (h *Handler) getInstance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	got, err := h.uc.Get(r.Context(), id, splitList(q.Get("fields")), cibiz.RetKey(q.Get("ret_key")))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toInstanceDTO(*got))
}

// splitList reads a comma-separated query parameter.
func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// positiveIntParam reads an optional positive integer, defaulting when absent.
func positiveIntParam(raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, errs.ErrInvalid
	}
	return n, nil
}

// pathID reads a positive numeric path parameter.
func pathID(r *http.Request, name string) (model.ID, error) {
	n, err := strconv.ParseInt(chi.URLParam(r, name), 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s", errs.ErrInvalid, name)
	}
	return model.ID(n), nil
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
