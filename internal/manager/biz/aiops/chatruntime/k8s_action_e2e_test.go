package chatruntime_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	aibiz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	k8sbiz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestRuntimeK8sActionToolCallPassesReviewGateAndController(t *testing.T) {
	replicas := 2
	args := mustJSON(t, map[string]any{
		"cluster_id": 1,
		"action":     "scale",
		"kind":       "Deployment",
		"namespace":  "default",
		"name":       "api",
		"replicas":   replicas,
		"dry_run":    true,
		"reason":     "e2e chat smoke",
	})
	model := newE2EScriptedChatModel(
		&schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_k8s_scale",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      aiopstools.ToolNameExecuteK8sAction,
					Arguments: args,
				},
			}},
		},
		&schema.Message{
			Role:    schema.Assistant,
			Content: "dry run preflight completed",
		},
	)
	controllerEdgeID := uint64(77)
	caller := &e2eK8sActionCaller{
		resp: mustJSON(t, tunnel.KubernetesActionResponse{
			ClusterID:  1,
			Action:     "scale",
			Kind:       "Deployment",
			APIVersion: "apps/v1",
			Namespace:  "default",
			Name:       "api",
			DryRun:     true,
			Applied:    false,
			Preflight: tunnel.KubernetesActionPreflight{
				Kind:            "Deployment",
				APIVersion:      "apps/v1",
				Namespace:       "default",
				Name:            "api",
				UID:             "deploy-uid",
				ResourceVersion: "10",
				Exists:          true,
			},
			ResultResourceVersion: "10",
			Message:               "dry run only",
		}),
	}
	reader := &e2eK8sSnapshotReader{
		clusters: []*k8smodel.Cluster{{
			ID:               1,
			Name:             "kind-local",
			Mode:             k8smodel.ModeFullNode,
			Status:           k8smodel.ClusterStatusOnline,
			ControllerEdgeID: &controllerEdgeID,
		}},
	}
	reviewer := &e2eReviewSpawner{
		result: "Decision: approve\nReason: scoped dry-run scale preflight is safe",
	}
	sink := &e2eProposalSink{}
	inner := aiopstools.NewExecuteK8sActionTool(caller, reader, slog.Default())
	k8sActionTool := decorators.WithTenantBind(decorators.WithReviewGate(inner, reviewer, decorators.ReviewGateConfig{
		Sink:    sink,
		Timeout: time.Second,
	}))

	sess := &aiopsmodel.Session{ID: "s-k8s", UserID: 42}
	store := newE2ESessionStore(sess)
	rt, err := chatruntime.NewRuntime(chatruntime.Config{
		Sessions:  store,
		ChatModel: model,
		ToolBag:   []basetool.BaseTool{k8sActionTool},
		GraphCfg:  graph.Config{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	var events []chatruntime.Event
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 42, Role: "admin"})
	reply, err := rt.Handle(ctx, &chatruntime.Request{
		SessionID: sess.ID,
		UserID:    sess.UserID,
		Role:      "admin",
		UserText:  "把 default/api Deployment 扩到 2 个副本，先 dry-run",
		Emit: func(ev chatruntime.Event) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if reply == nil || reply.Message == nil || reply.Message.Content == nil || *reply.Message.Content != "dry run preflight completed" {
		t.Fatalf("reply = %+v, want final assistant content", reply)
	}
	if model.calls.Load() != 2 {
		t.Fatalf("LLM Generate calls = %d, want 2", model.calls.Load())
	}

	if caller.lastEdgeID != controllerEdgeID {
		t.Fatalf("controller edge id = %d, want %d", caller.lastEdgeID, controllerEdgeID)
	}
	if caller.lastMethod != tunnel.MethodExecuteK8sAction {
		t.Fatalf("controller method = %q, want %q", caller.lastMethod, tunnel.MethodExecuteK8sAction)
	}
	var sent tunnel.KubernetesActionRequest
	if err := json.Unmarshal(caller.lastBody, &sent); err != nil {
		t.Fatalf("decode controller request: %v", err)
	}
	if sent.ClusterID != 1 || sent.Action != "scale" || sent.Kind != "Deployment" || sent.Namespace != "default" || sent.Name != "api" {
		t.Fatalf("unexpected controller request: %+v", sent)
	}
	if sent.Replicas == nil || *sent.Replicas != replicas || !sent.DryRun || sent.Reason != "e2e chat smoke" {
		t.Fatalf("missing normalized write args: %+v", sent)
	}

	if reviewer.calls.Load() != 1 {
		t.Fatalf("reviewer calls = %d, want 1", reviewer.calls.Load())
	}
	if reviewer.lastAgent != decorators.DefaultReviewerAgent {
		t.Fatalf("reviewer agent = %q, want %q", reviewer.lastAgent, decorators.DefaultReviewerAgent)
	}
	if reviewer.lastPrompt == "" || !containsAll(reviewer.lastPrompt, aiopstools.ToolNameExecuteK8sAction, `"dry_run": true`, `"action": "scale"`) {
		t.Fatalf("reviewer prompt missing proposal details: %s", reviewer.lastPrompt)
	}

	if len(sink.inserts) != 1 {
		t.Fatalf("proposal inserts = %d, want 1", len(sink.inserts))
	}
	insert := sink.inserts[0]
	if insert.ToolName != aiopstools.ToolNameExecuteK8sAction || insert.ToolClass != "write" || insert.OperatorUserID != 42 || insert.SessionID != sess.ID {
		t.Fatalf("unexpected proposal insert: %+v", insert)
	}
	if sink.decisions["proposal-1"] != "approve" || !sink.executed["proposal-1"] {
		t.Fatalf("proposal decision/execution = decisions=%v executed=%v", sink.decisions, sink.executed)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.toolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(store.toolCalls))
	}
	tc := store.toolCalls[0]
	if tc.ToolName != aiopstools.ToolNameExecuteK8sAction || tc.Status != aiopsmodel.StatusSuccess {
		t.Fatalf("unexpected persisted tool call: %+v", tc)
	}
	if tc.LLMCallID == nil || *tc.LLMCallID != "call_k8s_scale" {
		t.Fatalf("llm_call_id = %v, want call_k8s_scale", tc.LLMCallID)
	}
	if tc.ResultJSON == nil || !containsAll(*tc.ResultJSON, `"source":"kubernetes_api"`, `"controller_edge_id":77`, `"dry_run":true`) {
		t.Fatalf("tool result json = %v", tc.ResultJSON)
	}
	if !sawToolEvent(events, chatruntime.EventToolStart, aiopstools.ToolNameExecuteK8sAction) ||
		!sawToolEvent(events, chatruntime.EventToolEnd, aiopstools.ToolNameExecuteK8sAction) {
		t.Fatalf("missing tool lifecycle events: %+v", events)
	}
}

type e2eScriptedChatModel struct {
	mu      sync.Mutex
	replies []*schema.Message
	idx     int
	calls   atomic.Int32
}

func newE2EScriptedChatModel(replies ...*schema.Message) *e2eScriptedChatModel {
	return &e2eScriptedChatModel{replies: replies}
}

func (s *e2eScriptedChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
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

func (s *e2eScriptedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := s.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (s *e2eScriptedChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

func (s *e2eScriptedChatModel) WithTools(_ []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return s, nil
}

type e2eSessionStore struct {
	mu        sync.Mutex
	sessions  map[string]*aiopsmodel.Session
	messages  []*aiopsmodel.Message
	toolCalls []*aiopsmodel.ToolCall
	nextMsg   int
	nextTool  int
}

var _ aibiz.SessionRepo = (*e2eSessionStore)(nil)

func newE2ESessionStore(seed *aiopsmodel.Session) *e2eSessionStore {
	store := &e2eSessionStore{sessions: map[string]*aiopsmodel.Session{}}
	if seed != nil {
		cp := *seed
		store.sessions[seed.ID] = &cp
	}
	return store
}

func (s *e2eSessionStore) CreateSession(_ context.Context, sess *aiopsmodel.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.sessions[sess.ID] = &cp
	return nil
}

func (s *e2eSessionStore) GetSession(_ context.Context, id string) (*aiopsmodel.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *sess
	return &cp, nil
}

func (s *e2eSessionStore) ListSessions(_ context.Context, userID uint64, _, _ int, _ *uint64) ([]*aiopsmodel.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aiopsmodel.Session, 0)
	for _, sess := range s.sessions {
		if sess.UserID != userID {
			continue
		}
		cp := *sess
		out = append(out, &cp)
	}
	return out, nil
}

func (s *e2eSessionStore) ListByParent(_ context.Context, parentID string) ([]*aiopsmodel.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aiopsmodel.Session, 0)
	for _, sess := range s.sessions {
		if sess.ParentSessionID == nil || *sess.ParentSessionID != parentID {
			continue
		}
		cp := *sess
		out = append(out, &cp)
	}
	return out, nil
}

func (s *e2eSessionStore) CloseSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		now := time.Now().UTC()
		sess.ClosedAt = &now
	}
	return nil
}

