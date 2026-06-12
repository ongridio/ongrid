package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// fakeLLM stubs llm.Client; returns the canned content on Chat.
type fakeLLM struct {
	mu        sync.Mutex
	content   string
	err       error
	calls     int
	gotMsgs   []llm.Message
	delay     time.Duration
	gotModels []string
}

func (f *fakeLLM) Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls++
	f.gotMsgs = append([]llm.Message(nil), req.Messages...)
	f.gotModels = append(f.gotModels, req.Model)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &llm.ChatResp{
		Assistant: llm.Message{Role: "assistant", Content: f.content},
		Usage:     llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

// fakeTools stubs ToolInvoker; returns a canned bundle.
type fakeTools struct {
	mu        sync.Mutex
	bundle    json.RawMessage
	err       error
	gotName   string
	gotArgs   json.RawMessage
	callCount int
}

func (f *fakeTools) Invoke(_ context.Context, name string, args json.RawMessage) (aiopstools.ExecuteResult, error) {
	f.mu.Lock()
	f.gotName = name
	f.gotArgs = append(json.RawMessage(nil), args...)
	f.callCount++
	f.mu.Unlock()
	if f.err != nil {
		return aiopstools.ExecuteResult{}, f.err
	}
	return aiopstools.ExecuteResult{ResultJSON: f.bundle}, nil
}

// fakeWriter captures CreateEvent calls.
type fakeWriter struct {
	mu     sync.Mutex
	events []*model.Event
	err    error
}

func (w *fakeWriter) CreateEvent(_ context.Context, ev *model.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	cp := *ev
	w.events = append(w.events, &cp)
	return nil
}

func (w *fakeWriter) snapshot() []*model.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*model.Event, len(w.events))
	copy(out, w.events)
	return out
}

func newQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestInvestigateAsyncWritesEvent: a happy-path investigation produces
// one ai_initial_diagnosis event whose Message column carries the
// LLM's canned response.
func TestInvestigateAsyncWritesEvent(t *testing.T) {
	canned := "1. CPU 100% on host-7 critical scope.\n2. Likely runaway process; recent deploy.\n3. Restart service / check logs / page L2."
	llmFake := &fakeLLM{content: canned}
	toolsFake := &fakeTools{bundle: json.RawMessage(`{"incident":{"id":7}}`)}
	writer := &fakeWriter{}

	inv := New(llmFake, toolsFake, writer, Config{Workers: 1, QueueDepth: 4}, newQuietLogger())
	defer inv.Close()

	incident := &model.Incident{
		ID:       7,
		Title:    "cpu_high host-7",
		Rule:     "cpu_high",
		Severity: "critical",
		Status:   model.IncidentStatusOpen,
	}

	inv.InvestigateAsync(incident)
	inv.Close() // drains in-flight goroutines

	got := writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	ev := got[0]
	if ev.EventType != model.EventTypeAIInitialDiagnosis {
		t.Errorf("event_type=%q, want %q", ev.EventType, model.EventTypeAIInitialDiagnosis)
	}
	if ev.IncidentID != 7 {
		t.Errorf("incident_id=%d, want 7", ev.IncidentID)
	}
	if ev.ActorType != model.ActorTypeSystem {
		t.Errorf("actor_type=%q, want %q", ev.ActorType, model.ActorTypeSystem)
	}
	if ev.Message == nil || *ev.Message != canned {
		t.Errorf("message mismatch; got %v", ev.Message)
	}
	if ev.Title != "AI 初查" {
		t.Errorf("title=%q, want %q", ev.Title, "AI 初查")
	}
	if ev.Severity != "critical" {
		t.Errorf("severity=%q, want critical", ev.Severity)
	}

	// Tool invocation sanity.
	if toolsFake.gotName != aiopstools.ToolNameCorrelateIncident {
		t.Errorf("tool name=%q, want %q", toolsFake.gotName, aiopstools.ToolNameCorrelateIncident)
	}
	var argMap map[string]any
	_ = json.Unmarshal(toolsFake.gotArgs, &argMap)
	if argMap["incident_id"] != float64(7) {
		t.Errorf("expected incident_id=7 in args, got %v", argMap["incident_id"])
	}

	// LLM round-trip sanity: system prompt + user bundle.
	if llmFake.calls != 1 {
		t.Errorf("llm calls=%d, want 1", llmFake.calls)
	}
	if len(llmFake.gotMsgs) != 2 {
		t.Fatalf("expected 2 messages (system+user), got %d", len(llmFake.gotMsgs))
	}
	if llmFake.gotMsgs[0].Role != "system" {
		t.Errorf("msg[0].role=%q, want system", llmFake.gotMsgs[0].Role)
	}
	if !strings.Contains(llmFake.gotMsgs[0].Content, "ongrid AIOps") {
		t.Errorf("system prompt doesn't mention ongrid AIOps: %q", llmFake.gotMsgs[0].Content)
	}
	if llmFake.gotMsgs[1].Role != "user" {
		t.Errorf("msg[1].role=%q, want user", llmFake.gotMsgs[1].Role)
	}
}

func TestInvestigateAsyncEmptyModelUsesRouterDefault(t *testing.T) {
	llmFake := &fakeLLM{content: "initial diagnosis"}
	toolsFake := &fakeTools{bundle: json.RawMessage(`{"incident":{"id":8}}`)}
	writer := &fakeWriter{}

	inv := New(llmFake, toolsFake, writer, Config{Workers: 1, QueueDepth: 4}, newQuietLogger())
	defer inv.Close()

	inv.InvestigateAsync(&model.Incident{ID: 8, Rule: "cpu_high", Severity: "warning"})
	inv.Close()

	if len(llmFake.gotModels) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(llmFake.gotModels))
	}
	if llmFake.gotModels[0] != "" {
		t.Fatalf("legacy investigator model = %q, want empty router default", llmFake.gotModels[0])
	}
}

