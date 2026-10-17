package chatruntime

import (
	"context"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"go.opentelemetry.io/otel/trace"
	"testing"
)

func TestDetachedWorkerContextPreservesAuthorizationWithoutRequestState(t *testing.T) {
	type requestKey struct{}
	for _, allowed := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		ctx = context.WithValue(ctx, requestKey{}, "callback state")
		ctx = contract.WithVersion(ctx, contract.V2)
		ctx = contract.WithEmit(ctx, func(contract.Event) {})
		span := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
		ctx = trace.ContextWithSpanContext(ctx, span)
		ctx = basetool.WithWebSearchAllowed(ctx, allowed)
		ctx = basetool.WithAgentWriteAllowed(ctx, allowed)
		ctx = tenantctx.WithSlot(ctx)
		identity := tenantctx.Tenant{UserID: 42, Role: "admin", IsSuperuser: true}
		tenantctx.SetOnSlot(ctx, identity)
		worker := detachedWorkerContext(ctx)
		cancel()
		tenantctx.SetOnSlot(ctx, tenantctx.Tenant{UserID: 99})
		if worker.Err() != nil || worker.Done() != nil {
			t.Fatal("worker inherited request cancellation")
		}
		if contract.VersionFromContext(worker) != contract.V2 || contract.EmitFromContext(worker) != nil {
			t.Fatal("worker version lost or request emitter retained")
		}
		if worker.Value(requestKey{}) != nil {
			t.Fatal("worker retained request-only state")
		}
		if basetool.WebSearchAllowed(worker) != allowed || basetool.AgentWriteAllowedFromContext(worker) != allowed {
			t.Fatal("worker capability gates changed")
		}
		if got, ok := tenantctx.From(worker); !ok || got != identity {
			t.Fatalf("identity snapshot=%+v, present=%t", got, ok)
		}
		if got := trace.SpanContextFromContext(worker); !got.Equal(span) {
			t.Fatalf("trace ancestry=%v, want %v", got, span)
		}
	}
}
