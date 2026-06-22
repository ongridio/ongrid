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
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph/callbacks"
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
func (m *memSessions) UpdateToolCallResult(_ context.Context, id string, status string, resultJSON, errStr *string, endedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, tc := range m.toolCalls {
		if tc.ID != id {
			continue
		}
		tc.Status = status
		tc.ResultJSON = resultJSON
		tc.Error = errStr
		tc.EndedAt = &endedAt
		return nil
	}
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

func TestRuntime_Handle_ConfirmedConfigDraft_AppliesWithoutLLM(t *testing.T) {
	sess := &model.Session{ID: "s1", UserID: 7}
	store := newMemSessions(sess)
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "should not run",
	})
	apply := &captureApplyTool{
		resp: `{"kind":"config_apply_result","domain":"alert_rule","action":"create","status":"applied","resource_id":42,"resource":{"name":"MySQL 连接使用率过高预警","type":"alert_rule"}}`,
	}
	rt, err := NewRuntime(Config{
		Sessions:  store,
		ChatModel: scripted,
		ToolBag:   []basetool.BaseTool{apply},
		GraphCfg:  graph.Config{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	payload := `{"action":"create","draft_id":"draft-1","rule":{"rule_key":"mysql_connection_usage_high","name":"MySQL 连接使用率过高预警","kind":"metric_raw","severity":"warning","spec":{"expr":"up > 0","for":"5m"}}}`
	userText := "确认创建这条告警规则\n" +
		"domain: alert_rule\n" +
		"action: create\n" +
		"apply_tool: apply_config_change\n" +
		"draft_hash: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"payload:\n```json\n" + payload + "\n```"

	var events []Event
	reply, err := rt.Handle(context.Background(), &Request{
		SessionID: sess.ID,
		UserID:    sess.UserID,
		Role:      "admin",
		UserText:  userText,
		Emit: func(ev Event) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if scripted.calls.Load() != 0 {
		t.Fatalf("LLM calls = %d, want 0 for deterministic confirm apply", scripted.calls.Load())
	}
	if apply.calls.Load() != 1 {
		t.Fatalf("apply_config_change calls = %d, want 1", apply.calls.Load())
	}
	argsValue := apply.args.Load()
	if argsValue == nil {
		t.Fatalf("captured apply args is nil")
	}
	argsJSON, ok := argsValue.(string)
	if !ok {
		t.Fatalf("captured apply args type = %T, want string", argsValue)
	}
	for _, want := range []string{
		`"domain":"alert_rule"`,
		`"action":"create"`,
		`"confirmed":true`,
		`"draft_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		`"payload":{`,
		`"rule_key":"mysql_connection_usage_high"`,
	} {
		if !contains(argsJSON, want) {
			t.Fatalf("apply args missing %q: %s", want, argsJSON)
		}
	}
	if reply == nil || reply.Message == nil || reply.Message.Content == nil {
		t.Fatalf("expected persisted assistant reply")
	}
	if !contains(*reply.Message.Content, "已确认并创建告警规则") || !contains(*reply.Message.Content, "ID: 42") {
		t.Fatalf("reply content = %q", *reply.Message.Content)
	}

	var sawStart, sawEnd bool
	for _, ev := range events {
		if ev.Tool == nil || ev.Tool.Name != applyConfigChangeToolName {
			continue
		}
		switch ev.Type {
		case EventToolStart:
			sawStart = true
			if ev.Tool.Status != "running" {
				t.Fatalf("tool_start status = %q, want running", ev.Tool.Status)
			}
		case EventToolEnd:
			sawEnd = true
			if ev.Tool.Status != "success" {
				t.Fatalf("tool_end status = %q, want success", ev.Tool.Status)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing apply tool events: sawStart=%v sawEnd=%v events=%v", sawStart, sawEnd, events)
	}
	if len(store.messages) != 2 {
		t.Fatalf("persisted messages = %d, want user + assistant", len(store.messages))
	}
}

func TestRuntime_Handle_PlainOKAppliesLatestConfigDraftWithoutLLM(t *testing.T) {
	sess := &model.Session{ID: "s1", UserID: 7}
	store := newMemSessions(sess)
	payload := `{"action":"create","draft_id":"draft-plain-ok","rule":{"rule_key":"mysql_slow_queries_surge","name":"MySQL 慢查询异常增多","kind":"metric_raw","severity":"warning","spec":{"expr":"rate(mysql_global_status_slow_queries[5m]) > 0.5","for":"5m"}}}`
	draftResult := `{"kind":"config_draft","domain":"alert_rule","action":"create","draft_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","payload":` + payload + `}`
	store.messages = append(store.messages,
		&model.Message{ID: "m-user", SessionID: sess.ID, Role: model.RoleUser, Content: strPtr("创建 MySQL 慢查询告警"), CreatedAt: time.Now().Add(-3 * time.Minute)},
		&model.Message{ID: "m-tool", SessionID: sess.ID, Role: model.RoleTool, ToolName: strPtr("draft_config_change"), Content: strPtr(draftResult), CreatedAt: time.Now().Add(-2 * time.Minute)},
		&model.Message{ID: "m-assistant", SessionID: sess.ID, Role: model.RoleAssistant, Content: strPtr("草稿已生成，确认后创建。"), CreatedAt: time.Now().Add(-time.Minute)},
	)
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "should not run",
	})
	apply := &captureApplyTool{
		resp: `{"kind":"config_apply_result","domain":"alert_rule","action":"create","status":"applied","resource_id":95,"resource":{"name":"MySQL 慢查询异常增多","type":"alert_rule"}}`,
	}
	rt, err := NewRuntime(Config{
		Sessions:  store,
		ChatModel: scripted,
		ToolBag:   []basetool.BaseTool{apply},
		GraphCfg:  graph.Config{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	reply, err := rt.Handle(context.Background(), &Request{
		SessionID: sess.ID,
		UserID:    sess.UserID,
		Role:      "admin",
		UserText:  "ok",
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if scripted.calls.Load() != 0 {
		t.Fatalf("LLM calls = %d, want 0 for plain confirmation apply", scripted.calls.Load())
	}
	if apply.calls.Load() != 1 {
		t.Fatalf("apply_config_change calls = %d, want 1", apply.calls.Load())
	}
	argsValue := apply.args.Load()
	if argsValue == nil {
		t.Fatalf("captured apply args is nil")
	}
	argsJSON, ok := argsValue.(string)
	if !ok {
		t.Fatalf("captured apply args type = %T, want string", argsValue)
	}
	for _, want := range []string{
		`"confirmed":true`,
		`"draft_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`,
		`"draft_id":"draft-plain-ok"`,
		`"confirmation_text":"ok"`,
	} {
		if !contains(argsJSON, want) {
			t.Fatalf("apply args missing %q: %s", want, argsJSON)
		}
	}
	if reply == nil || reply.Message == nil || reply.Message.Content == nil {
		t.Fatalf("expected persisted assistant reply")
	}
	if !contains(*reply.Message.Content, "已确认并创建告警规则") || !contains(*reply.Message.Content, "ID: 95") {
		t.Fatalf("reply content = %q", *reply.Message.Content)
	}
}

func TestLatestConfigDraftApplyArgs_StopsAtAppliedResult(t *testing.T) {
	payload := `{"action":"create","draft_id":"draft-old","rule":{"rule_key":"old","name":"Old","kind":"metric_raw","severity":"warning","spec":{"expr":"up > 0","for":"5m"}}}`
	draftResult := `{"kind":"config_draft","domain":"alert_rule","action":"create","draft_hash":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","payload":` + payload + `}`
	applyResult := `{"kind":"config_apply_result","domain":"alert_rule","action":"create","status":"applied","resource_id":95}`

	args, err, ok := latestConfigDraftApplyArgs([]*model.Message{
		{Role: model.RoleUser, Content: strPtr("创建旧告警")},
		{Role: model.RoleTool, Content: strPtr(draftResult)},
		{Role: model.RoleUser, Content: strPtr("ok")},
		{Role: model.RoleTool, Content: strPtr(applyResult)},
		{Role: model.RoleUser, Content: strPtr("ok")},
	}, "ok")
	if err != nil {
		t.Fatalf("latestConfigDraftApplyArgs: %v", err)
	}
	if ok || args != "" {
		t.Fatalf("latestConfigDraftApplyArgs returned (%q, %v), want no pending draft", args, ok)
	}
}

func TestLatestConfigDraftApplyArgs_DoesNotCrossPreviousUserTurn(t *testing.T) {
	payload := `{"action":"create","draft_id":"draft-stale","rule":{"rule_key":"stale","name":"Stale","kind":"metric_raw","severity":"warning","spec":{"expr":"up > 0","for":"5m"}}}`
	draftResult := `{"kind":"config_draft","domain":"alert_rule","action":"create","draft_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","payload":` + payload + `}`

	args, err, ok := latestConfigDraftApplyArgs([]*model.Message{
		{Role: model.RoleUser, Content: strPtr("创建旧告警")},
		{Role: model.RoleTool, Content: strPtr(draftResult)},
		{Role: model.RoleAssistant, Content: strPtr("草稿已生成，确认后创建。")},
		{Role: model.RoleUser, Content: strPtr("先问另一个问题")},
		{Role: model.RoleAssistant, Content: strPtr("好的。")},
		{Role: model.RoleUser, Content: strPtr("ok")},
	}, "ok")
	if err != nil {
		t.Fatalf("latestConfigDraftApplyArgs: %v", err)
	}
	if ok || args != "" {
		t.Fatalf("latestConfigDraftApplyArgs returned (%q, %v), want no stale draft across user turn", args, ok)
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

	t.Run("new_alert_request_after_many_created_rules_no_repeat_tool_hint", func(t *testing.T) {
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("创建 CPU 告警")},
			makeToolMsg("list_metric_catalog", false),
			makeToolMsg("draft_config_change", false),
			{Role: model.RoleAssistant, Content: strPtr("草稿已生成")},
			{Role: model.RoleUser, Content: strPtr("ok")},
			{Role: model.RoleAssistant, Content: strPtr("已确认并创建告警规则")},
			{Role: model.RoleUser, Content: strPtr("创建 PostgreSQL 告警")},
			makeToolMsg("list_metric_catalog", false),
			makeToolMsg("draft_config_change", false),
			{Role: model.RoleAssistant, Content: strPtr("草稿已生成")},
			{Role: model.RoleUser, Content: strPtr("ok")},
			{Role: model.RoleAssistant, Content: strPtr("已确认并创建告警规则")},
			{Role: model.RoleUser, Content: strPtr("创建 Redis 告警：缓存命中率偏低时提醒我")},
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if contains(joined, "已重复调用") || contains(joined, "不要再调用同款工具") {
			t.Errorf("new alert request should not inherit old repeat-tool hint: %q", joined)
		}
	})

	t.Run("previous_turn_repeated_tool_still_emits_hint", func(t *testing.T) {
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("继续排查日志")},
			makeToolMsg("query_logql", false),
			makeToolMsg("query_logql", false),
			makeToolMsg("query_logql", false),
			{Role: model.RoleAssistant, Content: strPtr("还是没有更多结论")},
			{Role: model.RoleUser, Content: strPtr("继续")},
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if !contains(joined, "query_logql") || !contains(joined, "已重复调用") {
			t.Errorf("missing previous-turn repeat-tool hint: %q", joined)
		}
	})

	t.Run("alert_draft_guard_block_then_create_request_emits_draft_retry_hint", func(t *testing.T) {
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("为 MySQL 创建连接数超过 85% 的告警")},
			{Role: model.RoleAssistant, Content: strPtr(callbacks.AlertDraftGuardBlockedMessage)},
			{Role: model.RoleUser, Content: strPtr("为 MySQL 创建一条告警，连接数达到 max_connections 的 85% 且持续 5 分钟触发 Warning")},
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if !contains(joined, "draft_config_change") || !contains(joined, "不要要求用户重新发送") {
			t.Errorf("missing alert draft retry hint: %q", joined)
		}
	})

	t.Run("alert_draft_guard_block_then_why_question_no_draft_retry_hint", func(t *testing.T) {
		hist := []*model.Message{
			{Role: model.RoleUser, Content: strPtr("为 MySQL 创建连接数超过 85% 的告警")},
			{Role: model.RoleAssistant, Content: strPtr(callbacks.AlertDraftGuardBlockedMessage)},
			{Role: model.RoleUser, Content: strPtr("为什么要这样提示，不能直接创建 draft 吗")},
		}
		hints := rt.calcDynamicHints(hist)
		joined := ""
		for _, h := range hints {
			joined += h + "\n"
		}
		if contains(joined, "draft_config_change") {
			t.Errorf("why-question should not trigger draft retry hint: %q", joined)
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

type captureApplyTool struct {
	resp  string
	calls atomic.Int32
	args  atomic.Value
}

func (t *captureApplyTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        applyConfigChangeToolName,
		Description: "fake apply",
		Parameters:  []byte(`{"type":"object","properties":{}}`),
		Class:       "write",
	}, nil
}

func (t *captureApplyTool) InvokableRun(_ context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	t.calls.Add(1)
	t.args.Store(argsJSON)
	return t.resp, nil
}

// TestBuildEinoHistory_DropsOrphanToolMessage reproduces the .91 session
// eff73a55 corruption: parallel tools (get_host_processes, query_promql)
// completed out of issue-order, so query_promql's real response was
// persisted under the synthetic id "query_promql|einoToolAdapter" while
// an autoheal stub filled the real call_B slot. The assistant turn then
// survives the completeness precheck (both real ids have a row), but the
// orphan synthetic-id row used to be emitted bare in natural order →
// provider 400 "Messages with role 'tool' must be a response to a
// preceding message with 'tool_calls'". buildEinoHistory must drop it.
func TestBuildEinoHistory_DropsOrphanToolMessage(t *testing.T) {
	callA, callB := "call_00_aaa", "call_01_bbb"
	asst := &model.Message{
		ID:      "asst-1",
		Role:    model.RoleAssistant,
		Content: strPtr("checking host + metrics"),
		ToolCalls: []model.ToolCall{
			{ToolName: "get_host_processes", LLMCallID: strPtr(callA), ArgumentsJSON: "{}"},
			{ToolName: "query_promql", LLMCallID: strPtr(callB), ArgumentsJSON: "{}"},
		},
	}
	rows := []*model.Message{
		{ID: "u0", Role: model.RoleUser, Content: strPtr("load + cpu?")},
		asst,
		// real get_host_processes response (correct id)
		{ID: "t-a", Role: model.RoleTool, ToolCallID: strPtr(callA), ToolName: strPtr("get_host_processes"), Content: strPtr(`{"procs":[]}`)},
		// ORPHAN: query_promql's real response stamped with the synthetic
		// adapter id after the out-of-order completion.
		{ID: "t-orphan", Role: model.RoleTool, ToolCallID: strPtr("query_promql|einoToolAdapter"), ToolName: strPtr("query_promql"), Content: strPtr(`{"resultType":"matrix"}`)},
		// autoheal stub filling the real call_B slot.
		{ID: "t-b", Role: model.RoleTool, ToolCallID: strPtr(callB), ToolName: strPtr("query_promql"), Content: strPtr(`{"error":"tool response was not persisted","autoheal":true}`)},
		{ID: "u1", Role: model.RoleUser, Content: strPtr("1+2")},
	}

	out := buildEinoHistory(rows)

	// Every tool message must carry an id that the assistant actually
	// emitted — no orphan synthetic-id row survives.
	valid := map[string]bool{callA: true, callB: true}
	toolCount := 0
	for k, msg := range out {
		if msg.Role != schema.RoleType(model.RoleTool) {
			continue
		}
		toolCount++
		if !valid[msg.ToolCallID] {
			t.Errorf("orphan tool message survived: id=%q at %d", msg.ToolCallID, k)
		}
		// A tool message must be preceded (somewhere before) by an
		// assistant carrying a matching tool_call id.
		if k == 0 || out[k-1].Role == schema.RoleType(model.RoleUser) {
			t.Errorf("tool message at %d not preceded by an assistant/tool", k)
		}
	}
	if toolCount != 2 {
		t.Errorf("emitted %d tool messages, want 2 (callA + callB stub)", toolCount)
	}
	// The assistant slot must be present with both tool_calls.
	var sawAsst bool
	for _, msg := range out {
		if msg.Role == schema.RoleType(model.RoleAssistant) && len(msg.ToolCalls) == 2 {
			sawAsst = true
		}
	}
	if !sawAsst {
		t.Error("assistant turn with 2 tool_calls missing from replay")
	}
}

func TestBuildEinoHistory_SanitizesExpiredToolBudgetResult(t *testing.T) {
	callID := "call_budget_1"
	rows := []*model.Message{
		{ID: "u0", Role: model.RoleUser, Content: strPtr("创建数据库连接告警")},
		{
			ID:      "asst-1",
			Role:    model.RoleAssistant,
			Content: strPtr("checking metric catalog"),
			ToolCalls: []model.ToolCall{
				{ToolName: "list_metric_catalog", LLMCallID: strPtr(callID), ArgumentsJSON: `{"query":"mysql connection usage"}`},
			},
		},
		{
			ID:         "t-budget",
			Role:       model.RoleTool,
			ToolCallID: strPtr(callID),
			ToolName:   strPtr("list_metric_catalog"),
			Content:    strPtr(`{"status":"call_budget_exceeded","tool":"list_metric_catalog","calls":1,"instruction":"You have already called \"list_metric_catalog\" 1 times this turn. Do NOT call it again."}`),
		},
		{ID: "u1", Role: model.RoleUser, Content: strPtr("继续创建")},
	}

	out := buildEinoHistory(rows)

	var toolMsg *schema.Message
	for _, msg := range out {
		if msg.Role == schema.RoleType(model.RoleTool) {
			toolMsg = msg
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("expected replayed tool message")
	}
	if toolMsg.ToolCallID != callID {
		t.Fatalf("tool_call_id = %q, want %q", toolMsg.ToolCallID, callID)
	}
	if contains(toolMsg.Content, "Do NOT call it again") {
		t.Fatalf("stale current-turn directive survived replay: %q", toolMsg.Content)
	}
	if !contains(toolMsg.Content, `"scope":"expired_previous_turn"`) {
		t.Fatalf("expired scope missing from replay content: %q", toolMsg.Content)
	}
	if !contains(toolMsg.Content, "may be called again in the current user turn") {
		t.Fatalf("replay content should tell the model the budget expired: %q", toolMsg.Content)
	}
}
