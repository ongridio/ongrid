package callbacks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"
)

// MetricsDeps configures the metrics handler. Registerer is required;
// callers pass prometheus.DefaultRegisterer in production and a fresh
// prometheus.NewRegistry in tests so collectors do not collide.
type MetricsDeps struct {
	Registerer prometheus.Registerer
}

// metricsCollectors bundles the Prom collectors registered by the
// metrics handler. Allocated once per Registerer and shared across
// handler instances so multiple graph runs don't trip
// AlreadyRegisteredError.
//
// Cardinality red line: labels are limited to {name, result}
// (tools) / {provider, result} (chat models) / {result} (graph
// terminations). Never include user / tenant / session.
type metricsCollectors struct {
	toolInvocations *prometheus.CounterVec   // name, result
	toolDuration    *prometheus.HistogramVec // name
	graphIterations *prometheus.CounterVec   // result
	chatTurns       *prometheus.CounterVec   // result
}

var (
	metricsRegMu  sync.Mutex
	metricsRegMap = map[prometheus.Registerer]*metricsCollectors{}
)

func getMetricsCollectors(reg prometheus.Registerer) *metricsCollectors {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	metricsRegMu.Lock()
	defer metricsRegMu.Unlock()
	if mc, ok := metricsRegMap[reg]; ok {
		return mc
	}
	// Help string MUST match tools/decorators/metric.go byte-for-byte —
	// Prom panics on AlreadyRegistered if descriptors differ on Help.
	// Both layers observe the same counter from different vantage points
	// (dual-observation contract), so they MUST share desc.
	tinv := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ongrid_tool_invocations_total",
			Help: "Total ongrid tool invocations, split by name and result.",
		},
		[]string{"name", "result"},
	)
	tdur := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ongrid_tool_duration_seconds",
			Help:    "ongrid tool invocation duration in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
		},
		[]string{"name"},
	)
	gi := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ongrid_graph_iterations_total",
			Help: "Total ongrid agent graph terminations, split by result (success | max_iterations | error).",
		},
		[]string{"result"},
	)
	ct := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ongrid_graph_chat_turns_total",
			Help: "Total ChatModel turns observed during ongrid agent graph runs.",
		},
		[]string{"result"},
	)
	mc := &metricsCollectors{
		toolInvocations: regOrExist(reg, tinv).(*prometheus.CounterVec),
		toolDuration:    regOrExist(reg, tdur).(*prometheus.HistogramVec),
		graphIterations: regOrExist(reg, gi).(*prometheus.CounterVec),
		chatTurns:       regOrExist(reg, ct).(*prometheus.CounterVec),
	}
	metricsRegMap[reg] = mc
	return mc
}

// regOrExist mirrors the helper in tools/decorators/metric.go and
// llm/metrics.go: register; on AlreadyRegisteredError reuse the
// existing collector.
func regOrExist(reg prometheus.Registerer, c prometheus.Collector) prometheus.Collector {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		return are.ExistingCollector
	}
	panic(err)
}

// MetricsHandler observes tool + chat-model invocations from the
// graph's perspective. Note: the PR-3 decorator chain already records
// ongrid_tool_invocations_total at the per-call seam — this handler
// re-uses the SAME collector via the registry-keyed cache so we never
// double-register, and the counter increments are best-effort
// duplicates (acceptable : "callback 视角 + 装饰器视
// 角 双观察 OK"). Tests assert exactly one increment per call when
// the decorator chain is bypassed (the unit test calls the handler
// directly).
type MetricsHandler struct {
	collectors *metricsCollectors

	chatTurns    atomic.Int64
	toolStartsMu sync.Mutex
	toolStarts   map[string]time.Time

	terminated atomic.Bool
}

// NewMetricsHandler builds a handler against the given registerer.
// Returns nil if Registerer is nil.
func NewMetricsHandler(deps MetricsDeps) *MetricsHandler {
	if deps.Registerer == nil {
		return nil
	}
	return &MetricsHandler{
		collectors: getMetricsCollectors(deps.Registerer),
		toolStarts: make(map[string]time.Time),
	}
}

// Needed gates timings.
func (h *MetricsHandler) Needed(_ context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	if h == nil || info == nil {
		return false
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		switch timing {
		case callbacks.TimingOnEnd, callbacks.TimingOnError, callbacks.TimingOnEndWithStreamOutput:
			return true
		}
	case components.ComponentOfTool:
		switch timing {
		case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError:
			return true
		}
	default:
		// Graph terminations.
		switch timing {
		case callbacks.TimingOnEnd, callbacks.TimingOnError:
			return true
		}
	}
	return false
}

