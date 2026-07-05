package chatruntime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// TestSpawnWorker_Sync drives SpawnWorker with Background=false and
// verifies the returned worker is in WorkerStatusCompleted with the
// scripted assistant content as Result. Also asserts the worker's
// chat_sessions row was persisted with the audit columns
// (agent_id / parent_session_id / background).
func TestSpawnWorker_Sync(t *testing.T) {
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "worker-result",
	})
	store, parentID := newSeededRuntimeStore(t, 7)
	rt := newRuntimeWithStore(t, "incident-investigator", scripted, nil, store)

	w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName:     "incident-investigator",
		Prompt:        "diagnose host-01",
		ParentSession: parentID,
	})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if w == nil {
		t.Fatalf("expected non-nil worker")
	}
	if w.Status != WorkerStatusCompleted {
		t.Errorf("status = %q, want completed", w.Status)
	}
	if w.Result != "worker-result" {
		t.Errorf("result = %q, want worker-result", w.Result)
	}
	if w.AgentName != "incident-investigator" {
		t.Errorf("agent = %q", w.AgentName)
	}
	if !strings.HasPrefix(w.ID, "agent-") {
		t.Errorf("id = %q, want agent-* prefix", w.ID)
	}
	if !strings.HasPrefix(w.SessionID, "worker-") {
		t.Errorf("session_id = %q, want worker-* prefix", w.SessionID)
	}

	// chat_sessions audit row — assert all three columns are set.
	row, err := store.GetSession(context.Background(), w.SessionID)
	if err != nil || row == nil {
		t.Fatalf("worker chat_sessions row missing: %v", err)
	}
	if row.AgentID == nil || *row.AgentID != "incident-investigator" {
		t.Errorf("row.AgentID = %v, want incident-investigator", row.AgentID)
	}
	if row.ParentSessionID == nil || *row.ParentSessionID != parentID {
		t.Errorf("row.ParentSessionID = %v, want %q", row.ParentSessionID, parentID)
	}
	if row.Background {
		t.Errorf("row.Background = true, want false (sync spawn)")
	}
	if row.UserID != 7 {
		t.Errorf("row.UserID = %d, want 7 (inherited from parent)", row.UserID)
	}

	// ListByParent surfaces the worker session under the parent.
	kids, err := store.ListByParent(context.Background(), parentID)
	if err != nil {
		t.Fatalf("ListByParent: %v", err)
	}
	if len(kids) != 1 || kids[0].ID != w.SessionID {
		t.Errorf("ListByParent = %+v, want one row %q", kids, w.SessionID)
	}
}

