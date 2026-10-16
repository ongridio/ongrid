package callbacks

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// captureSSE collects SSEEvents emitted by the handler. Goroutine-safe
// — the handler is meant to be invoked from a single goroutine in
// production but tool fan-out + stream draining can interleave.
type captureSSE struct {
	mu     sync.Mutex
	events []SSEEvent
}

func (c *captureSSE) emit(e SSEEvent) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *captureSSE) snapshot() []SSEEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]SSEEvent, len(c.events))
	copy(cp, c.events)
	return cp
}

func (c *captureSSE) hasType(tp SSEEventType) bool {
	for _, e := range c.snapshot() {
		if e.Type == tp {
			return true
		}
	}
	return false
}

func TestSSEHandler_NilEmitterReturnsNil(t *testing.T) {
	t.Parallel()
	if NewSSEHandler(nil) != nil {
		t.Fatalf("expected nil when emitter is nil")
	}
}

func TestSSEHandler_AssistantStartEnd(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	ctx := context.Background()
	h.OnStart(ctx, chatModelInfo(), &einomodel.CallbackInput{})
	h.OnEnd(ctx, chatModelInfo(), &einomodel.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "done"},
	})
	events := cap.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 events got %d", len(events))
	}
	if events[0].Type != SSEEventAssistantStart {
		t.Errorf("event[0] = %s, want assistant_start", events[0].Type)
	}
	if events[0].Iteration != 1 {
		t.Errorf("iteration[0] = %d, want 1", events[0].Iteration)
	}
	if events[1].Type != SSEEventAssistantEnd {
		t.Errorf("event[1] = %s, want assistant_end", events[1].Type)
	}
	if events[1].Assistant == nil || events[1].Assistant.Content != "done" {
		t.Errorf("end content payload missing: %+v", events[1].Assistant)
	}
}

func TestSSEHandler_ToolLifecycle(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	ctx := WithToolCallID(context.Background(), "tc-1")
	h.OnStart(ctx, toolInfo("query_promql"), &einotool.CallbackInput{ArgumentsInJSON: `{"q":"up"}`})
	h.OnEnd(ctx, toolInfo("query_promql"), &einotool.CallbackOutput{Response: `{"ok":true}`})
	events := cap.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 events got %d", len(events))
	}
	if events[0].Type != SSEEventToolStart {
		t.Errorf("event[0] = %s, want tool_start", events[0].Type)
	}
	if events[0].Tool.ArgsJSON != `{"q":"up"}` {
		t.Errorf("args = %q, want %q", events[0].Tool.ArgsJSON, `{"q":"up"}`)
	}
	if events[1].Type != SSEEventToolEnd {
		t.Errorf("event[1] = %s, want tool_end", events[1].Type)
	}
	if events[1].Tool.Status != "success" {
		t.Errorf("status = %s, want success", events[1].Tool.Status)
	}
	if events[1].Tool.ResultJSON != `{"ok":true}` {
		t.Errorf("result = %q", events[1].Tool.ResultJSON)
	}
}

func TestSSEHandler_ToolError(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	ctx := WithToolCallID(context.Background(), "tc-2")
	h.OnStart(ctx, toolInfo("flaky"), &einotool.CallbackInput{})
	h.OnError(ctx, toolInfo("flaky"), errors.New("kaboom"))
	events := cap.snapshot()
	last := events[len(events)-1]
	if last.Type != SSEEventToolEnd {
		t.Fatalf("last = %s, want tool_end", last.Type)
	}
	if last.Tool.Status != "error" {
		t.Errorf("status = %s, want error", last.Tool.Status)
	}
	if last.Tool.Error != "kaboom" {
		t.Errorf("error = %q", last.Tool.Error)
	}
}

func TestSSEHandler_TopLevelErrorEmitsError(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	graphInfo := &callbacks.RunInfo{Name: "OngridReActAgent", Component: components.Component("Graph")}
	h.OnError(context.Background(), graphInfo, errors.New("graph went bad"))
	if !cap.hasType(SSEEventError) {
		t.Fatalf("expected error event, got %+v", cap.snapshot())
	}
}

func TestSSEHandler_GraphEndEmitsDone(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	graphInfo := &callbacks.RunInfo{Name: "OngridReActAgent", Component: components.Component("Graph")}
	h.OnEnd(context.Background(), graphInfo, nil)
	if !cap.hasType(SSEEventDone) {
		t.Fatalf("expected done event, got %+v", cap.snapshot())
	}
}

func TestSSEHandler_NeededGating(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	if !h.Needed(context.Background(), chatModelInfo(), callbacks.TimingOnStart) {
		t.Error("ChatModel start should be needed")
	}
	if !h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnEnd) {
		t.Error("Tool end should be needed")
	}
	graphInfo := &callbacks.RunInfo{Component: components.Component("Graph")}
	if !h.Needed(context.Background(), graphInfo, callbacks.TimingOnEnd) {
		t.Error("Graph end should be needed (drives done event)")
	}
}

func TestSSEHandler_IterationCount(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	h := NewSSEHandler(cap.emit)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		h.OnStart(ctx, chatModelInfo(), &einomodel.CallbackInput{})
		h.OnEnd(ctx, chatModelInfo(), &einomodel.CallbackOutput{Message: &schema.Message{Role: schema.Assistant}})
	}
	if h.IterationCount() != 3 {
		t.Errorf("IterationCount = %d, want 3", h.IterationCount())
	}
}

// TestSSEEvent_TaskNotification_Shape pins the wire-shape contract for
// the task_notification frame so the SPA's task_notification
// renderer doesn't drift. The emitter is invoked directly here — the
// runtime is the one that fires this event in production (it bypasses
// the graph callback chain and feeds the SSE channel directly).
func TestSSEEvent_TaskNotification_Shape(t *testing.T) {
	t.Parallel()
	cap := &captureSSE{}
	cap.emit(SSEEvent{
		Type: SSEEventTaskNotification,
		Notification: &TaskNotificationPayload{
			TaskID:  "agent-12ab34cd",
			Status:  "completed",
			Summary: "Agent incident-investigator completed",
			Result:  "the answer is 42",
			Usage:   map[string]any{"duration_ms": int64(1234)},
		},
	})
	events := cap.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != SSEEventTaskNotification {
		t.Errorf("type = %q, want task_notification", events[0].Type)
	}
	n := events[0].Notification
	if n == nil {
		t.Fatalf("Notification nil")
	}
	if n.TaskID != "agent-12ab34cd" {
		t.Errorf("task_id = %q", n.TaskID)
	}
	if n.Status != "completed" {
		t.Errorf("status = %q", n.Status)
	}
	if n.Result != "the answer is 42" {
		t.Errorf("result = %q", n.Result)
	}
	if got, _ := n.Usage["duration_ms"].(int64); got != 1234 {
		t.Errorf("usage.duration_ms = %v", n.Usage["duration_ms"])
	}
}
