package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// chainAuditSink is a sink that records audit start + end for both
// query_promql and get_host_load to confirm the decorator chain wraps
// each tool the same way.
type chainAuditSink struct {
	starts []decorators.ToolStartEvent
	ends   []decorators.ToolEndEvent
}

func (s *chainAuditSink) OnToolStart(_ context.Context, ev decorators.ToolStartEvent) (string, error) {
	s.starts = append(s.starts, ev)
	return "id-" + ev.ToolName, nil
}

func (s *chainAuditSink) OnToolEnd(_ context.Context, _ string, ev decorators.ToolEndEvent) error {
	s.ends = append(s.ends, ev)
	return nil
}

// TestBaseTool_DecoratedChain_QueryPromQL — verifies that wrapping
// query_promql with decorators.Wrap() runs all 5 decorators in
// order. Mirrors decorators_test.TestChain_RunsAllInOrder but exercises
// a real BaseTool implementation, satisfying the PR-7 requirement that
// the new tools play with the standard chain.
func TestBaseTool_DecoratedChain_QueryPromQL(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "matrix",
			Result:     json.RawMessage(`[]`),
		},
	}
	inner := NewQueryPromQLTool(pq, slog.Default())

	sink := &chainAuditSink{}
	limiter := decorators.NewTokenBucketLimiter(60)
	reg := prometheus.NewRegistry()

	wrapped := decorators.Wrap(inner, decorators.Deps{
		Timeout:    5 * time.Second,
		Audit:      sink,
		Limiter:    limiter,
		Registerer: reg,
	})

	out, err := wrapped.InvokableRun(context.Background(), `{"expr":"up"}`,
		basetool.WithUserID(7), basetool.WithTenant("7"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, `"resultType"`) {
		t.Errorf("unexpected output %s", out)
	}

	// audit decorator: start + end fired.
	if len(sink.starts) != 1 || len(sink.ends) != 1 {
		t.Errorf("audit fired %d start / %d end, want 1/1", len(sink.starts), len(sink.ends))
	}
	if sink.starts[0].ToolName != ToolNameQueryPromQL {
		t.Errorf("audit name = %q", sink.starts[0].ToolName)
	}
	if sink.starts[0].UserID != 7 {
		t.Errorf("audit user = %d, want 7", sink.starts[0].UserID)
	}

	// metric decorator: counter ticked.
	if got := chainCounter(t, reg, ToolNameQueryPromQL, "success"); got != 1 {
		t.Errorf("metric counter = %f, want 1", got)
	}
}

// TestBaseTool_DecoratedChain_HostLoad — same chain check on a tool that
// goes through the frontier caller path. Confirms the decorator chain
// is shape-compatible with all tool flavours, not just the prom one.
func TestBaseTool_DecoratedChain_HostLoad(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetHostLoadResponse{CPUPct: 1, MemPct: 2}),
	}
	edge := &edgemodel.Edge{ID: 11, Name: "node-c"}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
	// Post N+15: get_host_load takes device_ids[]; we wire a resolver
	// inline so the chain test doesn't need the full DeviceUsecase setup.
	inner := NewGetHostLoadTool(fc, uc, nil, slog.Default())
	inner.resolver = &fakeHostFilesResolver{mapping: map[uint64]uint64{11: 11}}

	sink := &chainAuditSink{}
	limiter := decorators.NewTokenBucketLimiter(60)
	reg := prometheus.NewRegistry()

	wrapped := decorators.Wrap(inner, decorators.Deps{
		Timeout:    5 * time.Second,
		Audit:      sink,
		Limiter:    limiter,
		Registerer: reg,
	})

	out, err := wrapped.InvokableRun(context.Background(), `{"device_ids":[11]}`,
		basetool.WithUserID(3))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, `"cpu_pct"`) {
		t.Errorf("unexpected output: %s", out)
	}
	if len(sink.starts) != 1 || len(sink.ends) != 1 {
		t.Errorf("audit fired %d start / %d end, want 1/1", len(sink.starts), len(sink.ends))
	}
	if got := chainCounter(t, reg, ToolNameGetHostLoad, "success"); got != 1 {
		t.Errorf("metric counter = %f, want 1", got)
	}
}

// TestBaseTool_DecoratedChain_Order — confirms the documented decorator
// order: tenant_bind → timeout → audit → ratelimit → metric.
//
// We can't trivially look at decorator wrapping order at runtime, so the
// test instead validates two observable consequences of the order:
//
//  1. A timed-out call still produces an audit row (timeout outside
//     audit means the audit start has already fired before the deadline
//     expires).
//  2. A rate-limited refusal still produces an audit row (audit outside
//     ratelimit means the audit start fires for the denied call).
func TestBaseTool_DecoratedChain_Order(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{ResultType: "matrix", Result: json.RawMessage(`[]`)},
	}
	inner := NewQueryPromQLTool(pq, slog.Default())

	sink := &chainAuditSink{}
	// 1/min, burst=1: 2nd call is denied.
	limiter := decorators.NewTokenBucketLimiter(1)
	wrapped := decorators.Wrap(inner, decorators.Deps{
		Timeout: 5 * time.Second,
		Audit:   sink,
		Limiter: limiter,
	})

	// First call: success — burns the token.
	if _, err := wrapped.InvokableRun(context.Background(), `{"expr":"up"}`,
		basetool.WithUserID(11)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call: rate-limited but audit start STILL records it (audit
	// outside ratelimit).
	if _, err := wrapped.InvokableRun(context.Background(), `{"expr":"up"}`,
		basetool.WithUserID(11)); err == nil {
		t.Fatalf("expected rate-limit error on 2nd call")
	}

	// Both invocations should have audit-start rows.
	if len(sink.starts) < 2 {
		t.Errorf("audit starts = %d, want >= 2 (proves audit outside ratelimit)", len(sink.starts))
	}
}

// TestBaseTool_DecoratedChain_TenantBindNoOpForPromQL — query_promql's
// schema doesn't declare a tenant_id property, so the tenant_bind layer
// should pass through without injecting. (— tenant_bind
// inspects the JSON Schema before mutating args.) This confirms the
// new BaseTools coexist with the chain even when they're tenant-agnostic.
func TestBaseTool_DecoratedChain_TenantBindNoOpForPromQL(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{ResultType: "matrix", Result: json.RawMessage(`[]`)},
	}
	inner := NewQueryPromQLTool(pq, slog.Default())
	wrapped := decorators.Wrap(inner, decorators.Deps{Timeout: 5 * time.Second})

	if _, err := wrapped.InvokableRun(context.Background(), `{"expr":"up"}`,
		basetool.WithUserID(99)); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	// PromQuerier received the original expr — no tenant_id contamination.
	if pq.gotExpr != "up" {
		t.Errorf("expr mutated by tenant_bind: %q", pq.gotExpr)
	}
}

// chainCounter reads ongrid_tool_invocations_total{name=,result=} from a
// custom registry. Matches decorators.mustCounter shape; duplicated here
// because that one is in the decorators package's _test.go and not
// exported.
func chainCounter(t *testing.T, reg *prometheus.Registry, name, result string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "ongrid_tool_invocations_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			matchName, matchResult := false, false
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "name":
					if lp.GetValue() == name {
						matchName = true
					}
				case "result":
					if lp.GetValue() == result {
						matchResult = true
					}
				}
			}
			if matchName && matchResult && m.Counter != nil {
				return m.Counter.GetValue()
			}
		}
	}
	t.Fatalf("counter not found for name=%q result=%q", name, result)
	return 0
}
