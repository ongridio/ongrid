package callbacks

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeSessionRepo is a goroutine-safe in-memory SessionRepo used by the
// persistence handler tests. Only the methods PersistenceHandler calls
// are exercised; the rest are no-ops to satisfy the interface.
type fakeSessionRepo struct {
	mu        sync.Mutex
	messages  []*model.Message
	toolCalls map[string]*model.ToolCall
	nextID    int

	failAppend             bool
	failCreate             bool
	failUpdate             bool
	respectCanceledContext bool
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{toolCalls: map[string]*model.ToolCall{}}
}

func (r *fakeSessionRepo) CreateSession(context.Context, *model.Session) error {
	return errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) GetSession(context.Context, string) (*model.Session, error) {
	return nil, errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) ListSessions(context.Context, uint64, int, int, *uint64) ([]*model.Session, error) {
	return nil, errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) ListByParent(context.Context, string) ([]*model.Session, error) {
	return nil, errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) RenameSession(context.Context, string, string) error { return nil }
func (r *fakeSessionRepo) CloseSession(context.Context, string) error {
	return errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) DeleteSession(context.Context, string) error {
	return errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) ctxErr(ctx context.Context) error {
	if !r.respectCanceledContext || ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (r *fakeSessionRepo) AppendMessage(ctx context.Context, m *model.Message) error {
	if err := r.ctxErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failAppend {
		return errors.New("inject: append")
	}
	r.nextID++
	if m.ID == "" {
		m.ID = "msg-" + strconv.Itoa(r.nextID)
	}
	cp := *m
	r.messages = append(r.messages, &cp)
	return nil
}
func (r *fakeSessionRepo) ListMessages(context.Context, string, int) ([]*model.Message, error) {
	return nil, errs.ErrNotWiredYet
}
func (r *fakeSessionRepo) CreateToolCall(ctx context.Context, tc *model.ToolCall) error {
	if err := r.ctxErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failCreate {
		return errors.New("inject: create")
	}
	r.nextID++
	if tc.ID == "" {
		tc.ID = "tc-" + strconv.Itoa(r.nextID)
	}
	cp := *tc
	r.toolCalls[tc.ID] = &cp
	return nil
}
func (r *fakeSessionRepo) UpdateToolCallResult(ctx context.Context, id, status string, resultJSON, errStr *string, endedAt time.Time) error {
	if err := r.ctxErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failUpdate {
		return errors.New("inject: update")
	}
	tc, ok := r.toolCalls[id]
	if !ok {
		return errs.ErrNotFound
	}
	tc.Status = status
	tc.ResultJSON = resultJSON
	tc.Error = errStr
	tc.EndedAt = &endedAt
	return nil
}
func (r *fakeSessionRepo) SumTokensSince(context.Context, time.Time) (biz.TokenSums, error) {
	return biz.TokenSums{}, nil
}

var _ biz.SessionRepo = (*fakeSessionRepo)(nil)

func chatModelInfo() *callbacks.RunInfo {
	return &callbacks.RunInfo{Name: "ChatModel", Type: "Test", Component: components.ComponentOfChatModel}
}

func toolInfo(name string) *callbacks.RunInfo {
	return &callbacks.RunInfo{Name: name, Type: "Test", Component: components.ComponentOfTool}
}

func TestPersistenceHandler_NewNilDeps(t *testing.T) {
	t.Parallel()
	if NewPersistenceHandler(PersistenceDeps{}) != nil {
		t.Fatalf("nil session id should yield nil handler")
	}
	if NewPersistenceHandler(PersistenceDeps{SessionID: "s"}) != nil {
		t.Fatalf("nil repo should yield nil handler")
	}
}

func TestPersistenceHandler_AssistantWriteOnEnd(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "sess-1", Repo: repo})
	if h == nil {
		t.Fatalf("handler is nil")
	}
	out := &einomodel.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "hello"},
		TokenUsage: &einomodel.TokenUsage{
			PromptTokens:     5,
			CompletionTokens: 3,
			TotalTokens:      8,
		},
	}
	h.OnEnd(context.Background(), chatModelInfo(), out)
	if got := h.AssistantWriteCount(); got != 1 {
		t.Errorf("assistant writes = %d, want 1", got)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.messages) != 1 {
		t.Fatalf("messages persisted = %d, want 1", len(repo.messages))
	}
	row := repo.messages[0]
	if row.SessionID != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", row.SessionID)
	}
	if row.Role != string(schema.Assistant) {
		t.Errorf("role = %q, want assistant", row.Role)
	}
	if row.Content == nil || *row.Content != "hello" {
		t.Errorf("content = %v, want hello", row.Content)
	}
	if row.PromptTokens == nil || *row.PromptTokens != 5 {
		t.Errorf("prompt tokens = %v, want 5", row.PromptTokens)
	}
}

