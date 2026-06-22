package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

const applyConfigChangeToolName = "apply_config_change"

var confirmDraftHashRE = regexp.MustCompile(`(?i)\bdraft_hash\b\s*[:=]\s*"?\s*(sha256:[a-f0-9]{64})\s*"?`)

func (rt *Runtime) tryApplyConfirmedConfigDraft(ctx context.Context, req *Request, sess *aiopsmodel.Session, history []*aiopsmodel.Message, emit Emit) (*Reply, bool) {
	if req == nil || sess == nil {
		return nil, false
	}
	argsJSON, parseErr, ok := parseConfirmedConfigDraftApplyArgs(req.UserText)
	if !ok {
		if !looksLikeConfigDraftConfirmation(req.UserText) {
			return nil, false
		}
		argsJSON, parseErr, ok = latestConfigDraftApplyArgs(history, req.UserText)
		if !ok {
			return nil, false
		}
	}
	if parseErr != nil {
		return rt.persistAndEmitDirectAssistant(ctx, sess.ID, emit, fmt.Sprintf("确认应用失败：%s", parseErr.Error())), true
	}
	tool, err := findToolByName(ctx, rt.cfg.ToolBag, applyConfigChangeToolName)
	if err != nil {
		return rt.persistAndEmitDirectAssistant(ctx, sess.ID, emit, fmt.Sprintf("确认应用失败：%s", err.Error())), true
	}

	callID := "direct_" + uuid.NewString()
	startedAt := time.Now().UTC()
	emit(Event{Type: EventToolStart, Tool: &ToolEvent{
		ToolCallID: callID,
		Name:       applyConfigChangeToolName,
		ArgsJSON:   argsJSON,
		Status:     "running",
		StartedAt:  startedAt,
	}})
	result, runErr := tool.InvokableRun(ctx, argsJSON,
		basetool.WithUserID(req.UserID),
		basetool.WithUserText(req.UserText),
	)
	endedAt := time.Now().UTC()
	status := "success"
	errText := ""
	if runErr != nil {
		status = "error"
		errText = runErr.Error()
	}
	emit(Event{Type: EventToolEnd, Tool: &ToolEvent{
		ToolCallID: callID,
		Name:       applyConfigChangeToolName,
		ArgsJSON:   argsJSON,
		ResultJSON: result,
		Status:     status,
		StartedAt:  startedAt,
		EndedAt:    &endedAt,
		DurationMs: endedAt.Sub(startedAt).Milliseconds(),
		Error:      errText,
	}})
	if runErr != nil {
		return rt.persistAndEmitDirectAssistant(ctx, sess.ID, emit, fmt.Sprintf("确认应用失败：%s", runErr.Error())), true
	}
	return rt.persistAndEmitDirectAssistant(ctx, sess.ID, emit, formatConfigApplyResultMessage(result)), true
}

func (rt *Runtime) persistAndEmitDirectAssistant(ctx context.Context, sessionID string, emit Emit, content string, toolCalls ...*aiopsmodel.ToolCall) *Reply {
	if emit == nil {
		emit = func(Event) {}
	}
	msg := &aiopsmodel.Message{
		SessionID: sessionID,
		Role:      aiopsmodel.RoleAssistant,
		Content:   &content,
		CreatedAt: time.Now().UTC(),
	}
	if err := rt.cfg.Sessions.AppendMessage(ctx, msg); err != nil {
		if rt.log != nil {
			rt.log.Warn("chatruntime: persist direct assistant failed",
				slog.String("session_id", sessionID),
				slog.String("err", err.Error()))
		}
	}
	emit(Event{Type: EventAssistant, Assistant: &AssistantEvent{
		MessageID: msg.ID,
		Content:   content,
		CreatedAt: msg.CreatedAt,
	}})
	reply := &Reply{Message: msg, Iterations: 1, ToolCalls: toolCalls}
	emit(Event{Type: EventDone, Done: reply})
	return reply
}