func (s *e2eSessionStore) RenameSession(_ context.Context, _, _ string) error { return nil }

func (s *e2eSessionStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

func (s *e2eSessionStore) AppendMessage(_ context.Context, msg *aiopsmodel.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *msg
	if cp.ID == "" {
		s.nextMsg++
		cp.ID = "msg-e2e-" + itoa(s.nextMsg)
	}
	s.messages = append(s.messages, &cp)
	msg.ID = cp.ID
	return nil
}

func (s *e2eSessionStore) ListMessages(_ context.Context, sessionID string, _ int) ([]*aiopsmodel.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aiopsmodel.Message, 0, len(s.messages))
	for _, msg := range s.messages {
		if msg.SessionID != sessionID {
			continue
		}
		cp := *msg
		if len(msg.ToolCalls) > 0 {
			cp.ToolCalls = append([]aiopsmodel.ToolCall(nil), msg.ToolCalls...)
		}
		out = append(out, &cp)
	}
	return out, nil
}

func (s *e2eSessionStore) CreateToolCall(_ context.Context, tc *aiopsmodel.ToolCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *tc
	if cp.ID == "" {
		s.nextTool++
		cp.ID = "tc-e2e-" + itoa(s.nextTool)
	}
	s.toolCalls = append(s.toolCalls, &cp)
	tc.ID = cp.ID
	return nil
}