// OnStart records tool start times.
func (h *MetricsHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	if info.Component == components.ComponentOfTool {
		h.toolStartsMu.Lock()
		h.toolStarts[toolCallIDFromCtx(ctx, info)] = time.Now()
		h.toolStartsMu.Unlock()
	}
	return ctx
}

// OnEnd increments counters for tool / chat / graph completion.
func (h *MetricsHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		h.chatTurns.Add(1)
		h.collectors.chatTurns.WithLabelValues("success").Inc()
		_ = output // ChatModel output not needed for counters
	case components.ComponentOfTool:
		key := toolCallIDFromCtx(ctx, info)
		h.toolStartsMu.Lock()
		started, ok := h.toolStarts[key]
		delete(h.toolStarts, key)
		h.toolStartsMu.Unlock()
		if ok {
			h.collectors.toolDuration.WithLabelValues(info.Name).Observe(time.Since(started).Seconds())
		}
		// We can't tell from CallbackOutput alone whether the tool
		// returned an error payload (it's just a JSON string). Treat
		// this hook as "success" — the OnError path covers actual
		// failures returned via tool.InvokableRun.
		h.collectors.toolInvocations.WithLabelValues(info.Name, "success").Inc()
	default:
		// Graph-scope terminal: success.
		if h.terminated.CompareAndSwap(false, true) {
			h.collectors.graphIterations.WithLabelValues("success").Inc()
		}
	}
	return ctx
}

// OnError increments error counters.
func (h *MetricsHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	if h == nil || info == nil || err == nil {
		return ctx
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		h.collectors.chatTurns.WithLabelValues("error").Inc()
	case components.ComponentOfTool:
		key := toolCallIDFromCtx(ctx, info)
		h.toolStartsMu.Lock()
		started, ok := h.toolStarts[key]
		delete(h.toolStarts, key)
		h.toolStartsMu.Unlock()
		result := "error"
		if isDeadlineErr(err) {
			result = "timeout"
		}
		if ok {
			h.collectors.toolDuration.WithLabelValues(info.Name).Observe(time.Since(started).Seconds())
		}
		h.collectors.toolInvocations.WithLabelValues(info.Name, result).Inc()
	default:
		// Graph-scope error: classify max-iterations vs other.
		result := "error"
		if isMaxIterations(err) {
			result = "max_iterations"
		}
		if h.terminated.CompareAndSwap(false, true) {
			h.collectors.graphIterations.WithLabelValues(result).Inc()
		}
	}
	return ctx
}

// OnStartWithStreamInput is a no-op. OnEndWithStreamOutput drains the
// handler's independent stream copy and applies the same ChatModel counters
// as the non-stream path.
func (h *MetricsHandler) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, in *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	if in != nil {
		in.Close()
	}
	return ctx
}

func (h *MetricsHandler) OnEndWithStreamOutput(ctx context.Context, _ *callbacks.RunInfo, out *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	if out != nil {
		if _, err := concatModelCallbackStream(out); err == nil {
			h.chatTurns.Add(1)
			h.collectors.chatTurns.WithLabelValues("success").Inc()
		}
	}
	return ctx
}

// ChatTurns reports the number of ChatModel turns observed. Exposed
// for tests.
func (h *MetricsHandler) ChatTurns() int {
	if h == nil {
		return 0
	}
	return int(h.chatTurns.Load())
}

// isDeadlineErr returns true when err signals a deadline exceeded.
// Exported only by virtue of being shared across handler files; lives
// in this package because it mirrors the legacy classifyToolOutcome
// used by agent.go.
func isDeadlineErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// isMaxIterations reports whether err looks like the eino runtime's
// "graph reached max steps" error. eino does not export a typed error
// for this, so we string-match the standard message it produces. The
// match is loose on purpose — the metric's `max_iterations` bucket is
// a hint, not authoritative.
func isMaxIterations(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "max steps") || strings.Contains(msg, "MaxRunSteps") || strings.Contains(msg, "max iterations")
}

// Compile-time checks.
var (
	_ callbacks.Handler       = (*MetricsHandler)(nil)
	_ callbacks.TimingChecker = (*MetricsHandler)(nil)
)
