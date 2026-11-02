package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func feishuApprovalContent(card bridge.ApprovalCard) map[string]any {
	// 操作参数用纯文本组件，避免命令/工具输出注入 Markdown 链接或伪造按钮。
	elements := []map[string]any{}
	addText := func(text string) {
		runes := []rune(text)
		for len(runes) > 0 {
			n := min(len(runes), 1500)
			elements = append(elements, map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": string(runes[:n])}})
			runes = runes[n:]
		}
	}
	addText(card.Text)
	if card.Details != "" {
		addText(card.Label("操作详情（敏感字段已隐藏）", "Operation details (sensitive fields hidden)"))
		addText(card.Details)
	}
	if card.Result != "" {
		addText(card.Label("执行结果", "Execution result"))
		addText(card.Result)
	}
	actions := []map[string]any{}
	for _, a := range []struct{ label, token, kind, confirmation string }{
		{card.Label("批准执行", "Approve execution"), card.Buttons.Approve, "primary", card.Label("确认执行卡片中展示的操作？这可能修改设备或数据。", "Execute the operation shown in this card? This may modify devices or data.")},
		{card.Label("拒绝", "Reject"), card.Buttons.Reject, "default", card.Label("确认拒绝本次操作？", "Reject this operation?")},
	} {
		if a.token != "" {
			actions = append(actions, map[string]any{"tag": "button", "type": a.kind, "text": map[string]string{"tag": "plain_text", "content": a.label}, "value": map[string]string{"ongrid_token": a.token}, "confirm": map[string]any{"title": map[string]string{"tag": "plain_text", "content": card.Label("确认审批", "Confirm decision")}, "text": map[string]string{"tag": "plain_text", "content": a.confirmation}}})
		}
	}
	if len(actions) > 0 {
		elements = append(elements, map[string]any{"tag": "action", "actions": actions})
	}
	return map[string]any{"config": map[string]bool{"wide_screen_mode": true, "update_multi": true, "enable_forward": false}, "elements": elements}
}

func (s senderAdapter) SendApproval(ctx context.Context, card bridge.ApprovalCard) (string, error) {
	path := "/open-apis/im/v1/messages?receive_id_type=chat_id"
	if s.threadID != "" {
		path = "/open-apis/im/v1/messages/" + url.PathEscape(s.threadID) + "/reply"
	}
	return s.client.approvalRequest(ctx, http.MethodPost, path, s.chatID, card)
}
func (s senderAdapter) EditApproval(ctx context.Context, id string, card bridge.ApprovalCard) error {
	_, err := s.client.approvalRequest(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+url.PathEscape(id), "", card)
	return err
}
func (c *Client) approvalRequest(ctx context.Context, method, path, chat string, card bridge.ApprovalCard) (string, error) {
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	content, err := json.Marshal(feishuApprovalContent(card))
	if err != nil {
		return "", err
	}
	body := map[string]string{"content": string(content)}
	if method == http.MethodPost {
		if !strings.HasSuffix(path, "/reply") {
			body["receive_id"] = chat
		}
		body["msg_type"] = "interactive"
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode == 429 {
		seconds, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		return "", &bridge.DeliveryError{Code: "rate_limited", RetryAfter: time.Duration(max(seconds, 1)) * time.Second}
	}
	if res.StatusCode == 404 && method == http.MethodPatch {
		return "", &bridge.DeliveryError{Code: "cannot_edit", CannotEdit: true}
	}
	var out struct {
		Code int `json:"code"`
		Data struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if res.StatusCode != 200 || out.Code != 0 {
		return "", fmt.Errorf("feishu approval: status=%d code=%d", res.StatusCode, out.Code)
	}
	if method == http.MethodPost && out.Data.ID == "" {
		return "", fmt.Errorf("feishu approval: missing message id")
	}
	return out.Data.ID, nil
}
func (c *StreamClient) onCard(parent context.Context, ev *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	response := &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "error", Content: "卡片验证失败，请使用当前会话中的最新审批卡片。"}}
	if ev == nil || ev.Event == nil {
		return response, nil
	}
	e := ev.Event
	if e.Operator == nil || e.Context == nil || e.Action == nil || e.Operator.TenantKey == nil {
		return response, nil
	}
	token, ok := e.Action.Value["ongrid_token"].(string)
	if !ok {
		return response, nil
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	err := c.bridge.HandleApprovalCallback(ctx, bridge.ApprovalCallback{ConnectionIdentity: bridge.ConnectionIdentity(c.app), Provider: "feishu", AppIdentifier: c.app.AppID, ChatID: e.Context.OpenChatID, MessageID: e.Context.OpenMessageID, Subject: bridge.Subject{Scope: *e.Operator.TenantKey, User: e.Operator.OpenID}, Token: token})
	if err != nil {
		response.Toast.Content = bridge.ApprovalReceipt(err, c.app.DefaultLocale, "feishu")
	}
	if err == nil {
		response.Toast = &callback.Toast{Type: "info", Content: bridge.ApprovalReceipt(nil, c.app.DefaultLocale, "feishu")}
	}
	return response, nil
}

// 不回显内部错误或平台身份；只返回用户能够采取的下一步。
func approvalCallbackError(err error) string {
	return bridge.ApprovalReceipt(err, "zh", "feishu")
}
