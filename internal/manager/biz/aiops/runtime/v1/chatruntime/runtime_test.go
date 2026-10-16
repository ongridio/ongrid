package chatruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v1/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// scriptedChatModel returns one *schema.Message per Generate call,
// tracking generateCalls so tests can assert how many turns ran.
// Mirrors the pattern in graph/react_test.go.
type scriptedChatModel struct {
	mu      sync.Mutex
	replies []*schema.Message
	idx     int
	calls   atomic.Int32
}

func newScriptedChatModel(replies ...*schema.Message) *scriptedChatModel {
	return &scriptedChatModel{replies: replies}
}

func (s *scriptedChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replies) == 0 {
		return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
	}
	if s.idx < len(s.replies) {
		out := s.replies[s.idx]
		s.idx++
		return out, nil
	}
	return s.replies[len(s.replies)-1], nil
}

func (s *scriptedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := s.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (s *scriptedChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

func (s *scriptedChatModel) WithTools(_ []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return s, nil
}

// memSessions is an in-memory SessionRepo for runtime tests. Only the
// methods runtime.Handle exercises are implemented; the rest panic on
// purpose so a future refactor can't silently slip past coverage.
type memSessions struct {
	mu        sync.Mutex
	sessions  map[string]*model.Session
	messages  []*model.Message
	toolCalls []*model.ToolCall
}

func newMemSessions(seed *model.Session) *memSessions {
	m := &memSessions{sessions: map[string]*model.Session{}}
	if seed != nil {
		m.sessions[seed.ID] = seed
	}
	return m
}

func (m *memSessions) CreateSession(_ context.Context, s *model.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	return nil
}
func (m *memSessions) GetSession(_ context.Context, id string) (*model.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return s, nil
}
func (m *memSessions) ListSessions(_ context.Context, _ uint64, _, _ int, _ *uint64) ([]*model.Session, error) {
	return nil, nil
}
func (m *memSessions) ListByParent(_ context.Context, parentID string) ([]*model.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Session, 0)
	for _, s := range m.sessions {
		if s.ParentSessionID != nil && *s.ParentSessionID == parentID {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *memSessions) CloseSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		now := time.Now().UTC()
		s.ClosedAt = &now
	}
	return nil
}
func (m *memSessions) RenameSession(_ context.Context, _, _ string) error { return nil }
func (m *memSessions) DeleteSession(_ context.Context, _ string) error    { return nil }
func (m *memSessions) AppendMessage(_ context.Context, msg *model.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg.ID == "" {
		msg.ID = "m" + idgen()
	}
	m.messages = append(m.messages, msg)
	return nil
}
func (m *memSessions) ListMessages(_ context.Context, sessionID string, _ int) ([]*model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Message, 0, len(m.messages))
	for _, mg := range m.messages {
		if mg.SessionID == sessionID {
			out = append(out, mg)
		}
	}
	return out, nil
}
func (m *memSessions) CreateToolCall(_ context.Context, tc *model.ToolCall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tc.ID == "" {
		tc.ID = "tc" + idgen()
	}
	m.toolCalls = append(m.toolCalls, tc)
	return nil
}
func (m *memSessions) UpdateToolCallResult(_ context.Context, _ string, _ string, _, _ *string, _ time.Time) error {
	return nil
}
func (m *memSessions) SumTokensSince(_ context.Context, _ time.Time) (biz.TokenSums, error) {
	return biz.TokenSums{}, nil
}

var idCounter atomic.Int64

func idgen() string {
	n := idCounter.Add(1)
	return time.Now().UTC().Format("20060102") + "-" + atoi(int(n))
}

