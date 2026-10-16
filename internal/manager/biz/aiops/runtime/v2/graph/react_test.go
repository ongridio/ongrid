package graph

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

func TestAssembleMessagesAddsCurrentTurnImages(t *testing.T) {
	data := "aGVsbG8="
	out, err := assembleMessages(&Input{
		UserText: "describe",
		UserImages: []schema.MessageInputPart{{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}},
		}},
	})
	if err != nil {
		t.Fatalf("assembleMessages: %v", err)
	}
	last := out[len(out)-1]
	if last.Content != "describe" || len(last.UserInputMultiContent) != 1 {
		t.Fatalf("unexpected current turn: %+v", last)
	}
}

// scriptedState is the shared mutable bookkeeping for scriptedChatModel
// instances. eino's ToolCallingChatModel.WithTools returns a *new*
// instance with tools bound, which means the test must share counters /
// reply pointers between the original and any derived instance — the
// inner ReAct agent calls .WithTools then dispatches to the new copy.
type scriptedState struct {
	mu            sync.Mutex
	replies       []*schema.Message
	idx           int
	generateCalls atomic.Int32
	genErr        error
	inputsLog     [][]*schema.Message
}

// scriptedChatModel is a tool-calling ChatModel test double used by
// the react_test integration tests. It returns one *schema.Message per
// Generate call from the `replies` list, in order; once exhausted, it
// returns the last reply (so loops with MaxIterations work as expected).
//
// Multiple scriptedChatModel instances may share a single *scriptedState
// (eino's WithTools copies the receiver) — counters must therefore be
// stored on the shared state, not the wrapping instance.
type scriptedChatModel struct {
	st *scriptedState

	// boundTools is captured by WithTools so the test can assert the
	// tool list shape was forwarded. Lives on the per-instance struct
	// because each derivation has its own bound tool list.
	boundTools []*schema.ToolInfo
}

func newScriptedChatModel(replies ...*schema.Message) *scriptedChatModel {
	return &scriptedChatModel{st: &scriptedState{replies: replies}}
}

func (s *scriptedChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	s.st.generateCalls.Add(1)
	s.st.mu.Lock()
	cp := make([]*schema.Message, len(input))
	copy(cp, input)
	s.st.inputsLog = append(s.st.inputsLog, cp)
	if s.st.genErr != nil {
		err := s.st.genErr
		s.st.mu.Unlock()
		return nil, err
	}
	if len(s.st.replies) == 0 {
		s.st.mu.Unlock()
		return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
	}
	if s.st.idx < len(s.st.replies) {
		out := s.st.replies[s.st.idx]
		s.st.idx++
		s.st.mu.Unlock()
		return out, nil
	}
	out := s.st.replies[len(s.st.replies)-1]
	s.st.mu.Unlock()
	return out, nil
}

func (s *scriptedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := s.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (s *scriptedChatModel) BindTools(tools []*schema.ToolInfo) error {
	s.boundTools = tools
	return nil
}

// WithTools returns a NEW instance that shares the underlying state
// pointer so Generate counters / reply cursor are observable from the
// original receiver after the inner ReAct agent dispatches through the
// derived copy.
func (s *scriptedChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &scriptedChatModel{st: s.st, boundTools: tools}, nil
}

func (s *scriptedChatModel) generateCalls() int32 { return s.st.generateCalls.Load() }

func makeAssistantNoTools(content string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, Content: content}
}

func makeAssistantToolCall(content, callID, toolName, args string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		ToolCalls: []schema.ToolCall{
			{
				ID:       callID,
				Type:     "function",
				Function: schema.FunctionCall{Name: toolName, Arguments: args},
			},
		},
	}
}