func TestPersistenceHandler_ToolStartEndCycle(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "sess-1", Repo: repo})
	ctx := WithToolCallID(WithMessageID(context.Background(), "msg-1"), "call-1")

	// Start
	h.OnStart(ctx, toolInfo("query_promql"), &einotool.CallbackInput{ArgumentsInJSON: `{"query":"up"}`})

	repo.mu.Lock()
	if len(repo.toolCalls) != 1 {
		repo.mu.Unlock()
		t.Fatalf("expected 1 pending tool_call after OnStart, got %d", len(repo.toolCalls))
	}
	var tcID string
	for k, tc := range repo.toolCalls {
		tcID = k
		if tc.Status != model.StatusPending {
			t.Errorf("tool_call.status = %q, want pending", tc.Status)
		}
		if tc.ToolName != "query_promql" {
			t.Errorf("tool_name = %q, want query_promql", tc.ToolName)
		}
		if tc.ArgumentsJSON != `{"query":"up"}` {
			t.Errorf("arguments_json = %q", tc.ArgumentsJSON)
		}
		if tc.MessageID != "msg-1" {
			t.Errorf("message_id = %q, want msg-1", tc.MessageID)
		}
	}
	repo.mu.Unlock()
	_ = tcID

	// End (success)
	h.OnEnd(ctx, toolInfo("query_promql"), &einotool.CallbackOutput{Response: `{"ok":true}`})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	tc := repo.toolCalls[tcID]
	if tc.Status != model.StatusSuccess {
		t.Errorf("status after success = %q, want success", tc.Status)
	}
	if tc.ResultJSON == nil || *tc.ResultJSON != `{"ok":true}` {
		t.Errorf("result_json = %v", tc.ResultJSON)
	}
	// chat_messages role=tool was appended
	if len(repo.messages) != 1 {
		t.Fatalf("expected 1 message (role=tool) after success, got %d", len(repo.messages))
	}
	if repo.messages[0].Role != model.RoleTool {
		t.Errorf("appended role = %q, want tool", repo.messages[0].Role)
	}
}

func TestPersistenceHandler_ToolEndPersistsAfterRequestContextCanceled(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	repo.respectCanceledContext = true
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "sess-1", Repo: repo})
	ctx := WithToolCallID(WithMessageID(context.Background(), "msg-1"), "call-canceled")

	h.OnStart(ctx, toolInfo("draft_config_change"), &einotool.CallbackInput{ArgumentsInJSON: `{"rule_key":"r1"}`})
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	h.OnEnd(canceledCtx, toolInfo("draft_config_change"), &einotool.CallbackOutput{Response: `{"kind":"config_draft"}`})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.toolCalls) != 1 {
		t.Fatalf("toolCalls count = %d, want 1", len(repo.toolCalls))
	}
	for _, tc := range repo.toolCalls {
		if tc.Status != model.StatusSuccess {
			t.Fatalf("tool_call.status = %q, want success", tc.Status)
		}
		if tc.ResultJSON == nil || *tc.ResultJSON != `{"kind":"config_draft"}` {
			t.Fatalf("tool_call.result_json = %v, want config draft response", tc.ResultJSON)
		}
	}
	if len(repo.messages) != 1 {
		t.Fatalf("messages = %d, want 1 role=tool message", len(repo.messages))
	}
	msg := repo.messages[0]
	if msg.Role != model.RoleTool {
		t.Fatalf("message.role = %q, want tool", msg.Role)
	}
	if msg.Content == nil || *msg.Content != `{"kind":"config_draft"}` {
		t.Fatalf("message.content = %v, want config draft response", msg.Content)
	}
}

