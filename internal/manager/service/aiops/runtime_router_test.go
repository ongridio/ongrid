package aiops

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type routerEngine struct {
	handle func(context.Context, *contract.Request) (*contract.Reply, error)
	spawn  func(context.Context, contract.SpawnRequest) (*contract.Worker, error)
	send   func(context.Context, string, string) error
	stop   func(context.Context, string) error
	worker *contract.Worker
}

func (e *routerEngine) Handle(ctx context.Context, req *contract.Request) (*contract.Reply, error) {
	return e.handle(ctx, req)
}
func (e *routerEngine) SpawnWorker(ctx context.Context, req contract.SpawnRequest) (*contract.Worker, error) {
	return e.spawn(ctx, req)
}
func (e *routerEngine) SendToWorker(ctx context.Context, id, message string) error {
	return e.send(ctx, id, message)
}
func (e *routerEngine) StopWorker(ctx context.Context, id string) error { return e.stop(ctx, id) }
func (e *routerEngine) GetWorker(id string) (*contract.Worker, bool) {
	return e.worker, e.worker != nil && e.worker.ID == id
}
func (e *routerEngine) CountWorkersByStatus() (int, int) { return 1, 2 }

func TestRuntimeRouterPreservesRequestEventsAndResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type identityKey struct{}
	ctx = context.WithValue(ctx, identityKey{}, "identity")
	reply := &contract.Reply{Message: &model.Message{ID: "final"}, Iterations: 7, ToolCalls: []*model.ToolCall{{ID: "tool"}}}
	events := []contract.Event{
		{Type: contract.EventAssistant, Assistant: &contract.AssistantEvent{MessageID: "a", Content: "answer"}},
		{Type: contract.EventToolStart, Tool: &contract.ToolEvent{ToolCallID: "t", Name: "web_search", ArgsJSON: `{"q":"test"}`}},
		{Type: contract.EventToolEnd, Tool: &contract.ToolEvent{ToolCallID: "t", ResultJSON: `{"result":"ok"}`}},
		{Type: contract.EventApprovalPending, Approval: &contract.ApprovalPending{ApprovalID: "approval", Credentials: []string{"credential"}}},
		{Type: contract.EventTaskNotification, Notification: &contract.TaskNotification{TaskID: "worker", Status: contract.WorkerStatusCompleted}},
		{Type: contract.EventError, Error: "provider error"},
		{Type: contract.EventDone, Done: reply},
	}
	var got []contract.Event
	req := &contract.Request{SessionID: "session", UserID: 42, Role: "admin", UserText: "prompt", Attachments: []model.Attachment{{ID: "image"}}, Mentions: []contract.Mention{{Type: "device", ID: "12"}}, Provider: "provider", Model: "model", Locale: "en", WebSearchEnabled: true, Emit: func(e contract.Event) { got = append(got, e) }}
	engine := &routerEngine{handle: func(inner context.Context, in *contract.Request) (*contract.Reply, error) {
		if in != req {
			t.Fatal("router replaced request")
		}
		if inner.Value(identityKey{}) != "identity" || inner.Done() != ctx.Done() || contract.VersionFromContext(inner) != contract.V3 {
			t.Fatal("context lost at routing boundary")
		}
		for _, event := range events {
			in.Emit(event)
		}
		return reply, nil
	}}
	router, err := NewRuntimeRouter(contract.V3, map[contract.Version]RuntimeEngine{contract.V3: engine})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.Handle(ctx, req)
	if err != nil || result != reply || !reflect.DeepEqual(got, events) {
		t.Fatalf("payload loss: result=%+v, err=%v, events=%+v", result, err, got)
	}
}