func TestSpawnWorker_Sync_ForwardsWorkerToolEventsToParent(t *testing.T) {
	scripted := newScriptedChatModel(
		&schema.Message{
			Role:    schema.Assistant,
			Content: "checking metric",
			ToolCalls: []schema.ToolCall{{
				ID:       "call_worker_metric",
				Type:     "function",
				Function: schema.FunctionCall{Name: "query_promql", Arguments: `{"query":"up"}`},
			}},
		},
		&schema.Message{Role: schema.Assistant, Content: "worker-result"},
	)
	store, parentID := newSeededRuntimeStore(t, 7)
	rt := newRuntimeWithStore(t, "specialist-sre", scripted, []basetool.BaseTool{
		&fakeTool{name: "query_promql", schema: `{"type":"object","properties":{"query":{"type":"string"}}}`},
	}, store)

	var events []Event
	emit := func(ev Event) {
		events = append(events, ev)
	}
	w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName:     "specialist-sre",
		Prompt:        "check prometheus",
		ParentSession: parentID,
		ParentEmit:    emit,
	})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if w.Status != WorkerStatusCompleted {
		t.Fatalf("status = %q, want completed", w.Status)
	}

	var sawStart, sawEnd bool
	for _, ev := range events {
		if ev.Type == EventAssistant {
			t.Fatalf("worker assistant event leaked to parent stream: %+v", ev)
		}
		if ev.Tool == nil || ev.Tool.Name != "query_promql" {
			continue
		}
		if !strings.HasPrefix(ev.Tool.ToolCallID, w.ID+":") {
			t.Fatalf("worker tool_call_id = %q, want %q prefix", ev.Tool.ToolCallID, w.ID+":")
		}
		switch ev.Type {
		case EventToolStart:
			sawStart = true
			if ev.Tool.ArgsJSON != `{"query":"up"}` {
				t.Fatalf("start args = %q", ev.Tool.ArgsJSON)
			}
		case EventToolEnd:
			sawEnd = true
			if ev.Tool.Status != "success" {
				t.Fatalf("end status = %q, want success", ev.Tool.Status)
			}
			if !strings.Contains(ev.Tool.ResultJSON, `"ok":true`) {
				t.Fatalf("end result = %q", ev.Tool.ResultJSON)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing forwarded worker tool events: sawStart=%v sawEnd=%v events=%+v", sawStart, sawEnd, events)
	}
}

// TestSpawnWorker_Async drives SpawnWorker with Background=true and
// verifies (a) immediate return with status running OR completed (race)
// + (b) eventual TaskNotification fires through ParentEmit.
func TestSpawnWorker_Async(t *testing.T) {
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "async-result",
	})
	rt := newRuntimeWithAgent(t, "general-purpose", scripted, nil)

	var (
		mu     sync.Mutex
		events []Event
		done   = make(chan struct{}, 1)
	)
	emit := func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		if ev.Type == EventTaskNotification {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}

	w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName:  "general-purpose",
		Prompt:     "go",
		Background: true,
		ParentEmit: emit,
	})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if w == nil {
		t.Fatalf("expected worker")
	}
	// On the goroutine race the snapshot can already be running OR
	// completed (scripted model is fast). Both are valid post-spawn
	// states.
	if w.Status != WorkerStatusRunning &&
		w.Status != WorkerStatusPending &&
		w.Status != WorkerStatusCompleted {
		t.Errorf("immediate status = %q", w.Status)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for task_notification")
	}

	mu.Lock()
	defer mu.Unlock()
	var notif *TaskNotification
	for _, e := range events {
		if e.Type == EventTaskNotification {
			notif = e.Notification
			break
		}
	}
	if notif == nil {
		t.Fatalf("no task_notification in events: %+v", events)
	}
	if notif.TaskID != w.ID {
		t.Errorf("notif.TaskID = %q, worker.ID = %q", notif.TaskID, w.ID)
	}
	if notif.Status != WorkerStatusCompleted {
		t.Errorf("notif.status = %q, want completed", notif.Status)
	}
	if notif.Result != "async-result" {
		t.Errorf("notif.Result = %q", notif.Result)
	}
	if !strings.Contains(notif.Summary, "general-purpose") {
		t.Errorf("notif.Summary = %q (expected agent name)", notif.Summary)
	}
}

// TestSendToWorker_Continuation continues a completed worker with a
// follow-up message and checks the Result is updated.
func TestSendToWorker_Continuation(t *testing.T) {
	scripted := newScriptedChatModel(
		&schema.Message{Role: schema.Assistant, Content: "first"},
		&schema.Message{Role: schema.Assistant, Content: "second"},
	)
	rt := newRuntimeWithAgent(t, "general-purpose", scripted, nil)

	w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName: "general-purpose",
		Prompt:    "step 1",
	})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if w.Result != "first" {
		t.Fatalf("first result = %q, want first", w.Result)
	}
	if err := rt.SendToWorker(context.Background(), w.ID, "step 2 please"); err != nil {
		t.Fatalf("SendToWorker: %v", err)
	}
	got, ok := rt.GetWorker(w.ID)
	if !ok || got == nil {
		t.Fatalf("GetWorker after send: not found")
	}
	if got.Result != "second" {
		t.Errorf("after send, result = %q, want second", got.Result)
	}
	if got.Status != WorkerStatusCompleted {
		t.Errorf("status = %q", got.Status)
	}
}

// TestStopWorker_Idempotent confirms StopWorker is safe to call on a
// terminal worker.
func TestStopWorker_Idempotent(t *testing.T) {
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "x",
	})
	rt := newRuntimeWithAgent(t, "general-purpose", scripted, nil)

	w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName: "general-purpose",
		Prompt:    "go",
	})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := rt.StopWorker(context.Background(), w.ID); err != nil {
		t.Errorf("Stop on completed: %v", err)
	}
	// Stop a non-existent id returns an error per contract.
	if err := rt.StopWorker(context.Background(), "agent-nonsuch"); err == nil {
		t.Errorf("expected error for unknown worker id")
	}
}