func TestPersistenceHandler_ToolErrorMarksError(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "sess-1", Repo: repo})
	ctx := WithToolCallID(context.Background(), "call-2")
	h.OnStart(ctx, toolInfo("flaky"), &einotool.CallbackInput{})
	h.OnError(ctx, toolInfo("flaky"), errors.New("boom"))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.toolCalls) != 1 {
		t.Fatalf("toolCalls count = %d", len(repo.toolCalls))
	}
	for _, tc := range repo.toolCalls {
		if tc.Status != model.StatusError {
			t.Errorf("status = %q, want error", tc.Status)
		}
		if tc.Error == nil || *tc.Error != "boom" {
			t.Errorf("error = %v", tc.Error)
		}
	}
}

func TestPersistenceHandler_ToolTimeoutClassified(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "s1", Repo: repo})
	ctx := WithToolCallID(context.Background(), "call-3")
	h.OnStart(ctx, toolInfo("slow"), &einotool.CallbackInput{})
	h.OnError(ctx, toolInfo("slow"), context.DeadlineExceeded)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, tc := range repo.toolCalls {
		if tc.Status != model.StatusTimeout {
			t.Errorf("status = %q, want timeout", tc.Status)
		}
	}
}

func TestPersistenceHandler_PersistFailureNonFatal(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	repo.failAppend = true
	reg := prometheus.NewRegistry()
	h := NewPersistenceHandler(PersistenceDeps{
		SessionID:  "s1",
		Repo:       repo,
		Registerer: reg,
	})
	out := &einomodel.CallbackOutput{Message: &schema.Message{Role: schema.Assistant, Content: "x"}}
	// No panic expected; failure is just logged + counted.
	h.OnEnd(context.Background(), chatModelInfo(), out)
}

func TestPersistenceHandler_NeededFiltersComponent(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "s", Repo: repo})
	if !h.Needed(context.Background(), chatModelInfo(), callbacks.TimingOnEnd) {
		t.Errorf("ChatModel OnEnd should be needed")
	}
	// ChatModel.OnStart became Needed once flushIncompleteBatch was
	// added — it's the cleanest signal that the previous tool batch
	// is terminally done, so the autoheal flush hooks there.
	if !h.Needed(context.Background(), chatModelInfo(), callbacks.TimingOnStart) {
		t.Errorf("ChatModel OnStart should be needed (autoheal flush hook)")
	}
	if !h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnStart) {
		t.Errorf("Tool OnStart should be needed")
	}
	other := &callbacks.RunInfo{Component: components.Component("Embedding")}
	if h.Needed(context.Background(), other, callbacks.TimingOnEnd) {
		t.Errorf("non-tool/non-chat component should be skipped")
	}
}

// --- autoheal / batch tracker tests ---------------------------------------

// assistantEndWithToolCalls drives ChatModel.OnEnd with a synthesised
// assistant message that emits N tool_calls — the same shape ChatModel
// produces when the model wants to fan out to tools. Used to set up
// the batch tracker for autoheal tests.
func assistantEndWithToolCalls(t *testing.T, h *PersistenceHandler, calls ...struct{ ID, Name string }) {
	t.Helper()
	toolCalls := make([]schema.ToolCall, 0, len(calls))
	for _, c := range calls {
		toolCalls = append(toolCalls, schema.ToolCall{
			ID:       c.ID,
			Type:     "function",
			Function: schema.FunctionCall{Name: c.Name},
		})
	}
	out := &einomodel.CallbackOutput{
		Message: &schema.Message{
			Role:      schema.Assistant,
			Content:   "",
			ToolCalls: toolCalls,
		},
	}
	h.OnEnd(context.Background(), chatModelInfo(), out)
}

