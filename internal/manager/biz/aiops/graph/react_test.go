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
	if !strings.Contains(reminder.Content, "历史里的 call_budget_exceeded") {
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
	if c.MaxIterations != 30 {
		t.Errorf("MaxIterations default = %d, want 30", c.MaxIterations)
	}
	if c.ToolTimeout.Seconds() != 15 {
		t.Errorf("ToolTimeout default = %v, want 15s", c.ToolTimeout)
	}
}
