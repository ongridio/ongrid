package graph

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type budgetStopModel struct {
	inner einomodel.ToolCallingChatModel
}

func wrapBudgetStopModel(inner einomodel.ToolCallingChatModel) einomodel.ToolCallingChatModel {
	if inner == nil {
		return nil
	}
	return &budgetStopModel{inner: inner}
}

func (m *budgetStopModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	// 回调只能标记预算拒绝；在普通推理和工具熔断总结之前终止模型调用。
	if err := llm.BudgetRejectionFromContext(ctx); err != nil {
		return nil, err
	}
	if env, ok := latestTerminalToolBudget(input); ok {
		return m.synthesizeAfterToolBudget(ctx, input, env.Tool, opts...)
	}
	msg, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	pruned, exhaustedTool := pruneToolCallsForBudget(input, msg)
	if exhaustedTool != "" {
		return m.synthesizeAfterToolBudget(ctx, input, exhaustedTool, opts...)
	}
	return pruned, nil
}

func (m *budgetStopModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := llm.BudgetRejectionFromContext(ctx); err != nil {
		return nil, err
	}
	if env, ok := latestTerminalToolBudget(input); ok {
		msg, err := m.synthesizeAfterToolBudget(ctx, input, env.Tool, opts...)
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
	}
	return m.inner.Stream(ctx, input, opts...)
}

func (m *budgetStopModel) synthesizeAfterToolBudget(ctx context.Context, input []*schema.Message, tool string, opts ...einomodel.Option) (*schema.Message, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "tool"
	}
	reminder := schema.SystemMessage("The per-tool circuit breaker for `" + tool + "` has been reached in this user turn. Do not call that tool again. Either synthesize the answer from existing evidence or call a different tool only when it provides materially different evidence. Follow the configured response language, explain evidence limits honestly, and never paste raw tool JSON or internal reasoning.")
	prompt := make([]*schema.Message, 0, len(input)+1)
	prompt = append(prompt, input...)
	prompt = append(prompt, reminder)
	msg, err := m.inner.Generate(ctx, prompt, opts...)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return &schema.Message{Role: schema.Assistant, Content: budgetPrunedFinalContent(input, tool)}, nil
	}
	if len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) != "" {
		return msg, nil
	}
	pruned, stillExhausted := pruneToolCallsForBudget(input, msg)
	if stillExhausted == "" && pruned != nil && (len(pruned.ToolCalls) > 0 || strings.TrimSpace(pruned.Content) != "") {
		return pruned, nil
	}
	cp := *msg
	cp.Role = schema.Assistant
	cp.ToolCalls = nil
	cp.Content = budgetPrunedFinalContent(input, tool)
	return &cp, nil
}

func pruneToolCallsForBudget(history []*schema.Message, msg *schema.Message) (*schema.Message, string) {
	if msg == nil || len(msg.ToolCalls) == 0 {
		return msg, ""
	}
	if guarded := guardUnresolvedDeviceTarget(history, msg); guarded != nil {
		return guarded, ""
	}
	perToolCounts := priorToolCounts(history)
	kept := make([]schema.ToolCall, 0, len(msg.ToolCalls))
	exhaustedTool := ""
	for _, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		if perToolCounts[name] >= maxCallsForTool(name) {
			if exhaustedTool == "" {
				exhaustedTool = name
			}
			continue
		}
		perToolCounts[name]++
		kept = append(kept, call)
	}
	if len(kept) == len(msg.ToolCalls) {
		return msg, ""
	}
	cp := *msg
	cp.ToolCalls = kept
	if len(kept) == 0 {
		cp.Content = budgetPrunedFinalContent(history, exhaustedTool)
		return &cp, exhaustedTool
	}
	return &cp, ""
}

