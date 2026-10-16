package aiops

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeRuntime implements RuntimeHandler. The graph kernel path
// dispatches through it; the test asserts the path was taken AND
// the SSE frame translation produces the legacy frame names.
type fakeRuntime struct {
	calls atomic.Int32
	reply *chatruntime.Reply
	err   error
}

func (f *fakeRuntime) Handle(_ context.Context, req *chatruntime.Request) (*chatruntime.Reply, error) {
	f.calls.Add(1)
	// Drive the SSE adapter through one full sequence so the frame
	// translation is exercised end-to-end. The call site asserts on
	// the resulting chatprotocol.Event slice.
	if req.Emit != nil {
		now := time.Now().UTC()
		end := now.Add(20 * time.Millisecond)
		req.Emit(chatruntime.Event{
			Type: chatruntime.EventAssistant,
			Assistant: &chatruntime.AssistantEvent{
				Iteration:        1,
				MessageID:        "m1",
				Content:          "thinking",
				CreatedAt:        now,
				PendingToolCalls: 1,
			},
		})
		req.Emit(chatruntime.Event{
			Type: chatruntime.EventToolStart,
			Tool: &chatruntime.ToolEvent{
				ToolCallID: "tc1",
				Name:       "echo",
				Status:     "pending",
				StartedAt:  now,
			},
		})
		req.Emit(chatruntime.Event{
			Type: chatruntime.EventToolEnd,
			Tool: &chatruntime.ToolEvent{
				ToolCallID: "tc1",
				Name:       "echo",
				Status:     "success",
				StartedAt:  now,
				EndedAt:    &end,
				DurationMs: 20,
				ResultJSON: `{"ok":true}`,
			},
		})
	}
	return f.reply, f.err
}

// memSessions is a tiny SessionRepo for service-layer tests.
type memSessions struct {
	mu       sync.Mutex
	sessions map[string]*model.Session
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
func (m *memSessions) ListByParent(_ context.Context, _ string) ([]*model.Session, error) {
	return nil, nil
}
func (m *memSessions) CloseSession(_ context.Context, _ string) error             { return nil }
func (m *memSessions) RenameSession(_ context.Context, _, _ string) error         { return nil }
func (m *memSessions) UpdateSessionModel(_ context.Context, _, _, _ string) error { return nil }
func (m *memSessions) DeleteSession(_ context.Context, _ string) error            { return nil }
func (m *memSessions) AppendMessage(_ context.Context, _ *model.Message) error {
	return nil
}
func (m *memSessions) ListMessages(_ context.Context, _ string, _ int) ([]*model.Message, error) {
	return nil, nil
}
func (m *memSessions) CreateToolCall(_ context.Context, _ *model.ToolCall) error { return nil }
func (m *memSessions) UpdateToolCallResult(_ context.Context, _ string, _ string, _, _ *string, _ time.Time) error {
	return nil
}
func (m *memSessions) SumTokensSince(_ context.Context, _ time.Time) (biz.TokenSums, error) {
	return biz.TokenSums{}, nil
}

func newSeededSessions(uid uint64) (*memSessions, string) {
	store := &memSessions{sessions: map[string]*model.Session{}}
	id := "sess-1"
	store.sessions[id] = &model.Session{ID: id, UserID: uid}
	return store, id
}

func TestService_Graph_DispatchesToRuntime(t *testing.T) {
	store, sid := newSeededSessions(7)
	body := "all done"
	rt := &fakeRuntime{
		reply: &chatruntime.Reply{
			Message: &model.Message{
				ID:        "m1",
				SessionID: sid,
				Role:      model.RoleAssistant,
				Content:   &body,
				CreatedAt: time.Now().UTC(),
			},
			Iterations: 2,
		},
	}
	svc := New(rt, store, nil, nil)

	var (
		evtMu sync.Mutex
		evts  []chatprotocol.Event
	)
	emit := func(ev chatprotocol.Event) {
		evtMu.Lock()
		defer evtMu.Unlock()
		evts = append(evts, ev)
	}
	caller := Caller{UserID: 7}
	reply, err := svc.PostMessageStream(context.Background(), caller, sid, "hello", emit)
	if err != nil {
		t.Fatalf("PostMessageStream: %v", err)
	}
	if reply == nil || reply.Message == nil || reply.Message.Content == nil || *reply.Message.Content != body {
		t.Fatalf("reply translation lost the assistant content")
	}
	if rt.calls.Load() != 1 {
		t.Errorf("runtime.Handle calls = %d, want 1", rt.calls.Load())
	}
	if reply.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2 (preserved across kernel translation)", reply.Iterations)
	}

	evtMu.Lock()
	defer evtMu.Unlock()
	wantSequence := []chatprotocol.EventType{
		chatprotocol.EventAssistant,
		chatprotocol.EventToolStart,
		chatprotocol.EventToolEnd,
	}
	if len(evts) < len(wantSequence) {
		t.Fatalf("got %d events, want at least %d (got %#v)", len(evts), len(wantSequence), evts)
	}
	for i, want := range wantSequence {
		if evts[i].Type != want {
			t.Errorf("event[%d] type = %q, want %q", i, evts[i].Type, want)
		}
	}
	// Tool fields must round-trip — Status and ToolCallID are the
	// SSE handler's correlation keys.
	if evts[1].Tool == nil || evts[1].Tool.ToolCallID != "tc1" {
		t.Errorf("tool_start did not carry tool_call_id")
	}
	if evts[2].Tool == nil || evts[2].Tool.Status != "success" {
		t.Errorf("tool_end did not carry status=success")
	}
	if evts[2].Tool.ResultJSON != `{"ok":true}` {
		t.Errorf("tool_end result_json lost across translation: %q", evts[2].Tool.ResultJSON)
	}
}

// TestService_MissingRuntimeFailsExplicitly guards
// against the misconfig where ONGRID_AGENT_KERNEL=graph but the
// runtime fails to build. We must fall back to legacy; the test
// confirms runtime is never called.
// 运行时不可用时应返回稳定错误，不能隐式执行另一套循环。
func TestService_MissingRuntimeFailsExplicitly(t *testing.T) {
	store, sid := newSeededSessions(7)
	svc := New(nil, store, nil, nil)
	_, err := svc.PostMessage(context.Background(), Caller{UserID: 7}, sid, "hello")
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error=%v, want runtime unavailable", err)
	}
}

// TestService_Graph_Ownership respects the owner check in
// the GetSession indirection — non-owner gets ErrNotFound BEFORE the
// runtime is consulted.
func TestService_Graph_Ownership(t *testing.T) {
	store, sid := newSeededSessions(7)
	rt := &fakeRuntime{}
	svc := New(rt, store, nil, nil)

	caller := Caller{UserID: 99}
	_, err := svc.PostMessage(context.Background(), caller, sid, "hello")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for non-owner", err)
	}
	if rt.calls.Load() != 0 {
		t.Errorf("runtime must NOT be consulted for non-owner, got %d", rt.calls.Load())
	}
}