func TestBuildReActGraph_FinalReplyOneTurn(t *testing.T) {
	t.Parallel()
	scripted := newScriptedChatModel(makeAssistantNoTools("hello world"))
	tool := &fakeBaseTool{name: "noop", parameters: `{"type":"object","properties":{}}`}
	g, err := BuildReActGraph(scripted, []basetool.BaseTool{tool}, Config{})
	if err != nil {
		t.Fatalf("BuildReActGraph: %v", err)
	}
	out, err := g.Invoke(context.Background(), &Input{
		SystemPrompt: "be helpful",
		UserText:     "hi",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out == nil || out.AssistantMessage == nil {
		t.Fatalf("expected non-nil output")
	}
	if out.AssistantMessage.Content != "hello world" {
		t.Errorf("content = %q, want hello world", out.AssistantMessage.Content)
	}
	if scripted.generateCalls() != 1 {
		t.Errorf("Generate calls = %d, want 1", scripted.generateCalls())
	}
}

func TestBuildReActGraph_ToolCallThenFinal(t *testing.T) {
	t.Parallel()
	scripted := newScriptedChatModel(
		makeAssistantToolCall("", "call_1", "echo", `{"a":1}`),
		makeAssistantNoTools("done"),
	)
	echo := &fakeBaseTool{
		name:       "echo",
		parameters: `{"type":"object","properties":{"a":{"type":"integer"}}}`,
		runResp:    `{"echoed":1}`,
	}
	g, err := BuildReActGraph(scripted, []basetool.BaseTool{echo}, Config{MaxIterations: 5})
	if err != nil {
		t.Fatalf("BuildReActGraph: %v", err)
	}
	out, err := g.Invoke(context.Background(), &Input{UserText: "do it"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.AssistantMessage.Content != "done" {
		t.Errorf("content = %q, want done", out.AssistantMessage.Content)
	}
	if echo.calls.Load() != 1 {
		t.Errorf("tool calls = %d, want 1", echo.calls.Load())
	}
	if scripted.generateCalls() != 2 {
		t.Errorf("Generate calls = %d, want 2", scripted.generateCalls())
	}
}

func TestBuildReActGraph_PersistsNestedToolInvocation(t *testing.T) {
	t.Parallel()
	scripted := newScriptedChatModel(
		makeAssistantToolCall("", "call_host_bash", "host_bash", `{"cmd":"docker images"}`),
		makeAssistantNoTools("done"),
	)
	hostBash := &fakeBaseTool{
		name:       "host_bash",
		class:      "read",
		parameters: `{"type":"object","properties":{"cmd":{"type":"string"}}}`,
		runResp:    `{"ok":true}`,
	}
	sink := &recordingToolPersistence{}
	g, err := BuildReActGraph(scripted, []basetool.BaseTool{hostBash}, Config{MaxIterations: 5, ToolPersistence: sink})
	if err != nil {
		t.Fatalf("BuildReActGraph: %v", err)
	}
	out, err := g.Invoke(context.Background(), &Input{UserText: "run docker images"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out == nil || out.AssistantMessage == nil || out.AssistantMessage.Content != "done" {
		t.Fatalf("terminal reply = %+v, want done", out)
	}
	starts, ends := sink.records()
	if len(starts) != 1 || starts[0] != `host_bash:{"cmd":"docker images"}` {
		t.Fatalf("start records = %v", starts)
	}
	if len(ends) != 1 || ends[0] != `host_bash:{"ok":true}` {
		t.Fatalf("end records = %v", ends)
	}
}

func TestBuildReActGraph_RecoversFromToolError(t *testing.T) {
	t.Parallel()
	scripted := newScriptedChatModel(
		makeAssistantToolCall("", "call_1", "flaky", `{}`),
		makeAssistantNoTools("recovered"),
	)
	flaky := &fakeBaseTool{
		name:       "flaky",
		parameters: `{"type":"object","properties":{}}`,
		runErr:     errors.New("temporary"),
	}
	g, err := BuildReActGraph(scripted, []basetool.BaseTool{flaky}, Config{MaxIterations: 5})
	if err != nil {
		t.Fatalf("BuildReActGraph: %v", err)
	}
	out, err := g.Invoke(context.Background(), &Input{UserText: "do it"})
	// einoToolAdapter wraps tool errors as JSON envelopes (
	// invariant: tool failures are facts the LLM consumes, not graph-
	// fatal events). ToolsNode therefore returns successfully with the
	// envelope in the tool message; ChatModel then runs the second
	// scripted reply ("recovered") and the graph completes cleanly.
	if err != nil {
		t.Fatalf("Invoke should recover from tool error, got %v", err)
	}
	if out == nil || out.AssistantMessage == nil || !strings.Contains(out.AssistantMessage.Content, "recovered") {
		t.Errorf("expected recovered final reply, got %+v", out)
	}
	if flaky.calls.Load() != 1 {
		t.Errorf("tool calls = %d, want 1 (one attempt then ChatModel recovers)", flaky.calls.Load())
	}
	if scripted.generateCalls() != 2 {
		t.Errorf("ChatModel generate calls = %d, want 2 (initial tool_call turn + recovery turn)", scripted.generateCalls())
	}
}

func TestBudgetStopModel_FinalizesAfterToolBudget(t *testing.T) {
	t.Parallel()
	inner := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "现有日志证据表明 api-gateway 错误率升高，但时间窗仍不足以确认根因。",
	})
	wrapped := wrapBudgetStopModel(inner)
	got, err := wrapped.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("查日志"),
		schema.ToolMessage(`{"status":"call_budget_exceeded","tool":"query_logql","final_answer_required":true}`, "call_1", schema.WithToolName("query_logql")),
		schema.UserMessage("<system-reminder>stop loops</system-reminder>"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Role != schema.Assistant || len(got.ToolCalls) != 0 {
		t.Fatalf("budget stop response = %+v, want assistant without tool calls", got)
	}
	if !strings.Contains(got.Content, "错误率升高") {
		t.Fatalf("budget stop did not use model synthesis: %q", got.Content)
	}
	if inner.st.generateCalls.Load() != 1 {
		t.Fatalf("inner model was called %d time(s), want 1", inner.st.generateCalls.Load())
	}
}

func TestBudgetStopModel_IgnoresPriorTurnToolBudget(t *testing.T) {
	t.Parallel()
	inner := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "new turn answered",
	})
	wrapped := wrapBudgetStopModel(inner)
	got, err := wrapped.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("查日志"),
		schema.ToolMessage(`{"status":"call_budget_exceeded","tool":"query_logql","final_answer_required":true}`, "call_1", schema.WithToolName("query_logql")),
		schema.UserMessage("改查最近 10 分钟 api-gateway"),
		schema.UserMessage("<system-reminder>stop loops</system-reminder>"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Role != schema.Assistant || got.Content != "new turn answered" {
		t.Fatalf("budget stop response = %+v, want inner model response", got)
	}
	if inner.st.generateCalls.Load() != 1 {
		t.Fatalf("inner model was called %d time(s), want 1", inner.st.generateCalls.Load())
	}
}

func TestBudgetStopModel_PerToolCountResetsAtNewUserTurn(t *testing.T) {
	t.Parallel()
	inner := newScriptedChatModel(makeAssistantToolCall("继续查询", "call_new_turn", "query_promql", `{}`))
	wrapped := wrapBudgetStopModel(inner)
	history := []*schema.Message{schema.UserMessage("上一轮查询")}
	for i := 0; i < maxCallsForTool("query_promql"); i++ {
		history = append(history, schema.ToolMessage(`{"resultType":"vector","result":[]}`, "call_old_"+string(rune('a'+i)), schema.WithToolName("query_promql")))
	}
	history = append(history, schema.UserMessage("新一轮继续查询另一个时间窗"))

	got, err := wrapped.Generate(context.Background(), history)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "query_promql" {
		t.Fatalf("new user turn should have a fresh per-tool budget: %+v", got)
	}
}

func TestBudgetStopModel_PrunesSameBatchRepeatedToolCalls(t *testing.T) {
	t.Parallel()
	calls := make([]schema.ToolCall, 0, maxCallsForTool("query_k8s_snapshot")+3)
	for i := 0; i < maxCallsForTool("query_k8s_snapshot")+3; i++ {
		calls = append(calls, schema.ToolCall{
			ID:       "call_k8s_" + string(rune('a'+i)),
			Function: schema.FunctionCall{Name: "query_k8s_snapshot", Arguments: `{}`},
		})
	}
	inner := newScriptedChatModel(&schema.Message{Role: schema.Assistant, ToolCalls: calls})
	wrapped := wrapBudgetStopModel(inner)
	got, err := wrapped.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("找 Kubernetes 异常 Pod"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != maxCallsForTool("query_k8s_snapshot") {
		t.Fatalf("tool calls = %d, want %d", len(got.ToolCalls), maxCallsForTool("query_k8s_snapshot"))
	}
}

func TestBudgetStopModel_DoesNotApplyAggregateToolBudget(t *testing.T) {
	t.Parallel()
	const distinctTools = 24
	calls := make([]schema.ToolCall, 0, distinctTools)
	for i := 0; i < distinctTools; i++ {
		calls = append(calls, schema.ToolCall{
			ID:       "call_tool_" + string(rune('a'+i)),
			Function: schema.FunctionCall{Name: "tool_" + string(rune('a'+i)), Arguments: `{}`},
		})
	}
	inner := newScriptedChatModel(&schema.Message{Role: schema.Assistant, ToolCalls: calls})
	wrapped := wrapBudgetStopModel(inner)
	got, err := wrapped.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("做一次宽排查"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != distinctTools {
		t.Fatalf("tool calls = %d, want all %d distinct tools", len(got.ToolCalls), distinctTools)
	}
}

func TestBudgetStopModel_PerToolLimitUsesModelSynthesisWithoutRawJSON(t *testing.T) {
	t.Parallel()
	inner := newScriptedChatModel(
		&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:       "call_extra",
			Function: schema.FunctionCall{Name: "query_promql", Arguments: `{}`},
		}}},
		&schema.Message{Role: schema.Assistant, Content: "本周 CPU 均值证据存在，但 Prometheus 保留期不足，无法可靠计算上周对比。"},
	)
	wrapped := wrapBudgetStopModel(inner)
	history := []*schema.Message{schema.UserMessage("对比本周和上周 CPU")}
	for i := 0; i < maxCallsForTool("query_promql"); i++ {
		history = append(history, schema.ToolMessage(`{"resultType":"matrix","result":[{"values":[[1787212275.995,"0.006107340485348943"]]}]}`, "call_prom_"+string(rune('a'+i)), schema.WithToolName("query_promql")))
	}

	got, err := wrapped.Generate(context.Background(), history)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("tool calls = %d, want 0", len(got.ToolCalls))
	}
	if !strings.Contains(got.Content, "Prometheus 保留期不足") {
		t.Fatalf("content missing semantic synthesis: %q", got.Content)
	}
	if strings.Contains(got.Content, "1787212275") || strings.Contains(got.Content, "resultType") {
		t.Fatalf("content leaked raw PromQL evidence: %q", got.Content)
	}
	if inner.generateCalls() != 2 {
		t.Fatalf("model calls = %d, want initial decision plus synthesis", inner.generateCalls())
	}
}

func TestBudgetStopModel_PerToolLimitAllowsDifferentEvidenceTool(t *testing.T) {
	t.Parallel()
	inner := newScriptedChatModel(
		makeAssistantToolCall("继续查询指标", "call_prom_over", "query_promql", `{}`),
		makeAssistantToolCall("改查同期告警", "call_alerts", "query_incidents", `{}`),
	)
	wrapped := wrapBudgetStopModel(inner)
	history := []*schema.Message{schema.UserMessage("做一次跨信号深度排查")}
	for i := 0; i < maxCallsForTool("query_promql"); i++ {
		history = append(history, schema.ToolMessage(`{"resultType":"vector","result":[]}`, "call_metric_"+string(rune('a'+i)), schema.WithToolName("query_promql")))
	}

	got, err := wrapped.Generate(context.Background(), history)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "query_incidents" {
		t.Fatalf("different evidence tool should remain available: %+v", got)
	}
}

func TestBudgetStopModel_FallbackIsLocalizedAndNeverLeaksRawJSON(t *testing.T) {
	t.Parallel()
	retryCall := makeAssistantToolCall("", "call_retry", "query_promql", `{}`)
	inner := newScriptedChatModel(retryCall, retryCall)
	wrapped := wrapBudgetStopModel(inner)
	history := []*schema.Message{
		schema.SystemMessage("Respond in English."),
		schema.UserMessage("Compare CPU week over week"),
	}
	for i := 0; i < maxCallsForTool("query_promql"); i++ {
		history = append(history, schema.ToolMessage(`{"resultType":"matrix","result":[{"values":[[1787212275.995,"0.006107340485348943"]]}]}`, "call_en_"+string(rune('a'+i)), schema.WithToolName("query_promql")))
	}

	got, err := wrapped.Generate(context.Background(), history)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != 0 || !strings.Contains(got.Content, "No raw tool output is shown") {
		t.Fatalf("fallback = %+v", got)
	}
	if strings.Contains(got.Content, "1787212275") || strings.Contains(got.Content, "resultType") {
		t.Fatalf("fallback leaked raw evidence: %q", got.Content)
	}
}

func TestBudgetStopModel_BlocksHostToolAfterEmptyNamedDeviceLookup(t *testing.T) {
	t.Parallel()
	inner := newScriptedChatModel(&schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       "call_processes",
			Function: schema.FunctionCall{Name: "get_host_processes", Arguments: `{"device_ids":[1],"top_n":5}`},
		}},
	})
	wrapped := wrapBudgetStopModel(inner)
	got, err := wrapped.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("找 edge-001 最占内存的进程"),
		schema.ToolMessage(`{"count":0,"items":[]}`, "call_devices", schema.WithToolName("query_devices")),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("tool calls = %d, want blocked", len(got.ToolCalls))
	}
	if !strings.Contains(got.Content, "edge-001") || !strings.Contains(got.Content, "不会自动改用其他在线设备") {
		t.Fatalf("content = %q, want explicit no-fallback clarification", got.Content)
	}
}