func guardUnresolvedDeviceTarget(history []*schema.Message, msg *schema.Message) *schema.Message {
	target := currentNamedDeviceTarget(history)
	if target == "" || !currentTurnHasEmptyDeviceLookup(history) || !containsHostScopedToolCall(msg.ToolCalls) {
		return nil
	}
	cp := *msg
	cp.ToolCalls = nil
	if wantsEnglishResponse(history) {
		cp.Content = "I could not find device `" + target + "` in the fleet. I will not substitute another online device automatically. Please confirm the correct device or select it with @ before I run host-level checks."
	} else {
		cp.Content = "没有在设备清单中找到 `" + target + "`。我不会自动改用其他在线设备执行主机检查；请确认正确设备，或使用 @ 选择目标设备后我再继续。"
	}
	return &cp
}

var namedDeviceTargetRe = regexp.MustCompile(`(?i)\b(?:edge|vm|node|host|server|device)-[a-z0-9_.-]+\b`)

func currentNamedDeviceTarget(messages []*schema.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil || msg.Role != schema.User || isSystemReminderMessage(msg.Content) {
			continue
		}
		if strings.Contains(strings.ToLower(msg.Content), "device_id") || strings.Contains(msg.Content, "@") {
			return ""
		}
		return namedDeviceTargetRe.FindString(msg.Content)
	}
	return ""
}

func currentTurnHasEmptyDeviceLookup(messages []*schema.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.User && !isSystemReminderMessage(msg.Content) {
			break
		}
		if msg.Role != schema.Tool || msg.ToolName != "query_devices" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(msg.Content), &payload); err != nil {
			continue
		}
		count, ok := jsonNumber(payload["count"])
		if ok && count == 0 {
			return true
		}
	}
	return false
}

func containsHostScopedToolCall(calls []schema.ToolCall) bool {
	for _, call := range calls {
		switch strings.TrimSpace(call.Function.Name) {
		case "get_host_load", "get_host_processes", "host_bash", "host_find_large_files", "host_du_summary", "host_stat_file", "host_restart_service", "capture_pcap":
			return true
		}
	}
	return false
}

func priorToolCounts(messages []*schema.Message) map[string]int {
	counts := make(map[string]int)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.User && !isSystemReminderMessage(msg.Content) {
			break
		}
		if msg.Role != schema.Tool {
			continue
		}
		name := strings.TrimSpace(msg.ToolName)
		if name == "" {
			continue
		}
		counts[name]++
	}
	return counts
}

func budgetPrunedFinalContent(messages []*schema.Message, tool string) string {
	if strings.TrimSpace(tool) == "" {
		tool = "tool"
	}
	if wantsEnglishResponse(messages) {
		return "This turn reached the per-tool safety limit for `" + tool + "`, and the model did not produce a usable synthesis. No raw tool output is shown. Send a narrower target or time window to continue in a new turn."
	}
	return "本轮 `" + tool + "` 已达到单工具安全上限，模型未能生成可用的归纳结论。这里不会展示工具原始结果；请在下一条消息给出更窄的目标或时间窗后继续。"
}

func summarizeRecentToolEvidence(messages []*schema.Message) string {
	lines := make([]string, 0, 6)
	for i := len(messages) - 1; i >= 0 && len(lines) < 6; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.User && !isSystemReminderMessage(msg.Content) {
			break
		}
		if msg.Role != schema.Tool {
			continue
		}
		line := summarizeToolMessage(msg.ToolName, msg.Content)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return "- " + strings.Join(lines, "\n- ")
}

