package callbacks

import (
	"context"
	"log/slog"
	"testing"

	"github.com/cloudwego/eino/callbacks"
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

// stubBudgetChecker is a minimal llm.BudgetChecker that allows everything.
type stubBudgetChecker struct{}

func (stubBudgetChecker) Check(_ context.Context, _ uint64, _ int) error { return nil }
func (stubBudgetChecker) Record(_ context.Context, _ uint64, _ llm.Usage) error {
	return nil
}