func TestBuildReActGraph_NilModelFails(t *testing.T) {
	t.Parallel()
	if _, err := BuildReActGraph(nil, nil, Config{}); err == nil {
		t.Fatalf("expected error for nil model")
	}
}

func TestBuildReActGraph_BadToolSchemaFailsBuild(t *testing.T) {
	t.Parallel()
	scripted := newScriptedChatModel()
	bad := &fakeBaseTool{name: "bad", parameters: `{not json`}
	if _, err := BuildReActGraph(scripted, []basetool.BaseTool{bad}, Config{}); err == nil {
		t.Fatalf("expected build to fail when tool schema parsing fails")
	}
}

func TestAssembleMessages_MentionsAndReminderInline(t *testing.T) {
	t.Parallel()
	in := &Input{
		SystemPrompt:     "agent rules",
		UserText:         "find big files on edge-1",
		MentionsRendered: "- edge-1: device_id=42 (online)",
		WebSearchEnabled: false,
	}
	out, err := assembleMessages(in)
	if err != nil {
		t.Fatalf("assembleMessages: %v", err)
	}
	// system + reminder (user) + user (mentions+text) = 3
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d (%+v)", len(out), out)
	}
	if out[0].Role != schema.System || out[0].Content != "agent rules" {
		t.Errorf("first message should be system: got role=%s content=%q", out[0].Role, out[0].Content)
	}
	reminder := out[1]
	if reminder.Role != schema.User {
		t.Errorf("reminder role = %s, want user", reminder.Role)
	}
	if !strings.HasPrefix(reminder.Content, "<system-reminder>") || !strings.HasSuffix(reminder.Content, "</system-reminder>") {
		t.Errorf("reminder message is not a bare <system-reminder> block: %q", reminder.Content)
	}
	if !strings.Contains(reminder.Content, "web_search 已被关闭") {
		t.Errorf("reminder missing web_search disabled note: %q", reminder.Content)
	}
	if !strings.Contains(reminder.Content, "call_budget_exceeded 只限制当前用户消息") {
		t.Errorf("reminder missing per-turn budget reset note: %q", reminder.Content)
	}
	user := out[2]
	if user.Role != schema.User {
		t.Errorf("last role should be user, got %s", user.Role)
	}
	if strings.Contains(user.Content, "<system-reminder>") {
		t.Errorf("user message must NOT inline the reminder block (it is now a separate message): %q", user.Content)
	}
	if !strings.Contains(user.Content, "edge-1: device_id=42") {
		t.Errorf("user content missing mention block: %q", user.Content)
	}
	if !strings.Contains(user.Content, "find big files on edge-1") {
		t.Errorf("user content missing the actual user text: %q", user.Content)
	}
}

