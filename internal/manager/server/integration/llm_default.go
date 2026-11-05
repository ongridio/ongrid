package integration

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	bizaudit "github.com/ongridio/ongrid/internal/manager/biz/audit"
	bizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	auditmw "github.com/ongridio/ongrid/internal/manager/server/middleware"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// @Summary Save the global default model and reasoning effort
// @Tags integrations
// @Accept json
// @Produce json
// @Param request body bizsetting.LLMDefaultSelection true "Configured model selection"
// @Success 200 {object} bizsetting.LLMDefaultSelection
// @Failure 400 {object} errorBody
// @Failure 401 {object} errorBody
// @Failure 403 {object} errorBody
// @Failure 500 {object} errorBody
// @Router /v1/integrations/llm/default [put]
// @Security BearerAuth
func (h *Handler) saveLLMDefault(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.llmProbe == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var in bizsetting.LLMDefaultSelection
	if err := decoder.Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, errs.ErrInvalid)
		return
	}
	selection, err := h.llmProbe.SaveDefault(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.llmRouter != nil {
		h.llmRouter.Invalidate()
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action: auditmodel.ActionSettingUpdate, ResourceType: auditmodel.ResourceSetting,
		ResourceID: "llm/default", Status: auditmodel.StatusSuccess,
		Payload: map[string]any{"provider": selection.Provider, "model": selection.Model, "reasoning_effort": selection.ReasoningEffort},
	})
	writeJSON(w, http.StatusOK, selection)
}
