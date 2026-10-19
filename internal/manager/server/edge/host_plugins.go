package edge

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"net/http"
)

type hostPluginService interface {
	ListForHost(context.Context, uint64) ([]biz.PluginRow, uint64, error)
	SetForHost(context.Context, uint64, string, biz.SetInput) (*biz.PluginRow, error)
}

// listHostPlugins 返回设备配置与当前安装健康状态。
// @Summary List durable host plugin configuration
// @Router /v1/devices/{id}/plugins [get]
// @Success 200 {object} map[string]interface{}
func (h *Handler) listHostPlugins(w http.ResponseWriter, r *http.Request) {
	svc, ok := h.pluginCfg.(hostPluginService)
	if !ok {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, edgeID, err := svc.ListForHost(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.writePluginRows(w, r, edgeID, rows)
}

// setHostPlugin 保存设备意图，通知当前安装；不会绑定旧 Edge。
// @Summary Set durable host plugin configuration
// @Router /v1/devices/{id}/plugins/{name} [put]
// @Success 200 {object} biz.PluginRow
func (h *Handler) setHostPlugin(w http.ResponseWriter, r *http.Request) {
	svc, ok := h.pluginCfg.(hostPluginService)
	if !ok {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in biz.SetInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	out, err := svc.SetForHost(r.Context(), id, chi.URLParam(r, "name"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
