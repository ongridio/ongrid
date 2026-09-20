package device

import (
	"encoding/json"
	"net/http"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// getEnvironment resolves a device default and its inheritance source.
// @Summary Get device default environment
// @Router /v1/devices/{id}/environment [get]
// @Success 200 {object} map[string]interface{}
func (h *Handler) getEnvironment(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	value, err := h.uc.ResolveEnvironment(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "message": "", "data": value})
}

// setEnvironment updates the shared device property; empty restores inheritance.
// @Summary Set device default environment
// @Router /v1/devices/{id}/environment [put]
// @Success 200 {object} map[string]interface{}
func (h *Handler) setEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var input struct {
		Environment *string `json:"environment"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || input.Environment == nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	value, err := h.uc.SetEnvironment(r.Context(), id, *input.Environment)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "message": "", "data": value})
}