func TestAssembleMessages_AgentReminderAndDynamicHints(t *testing.T) {
	t.Parallel()
	in := &Input{
		SystemPrompt:     "agent rules",
		UserText:         "继续诊断",
		WebSearchEnabled: true,
		AgentReminder:    "结论先于过程，最多列 3 条假设",
		DynamicHints: []string{
			"注意: query_logql 已连续失败 2 次，请换工具或要求用户澄清",
			"已经跑了 22 轮，如不能在下一轮给出最终回答应当总结当前已知信息回答用户",
		},
	}
	out, err := assembleMessages(in)
	if err != nil {
		t.Fatalf("assembleMessages: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 messages got %d", len(out))
	}
	rem := out[1].Content
	if !strings.Contains(rem, "结论先于过程") {
		t.Errorf("reminder missing AgentReminder bullet: %q", rem)
	}
	if !strings.Contains(rem, "query_logql 已连续失败 2 次") {
		t.Errorf("reminder missing dynamic-hint failure bullet: %q", rem)
	}
	if !strings.Contains(rem, "已经跑了 22 轮") {
		t.Errorf("reminder missing dynamic-hint iteration bullet: %q", rem)
	}
	// web_search ON -> the disabled-note bullet must NOT be present.
	if strings.Contains(rem, "web_search 已被关闭") {
		t.Errorf("reminder should not mention web_search disabled when WebSearchEnabled=true: %q", rem)
	}
}