func parseConfirmedConfigDraftApplyArgs(text string) (string, error, bool) {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "apply_config_change") ||
		!strings.Contains(lower, "draft_hash") ||
		!strings.Contains(lower, "payload") {
		return "", nil, false
	}
	hashMatch := confirmDraftHashRE.FindStringSubmatch(text)
	if len(hashMatch) != 2 {
		return "", fmt.Errorf("确认消息缺少有效 draft_hash"), true
	}
	payload, err := extractPayloadJSON(text)
	if err != nil {
		return "", err, true
	}
	args := struct {
		Domain           string          `json:"domain"`
		Action           string          `json:"action"`
		Confirmed        bool            `json:"confirmed"`
		DraftHash        string          `json:"draft_hash"`
		Payload          json.RawMessage `json:"payload"`
		ConfirmationText string          `json:"confirmation_text,omitempty"`
	}{
		Domain:           "alert_rule",
		Action:           "create",
		Confirmed:        true,
		DraftHash:        hashMatch[1],
		Payload:          payload,
		ConfirmationText: "confirmed via chat confirmation button",
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("构造 apply_config_change 参数失败: %w", err), true
	}
	return string(b), nil, true
}

func latestConfigDraftApplyArgs(history []*aiopsmodel.Message, confirmationText string) (string, error, bool) {
	skippedCurrentUser := false
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg == nil {
			continue
		}
		if msg.Role == aiopsmodel.RoleUser {
			if !skippedCurrentUser {
				skippedCurrentUser = true
				continue
			}
			return "", nil, false
		}
		if !skippedCurrentUser {
			continue
		}
		if msg.Content != nil {
			args, err, ok := configDraftApplyArgsFromToolResult(*msg.Content, confirmationText)
			if err != nil || ok {
				return args, err, ok
			}
			if isConfigApplyResult(*msg.Content) {
				return "", nil, false
			}
		}
		for j := len(msg.ToolCalls) - 1; j >= 0; j-- {
			tc := msg.ToolCalls[j]
			if tc.ResultJSON == nil {
				continue
			}
			args, err, ok := configDraftApplyArgsFromToolResult(*tc.ResultJSON, confirmationText)
			if err != nil || ok {
				return args, err, ok
			}
			if isConfigApplyResult(*tc.ResultJSON) {
				return "", nil, false
			}
		}
	}
	return "", nil, false
}