// TestSpawnWorker_ClosesSession is the regression test for the orphan
// worker session bug that accumulated 161 closed_at=NULL rows on the
// test env before the defer-close was added in worker.go. Both the
// sync and async termination paths must call CloseSession; this drives
// both and asserts ClosedAt is non-nil after termination.
func TestSpawnWorker_ClosesSession(t *testing.T) {
	t.Run("sync", func(t *testing.T) {
		scripted := newScriptedChatModel(&schema.Message{
			Role: schema.Assistant, Content: "done",
		})
		store, parentID := newSeededRuntimeStore(t, 1)
		rt := newRuntimeWithStore(t, "incident-investigator", scripted, nil, store)

		w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
			AgentName:     "incident-investigator",
			Prompt:        "go",
			ParentSession: parentID,
		})
		if err != nil {
			t.Fatalf("SpawnWorker: %v", err)
		}
		row, err := store.GetSession(context.Background(), w.SessionID)
		if err != nil || row == nil {
			t.Fatalf("session row missing: %v", err)
		}
		if row.ClosedAt == nil {
			t.Fatalf("closed_at = nil after sync spawn; want non-nil so the row stops looking active")
		}
	})

	t.Run("async", func(t *testing.T) {
		scripted := newScriptedChatModel(&schema.Message{
			Role: schema.Assistant, Content: "done",
		})
		store, parentID := newSeededRuntimeStore(t, 1)
		rt := newRuntimeWithStore(t, "general-purpose", scripted, nil, store)

		done := make(chan struct{}, 1)
		emit := func(ev Event) {
			if ev.Type == EventTaskNotification {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		}
		w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
			AgentName:     "general-purpose",
			Prompt:        "go",
			Background:    true,
			ParentSession: parentID,
			ParentEmit:    emit,
		})
		if err != nil {
			t.Fatalf("SpawnWorker: %v", err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for task_notification")
		}
		// CloseSession is deferred after the notify fires, so give the
		// goroutine a tick to return.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			row, _ := store.GetSession(context.Background(), w.SessionID)
			if row != nil && row.ClosedAt != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("closed_at still nil after async spawn — defer didn't fire")
	})
}

// TestSpawnWorker_UnknownAgent confirms SpawnWorker rejects a name not
// in the AgentRegistry.
func TestSpawnWorker_UnknownAgent(t *testing.T) {
	scripted := newScriptedChatModel()
	rt := newRuntimeWithAgent(t, "general-purpose", scripted, nil)
	_, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName: "no-such-agent",
		Prompt:    "x",
	})
	if err == nil {
		t.Fatalf("expected error for unknown agent")
	}
}

// TestFilterToolsForAgent verifies whitelist + blacklist + the
// implicit coordinator-only strip.
func TestFilterToolsForAgent(t *testing.T) {
	bag := []basetool.BaseTool{
		&fakeTool{name: "AgentTool", schema: `{"type":"object"}`},            // coordinator-only — always stripped
		&fakeTool{name: "SendMessage", schema: `{"type":"object"}`},          // coordinator-only — always stripped
		&fakeTool{name: "TaskStop", schema: `{"type":"object"}`},             // coordinator-only — always stripped
		&fakeTool{name: "query_promql", schema: `{"type":"object"}`},         // whitelisted
		&fakeTool{name: "query_logql", schema: `{"type":"object"}`},          // not whitelisted
		&fakeTool{name: "host_restart_service", schema: `{"type":"object"}`}, // blacklisted
		&fakeTool{name: "kill_process", schema: `{"type":"object"}`},         // blacklisted via *_process
	}
	ag := &Agent{
		Name:            "test-worker",
		Tools:           []string{"query_promql"},
		DisallowedTools: []string{"host_restart_service", "*_process"},
	}
	// Worker path: isCoordinator=false → AgentTool/SendMessage/TaskStop stripped.
	out := filterToolsForAgent(bag, ag, false)
	names := make([]string, 0, len(out))
	for _, t := range out {
		info, _ := t.Info(context.Background())
		names = append(names, info.Name)
	}
	if len(names) != 1 || names[0] != "query_promql" {
		t.Errorf("worker filtered names = %v, want [query_promql]", names)
	}

	// Coordinator path: isCoordinator=true → coordinator-only tools
	// survive even when the persona's Tools whitelist doesn't include
	// them. (The whitelist still gates non-control-plane tools.)
	out = filterToolsForAgent(bag, ag, true)
	names = names[:0]
	for _, t := range out {
		info, _ := t.Info(context.Background())
		names = append(names, info.Name)
	}
	gotCoord := map[string]bool{}
	for _, n := range names {
		gotCoord[n] = true
	}
	if !gotCoord["AgentTool"] || !gotCoord["SendMessage"] || !gotCoord["TaskStop"] || !gotCoord["query_promql"] {
		t.Errorf("coordinator filtered names = %v; want all of AgentTool / SendMessage / TaskStop / query_promql", names)
	}
	if gotCoord["query_logql"] || gotCoord["host_restart_service"] || gotCoord["kill_process"] {
		t.Errorf("coordinator filtered names = %v; whitelist or blacklist leaked", names)
	}
}

