package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// ----- fakes -----

type fakeRepo struct {
	mu        sync.Mutex
	sessions  map[string]*model.Session
	messages  map[string][]*model.Message // keyed by session id
	toolCalls map[string]*model.ToolCall
	nextSess  int
	nextMsg   int
	nextTC    int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		sessions:  map[string]*model.Session{},
		messages:  map[string][]*model.Message{},
		toolCalls: map[string]*model.ToolCall{},
	}
}

// fakeID returns a stable, predictable id of the form "fake-<prefix>-<n>"
// so test assertions can use literal strings instead of UUIDs. Real
// production code uses uuid.NewString in the GORM BeforeCreate hook.
func fakeID(prefix string, n int) string { return prefix + "-" + strconv.Itoa(n) }

func (r *fakeRepo) CreateSession(_ context.Context, s *model.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextSess++
	if s.ID == "" {
		s.ID = fakeID("sess", r.nextSess)
	}
	cp := *s
	r.sessions[s.ID] = &cp
	return nil
}
func (r *fakeRepo) GetSession(_ context.Context, id string) (*model.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *s
	return &cp, nil
}
func (r *fakeRepo) ListSessions(_ context.Context, userID uint64, _, _ int, _ *uint64) ([]*model.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Session
	for _, s := range r.sessions {
		if s.UserID == userID {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (r *fakeRepo) ListByParent(_ context.Context, parentID string) ([]*model.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.Session, 0)
	for _, s := range r.sessions {
		if s.ParentSessionID != nil && *s.ParentSessionID == parentID {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (r *fakeRepo) RenameSession(_ context.Context, _, _ string) error { return nil }
func (r *fakeRepo) CloseSession(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return errs.ErrNotFound
	}
	now := time.Now().UTC()
	s.ClosedAt = &now
	return nil
}
func (r *fakeRepo) DeleteSession(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[id]; !ok {
		return errs.ErrNotFound
	}
	delete(r.sessions, id)
	return nil
}
func (r *fakeRepo) AppendMessage(_ context.Context, m *model.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextMsg++
	if m.ID == "" {
		m.ID = fakeID("msg", r.nextMsg)
	}
	cp := *m
	r.messages[m.SessionID] = append(r.messages[m.SessionID], &cp)
	return nil
}
func (r *fakeRepo) ListMessages(_ context.Context, sessionID string, limit int) ([]*model.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs := r.messages[sessionID]
	out := make([]*model.Message, len(msgs))
	for i, m := range msgs {
		cp := *m
		out[i] = &cp
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
func (r *fakeRepo) CreateToolCall(_ context.Context, tc *model.ToolCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextTC++
	if tc.ID == "" {
		tc.ID = fakeID("tc", r.nextTC)
	}
	cp := *tc
	r.toolCalls[tc.ID] = &cp
	return nil
}
func (r *fakeRepo) UpdateToolCallResult(_ context.Context, id string, status string, resultJSON, errStr *string, endedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *fakeRepo) SumTokensSince(_ context.Context, _ time.Time) (biz.TokenSums, error) {
	return biz.TokenSums{}, nil
}

var _ biz.SessionRepo = (*fakeRepo)(nil)

type chatScript struct {
	resps []llm.ChatResp
	errs  []error
	idx   int32
	calls atomic.Int32
}

type fakeLLM struct {
	script *chatScript
}

func (f *fakeLLM) Chat(_ context.Context, _ llm.ChatReq) (*llm.ChatResp, error) {
	i := int(f.script.idx)
	f.script.idx++
	f.script.calls.Add(1)
	if i >= len(f.script.resps) {
		return nil, errors.New("fakeLLM: out of script")
	}
	if i < len(f.script.errs) && f.script.errs[i] != nil {
		return nil, f.script.errs[i]
	}
	r := f.script.resps[i]
	return &r, nil
}

// fakeCaller mimics the frontierbound.Client.Call surface. Tests preload
// a typed response value (marshaled on demand) or an error.
type fakeCaller struct {
	resp any
	err  error
}

func (f *fakeCaller) Call(_ context.Context, _ uint64, _ string, _ []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, err := json.Marshal(f.resp)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// buildRegistry wires a real tools.Registry around a fakeCaller that
// resolves every reverse Call with the JSON-encoded resp.
func buildRegistry(_ *testing.T, resp any) *tools.Registry {
	edges := newFakeEdgeRepoAgent(&edgemodel.Edge{ID: 1, Name: "node-a"})
	uc := edgebiz.NewUsecase(edges, nil, nil, slog.Default())
	return tools.NewRegistry(&fakeCaller{resp: resp}, uc, nil, nil, nil, nil, nil, slog.Default())
}

// buildRegistryWithErr is like buildRegistry but every Call returns the
// supplied error.
func buildRegistryWithErr(_ *testing.T, errStr string) *tools.Registry {
	edges := newFakeEdgeRepoAgent(&edgemodel.Edge{ID: 1, Name: "node-a"})
	uc := edgebiz.NewUsecase(edges, nil, nil, slog.Default())
	return tools.NewRegistry(&fakeCaller{err: errors.New(errStr)}, uc, nil, nil, nil, nil, nil, slog.Default())
}

// keep tunnel import alive (used by other tests in this file via Response types).
var _ = tunnel.MethodGetHostLoad

// ---- edge repo fake (same shape as in tools/registry_test but local). ----

type fakeEdgeRepoAgent struct {
	byName map[string]*edgemodel.Edge
	byID   map[uint64]*edgemodel.Edge
}

func newFakeEdgeRepoAgent(es ...*edgemodel.Edge) *fakeEdgeRepoAgent {
	r := &fakeEdgeRepoAgent{
		byName: map[string]*edgemodel.Edge{},
		byID:   map[uint64]*edgemodel.Edge{},
	}
	for _, e := range es {
		r.byName[e.Name] = e
		r.byID[e.ID] = e
	}
	return r
}

func (r *fakeEdgeRepoAgent) Create(_ context.Context, _ *edgemodel.Edge) error { return nil }
func (r *fakeEdgeRepoAgent) GetByID(_ context.Context, id uint64) (*edgemodel.Edge, error) {
	if e, ok := r.byID[id]; ok {
		return e, nil
	}
	return nil, errs.ErrNotFound
}
func (r *fakeEdgeRepoAgent) GetByAccessKey(_ context.Context, _ string) (*edgemodel.Edge, error) {
	return nil, errs.ErrNotFound
}
func (r *fakeEdgeRepoAgent) GetByName(_ context.Context, name string) (*edgemodel.Edge, error) {
	if e, ok := r.byName[name]; ok {
		return e, nil
	}
	return nil, errs.ErrNotFound
}
func (r *fakeEdgeRepoAgent) List(_ context.Context, _ edgebiz.ListFilter) ([]*edgemodel.Edge, error) {
	out := make([]*edgemodel.Edge, 0, len(r.byID))
	for _, e := range r.byID {
		out = append(out, e)
	}
	return out, nil
}
func (r *fakeEdgeRepoAgent) UpdateSecretHash(_ context.Context, _ uint64, _ string) error {
	return nil
}
func (r *fakeEdgeRepoAgent) UpdateStatus(_ context.Context, _ uint64, _ string, _ time.Time) error {
	return nil
}
func (r *fakeEdgeRepoAgent) UpdateRoles(_ context.Context, _ uint64, _ uint8) error {
	return nil
}
func (r *fakeEdgeRepoAgent) UpdateName(_ context.Context, _ uint64, _ string) error      { return nil }
func (r *fakeEdgeRepoAgent) SetDeviceID(_ context.Context, _ uint64, _ uint64) error     { return nil }
func (r *fakeEdgeRepoAgent) ClearDeviceID(_ context.Context, _ uint64) error             { return nil }
func (r *fakeEdgeRepoAgent) SetAgentVersion(_ context.Context, _ uint64, _ string) error { return nil }
func (r *fakeEdgeRepoAgent) Delete(_ context.Context, _ uint64) error                    { return nil }
func (r *fakeEdgeRepoAgent) Count(_ context.Context) (int64, error)                      { return 1, nil }

// ----- tests -----

func createSession(t *testing.T, repo *fakeRepo, userID uint64) *model.Session {
	t.Helper()
	s := &model.Session{UserID: userID, Title: "t", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repo.CreateSession(context.Background(), s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return s
}

func TestRun_SingleShotReplyNoTools(t *testing.T) {
	repo := newFakeRepo()
	sess := createSession(t, repo, 42)
	reg := buildRegistry(t, tunnel.GetHostLoadResponse{CPUPct: 10})

	script := &chatScript{
		resps: []llm.ChatResp{
			{
				Assistant: llm.Message{Role: "assistant", Content: "node-a looks fine."},
				Usage:     llm.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
			},
		},
	}
	a := New(&fakeLLM{script: script}, reg, repo, Config{Model: "m", MaxIterations: 4}, slog.Default())

	reply, err := a.Run(context.Background(), sess.ID, 42, "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", reply.Iterations)
	}
	if reply.Message == nil || reply.Message.Content == nil || *reply.Message.Content != "node-a looks fine." {
		t.Errorf("reply message = %+v", reply.Message)
	}
	if reply.Usage.TotalTokens != 12 {
		t.Errorf("usage.total = %d, want 12", reply.Usage.TotalTokens)
	}
	if len(reply.ToolCalls) != 0 {
		t.Errorf("tool calls = %d, want 0", len(reply.ToolCalls))
	}
	if got := script.calls.Load(); got != 1 {
		t.Errorf("llm called %d times, want 1", got)
	}
	// messages: user + assistant
	msgs, _ := repo.ListMessages(context.Background(), sess.ID, 0)
	if len(msgs) != 2 {
		t.Errorf("persisted msgs = %d, want 2", len(msgs))
	}
}

func TestRun_OneToolRound(t *testing.T) {
	repo := newFakeRepo()
	sess := createSession(t, repo, 42)
	reg := buildRegistry(t, tunnel.GetHostLoadResponse{CPUPct: 77, MemPct: 44})

	script := &chatScript{
		resps: []llm.ChatResp{
			{
				Assistant: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call_1", Name: "get_host_load",
						Args: json.RawMessage(`{"edge_name":"node-a"}`),
					}},
				},
				Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
			},
			{
				Assistant: llm.Message{Role: "assistant", Content: "cpu is 77%."},
				Usage:     llm.Usage{PromptTokens: 15, CompletionTokens: 4, TotalTokens: 19},
			},
		},
	}
	a := New(&fakeLLM{script: script}, reg, repo, Config{Model: "m", MaxIterations: 4}, slog.Default())

	reply, err := a.Run(context.Background(), sess.ID, 42, "how is node-a?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", reply.Iterations)
	}
	if len(reply.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(reply.ToolCalls))
	}
	if reply.ToolCalls[0].Status != model.StatusSuccess {
		t.Errorf("tool call status = %q, want success", reply.ToolCalls[0].Status)
	}
	if reply.ToolCalls[0].DeviceID == nil || *reply.ToolCalls[0].DeviceID != 1 {
		t.Errorf("tool call edge id = %v, want *1", reply.ToolCalls[0].DeviceID)
	}
	// Total usage sums both chat calls.
	if reply.Usage.TotalTokens != 31 {
		t.Errorf("total tokens = %d, want 31", reply.Usage.TotalTokens)
	}

	// Persisted messages: user + assistant(tool_call) + tool(result) + assistant(final) = 4
	msgs, _ := repo.ListMessages(context.Background(), sess.ID, 0)
	if len(msgs) != 4 {
		t.Errorf("persisted msgs = %d, want 4", len(msgs))
	}
}

func TestRun_MaxIterations(t *testing.T) {
	repo := newFakeRepo()
	sess := createSession(t, repo, 1)
	reg := buildRegistry(t, tunnel.GetHostLoadResponse{})

	// Script that always emits a tool call, never a final reply.
	resp := llm.ChatResp{
		Assistant: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID: "call_x", Name: "get_host_load",
			Args: json.RawMessage(`{"edge_name":"node-a"}`),
		}}},
	}
	script := &chatScript{resps: []llm.ChatResp{resp, resp, resp, resp, resp}}
	a := New(&fakeLLM{script: script}, reg, repo, Config{Model: "m", MaxIterations: 3}, slog.Default())

	reply, err := a.Run(context.Background(), sess.ID, 1, "go")
	if err != nil {
		t.Fatalf("Run returned err: %v", err)
	}
	if reply == nil || reply.Message == nil {
		t.Fatalf("expected reply with apology message")
	}
	if reply.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", reply.Iterations)
	}
	if !strings.Contains(*reply.Message.Content, "尝试了") {
		t.Errorf("apology missing zh hint, got %q", *reply.Message.Content)
	}
	if got := script.calls.Load(); got != 3 {
		t.Errorf("llm calls = %d, want 3", got)
	}
}

