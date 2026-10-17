package aiops

import (
	"context"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	v2 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/chatruntime"
)

// V2Runtime adapts immutable worker snapshots; execution remains in the v2 Graph.
type V2Runtime struct{ *v2.Runtime }

var _ RuntimeEngine = V2Runtime{}

func (r V2Runtime) SpawnWorker(ctx context.Context, req contract.SpawnRequest) (*contract.Worker, error) {
	w, err := r.Runtime.SpawnWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	return v2WorkerSnapshot(w), nil
}
func (r V2Runtime) GetWorker(id string) (*contract.Worker, bool) {
	w, ok := r.Runtime.GetWorker(id)
	return v2WorkerSnapshot(w), ok
}
func v2WorkerSnapshot(w *v2.Worker) *contract.Worker {
	if w == nil {
		return nil
	}
	return &contract.Worker{ID: w.ID, AgentName: w.AgentName, SessionID: w.SessionID,
		ParentSessionID: w.ParentSessionID, Prompt: w.Prompt, Status: w.Status, Background: w.Background,
		StartedAt: w.StartedAt, EndedAt: w.EndedAt, Result: w.Result, Err: w.Err}
}

type V2AgentRegistry struct{ Inner *v2.AgentRegistry }

func (r V2AgentRegistry) HasAgent(name string) bool {
	if r.Inner == nil {
		return false
	}
	_, ok := r.Inner.ByName(name)
	return ok
}