// TestNotificationFor_FieldShape locks down the wire shape consumers
// (the SPA's task_notification renderer) depend on.
func TestNotificationFor_FieldShape(t *testing.T) {
	scripted := newScriptedChatModel(&schema.Message{
		Role:    schema.Assistant,
		Content: "shape-test",
	})
	rt := newRuntimeWithAgent(t, "general-purpose", scripted, nil)
	w, err := rt.SpawnWorker(context.Background(), SpawnRequest{
		AgentName: "general-purpose",
		Prompt:    "shape",
	})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	// Reach into the internal map to invoke notificationFor on the
	// canonical worker ptr (snapshots have nil mu).
	rt.workersMu.Lock()
	internal := rt.workers[w.ID]
	rt.workersMu.Unlock()
	ev := rt.notificationFor(internal)
	if ev.Type != EventTaskNotification {
		t.Errorf("type = %q", ev.Type)
	}
	if ev.Notification == nil {
		t.Fatalf("Notification nil")
	}
	if ev.Notification.TaskID != w.ID {
		t.Errorf("TaskID = %q", ev.Notification.TaskID)
	}
	if ev.Notification.Status != WorkerStatusCompleted {
		t.Errorf("Status = %q", ev.Notification.Status)
	}
	if ev.Notification.Result != "shape-test" {
		t.Errorf("Result = %q", ev.Notification.Result)
	}
	if _, ok := ev.Notification.Usage["duration_ms"]; !ok {
		t.Errorf("usage.duration_ms missing")
	}
}

// newRuntimeWithAgent builds a Runtime with one persona registered and
// no tool bag. cfg.AgentRegistry's Add is used to inject the persona
// inline so the test doesn't need a fixture file. Default
// graph.MaxIterations is bumped down to keep tests snappy.
func newRuntimeWithAgent(t *testing.T, name string, cm *scriptedChatModel, toolBag []basetool.BaseTool) *Runtime {
	t.Helper()
	sess := &model.Session{ID: "s1", UserID: 7}
	return newRuntimeWithStore(t, name, cm, toolBag, newMemSessions(sess))
}

// newRuntimeWithStore is the shape used by tests that need to inspect
// the SessionRepo after the spawn (worker session row, ListByParent).
// Caller passes the pre-seeded *memSessions so the test can read it back.
func newRuntimeWithStore(t *testing.T, name string, cm *scriptedChatModel, toolBag []basetool.BaseTool, store *memSessions) *Runtime {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Add(&Agent{
		Name:        name,
		Description: "test agent",
		WhenToUse:   "test",
		MaxTurns:    3,
		Tools:       []string{}, // empty whitelist = inherit all tools (minus coordinator-only)
	})
	rt, err := NewRuntime(Config{
		Sessions:      store,
		ChatModel:     cm,
		ToolBag:       toolBag,
		AgentRegistry: reg,
		GraphCfg:      graph.Config{MaxIterations: 3},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

// newSeededRuntimeStore returns a memSessions with one parent (coordinator)
// session pre-inserted, plus that parent's id. UserID is the owner.
func newSeededRuntimeStore(t *testing.T, userID uint64) (*memSessions, string) {
	t.Helper()
	parentID := "parent-sess-1"
	store := newMemSessions(&model.Session{ID: parentID, UserID: userID, Title: "parent"})
	return store, parentID
}