func TestAssembleMessages_LocaleDirective(t *testing.T) {
	t.Parallel()
	// en-US: both the system message and the per-turn reminder must carry
	// an explicit English directive (personas are Chinese).
	en, err := assembleMessages(&Input{SystemPrompt: "agent rules", UserText: "status?", Locale: "en-US"})
	if err != nil {
		t.Fatalf("assembleMessages: %v", err)
	}
	if !strings.Contains(en[0].Content, "agent rules") || !strings.Contains(en[0].Content, "Respond in English") {
		t.Errorf("en system message should append the English directive: %q", en[0].Content)
	}
	if !strings.Contains(en[1].Content, "Respond in English") {
		t.Errorf("en reminder should carry the English directive: %q", en[1].Content)
	}

	// zh-CN: Chinese directive instead.
	zh, _ := assembleMessages(&Input{SystemPrompt: "agent rules", UserText: "状态？", Locale: "zh-CN"})
	if !strings.Contains(zh[0].Content, "用中文回复") {
		t.Errorf("zh system message should append the Chinese directive: %q", zh[0].Content)
	}

	// empty locale: no directive at all (back-compat for non-SPA callers
	// like the IM bridge — system prompt is left exactly as-is).
	none, _ := assembleMessages(&Input{SystemPrompt: "agent rules", UserText: "x"})
	if none[0].Content != "agent rules" {
		t.Errorf("empty locale must not alter the system prompt: %q", none[0].Content)
	}
	if strings.Contains(none[1].Content, "Respond in English") || strings.Contains(none[1].Content, "用中文回复") {
		t.Errorf("empty locale reminder must carry no language directive: %q", none[1].Content)
	}
}

