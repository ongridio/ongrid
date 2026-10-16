package aiops

import (
	"context"
	"fmt"
	managerbizaiopsmentions "github.com/ongridio/ongrid/internal/manager/biz/aiops/mentions"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"sync"
)

// ChatMentionResolver adapts resource mentions for the unified runtime.
type ChatMentionResolver struct {
	Inner *managerbizaiopsmentions.Searcher
}

func (a ChatMentionResolver) Resolve(ctx context.Context, in []contract.Mention) []string {
	if a.Inner == nil || len(in) == 0 {
		return nil
	}
	out := make([]managerbizaiopsmentions.Mention, 0, len(in))
	for _, m := range in {
		out = append(out, managerbizaiopsmentions.Mention{
			Type:  managerbizaiopsmentions.Type(m.Type),
			ID:    m.ID,
			Label: m.Label,
		})
	}
	return a.Inner.Resolve(ctx, out)
}

// WorkerSpawner adapts a *chatruntime.Runtime to the narrow
// tools.WorkerSpawner interface. The tools package can't import
// chatruntime (chatruntime already depends on tools/basetool), so this
// shim lives at the wiring site where both packages are visible.
//
// — the AgentTool / SendMessage / TaskStop trio talk
// through this shim; the shim translates between the seam-side request
// shape (tools.SpawnWorkerRequest) and the kernel's native shape
// (chatruntime.SpawnRequest), and threads the per-request streaming
// emitter from ctx so a background worker's task_notification frame
// lands on the user's SSE channel.
type WorkerSpawner struct {
	Runtime WorkerRuntime
}

func (s WorkerSpawner) SpawnWorker(ctx context.Context, req aiopstools.SpawnWorkerRequest) (*aiopstools.WorkerHandle, error) {
	w, err := s.Runtime.SpawnWorker(ctx, contract.SpawnRequest{
		AgentName:     req.AgentName,
		Prompt:        req.Prompt,
		Background:    req.Background,
		ParentSession: req.ParentSession,
		ParentEmit:    contract.EmitFromContext(ctx),
		Locale:        req.Locale,
		Provider:      req.Provider,
		Model:         req.Model,
	})
	if err != nil {
		return nil, err
	}
	return workerToHandle(w), nil
}

func (s WorkerSpawner) SendToWorker(ctx context.Context, workerID, message string) error {
	return s.Runtime.SendToWorker(ctx, workerID, message)
}

func (s WorkerSpawner) StopWorker(ctx context.Context, workerID string) error {
	return s.Runtime.StopWorker(ctx, workerID)
}

func (s WorkerSpawner) GetWorker(workerID string) (*aiopstools.WorkerHandle, bool) {
	w, ok := s.Runtime.GetWorker(workerID)
	if !ok {
		return nil, false
	}
	return workerToHandle(w), true
}

// ReviewSpawner lets the decorator chain wrap mutating base tools
// before the Runtime value exists. buildAIOpsRuntime binds the runtime after
// NewRuntime returns; until then mutating tools fail closed.
type ReviewSpawner struct {
	mu sync.RWMutex
	rt WorkerRuntime
}

func (s *ReviewSpawner) SetRuntime(rt WorkerRuntime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rt = rt
}

func (s *ReviewSpawner) SpawnReviewer(ctx context.Context, req aiopstoolsdec.ReviewSpawnRequest) (*aiopstoolsdec.ReviewSpawnResult, error) {
	s.mu.RLock()
	rt := s.rt
	s.mu.RUnlock()
	if rt == nil {
		return nil, fmt.Errorf("reviewer runtime not wired")
	}
	w, err := rt.SpawnWorker(ctx, contract.SpawnRequest{
		AgentName:  req.AgentName,
		Prompt:     req.Prompt,
		Background: false,
		ParentEmit: contract.EmitFromContext(ctx),
	})
	if err != nil {
		return nil, err
	}
	return &aiopstoolsdec.ReviewSpawnResult{
		TaskID: w.ID,
		Result: w.Result,
		Err:    w.Err,
	}, nil
}

// workerToHandle copies the chatruntime.Worker fields the tools layer
// cares about into the seam-side shape. Duration is computed from
// StartedAt + EndedAt when both are set; zero otherwise.
func workerToHandle(w *contract.Worker) *aiopstools.WorkerHandle {
	if w == nil {
		return nil
	}
	out := &aiopstools.WorkerHandle{
		ID:         w.ID,
		AgentName:  w.AgentName,
		Status:     string(w.Status),
		Background: w.Background,
		Result:     w.Result,
		Err:        w.Err,
	}
	if !w.StartedAt.IsZero() && w.EndedAt != nil {
		out.DurationMs = w.EndedAt.Sub(w.StartedAt).Milliseconds()
	}
	return out
}

// AgentRegistry adapts *chatruntime.AgentRegistry to the local
// tools.SubagentRegistry seam. AgentTool only needs to validate
// that a subagent_type exists at args-parse time — see agent_tool.go.
type AgentRegistry struct {
	Inner *aiopschatruntime.AgentRegistry
}

func (s AgentRegistry) HasAgent(name string) bool {
	if s.Inner == nil {
		return false
	}
	_, ok := s.Inner.ByName(name)
	return ok
}

// ProviderClient wraps an llm.Client and stamps a fixed
// Provider id into every ChatReq before forwarding. Used by
// buildAIOpsRuntime to keep RoutingChatModel's per-provider inner
// ChatModels routing through the existing MultiClient (which already
// honours ChatReq.Provider) without writing N near-identical adapters.
type ProviderClient struct {
	Inner    llm.Client
	Provider string
}

func (p *ProviderClient) Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	if req.Provider == "" {
		req.Provider = p.Provider
	}
	return p.Inner.Chat(ctx, req)
}