func summarizeToolMessage(toolName, content string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "tool"
	}
	switch toolName {
	case "ToolSearch", "get_edge_summary", "query_knowledge", "grep_source", "list_repo_sources":
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err == nil {
		switch toolName {
		case "query_incidents":
			return summarizeIncidentPayload(toolName, payload)
		case "correlate_incident":
			return summarizeCorrelationPayload(toolName, payload)
		case "host_du_summary":
			return summarizeDiskUsagePayload(toolName, payload)
		case "host_find_large_files":
			return summarizeLargeFilesPayload(toolName, payload)
		case "host_bash":
			return summarizeHostBashPayload(toolName, payload)
		}
		if count, ok := jsonNumber(payload["count"]); ok {
			return toolName + ": 返回 " + formatNumber(count) + " 条结果。"
		}
	}
	compact := strings.Join(strings.Fields(content), " ")
	if compact == "" {
		return ""
	}
	if len(compact) > 180 {
		compact = compact[:180] + "..."
	}
	return toolName + ": " + compact
}

func summarizeIncidentPayload(toolName string, payload map[string]any) string {
	count, _ := jsonNumber(payload["count"])
	incidents, _ := payload["incidents"].([]any)
	titles := make([]string, 0, 3)
	for _, item := range incidents {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := obj["title"].(string)
		if title == "" {
			continue
		}
		titles = append(titles, trimRunes(title, 72))
		if len(titles) >= 3 {
			break
		}
	}
	if len(titles) == 0 {
		return toolName + ": 返回 " + formatNumber(count) + " 条告警。"
	}
	return toolName + ": 返回 " + formatNumber(count) + " 条告警，代表项：" + strings.Join(titles, "；")
}

func summarizeCorrelationPayload(toolName string, payload map[string]any) string {
	results, _ := payload["results"].([]any)
	parts := make([]string, 0, 3)
	for _, item := range results {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := jsonNumber(obj["incident_id"])
		bundle, _ := obj["bundle"].(map[string]any)
		incident, _ := bundle["incident"].(map[string]any)
		title, _ := incident["title"].(string)
		value, hasValue := jsonNumber(incident["value"])
		part := "incident " + formatNumber(id)
		if title != "" {
			part += " " + trimRunes(title, 56)
		}
		if hasValue {
			part += "，value=" + formatNumber(value)
		}
		parts = append(parts, part)
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return toolName + ": 返回关联分析结果。"
	}
	return toolName + ": " + strings.Join(parts, "；")
}

func summarizeDiskUsagePayload(toolName string, payload map[string]any) string {
	results, _ := payload["results"].([]any)
	parts := make([]string, 0, 4)
	for _, item := range results {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := obj["path"].(string)
		subpaths, _ := obj["subpaths"].([]any)
		if path == "" || len(subpaths) == 0 {
			continue
		}
		top, _ := subpaths[0].(map[string]any)
		subpath, _ := top["subpath"].(string)
		size, _ := top["size_human"].(string)
		if subpath == "" {
			continue
		}
		if size != "" {
			parts = append(parts, path+" 最大项 "+subpath+"="+size)
		} else {
			parts = append(parts, path+" 最大项 "+subpath)
		}
		if len(parts) >= 4 {
			break
		}
	}
	if len(parts) == 0 {
		return toolName + ": 返回磁盘占用分析结果。"
	}
	return toolName + ": " + strings.Join(parts, "；")
}

func summarizeLargeFilesPayload(toolName string, payload map[string]any) string {
	results, _ := payload["results"].([]any)
	parts := make([]string, 0, 5)
	for _, item := range results {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		files, _ := obj["files"].([]any)
		for _, file := range files {
			fileObj, ok := file.(map[string]any)
			if !ok {
				continue
			}
			path, _ := fileObj["path"].(string)
			size, _ := fileObj["size_human"].(string)
			if path == "" {
				continue
			}
			if size != "" {
				parts = append(parts, path+"="+size)
			} else {
				parts = append(parts, path)
			}
			if len(parts) >= 5 {
				return toolName + ": 大文件 " + strings.Join(parts, "；")
			}
		}
	}
	if len(parts) == 0 {
		return toolName + ": 返回大文件扫描结果。"
	}
	return toolName + ": 大文件 " + strings.Join(parts, "；")
}

