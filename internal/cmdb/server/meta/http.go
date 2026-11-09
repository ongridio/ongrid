// Package meta builds the HTTP routes for CMDB schema provenance.
package meta

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	metabiz "github.com/ongridio/ongrid/internal/cmdb/biz/meta"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Handler exposes the CMDB schema status route.
type Handler struct {
	uc *metabiz.Usecase
}

// NewHandler builds the handler around the meta usecase.
func NewHandler(uc *metabiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the CMDB status routes on r.
//
// The caller is responsible for r already carrying the auth middleware —
// CMDB is mounted inside cmd/ongrid-cmdb's protected /api group, so identity
// and bearer verification are inherited rather than re-implemented here.
//
// Routes:
//
//	GET /v1/cmdb/health (any authed) — schema lineage, not a liveness probe
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/cmdb/health", h.health)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	status, err := h.uc.Status(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
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

// writeErr maps biz errors through errs.HTTPStatus, which is the single
// source of truth for the status code, and adds a stable machine-readable
// slug for the SPA.
func writeErr(w http.ResponseWriter, err error) {
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
	default:
		return "internal"
	}
}
