// Package relationtype builds the HTTP routes for the CMDB relation-type
// dictionary.
package relationtype

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	relationtypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/relationtype"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// roleAdmin mirrors iam/model.RoleAdmin without crossing the BC boundary
// (arch-lint forbids cmdb -> iam imports). Same constant and rationale as
// internal/manager/server/device.
const roleAdmin = "admin"

// Handler exposes /v1/cmdb/relation-types.
type Handler struct {
	uc *relationtypebiz.Usecase
}

// NewHandler builds the handler around the relation-type usecase.
func NewHandler(uc *relationtypebiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the relation-type routes on r.
//
// The caller is responsible for r already carrying the auth middleware — CMDB
// is mounted inside cmd/ongrid-cmdb's protected /api group, so bearer
// verification is inherited rather than re-implemented.
//
// Routes:
//
//	GET    /v1/cmdb/relation-types      (any authed)
//	POST   /v1/cmdb/relation-types      (admin)
//	PUT    /v1/cmdb/relation-types/{id} (admin)
//	DELETE /v1/cmdb/relation-types/{id} (admin)
//
// Write access is gated on the admin role, mirroring cantor's
// `perms_role_required(..., admin_name)` on the same three operations: the
// relation-type dictionary is system vocabulary, not per-instance data, so the
// per-CI-type authorization the rest of CMDB uses does not apply to it.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/cmdb/relation-types", h.list)
	r.With(h.requireAdmin).Post("/v1/cmdb/relation-types", h.create)
	r.With(h.requireAdmin).Put("/v1/cmdb/relation-types/{id}", h.update)
	r.With(h.requireAdmin).Delete("/v1/cmdb/relation-types/{id}", h.delete)
}

// requireAdmin 403s callers without the admin role. A missing identity is
// treated as unauthenticated rather than forbidden, so a misconfigured route
// group fails closed with a 401 instead of looking like a permissions problem.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok {
			writeErr(w, errs.ErrUnauthorized)
			return
		}
		if t.IsSuperuser || t.Role == roleAdmin {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, errs.ErrForbidden)
	})
}

// relationTypeDTO is the wire shape of a relation type.
//
// It is a hand-authored DTO rather than the GORM entity, so storage concerns
// (delete_marker, soft-delete bookkeeping) cannot leak into the API. cantor's
// to_dict() serialised every mapped column, including the soft-delete fields;
// the port deliberately narrows that.
type relationTypeDTO struct {
	ID        int32  `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func toDTO(rt citypemodel.RelationType) relationTypeDTO {
	dto := relationTypeDTO{ID: rt.ID, Name: rt.Name, Color: rt.Color}
	if !rt.CreatedAt.IsZero() {
		dto.CreatedAt = rt.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !rt.UpdatedAt.IsZero() {
		dto.UpdatedAt = rt.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// listResponse follows Ongrid's list convention: {items, total}. cantor
// returned a bare JSON array here; the port uses the house shape because the
// consumer is being rewritten anyway and AGENTS.md fixes the convention for
// list endpoints.
type listResponse struct {
	Items []relationTypeDTO `json:"items"`
	Total int               `json:"total"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.uc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]relationTypeDTO, 0, len(rows))
	for _, rt := range rows {
		items = append(items, toDTO(rt))
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, Total: len(items)})
}

type writeRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func decodeWriteRequest(r *http.Request) (writeRequest, error) {
	var req writeRequest
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return writeRequest{}, err
	}
	return req, nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	req, err := decodeWriteRequest(r)
	if err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	created, err := h.uc.Create(r.Context(), req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(*created))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	req, err := decodeWriteRequest(r)
	if err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	updated, err := h.uc.Update(r.Context(), id, req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(*updated))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
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

// deleteResponse is what cantor returned from DELETE ({"rel_id": id}). The
// port returns 204 with no body instead: the id is already in the URL, and
// echoing it adds a response shape to maintain for no information.
func pathID(r *http.Request) (int32, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || id <= 0 {
		return 0, errs.ErrInvalid
	}
	return int32(id), nil
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

// writeErr maps an error through errs.HTTPStatus — the single source of truth
// for the status code — and adds a stable machine-readable slug for the SPA.
//
// Every biz error in this module wraps an errs sentinel (see
// relationtypebiz's error definitions), so there is no fallback path here: an
// error with no sentinel is a genuine internal fault and 500 is the honest
// answer.
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
	case errors.Is(err, errs.ErrTenantMismatch):
		return "tenant_mismatch"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not_wired_yet"
	}
	return "internal"
}