func TestBuildSystemReminder_EmptyHintsTrimmed(t *testing.T) {
	t.Parallel()
	got := buildSystemReminder(&Input{
		WebSearchEnabled: true,
		AgentReminder:    "   ", // all whitespace -> dropped
		DynamicHints:     []string{"", "   ", "real hint"},
	})
	if !strings.Contains(got, "real hint") {
		t.Errorf("real hint missing from reminder: %q", got)
	}
	// AgentReminder was whitespace-only -> no extra empty bullet.
	if strings.Contains(got, "- \n") || strings.Contains(got, "-  \n") {
		t.Errorf("empty bullet leaked: %q", got)
	}
}

func TestAssembleMessages_HistoryReplay(t *testing.T) {
	t.Parallel()
	in := &Input{
		SystemPrompt: "be cool",
		History: []*schema.Message{
			{Role: schema.User, Content: "earlier"},
			{Role: schema.Assistant, Content: "earlier reply"},
		},
		UserText: "follow up",
	}
	out, err := assembleMessages(in)
	if err != nil {
		t.Fatalf("assembleMessages: %v", err)
	}
	// system + 2 history + 1 reminder (user) + 1 user = 5
	if len(out) != 5 {
		t.Fatalf("expected 5 messages got %d", len(out))
	}
	if out[1].Content != "earlier" {
		t.Errorf("history user content lost: %+v", out[1])
	}
	if !strings.HasPrefix(out[3].Content, "<system-reminder>") {
		t.Errorf("expected reminder at index 3: %q", out[3].Content)
	}
	if out[4].Content != "follow up" {
		t.Errorf("user text lost: %q", out[4].Content)
	}
}

func TestAssembleMessages_NilInputFails(t *testing.T) {
	t.Parallel()
	if _, err := assembleMessages(nil); err == nil {
		t.Fatalf("expected error for nil input")
	}
}

func TestConfig_Defaults(t *testing.T) {
	t.Parallel()
	c := Config{}.applyDefaults()
	if c.MaxIterations != 12 {
		t.Errorf("MaxIterations default = %d, want 12", c.MaxIterations)
	}
	if c.ToolTimeout.Seconds() != 15 {
		t.Errorf("ToolTimeout default = %v, want 15s", c.ToolTimeout)
	}
}