// TestInvestigateAsyncDropsOnFullQueue: when the queue is saturated,
// new jobs are silently dropped (logged warning) rather than blocking.
func TestInvestigateAsyncDropsOnFullQueue(t *testing.T) {
	// Slow LLM so the single worker stays occupied while we enqueue
	// more jobs than the queue can hold.
	llmFake := &fakeLLM{content: "ok", delay: 200 * time.Millisecond}
	toolsFake := &fakeTools{bundle: json.RawMessage(`{}`)}
	writer := &fakeWriter{}

	inv := New(llmFake, toolsFake, writer, Config{Workers: 1, QueueDepth: 2}, newQuietLogger())
	defer inv.Close()

	// 1 in-flight + 2 queued + N dropped. Submit 10.
	const submitted = 10
	for i := 1; i <= submitted; i++ {
		inv.InvestigateAsync(&model.Incident{
			ID:       uint64(i),
			Rule:     "cpu_high",
			Severity: "warning",
		})
	}
	// Wait for all workers to finish.
	inv.Close()

	got := len(writer.snapshot())
	// At most workers + queueDepth investigations succeeded. With
	// Workers=1 + QueueDepth=2 the absolute upper bound is 3.
	if got > 3 {
		t.Errorf("got %d events; expected at most 3 (1 worker + 2 queue)", got)
	}
	if got < 1 {
		t.Errorf("got 0 events; expected at least 1 to complete")
	}
	if got >= submitted {
		t.Errorf("queue overflow not honored: %d events == %d submitted", got, submitted)
	}
}

// TestInvestigateAsyncNilSafety: a nil Investigator is a no-op.
func TestInvestigateAsyncNilSafety(t *testing.T) {
	var inv *Investigator
	inv.InvestigateAsync(&model.Incident{ID: 1}) // must not panic
	inv.Close()                                  // must not panic
}

// TestInvestigateAsyncSkipsOnNoAPIKey: ErrNoAPIKey from llm.Chat is
// treated as benign (LLM disabled at runtime); no event written, no
// noisy WARN.
func TestInvestigateAsyncSkipsOnNoAPIKey(t *testing.T) {
	llmFake := &fakeLLM{err: llm.ErrNoAPIKey}
	toolsFake := &fakeTools{bundle: json.RawMessage(`{"incident":{"id":1}}`)}
	writer := &fakeWriter{}

	inv := New(llmFake, toolsFake, writer, Config{Workers: 1, QueueDepth: 4}, newQuietLogger())
	defer inv.Close()

	inv.InvestigateAsync(&model.Incident{ID: 1, Rule: "cpu_high", Severity: "warning"})
	inv.Close()

	if got := len(writer.snapshot()); got != 0 {
		t.Errorf("expected 0 events on ErrNoAPIKey, got %d", got)
	}
}

// TestInvestigateAsyncToolError: a correlate_incident failure aborts
// without writing an event.
func TestInvestigateAsyncToolError(t *testing.T) {
	llmFake := &fakeLLM{content: "should not be called"}
	toolsFake := &fakeTools{err: errors.New("prom unreachable")}
	writer := &fakeWriter{}

	inv := New(llmFake, toolsFake, writer, Config{Workers: 1, QueueDepth: 4}, newQuietLogger())
	defer inv.Close()

	inv.InvestigateAsync(&model.Incident{ID: 1, Rule: "cpu_high", Severity: "warning"})
	inv.Close()

	if llmFake.calls != 0 {
		t.Errorf("llm called %d times despite tool error; want 0", llmFake.calls)
	}
	if got := len(writer.snapshot()); got != 0 {
		t.Errorf("expected 0 events on tool error, got %d", got)
	}
}

// TestCapUserMessage exercises the size-cap helper.
func TestCapUserMessage(t *testing.T) {
	in := []byte(strings.Repeat("a", 100))
	out := capUserMessage(in, 50)
	if len(out) <= 50 {
		t.Errorf("expected len > 50 (cap+marker), got %d", len(out))
	}
	if !strings.HasSuffix(string(out), "...(truncated)") {
		t.Errorf("expected truncation marker; got tail %q", string(out[len(out)-32:]))
	}
	// Below-cap bytes pass through.
	in = []byte("short")
	out = capUserMessage(in, 50)
	if string(out) != "short" {
		t.Errorf("under-cap input mutated: got %q", string(out))
	}
}

// TestConcurrencyMultipleWorkers: 5 jobs against 3 workers complete
// without races; every job lands a unique event.
func TestConcurrencyMultipleWorkers(t *testing.T) {
	llmFake := &fakeLLM{content: "ok", delay: 50 * time.Millisecond}
	toolsFake := &fakeTools{bundle: json.RawMessage(`{}`)}
	writer := &fakeWriter{}

	inv := New(llmFake, toolsFake, writer, Config{Workers: 3, QueueDepth: 16}, newQuietLogger())
	defer inv.Close()

	var enqueued atomic.Int32
	for i := 1; i <= 5; i++ {
		enqueued.Add(1)
		inv.InvestigateAsync(&model.Incident{
			ID:       uint64(i),
			Rule:     "cpu_high",
			Severity: "warning",
		})
	}
	inv.Close()

	if got := len(writer.snapshot()); got != int(enqueued.Load()) {
		t.Errorf("expected %d events, got %d", enqueued.Load(), got)
	}
}