func TestAutoheal_NoMissing_NoStubInserted(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "s", Repo: repo})

	// Assistant emits 2 tool_calls and both OnEnds fire normally.
	assistantEndWithToolCalls(t, h,
		struct{ ID, Name string }{ID: "call_a", Name: "host_bash"},
		struct{ ID, Name string }{ID: "call_b", Name: "host_bash"},
	)
	for _, id := range []string{"call_a", "call_b"} {
		ctx := WithToolCallID(context.Background(), id)
		h.OnStart(ctx, toolInfo("host_bash"), &einotool.CallbackInput{ArgumentsInJSON: `{}`})
		h.OnEnd(ctx, toolInfo("host_bash"), &einotool.CallbackOutput{Response: `{"ok":true}`})
	}

	// Trigger flush via next ChatModel.OnStart.
	h.OnStart(context.Background(), chatModelInfo(), nil)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	// Expect: 1 assistant + 2 tool messages = 3 total. No autoheal stub.
	if got := len(repo.messages); got != 3 {
		t.Fatalf("messages = %d, want 3 (1 assistant + 2 tool)", got)
	}
	for _, m := range repo.messages {
		if m.Content != nil && contains(*m.Content, `"autoheal":true`) {
			t.Errorf("autoheal stub written when not expected: %s", *m.Content)
		}
	}
}

func TestAutoheal_TwoOfFourMissing_StubsInserted(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	reg := prometheus.NewRegistry()
	h := NewPersistenceHandler(PersistenceDeps{
		SessionID:  "s",
		Repo:       repo,
		Registerer: reg,
	})

	// Assistant emits 4 parallel host_bash tool_calls.
	assistantEndWithToolCalls(t, h,
		struct{ ID, Name string }{ID: "call_00", Name: "host_bash"},
		struct{ ID, Name string }{ID: "call_01", Name: "host_bash"},
		struct{ ID, Name string }{ID: "call_02", Name: "host_bash"},
		struct{ ID, Name string }{ID: "call_03", Name: "host_bash"},
	)
	// Only 01 and 03's OnStart+OnEnd fire — 00 and 02 silently dropped.
	for _, id := range []string{"call_01", "call_03"} {
		ctx := WithToolCallID(context.Background(), id)
		h.OnStart(ctx, toolInfo("host_bash"), &einotool.CallbackInput{ArgumentsInJSON: `{}`})
		h.OnEnd(ctx, toolInfo("host_bash"), &einotool.CallbackOutput{Response: `{"ok":true}`})
	}

	// Next ChatModel.OnStart triggers flush.
	h.OnStart(context.Background(), chatModelInfo(), nil)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	// Expect 1 assistant + 2 real tool + 2 stub tool = 5.
	if got := len(repo.messages); got != 5 {
		t.Fatalf("messages = %d, want 5 (1 asst + 2 real tool + 2 stub)", got)
	}
	stubIDs := map[string]bool{}
	for _, m := range repo.messages {
		if m.Role != model.RoleTool {
			continue
		}
		if m.Content != nil && contains(*m.Content, `"autoheal":true`) {
			if m.ToolCallID == nil {
				t.Fatalf("stub row has no ToolCallID")
			}
			stubIDs[*m.ToolCallID] = true
		}
	}
	if !stubIDs["call_00"] || !stubIDs["call_02"] {
		t.Errorf("stub ids = %v, want call_00 + call_02", stubIDs)
	}
	if stubIDs["call_01"] || stubIDs["call_03"] {
		t.Errorf("real responses got autohealed: %v", stubIDs)
	}
	if v := counterValue(t, reg, "ongrid_chat_tool_response_loss_total", "autoheal_stub", "host_bash"); v != 2 {
		t.Errorf("autoheal counter = %v, want 2", v)
	}
}