func (s *e2eSessionStore) UpdateToolCallResult(_ context.Context, id string, status string, resultJSON, errStr *string, endedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tc := range s.toolCalls {
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

func (s *e2eSessionStore) SumTokensSince(_ context.Context, _ time.Time) (aibiz.TokenSums, error) {
	return aibiz.TokenSums{}, nil
}

type e2eK8sActionCaller struct {
	resp       string
	lastEdgeID uint64
	lastMethod string
	lastBody   []byte
}

func (c *e2eK8sActionCaller) Call(_ context.Context, edgeID uint64, method string, body []byte) ([]byte, error) {
	c.lastEdgeID = edgeID
	c.lastMethod = method
	c.lastBody = append([]byte(nil), body...)
	return []byte(c.resp), nil
}

type e2eK8sSnapshotReader struct {
	clusters []*k8smodel.Cluster
}

func (r *e2eK8sSnapshotReader) ListClusters(_ context.Context, _ k8sbiz.ListClustersFilter) ([]*k8smodel.Cluster, error) {
	return r.clusters, nil
}

func (r *e2eK8sSnapshotReader) GetCluster(_ context.Context, id uint64) (*k8smodel.Cluster, error) {
	for _, cluster := range r.clusters {
		if cluster.ID == id {
			cp := *cluster
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *e2eK8sSnapshotReader) ListNodes(context.Context, uint64) ([]*k8smodel.Node, error) {
	return nil, nil
}

func (r *e2eK8sSnapshotReader) CountNodes(context.Context, uint64) (int64, error) { return 0, nil }

func (r *e2eK8sSnapshotReader) ListWorkloads(context.Context, k8sbiz.ListWorkloadsFilter) ([]*k8smodel.Workload, error) {
	return nil, nil
}

func (r *e2eK8sSnapshotReader) CountWorkloads(context.Context, k8sbiz.ListWorkloadsFilter) (int64, error) {
	return 0, nil
}

func (r *e2eK8sSnapshotReader) ListPods(context.Context, k8sbiz.ListPodsFilter) ([]*k8smodel.Pod, error) {
	return nil, nil
}

func (r *e2eK8sSnapshotReader) CountPods(context.Context, k8sbiz.ListPodsFilter) (int64, error) {
	return 0, nil
}

func (r *e2eK8sSnapshotReader) ListEvents(context.Context, k8sbiz.ListEventsFilter) ([]*k8smodel.Event, error) {
	return nil, nil
}

func (r *e2eK8sSnapshotReader) CountEvents(context.Context, k8sbiz.ListEventsFilter) (int64, error) {
	return 0, nil
}

type e2eReviewSpawner struct {
	result     string
	lastAgent  string
	lastPrompt string
	calls      atomic.Int32
}

func (s *e2eReviewSpawner) SpawnReviewer(_ context.Context, req decorators.ReviewSpawnRequest) (*decorators.ReviewSpawnResult, error) {
	s.calls.Add(1)
	s.lastAgent = req.AgentName
	s.lastPrompt = req.Prompt
	return &decorators.ReviewSpawnResult{TaskID: "review-e2e-1", Result: s.result}, nil
}

type e2eProposalSink struct {
	inserts   []decorators.MutatingProposalEvent
	decisions map[string]string
	executed  map[string]bool
}

func (s *e2eProposalSink) Insert(_ context.Context, ev decorators.MutatingProposalEvent) (string, error) {
	s.inserts = append(s.inserts, ev)
	if s.decisions == nil {
		s.decisions = map[string]string{}
	}
	if s.executed == nil {
		s.executed = map[string]bool{}
	}
	return "proposal-" + itoa(len(s.inserts)), nil
}

func (s *e2eProposalSink) UpdateDecision(_ context.Context, id, decision string, _ string) error {
	if s.decisions == nil {
		s.decisions = map[string]string{}
	}
	s.decisions[id] = decision
	return nil
}

func (s *e2eProposalSink) MarkExecuted(_ context.Context, id string, _ time.Time) error {
	if s.executed == nil {
		s.executed = map[string]bool{}
	}
	s.executed[id] = true
	return nil
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(b)
}

func containsAll(s string, wants ...string) bool {
	for _, want := range wants {
		if !contains(s, want) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func sawToolEvent(events []chatruntime.Event, typ chatruntime.EventType, name string) bool {
	for _, ev := range events {
		if ev.Type == typ && ev.Tool != nil && ev.Tool.Name == name {
			return true
		}
	}
	return false
}

func itoa(n int) string {
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
