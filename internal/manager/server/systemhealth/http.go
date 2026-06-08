// Package systemhealth exposes the platform health-check API.
package systemhealth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
	healthsvc "github.com/ongridio/ongrid/internal/manager/service/systemhealth"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type HealthService interface {
	Check(ctx context.Context, caller alertsvc.Caller) (*healthsvc.Report, error)
}

type Handler struct {
	svc HealthService
}

func NewHandler(svc HealthService) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/system/health", h.check)
	r.Post("/v1/system/health/check", h.check)
}

func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if h.svc == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	report, err := h.svc.Check(r.Context(), caller)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) (alertsvc.Caller, bool) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return alertsvc.Caller{}, false
	}
	if t.Role != "admin" {
		writeErr(w, errs.ErrForbidden)
		return alertsvc.Caller{}, false
	}
	return alertsvc.Caller{UserID: t.UserID, Role: t.Role}, true
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
