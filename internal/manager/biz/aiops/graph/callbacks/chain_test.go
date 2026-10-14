package callbacks

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func TestNewDefaultHandlers_AllWiredEmitsFive(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	reg := prometheus.NewRegistry()
	deps := Deps{
		Persistence:   PersistenceDeps{SessionID: "s", Repo: repo, Registerer: reg},
		SSE:           func(SSEEvent) {},
		Audit:         AuditDeps{Logger: slog.Default(), SessionID: "s"},
		Metrics:       MetricsDeps{Registerer: reg},
		BudgetChecker: stubBudgetChecker{},
		BudgetUserID:  1,
	}
	got := NewDefaultHandlers(deps)
	if len(got) != 5 {
		t.Fatalf("expected 5 handlers, got %d", len(got))
	}
	// Spot-check ordering: persistence first, budget last.
	if _, ok := got[0].(*PersistenceHandler); !ok {
		t.Errorf("got[0] = %T, want *PersistenceHandler", got[0])
	}
	if _, ok := got[len(got)-1].(*llm.BudgetCallbackHandler); !ok {
		t.Errorf("got[last] = %T, want *llm.BudgetCallbackHandler", got[len(got)-1])
	}
}

func TestNewDefaultHandlers_PartialDepsSkipsHandlers(t *testing.T) {
	t.Parallel()
	got := NewDefaultHandlers(Deps{})
	if len(got) != 0 {
		t.Fatalf("expected empty chain when no deps wired, got %d", len(got))
	}
}

func TestNewDefaultHandlers_OnlyMetrics(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	got := NewDefaultHandlers(Deps{Metrics: MetricsDeps{Registerer: reg}})
	if len(got) != 1 {
		t.Fatalf("expected 1 handler got %d", len(got))
	}
	if _, ok := got[0].(*MetricsHandler); !ok {
		t.Errorf("got[0] = %T", got[0])
	}
}

func TestNewDefaultHandlers_AllImplementHandlerInterface(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	reg := prometheus.NewRegistry()
	deps := Deps{
		Persistence:   PersistenceDeps{SessionID: "s", Repo: repo, Registerer: reg},
		SSE:           func(SSEEvent) {},
		Audit:         AuditDeps{Logger: slog.Default(), SessionID: "s"},
		Metrics:       MetricsDeps{Registerer: reg},
		BudgetChecker: stubBudgetChecker{},
	}
	for i, h := range NewDefaultHandlers(deps) {
		if _, ok := h.(callbacks.Handler); !ok {
			t.Errorf("got[%d] = %T does not satisfy callbacks.Handler", i, h)
		}
	}
}

func TestPersistenceAndSSEHandlersStreamIndependently(t *testing.T) {
	t.Parallel()
	repo := newFakeSessionRepo()
	persistence := NewPersistenceHandler(PersistenceDeps{SessionID: "session-1", Repo: repo})
	events := make(chan SSEEvent, 8)
	sse := NewSSEHandler(func(event SSEEvent) { events <- event })
	relay := &assistantIDRelay{done: make(chan struct{})}
	persistence.assistantIDRelay = relay
	sse.assistantIDRelay = relay
	if persistence == nil || sse == nil {
		t.Fatal("handlers were not wired")
	}

	modelInfo := &callbacks.RunInfo{Name: "ChatModel", Component: components.ComponentOfChatModel}
	sse.OnStart(context.Background(), modelInfo, []*schema.Message{{Role: schema.User, Content: "hi"}})
	chunks := []callbacks.CallbackOutput{
		&schema.Message{Role: schema.Assistant, Content: "你"},
		&schema.Message{Role: schema.Assistant, Content: "好"},
		&schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{
				Name: "query", Arguments: `{}`,
			}}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 3}},
		},
	}

	go func() {
		sse.OnEndWithStreamOutput(context.Background(), modelInfo, schema.StreamReaderFromArray(chunks))
	}()
	persistence.OnEndWithStreamOutput(context.Background(), modelInfo, schema.StreamReaderFromArray(chunks))
	select {
	case <-relay.done:
	default:
		t.Fatalf("persistence did not complete; repo messages: %+v", repo.messages)
	}

	var end SSEEvent
	seen := make([]SSEEventType, 0, 4)
	timeout := time.After(2 * time.Second)
	for end.Type != SSEEventAssistantEnd {
		select {
		case event := <-events:
			seen = append(seen, event.Type)
			if event.Type == SSEEventAssistantEnd {
				end = event
			}
		case <-timeout:
			t.Fatalf("timed out waiting for assistant_end; seen=%v", seen)
		}
	}
	if end.Assistant == nil || end.Assistant.Content != "你好" || end.Assistant.MessageID == "" {
		t.Fatalf("assistant_end = %+v", end.Assistant)
	}
	if len(repo.messages) != 1 || repo.messages[0].Content == nil || *repo.messages[0].Content != "你好" {
		t.Fatalf("persisted messages = %+v", repo.messages)
	}
}

// stubBudgetChecker is a minimal llm.BudgetChecker that allows everything.
type stubBudgetChecker struct{}

func (stubBudgetChecker) Check(_ context.Context, _ uint64, _ int) error { return nil }
func (stubBudgetChecker) Record(_ context.Context, _ uint64, _ llm.Usage) error {
	return nil
}
