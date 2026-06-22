// Package systemupgrade exposes the platform upgrade-check API.
package systemupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	upgradesvc "github.com/ongridio/ongrid/internal/manager/service/systemupgrade"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type UpgradeService interface {
	Check(ctx context.Context) (*upgradesvc.Info, error)
}

type Info = upgradesvc.Info

type Handler struct {
	svc UpgradeService
}

func NewHandler(svc UpgradeService) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/system/upgrade", h.check)
	r.Post("/v1/system/upgrade/check", h.check)
}

// check godoc
// @Summary Check platform upgrade
// @Router /v1/system/upgrade [get]
// @Router /v1/system/upgrade/check [post]
// @Success 200 {object} systemupgrade.Info
func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.svc == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	info, err := h.svc.Check(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return false
	}
	if t.Role != "admin" {
		writeErr(w, errs.ErrForbidden)
		return false
	}
	return true
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	slug := "internal"
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		status, slug = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		status, slug = http.StatusForbidden, "forbidden"
	case errors.Is(err, errs.ErrInvalid):
		status, slug = http.StatusBadRequest, "invalid"
	case errors.Is(err, errs.ErrNotFound):
		status, slug = http.StatusNotFound, "not-found"
	default:
		status, slug = http.StatusBadGateway, "upstream"
	}
	writeJSON(w, status, errorBody{Error: err.Error(), Code: slug})
}