func TestAutoheal_NoBatch_NoOp(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "s", Repo: repo})
	// Very first turn — no prior assistant batch.
	h.OnStart(context.Background(), chatModelInfo(), nil)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if got := len(repo.messages); got != 0 {
		t.Fatalf("messages = %d, want 0 (flush should no-op without a batch)", got)
	}
}

func TestAutoheal_SequentialBatches_OnlyOwnFlushed(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "s", Repo: repo})

	// Batch 1: 2 tool_calls, both complete.
	assistantEndWithToolCalls(t, h,
		struct{ ID, Name string }{ID: "b1_a", Name: "host_bash"},
		struct{ ID, Name string }{ID: "b1_b", Name: "host_bash"},
	)
	for _, id := range []string{"b1_a", "b1_b"} {
		ctx := WithToolCallID(context.Background(), id)
		h.OnStart(ctx, toolInfo("host_bash"), &einotool.CallbackInput{})
		h.OnEnd(ctx, toolInfo("host_bash"), &einotool.CallbackOutput{Response: `{}`})
	}
	// Next ChatModel.OnStart flushes batch 1 (no stubs).
	h.OnStart(context.Background(), chatModelInfo(), nil)
	// Batch 2: 3 tool_calls, only 1 completes.
	assistantEndWithToolCalls(t, h,
		struct{ ID, Name string }{ID: "b2_a", Name: "host_bash"},
		struct{ ID, Name string }{ID: "b2_b", Name: "host_bash"},
		struct{ ID, Name string }{ID: "b2_c", Name: "host_bash"},
	)
	ctx := WithToolCallID(context.Background(), "b2_a")
	h.OnStart(ctx, toolInfo("host_bash"), &einotool.CallbackInput{})
	h.OnEnd(ctx, toolInfo("host_bash"), &einotool.CallbackOutput{Response: `{}`})
	// FinalizeBatch should flush batch 2's missing b2_b and b2_c only.
	h.FinalizeBatch(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	stubIDs := map[string]bool{}
	for _, m := range repo.messages {
		if m.Role != model.RoleTool || m.Content == nil {
			continue
		}
		if contains(*m.Content, `"autoheal":true`) && m.ToolCallID != nil {
			stubIDs[*m.ToolCallID] = true
		}
	}
	if stubIDs["b1_a"] || stubIDs["b1_b"] {
		t.Errorf("batch 1 leaked stubs after its flush: %v", stubIDs)
	}
	if !stubIDs["b2_b"] || !stubIDs["b2_c"] {
		t.Errorf("batch 2 stub ids = %v, want b2_b + b2_c", stubIDs)
	}
	if stubIDs["b2_a"] {
		t.Errorf("completed call b2_a got stubbed: %v", stubIDs)
	}
}

func TestAutoheal_FinalizeBatchIdempotent(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	h := NewPersistenceHandler(PersistenceDeps{SessionID: "s", Repo: repo})
	assistantEndWithToolCalls(t, h,
		struct{ ID, Name string }{ID: "x", Name: "host_bash"},
	)
	h.FinalizeBatch(context.Background())
	h.FinalizeBatch(context.Background()) // second call should be a no-op
	repo.mu.Lock()
	defer repo.mu.Unlock()
	stubCount := 0
	for _, m := range repo.messages {
		if m.Content != nil && contains(*m.Content, `"autoheal":true`) {
			stubCount++
		}
	}
	if stubCount != 1 {
		t.Errorf("stub count = %d, want 1 (second Finalize must be no-op)", stubCount)
	}
}

// --- helpers --------------------------------------------------------------

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// counterValue gathers the registry and returns the value for a
// label-keyed counter, asserting it exists. Returns -1 when not found.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labelValues ...string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
		for _, m := range fam.GetMetric() {
			labels := m.GetLabel()
			if len(labels) != len(labelValues) {
				continue
			}
			match := true
			for i, lv := range labelValues {
				if labels[i].GetValue() != lv {
					match = false
					break
				}
			}
			if match {
				return m.GetCounter().GetValue()
			}
		}
	}
	return -1
}