func TestRun_ToolErrorFeedsIntoModel(t *testing.T) {
	repo := newFakeRepo()
	sess := createSession(t, repo, 1)

	// Stub frontier resolves every reverse call with errStr; the registry
	// will wrap it and the agent should persist status=error and hand back
	// a `{"error":...}` tool msg.
	reg := buildRegistryWithErr(t, "tunnel blew up")

	script := &chatScript{
		resps: []llm.ChatResp{
			{
				Assistant: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
					ID: "call_e", Name: "get_host_load",
					Args: json.RawMessage(`{"edge_name":"node-a"}`),
				}}},
			},
			{Assistant: llm.Message{Role: "assistant", Content: "sorry, tool blew up"}},
		},
	}
	a := New(&fakeLLM{script: script}, reg, repo, Config{Model: "m", MaxIterations: 4}, slog.Default())

	reply, err := a.Run(context.Background(), sess.ID, 1, "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Status != model.StatusError {
		t.Errorf("tool call = %+v, want status=error", reply.ToolCalls[0])
	}
	msgs, _ := repo.ListMessages(context.Background(), sess.ID, 0)
	// user + asst(tool) + tool(error) + asst(final) = 4
	if len(msgs) != 4 {
		t.Errorf("msgs = %d, want 4", len(msgs))
	}
	// Tool message content should contain error key.
	var toolMsg *model.Message
	for _, m := range msgs {
		if m.Role == model.RoleTool {
			toolMsg = m
			break
		}
	}
	if toolMsg == nil || toolMsg.Content == nil {
		t.Fatal("tool msg not persisted")
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(*toolMsg.Content), &parsed); err != nil {
		t.Fatalf("tool msg not JSON: %v", err)
	}
	if _, ok := parsed["error"]; !ok {
		t.Errorf("tool msg lacks error key: %q", *toolMsg.Content)
	}
}

func TestRun_SessionOwnershipEnforced(t *testing.T) {
	repo := newFakeRepo()
	sess := createSession(t, repo, 42)
	reg := buildRegistry(t, tunnel.GetHostLoadResponse{})
	a := New(&fakeLLM{script: &chatScript{}}, reg, repo, Config{Model: "m", MaxIterations: 2}, slog.Default())

	// Caller user id != session owner -> ErrNotFound (no info leak).
	_, err := a.Run(context.Background(), sess.ID, 43, "hi")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("want ErrNotFound for non-owner, got %v", err)
	}
}
