package imbridge

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"net/http"
)

func (h *Handler) SetInteraction(i *biz.Interaction) { h.interaction = i }
func (h *Handler) interactionUser(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	if !requireAdmin(w, r) {
		return 0, false
	}
	if h.interaction == nil {
		http.Error(w, "IM native approvals are disabled", http.StatusServiceUnavailable)
		return 0, false
	}
	t, _ := tenantctx.From(r.Context())
	return t.UserID, true
}
func interactionResult(w http.ResponseWriter, data any, err error) {
	if err != nil {
		writeJSON(w, errs.HTTPStatus(err), map[string]any{"code": "interaction_failed", "message": "操作失败，请核对绑定、权限和有效期。", "data": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "", "data": data})
}

// startBinding 创建短期绑定口令。
// @Summary 创建 IM 身份绑定口令
// @Router /v1/im/bindings/challenges [post]
// @Success 200 {object} biz.ChallengeView
func (h *Handler) startBinding(w http.ResponseWriter, r *http.Request) {
	user, ok := h.interactionUser(w, r)
	if !ok {
		return
	}
	var in struct {
		AppID uint64 `json:"app_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		interactionResult(w, nil, errs.ErrInvalid)
		return
	}
	v, err := h.interaction.StartBinding(r.Context(), user, in.AppID)
	interactionResult(w, v, err)
}

// bindingChallenge 查询本人发起的绑定。
// @Summary 查询 IM 绑定证明
// @Router /v1/im/bindings/challenges/{id} [get]
// @Success 200 {object} biz.ChallengeView
func (h *Handler) bindingChallenge(w http.ResponseWriter, r *http.Request) {
	user, ok := h.interactionUser(w, r)
	if !ok {
		return
	}
	v, err := h.interaction.BindingChallenge(r.Context(), user, chi.URLParam(r, "id"))
	interactionResult(w, v, err)
}

// confirmBinding 在 Web 核对平台身份后确认绑定。
// @Summary 确认 IM 身份绑定
// @Router /v1/im/bindings/challenges/{id}/confirm [post]
// @Success 200 {object} map[string]interface{}
func (h *Handler) confirmBinding(w http.ResponseWriter, r *http.Request) {
	user, ok := h.interactionUser(w, r)
	if !ok {
		return
	}
	err := h.interaction.ConfirmBinding(r.Context(), user, chi.URLParam(r, "id"))
	interactionResult(w, nil, err)
}

// bindings 列出当前用户的绑定。
// @Summary 查询本人 IM 身份绑定
// @Router /v1/im/bindings [get]
// @Success 200 {object} map[string]interface{}
func (h *Handler) bindings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.interactionUser(w, r)
	if !ok {
		return
	}
	rows, err := h.interaction.Bindings(r.Context(), user)
	interactionResult(w, map[string]any{"items": rows}, err)
}

// unbind 解绑后排队的审批执行前会再检查绑定。
// @Summary 撤销本人 IM 身份绑定
// @Router /v1/im/bindings/{id} [delete]
// @Success 200 {object} map[string]interface{}
func (h *Handler) unbind(w http.ResponseWriter, r *http.Request) {
	user, ok := h.interactionUser(w, r)
	if !ok {
		return
	}
	interactionResult(w, nil, h.interaction.Unbind(r.Context(), user, chi.URLParam(r, "id")))
}

// preauthorizeBinding 管理员预先授权本人在平台的精确身份。
// @Summary 预先授权本人的 IM 身份
// @Router /v1/im/bindings/preauthorize [post]
// @Success 200 {object} biz.BindingView
func (h *Handler) preauthorizeBinding(w http.ResponseWriter, r *http.Request) {
	user, ok := h.interactionUser(w, r)
	if !ok {
		return
	}
	var in struct {
		AppID          uint64 `json:"app_id"`
		Scope          string `json:"scope"`
		PlatformUserID string `json:"platform_user_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		interactionResult(w, nil, errs.ErrInvalid)
		return
	}
	v, err := h.interaction.PreauthorizeBinding(r.Context(), user, in.AppID, biz.Subject{Scope: in.Scope, User: in.PlatformUserID})
	interactionResult(w, v, err)
}