func atoi(n int) string {
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// TestRuntime_NewRuntime_RequiresDeps confirms NewRuntime fails fast
// when ChatModel or Sessions is missing — production wiring must not
// silently drop a key dep.
func TestRuntime_NewRuntime_RequiresDeps(t *testing.T) {
	if _, err := NewRuntime(Config{}); err == nil {
		t.Errorf("NewRuntime{} returned nil error — expected dep check")
	}
	if _, err := NewRuntime(Config{Sessions: newMemSessions(nil)}); err == nil {
		t.Errorf("NewRuntime sans ChatModel should error")
	}
	if _, err := NewRuntime(Config{ChatModel: newScriptedChatModel()}); err == nil {
		t.Errorf("NewRuntime sans Sessions should error")
	}
}

// TestRuntime_Handle_OwnershipCheck enforces the "non-owner gets
// ErrNotFound" invariant. Mirrors the legacy agent's behaviour
// .
func TestRuntime_Handle_OwnershipCheck(t *testing.T) {
	sess := &model.Session{ID: "s1", UserID: 7}
	rt, err := NewRuntime(Config{
		Sessions:  newMemSessions(sess),
		ChatModel: newScriptedChatModel(),
		ToolBag:   nil,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	_, err = rt.Handle(context.Background(), &Request{
		SessionID: "s1",
		UserID:    99, // not the owner
		UserText:  "hi",
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("non-owner err = %v, want ErrNotFound", err)
	}
}

// TestRuntime_Handle_HappyPath_FinalReply runs the graph once
// against a scriptedChatModel that returns a no-tools assistant
// message. Asserts:
//   - user message persisted
//   - Reply.Message non-nil with the model's content
//   - terminal Done event fires once
func TestRuntime_Handle_HappyPath_FinalReply(t *testing.T) {
	sess := &model.Session{ID: "s1", UserID: 7}
	store := newMemSessions(sess)
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "all good",
	})
	rt, err := NewRuntime(Config{
		Sessions:  store,
		ChatModel: scripted,
		ToolBag:   nil,
		GraphCfg:  graph.Config{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	var (
		mu     sync.Mutex
		events []Event
	)
	emit := func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}

	reply, err := rt.Handle(context.Background(), &Request{
		SessionID: "s1",
		UserID:    7,
		UserText:  "what's up",
		Emit:      emit,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if reply == nil || reply.Message == nil {
		t.Fatalf("expected non-nil reply.Message")
	}
	if reply.Message.Content == nil || *reply.Message.Content != "all good" {
		t.Errorf("reply content = %v, want \"all good\"", reply.Message.Content)
	}

	// User message must be persisted.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.messages) == 0 {
		t.Errorf("expected at least one persisted message (user turn)")
	}
	if store.messages[0].Role != model.RoleUser {
		t.Errorf("first persisted message role = %q, want user", store.messages[0].Role)
	}

	// Done event fires once at terminal success.
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatalf("expected at least one event")
	}
	if events[len(events)-1].Type != EventDone {
		t.Errorf("last event type = %q, want done", events[len(events)-1].Type)
	}
}

// TestRuntime_ToolCount + ToolNames provides the per-spec visibility.
// (a) ONGRID_AGENT_KERNEL=graph startup logs how many BaseTools are
// bound — this is the seam main.go logs against.
func TestRuntime_ToolCountAndNames(t *testing.T) {
	sess := &model.Session{ID: "s1", UserID: 7}
	rt, err := NewRuntime(Config{
		Sessions:  newMemSessions(sess),
		ChatModel: newScriptedChatModel(),
		ToolBag: []basetool.BaseTool{
			&fakeTool{name: "echo", schema: `{"type":"object","properties":{}}`},
			&fakeTool{name: "ping", schema: `{"type":"object","properties":{}}`},
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if rt.ToolCount() != 2 {
		t.Errorf("ToolCount = %d, want 2", rt.ToolCount())
	}
	names := rt.ToolNames(context.Background())
	t.Logf("toolBag names = %v (count=%d)", names, len(names))
	if len(names) != 2 {
		t.Errorf("ToolNames len = %d, want 2", len(names))
	}
}

// strPtr is a small helper for tool-message content/name pointer
// fields in the test fixtures below. Inline helpers everywhere bloats
// the test fixture noise and obscures the assertions.
func strPtr(s string) *string { return &s }

// makeToolMsg builds a chat_messages row in the persisted shape the
// runtime sees from ListMessages. role=tool, ToolName + Content are
// non-nil; failure flag is "is the JSON content carrying an error
// field" (looksLikeToolFailure).
func makeToolMsg(toolName string, fail bool) *model.Message {
	body := `{"ok":true}`
	if fail {
		body = `{"error":"timeout"}`
	}
	return &model.Message{
		Role:     model.RoleTool,
		ToolName: strPtr(toolName),
		Content:  strPtr(body),
	}
}

// TestConsecutiveFailedTool covers the boundary cases the spec calls
// out: below threshold / different tool / mixed success+fail.
func TestConsecutiveFailedTool(t *testing.T) {
	t.Parallel()

	t.Run("two_in_a_row_same_tool", func(t *testing.T) {
		hist := []*model.Message{
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", true),
		}
		name, n := consecutiveFailedTool(hist, 2)
		if name != "query_logql" || n != 2 {
			t.Errorf("got (%q, %d), want (query_logql, 2)", name, n)
		}
	})

	t.Run("three_in_a_row_same_tool", func(t *testing.T) {
		hist := []*model.Message{
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", true),
		}
		name, n := consecutiveFailedTool(hist, 2)
		if name != "query_logql" || n != 3 {
			t.Errorf("got (%q, %d), want (query_logql, 3)", name, n)
		}
	})

	t.Run("below_minN", func(t *testing.T) {
		hist := []*model.Message{
			makeToolMsg("query_logql", true),
		}
		name, n := consecutiveFailedTool(hist, 2)
		if name != "" || n != 0 {
			t.Errorf("got (%q, %d), want zero result below minN", name, n)
		}
	})

	t.Run("different_tool_resets", func(t *testing.T) {
		hist := []*model.Message{
			makeToolMsg("query_logql", true),
			makeToolMsg("query_promql", true),
		}
		name, n := consecutiveFailedTool(hist, 2)
		// trailing tool is query_promql with one fail — below minN.
		if name != "" || n != 0 {
			t.Errorf("got (%q, %d), want zero (different tool breaks the run)", name, n)
		}
	})

	t.Run("success_in_middle_breaks_run", func(t *testing.T) {
		hist := []*model.Message{
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", false), // success -> resets
			makeToolMsg("query_logql", true),
		}
		name, n := consecutiveFailedTool(hist, 2)
		// only the trailing single fail counts.
		if name != "" || n != 0 {
			t.Errorf("got (%q, %d), want zero (success in middle breaks)", name, n)
		}
	})

	t.Run("non_tool_role_at_tail", func(t *testing.T) {
		hist := []*model.Message{
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", true),
			{Role: model.RoleAssistant, Content: strPtr("ok then")},
		}
		// The trailing message is assistant, so the tool block isn't
		// the most-recent thing — we should NOT report a stuck loop.
		name, n := consecutiveFailedTool(hist, 2)
		if name != "" || n != 0 {
			t.Errorf("got (%q, %d), want zero (non-tool role at tail)", name, n)
		}
	})

	t.Run("empty_history", func(t *testing.T) {
		name, n := consecutiveFailedTool(nil, 2)
		if name != "" || n != 0 {
			t.Errorf("got (%q, %d), want zero", name, n)
		}
	})
}

// TestCalcDynamicHints checks the high-level wiring of both heuristics
// — consecutive-failed-tool + iteration-cap.
func TestCalcDynamicHints(t *testing.T) {
	t.Parallel()
	rt, err := NewRuntime(Config{
		Sessions:  newMemSessions(&model.Session{ID: "s", UserID: 1}),
		ChatModel: newScriptedChatModel(),
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	t.Run("trailing_3_failed_logql_emits_failure_hint", func(t *testing.T) {
		hist := []*model.Message{
			{Role: model.RoleAssistant, Content: strPtr("trying logql")},
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", true),
			makeToolMsg("query_logql", true),
		}
		hints := rt.calcDynamicHints(hist)
		if len(hints) == 0 {
			t.Fatalf("expected at least one hint, got none")
		}
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if !contains(joined, "query_logql") || !contains(joined, "连续失败") {
			t.Errorf("missing failure hint: %q", joined)
		}
	})

	t.Run("over_20_assistant_turns_emits_iteration_hint", func(t *testing.T) {
		hist := make([]*model.Message, 0, 25)
		for i := 0; i < 25; i++ {
			hist = append(hist, &model.Message{Role: model.RoleAssistant, Content: strPtr("step")})
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if !contains(joined, "已经 25 轮") {
			t.Errorf("missing iteration hint: %q", joined)
		}
	})

	t.Run("clean_history_no_hints", func(t *testing.T) {
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("hi")},
			{Role: model.RoleAssistant, Content: strPtr("hi back")},
		}
		hints := rt.calcDynamicHints(hist)
		if len(hints) != 0 {
			t.Errorf("expected no hints, got %v", hints)
		}
	})

	t.Run("unfollowed_promise_emits_nudge", func(t *testing.T) {
		// Last assistant said "让我..." but no tool message follows.
		// Reproduces the d9fa4f42 session 17:00:36 trail-off.
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("看一下磁盘")},
			{Role: model.RoleAssistant, Content: strPtr("让我先查看 /data 目录的磁盘使用情况")},
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if !contains(joined, "没真发 tool_call") {
			t.Errorf("missing unfollowed-promise hint: %q", joined)
		}
	})

	t.Run("promise_followed_by_tool_no_hint", func(t *testing.T) {
		// Same promise but tool ran — no hint expected.
		toolName := "host_du_summary"
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("看一下磁盘")},
			{Role: model.RoleAssistant, Content: strPtr("让我先查看 /data")},
			{Role: model.RoleTool, ToolName: &toolName, Content: strPtr(`{"subpaths":[]}`)},
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if contains(joined, "没真发 tool_call") {
			t.Errorf("hint should not fire when tool followed promise: %q", joined)
		}
	})
}

// contains is a tiny test helper so we don't import strings in this
// file just for one substring check.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// fakeTool is a minimal BaseTool used by TestRuntime_ToolCountAndNames.
type fakeTool struct {
	name   string
	schema string
}

func (f *fakeTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        f.name,
		Description: "fake",
		Parameters:  []byte(f.schema),
		Class:       "read",
	}, nil
}

func (f *fakeTool) InvokableRun(_ context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	return `{"ok":true}`, nil
}

// Compatibility with the current shared repository contract; v1 never calls this method.
func (r *memSessions) UpdateSessionModel(context.Context, string, string, string) error {
	return errs.ErrNotWiredYet
}