func configDraftApplyArgsFromToolResult(result string, confirmationText string) (string, error, bool) {
	result = strings.TrimSpace(result)
	if !strings.HasPrefix(result, "{") {
		return "", nil, false
	}
	var draft struct {
		Kind      string          `json:"kind"`
		Domain    string          `json:"domain"`
		Action    string          `json:"action"`
		DraftHash string          `json:"draft_hash"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(result), &draft); err != nil {
		return "", nil, false
	}
	if draft.Kind != "config_draft" {
		return "", nil, false
	}
	if strings.TrimSpace(draft.DraftHash) == "" {
		return "", fmt.Errorf("最近的 config_draft 缺少 draft_hash"), true
	}
	if len(draft.Payload) == 0 || !json.Valid(draft.Payload) {
		return "", fmt.Errorf("最近的 config_draft payload 无效"), true
	}
	domain := strings.TrimSpace(draft.Domain)
	if domain == "" {
		domain = "alert_rule"
	}
	action := strings.TrimSpace(draft.Action)
	if action == "" {
		action = "create"
	}
	args := struct {
		Domain           string          `json:"domain"`
		Action           string          `json:"action"`
		Confirmed        bool            `json:"confirmed"`
		DraftHash        string          `json:"draft_hash"`
		Payload          json.RawMessage `json:"payload"`
		ConfirmationText string          `json:"confirmation_text,omitempty"`
	}{
		Domain:           domain,
		Action:           action,
		Confirmed:        true,
		DraftHash:        strings.TrimSpace(draft.DraftHash),
		Payload:          draft.Payload,
		ConfirmationText: strings.TrimSpace(confirmationText),
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("构造 apply_config_change 参数失败: %w", err), true
	}
	return string(b), nil, true
}

func isConfigApplyResult(result string) bool {
	result = strings.TrimSpace(result)
	if !strings.HasPrefix(result, "{") {
		return false
	}
	var resp struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return false
	}
	return resp.Kind == "config_apply_result"
}

func looksLikeConfigDraftConfirmation(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	normalized = strings.Trim(normalized, " \t\r\n。.!！?？")
	if normalized == "" || len([]rune(normalized)) > 40 {
		return false
	}
	if strings.Contains(normalized, "取消") ||
		strings.Contains(normalized, "不要") ||
		strings.Contains(normalized, "别") ||
		strings.Contains(normalized, "no") ||
		strings.Contains(normalized, "cancel") {
		return false
	}
	switch normalized {
	case "ok", "okay", "yes", "y", "confirm", "confirmed", "approve", "approved", "apply",
		"好", "好的", "可以", "行", "嗯", "是", "确认", "确认创建", "确认应用", "应用", "创建", "同意", "通过":
		return true
	default:
		return (strings.Contains(normalized, "确认") || strings.Contains(normalized, "同意")) &&
			(strings.Contains(normalized, "创建") || strings.Contains(normalized, "应用") || strings.Contains(normalized, "生效"))
	}
}

func extractPayloadJSON(text string) (json.RawMessage, error) {
	lower := strings.ToLower(text)
	payloadIdx := strings.Index(lower, "payload")
	if payloadIdx < 0 {
		return nil, fmt.Errorf("确认消息缺少 payload")
	}
	afterPayload := text[payloadIdx:]
	if fenced := extractFencedJSON(afterPayload); len(fenced) > 0 {
		return validateRawJSONObject(fenced)
	}
	openRel := strings.Index(afterPayload, "{")
	if openRel < 0 {
		return nil, fmt.Errorf("确认消息 payload 缺少 JSON 对象")
	}
	raw, err := extractBalancedJSONObject(afterPayload[openRel:])
	if err != nil {
		return nil, err
	}
	return validateRawJSONObject(raw)
}

func extractFencedJSON(text string) []byte {
	fenceStart := strings.Index(text, "```")
	if fenceStart < 0 {
		return nil
	}
	bodyStart := fenceStart + len("```")
	if strings.HasPrefix(strings.ToLower(text[bodyStart:]), "json") {
		bodyStart += len("json")
	}
	body := text[bodyStart:]
	fenceEnd := strings.Index(body, "```")
	if fenceEnd < 0 {
		return nil
	}
	return []byte(strings.TrimSpace(body[:fenceEnd]))
}

func extractBalancedJSONObject(text string) ([]byte, error) {
	depth := 0
	inString := false
	escaped := false
	for i, r := range text {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(text[:i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("确认消息 payload JSON 不完整")
}

func validateRawJSONObject(raw []byte) (json.RawMessage, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("确认消息 payload 不是有效 JSON: %w", err)
	}
	if len(obj) == 0 {
		return nil, fmt.Errorf("确认消息 payload 不能为空")
	}
	return json.RawMessage(raw), nil
}

func findToolByName(ctx context.Context, tools []basetool.BaseTool, name string) (basetool.BaseTool, error) {
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		info, err := tool.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		if info.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("%s 未配置", name)
}

func formatConfigApplyResultMessage(result string) string {
	var resp struct {
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		ResourceID uint64 `json:"resource_id"`
		Resource   struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"resource"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return "已确认并应用配置变更。"
	}
	name := strings.TrimSpace(resp.Resource.Name)
	if name == "" {
		name = "告警规则"
	}
	status := strings.TrimSpace(resp.Status)
	if status == "" {
		status = "applied"
	}
	if resp.ResourceID > 0 {
		return fmt.Sprintf("已确认并创建告警规则：%s（ID: %d，状态: %s）。", name, resp.ResourceID, status)
	}
	return fmt.Sprintf("已确认并创建告警规则：%s（状态: %s）。", name, status)
}