func summarizeHostBashPayload(toolName string, payload map[string]any) string {
	cmd, _ := payload["cmd"].(string)
	results, _ := payload["results"].([]any)
	stdouts := make([]string, 0, len(results))
	for _, item := range results {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		stdout, _ := obj["stdout"].(string)
		stdout = strings.TrimSpace(stdout)
		if stdout != "" {
			stdouts = append(stdouts, stdout)
		}
	}
	if len(stdouts) == 0 {
		return toolName + ": 命令 `" + trimRunes(cmd, 60) + "` 无有效输出。"
	}
	joined := strings.Join(stdouts, "\n")
	if strings.Contains(cmd, "df ") {
		line := firstDataLine(joined)
		if line != "" {
			return toolName + ": `df` 显示 " + trimRunes(line, 120)
		}
	}
	if strings.Contains(cmd, "du ") {
		tops := topOutputLines(joined, 4)
		if len(tops) > 0 {
			return toolName + ": `du` Top 项 " + strings.Join(tops, "；")
		}
	}
	lines := topOutputLines(joined, 3)
	if len(lines) == 0 {
		return toolName + ": 命令 `" + trimRunes(cmd, 60) + "` 已执行。"
	}
	return toolName + ": `" + trimRunes(cmd, 60) + "` 输出 " + strings.Join(lines, "；")
}

func firstDataLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || strings.HasPrefix(strings.ToLower(line), "filesystem ") {
			continue
		}
		return line
	}
	return ""
}

func topOutputLines(output string, max int) []string {
	lines := make([]string, 0, max)
	for _, line := range strings.Split(output, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		lines = append(lines, trimRunes(line, 80))
		if len(lines) >= max {
			break
		}
	}
	return lines
}

func jsonNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func trimRunes(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}

func (m *budgetStopModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	next, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &budgetStopModel{inner: next}, nil
}

func finalAnswerAfterToolBudget(messages []*schema.Message) (*schema.Message, bool) {
	env, ok := latestTerminalToolBudget(messages)
	if !ok {
		return nil, false
	}
	tool := strings.TrimSpace(env.Tool)
	if tool == "" {
		tool = "the tool"
	}
	content := "我已收敛本轮排查，没有继续反复调用 `" + tool + "`。基于本轮已经拿到的结果：如果上面的数据已经出现异常信号，就按这些信号给出结论和下一步；如果结果为空或报错，本轮缺少可判定证据，请在下一条消息补充更具体的时间窗、service 或 device_id 后再查。"
	if wantsEnglishResponse(messages) {
		content = "I have converged this investigation instead of repeatedly calling `" + tool + "`. Based on the evidence already collected: if the earlier results show an abnormal signal, use that signal for the conclusion and next step; if they were empty or errored, this turn lacks decisive evidence, so send a narrower time window, service, or device_id in the next message and I can query again."
	}
	return &schema.Message{Role: schema.Assistant, Content: content}, true
}

type toolBudgetEnvelope struct {
	Status      string `json:"status"`
	Tool        string `json:"tool"`
	FinalAnswer bool   `json:"final_answer_required"`
}

func latestTerminalToolBudget(messages []*schema.Message) (toolBudgetEnvelope, bool) {
	var zero toolBudgetEnvelope
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.User && !isSystemReminderMessage(msg.Content) {
			return zero, false
		}
		if msg.Role != schema.Tool {
			continue
		}
		var env toolBudgetEnvelope
		if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
			continue
		}
		if env.Status == "call_budget_exceeded" && env.FinalAnswer {
			return env, true
		}
	}
	return zero, false
}

func isSystemReminderMessage(content string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(content))
	return strings.HasPrefix(trimmed, "<system-reminder>")
}

func wantsEnglishResponse(messages []*schema.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role != schema.System && msg.Role != schema.User {
			continue
		}
		if strings.Contains(strings.ToLower(msg.Content), "respond in english") {
			return true
		}
	}
	return false
}
