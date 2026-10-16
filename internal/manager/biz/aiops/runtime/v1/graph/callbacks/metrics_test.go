package callbacks

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsHandler_NewNilRegistererReturnsNil(t *testing.T) {
	t.Parallel()
	if NewMetricsHandler(MetricsDeps{}) != nil {
		t.Fatalf("expected nil when registerer is nil")
	}
}

func TestMetricsHandler_ToolSuccessIncrements(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	ctx := WithToolCallID(context.Background(), "tc-1")
	h.OnStart(ctx, toolInfo("query_promql"), &einotool.CallbackInput{})
	h.OnEnd(ctx, toolInfo("query_promql"), &einotool.CallbackOutput{Response: "{}"})

	got := testutil.ToFloat64(h.collectors.toolInvocations.WithLabelValues("query_promql", "success"))
	if got != 1 {
		t.Errorf("invocations counter = %v, want 1", got)
	}
}

func TestMetricsHandler_ToolErrorIncrementsErrorBucket(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	ctx := WithToolCallID(context.Background(), "tc-2")
	h.OnStart(ctx, toolInfo("flaky"), &einotool.CallbackInput{})
	h.OnError(ctx, toolInfo("flaky"), errors.New("boom"))

	got := testutil.ToFloat64(h.collectors.toolInvocations.WithLabelValues("flaky", "error"))
	if got != 1 {
		t.Errorf("invocations error counter = %v, want 1", got)
	}
}

func TestMetricsHandler_ToolTimeoutClassified(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	ctx := WithToolCallID(context.Background(), "tc-3")
	h.OnStart(ctx, toolInfo("slow"), &einotool.CallbackInput{})
	h.OnError(ctx, toolInfo("slow"), context.DeadlineExceeded)

	if got := testutil.ToFloat64(h.collectors.toolInvocations.WithLabelValues("slow", "timeout")); got != 1 {
		t.Errorf("timeout counter = %v, want 1", got)
	}
}

func TestMetricsHandler_ChatTurnCounter(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	for i := 0; i < 3; i++ {
		h.OnEnd(context.Background(), chatModelInfo(), nil)
	}
	if got := testutil.ToFloat64(h.collectors.chatTurns.WithLabelValues("success")); got != 3 {
		t.Errorf("chat_turns success = %v, want 3", got)
	}
	if h.ChatTurns() != 3 {
		t.Errorf("ChatTurns() = %d, want 3", h.ChatTurns())
	}
}

func TestMetricsHandler_GraphTermination(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	graphInfo := &callbacks.RunInfo{Component: components.Component("Graph")}
	h.OnEnd(context.Background(), graphInfo, nil)
	if got := testutil.ToFloat64(h.collectors.graphIterations.WithLabelValues("success")); got != 1 {
		t.Errorf("graph success = %v, want 1", got)
	}
}

func TestMetricsHandler_GraphMaxIterationsClassified(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	graphInfo := &callbacks.RunInfo{Component: components.Component("Graph")}
	h.OnError(context.Background(), graphInfo, errors.New("graph reached max steps limit"))
	if got := testutil.ToFloat64(h.collectors.graphIterations.WithLabelValues("max_iterations")); got != 1 {
		t.Errorf("max_iterations counter = %v, want 1", got)
	}
}

func TestMetricsHandler_RegistererCollectorReuse(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h1 := NewMetricsHandler(MetricsDeps{Registerer: reg})
	h2 := NewMetricsHandler(MetricsDeps{Registerer: reg})
	if h1.collectors != h2.collectors {
		t.Fatalf("expected the same collectors to be reused for the same registerer")
	}
}

func TestMetricsHandler_NeededGating(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	h := NewMetricsHandler(MetricsDeps{Registerer: reg})
	if !h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnEnd) {
		t.Error("Tool OnEnd should be needed")
	}
	if h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnStartWithStreamInput) {
		t.Error("Tool OnStartWithStreamInput should NOT be needed")
	}
}
