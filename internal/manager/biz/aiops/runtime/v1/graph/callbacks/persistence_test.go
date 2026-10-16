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

	failAppend bool
	failCreate bool
	failUpdate bool
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
func (r *fakeSessionRepo) AppendMessage(_ context.Context, m *model.Message) error {
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
func (r *fakeSessionRepo) CreateToolCall(_ context.Context, tc *model.ToolCall) error {
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
func (r *fakeSessionRepo) UpdateToolCallResult(_ context.Context, id, status string, resultJSON, errStr *string, endedAt time.Time) error {
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
	if h.Needed(context.Background(), chatModelInfo(), callbacks.TimingOnStart) {
		t.Errorf("ChatModel OnStart should NOT be needed (user msg is persisted upstream)")
	}
	if !h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnStart) {
		t.Errorf("Tool OnStart should be needed")
	}
	other := &callbacks.RunInfo{Component: components.Component("Embedding")}
	if h.Needed(context.Background(), other, callbacks.TimingOnEnd) {
		t.Errorf("non-tool/non-chat component should be skipped")
	}
}

// Compatibility with the current shared repository contract; v1 never calls this method.
func (r *fakeSessionRepo) UpdateSessionModel(context.Context, string, string, string) error {
	return errs.ErrNotWiredYet
}