func TestRuntimeRouterRejectsUnavailableVersionWithoutFallback(t *testing.T) {
	var calls atomic.Int32
	engine := &routerEngine{handle: func(context.Context, *contract.Request) (*contract.Reply, error) { calls.Add(1); return nil, nil }}
	registry := map[contract.Version]RuntimeEngine{contract.V3: engine}
	router, err := NewRuntimeRouter(contract.V3, registry)
	if err != nil {
		t.Fatal(err)
	}
	delete(registry, contract.V3) // the caller cannot mutate the router registry
	for _, version := range []contract.Version{contract.V1, contract.V2, "v99"} {
		if _, err := router.Handle(contract.WithVersion(t.Context(), version), &contract.Request{}); !errors.Is(err, errs.ErrNotWiredYet) {
			t.Fatalf("version %s: %v", version, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported version silently fell back")
	}
	if _, err := router.Handle(t.Context(), &contract.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Handle(t.Context(), nil); !errors.Is(err, errs.ErrInvalid) {
		t.Fatal(err)
	}
	for _, registry := range []map[contract.Version]RuntimeEngine{nil, {contract.V3: nil}, {contract.V3: (*routerEngine)(nil)}, {"v99": engine}} {
		if _, err := NewRuntimeRouter(contract.V3, registry); err == nil {
			t.Fatal("invalid registry accepted")
		}
	}
}

func TestRuntimeRouterRoutesWorkerControlToOwner(t *testing.T) {
	// Workers created directly by version-local review paths must remain visible.
	first := &routerEngine{worker: &contract.Worker{ID: "v2-worker", Status: contract.WorkerStatusRunning}}
	second := &routerEngine{worker: &contract.Worker{ID: "v3-worker", Status: contract.WorkerStatusRunning}}
	var sends, stops atomic.Int32
	first.send = func(ctx context.Context, id, message string) error {
		if contract.VersionFromContext(ctx) != contract.V2 || id != "v2-worker" || message != "follow up" {
			t.Error("incorrect worker route")
		}
		sends.Add(1)
		return nil
	}
	first.stop = func(ctx context.Context, id string) error {
		if contract.VersionFromContext(ctx) != contract.V2 || id != "v2-worker" {
			t.Error("incorrect stop route")
		}
		stops.Add(1)
		return nil
	}
	router, err := NewRuntimeRouter(contract.V3, map[contract.Version]RuntimeEngine{contract.V2: first, contract.V3: second})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := router.SendToWorker(t.Context(), "v2-worker", "follow up"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := router.StopWorker(t.Context(), "v2-worker"); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 8 || stops.Load() != 1 {
		t.Fatal("worker controls lost")
	}
	if err := router.StopWorker(contract.WithVersion(t.Context(), contract.V3), "v2-worker"); !errors.Is(err, errs.ErrInvalid) {
		t.Fatal("cross-version control accepted")
	}
	if _, ok := router.GetWorker("missing"); ok {
		t.Fatal("missing worker found")
	}
	if err := router.SendToWorker(t.Context(), "missing", "x"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatal(err)
	}
	if w, ok := router.GetWorker("v2-worker"); !ok || w.ID != "v2-worker" {
		t.Fatal("worker lookup lost")
	}
	if running, pending := router.CountWorkersByStatus(); running != 2 || pending != 4 {
		t.Fatalf("worker metrics lost: %d %d", running, pending)
	}
}

func TestRuntimeRouterPreservesWorkerSpawnAndErrors(t *testing.T) {
	sentinel := errors.New("execution failed")
	now := time.Now()
	want := &contract.Worker{ID: "worker", AgentName: "expert", SessionID: "session", ParentSessionID: "parent", Prompt: "prompt", Status: contract.WorkerStatusCompleted, Background: true, StartedAt: now, EndedAt: &now, Result: "result", Err: "details"}
	var notified bool
	req := contract.SpawnRequest{AgentName: "expert", Prompt: "prompt", Background: true, ParentSession: "parent", SessionKind: "flow", OwnerUserID: 42, Locale: "en", Provider: "provider", Model: "model", ParentEmit: func(contract.Event) { notified = true }}
	engine := &routerEngine{spawn: func(ctx context.Context, in contract.SpawnRequest) (*contract.Worker, error) {
		if contract.VersionFromContext(ctx) != contract.V3 {
			t.Fatal("worker version lost")
		}
		if in.ParentEmit == nil {
			t.Fatal("worker event sink lost")
		}
		in.ParentEmit(contract.Event{})
		in.ParentEmit = nil
		copy := req
		copy.ParentEmit = nil
		if !reflect.DeepEqual(in, copy) {
			t.Fatal("worker input lost")
		}
		return want, nil
	}, handle: func(context.Context, *contract.Request) (*contract.Reply, error) { return nil, sentinel }}
	router, err := NewRuntimeRouter(contract.V3, map[contract.Version]RuntimeEngine{contract.V3: engine})
	if err != nil {
		t.Fatal(err)
	}
	got, err := router.SpawnWorker(t.Context(), req)
	if err != nil || got != want || !notified {
		t.Fatal("worker result or event lost")
	}
	if _, err := router.Handle(t.Context(), &contract.Request{}); !errors.Is(err, sentinel) {
		t.Fatal("runtime error lost")
	}
}

func TestRuntimeRouterRejectsAmbiguousWorkerIdentity(t *testing.T) {
	engine := func() *routerEngine { return &routerEngine{worker: &contract.Worker{ID: "collision"}} }
	router, err := NewRuntimeRouter(contract.V3, map[contract.Version]RuntimeEngine{contract.V2: engine(), contract.V3: engine()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := router.GetWorker("collision"); ok {
		t.Fatal("ambiguous worker selected")
	}
	if err := router.StopWorker(t.Context(), "collision"); err == nil {
		t.Fatal("ambiguous worker controlled")
	}
}
