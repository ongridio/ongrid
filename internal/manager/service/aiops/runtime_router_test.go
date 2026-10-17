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
	router, err := NewRuntimeRouter(nil, contract.V3, map[contract.Version]RuntimeEngine{contract.V3: engine})
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
	router, err := NewRuntimeRouter(nil, contract.V3, registry)
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
		if _, err := NewRuntimeRouter(nil, contract.V3, registry); err == nil {
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
	router, err := NewRuntimeRouter(nil, contract.V3, map[contract.Version]RuntimeEngine{contract.V2: first, contract.V3: second})
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
	router, err := NewRuntimeRouter(nil, contract.V3, map[contract.Version]RuntimeEngine{contract.V3: engine})
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
	router, err := NewRuntimeRouter(nil, contract.V3, map[contract.Version]RuntimeEngine{contract.V2: engine(), contract.V3: engine()})
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

func TestRuntimeRouter_DurableSessionVersion(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		requested    contract.Version
		want         contract.Version
		fail         bool
	}{
		{name: "pinned overrides default", stored: "v2", want: contract.V2},
		{name: "historical retains current default", want: contract.V3},
		{name: "matching context", stored: "v2", requested: contract.V2, want: contract.V2},
		{name: "conflict rejected", stored: "v2", requested: contract.V3, fail: true},
		{name: "unavailable rejected", stored: "v1", fail: true},
		{name: "unknown rejected", stored: "v99", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &memSessions{sessions: map[string]*model.Session{"s": {ID: "s", RuntimeVersion: tc.stored}}}
			calls := 0
			engine := func(version contract.Version) *routerEngine {
				return &routerEngine{
					handle: func(ctx context.Context, _ *contract.Request) (*contract.Reply, error) {
						calls++
						if version != tc.want || contract.VersionFromContext(ctx) != version {
							t.Fatalf("wrong engine: %s", version)
						}
						return &contract.Reply{}, nil
					},
					spawn: func(ctx context.Context, _ contract.SpawnRequest) (*contract.Worker, error) {
						calls++
						if version != tc.want || contract.VersionFromContext(ctx) != version {
							t.Fatalf("wrong worker engine: %s", version)
						}
						return &contract.Worker{ID: "w"}, nil
					},
				}
			}
			router, err := NewRuntimeRouter(repo, contract.V3, map[contract.Version]RuntimeEngine{contract.V2: engine(contract.V2), contract.V3: engine(contract.V3)})
			if err != nil {
				t.Fatal(err)
			}
			ctx := contract.WithVersion(context.Background(), tc.requested)
			_, err = router.Handle(ctx, &contract.Request{SessionID: "s"})
			if (err != nil) != tc.fail {
				t.Fatalf("Handle error=%v", err)
			}
			_, err = router.SpawnWorker(ctx, contract.SpawnRequest{ParentSession: "s"})
			if (err != nil) != tc.fail {
				t.Fatalf("SpawnWorker error=%v", err)
			}
			if tc.fail && calls != 0 {
				t.Fatal("rejected request executed")
			}
			if !tc.fail && calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			_, err = router.Handle(ctx, &contract.Request{SessionID: "missing"})
			if !errors.Is(err, errs.ErrNotFound) {
				t.Fatalf("missing session=%v", err)
			}
		})
	}
}

func TestCreateSession_PinsValidatedRuntimeDefault(t *testing.T) {
	repo := &memSessions{sessions: map[string]*model.Session{}}
	router, err := NewRuntimeRouter(repo, contract.V2, map[contract.Version]RuntimeEngine{contract.V2: &routerEngine{}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{sessions: repo, runtime: router}
	row, err := svc.CreateSession(context.Background(), Caller{UserID: 7}, CreateSessionInput{Title: "pinned"})
	if err != nil || row.RuntimeVersion != "v2" {
		t.Fatalf("create=%+v %v", row, err)
	}
	_, err = svc.CreateSession(contract.WithVersion(context.Background(), contract.V3), Caller{UserID: 7}, CreateSessionInput{})
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("unavailable version=%v", err)
	}
}

func TestRuntimeDefaultSettingDoesNotMoveExistingSessions(t *testing.T) {
	repo := &memSessions{sessions: map[string]*model.Session{
		"legacy": {ID: "legacy"}, "pinned": {ID: "pinned", RuntimeVersion: "v3"},
	}}
	providerCalls := 0
	selected := contract.V2
	providerErr := error(nil)
	engine := &routerEngine{handle: func(ctx context.Context, _ *contract.Request) (*contract.Reply, error) {
		if contract.VersionFromContext(ctx) != contract.V3 {
			t.Fatal("existing session moved")
		}
		return &contract.Reply{}, nil
	}}
	router, err := NewRuntimeRouter(repo, contract.V3, map[contract.Version]RuntimeEngine{contract.V3: engine},
		WithRuntimeDefaultProvider(func(context.Context) (contract.Version, error) { providerCalls++; return selected, providerErr }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.SessionVersion(t.Context()); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("unavailable selection: %v", err)
	}
	providerErr = errors.New("settings unavailable")
	if _, err := router.SessionVersion(t.Context()); !errors.Is(err, providerErr) {
		t.Fatalf("lost settings error: %v", err)
	}
	before := providerCalls
	for _, id := range []string{"legacy", "pinned"} {
		if _, err := router.Handle(t.Context(), &contract.Request{SessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if providerCalls != before {
		t.Fatal("existing sessions consulted new-session policy")
	}
	providerErr = nil
	for _, value := range []contract.Version{"", contract.V3} {
		selected = value
		if got, err := router.SessionVersion(t.Context()); err != nil || got != contract.V3 {
			t.Fatalf("selection %q: %q %v", value, got, err)
		}
	}
}
