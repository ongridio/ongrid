package callbacks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// assistantIDRelay is the shared slot the PersistenceHandler writes
// the freshly-persisted chat_messages.id into; SSEHandler reads it
// inside OnEnd to attach to the assistant_end SSE frame. Both handlers
// run on the same goroutine for a given graph iteration (persistence
// is registered before SSE in NewDefaultHandlers and eino preserves
// registration order on OnEnd), so a plain string with atomic Load/
// Store is sufficient — no race between writer and reader within an
// iteration.
//
// Frontend ChatThread used to dedupe by a synthetic assistant-iter-N
// id (v0.7.63 hotfix) because this field was empty. Now it can use
// the real DB id.
type assistantIDRelay struct {
	id       atomic.Pointer[string]
	done     chan struct{}
	doneOnce sync.Once
}

func (r *assistantIDRelay) store(id string) {
	if r == nil {
		return
	}
	r.id.Store(&id)
}

func (r *assistantIDRelay) load() string {
	if r == nil {
		return ""
	}
	if p := r.id.Load(); p != nil {
		return *p
	}
	return ""
}

func (r *assistantIDRelay) complete() {
	if r == nil {
		return
	}
	r.doneOnce.Do(func() {
		if r.done != nil {
			close(r.done)
		}
	})
}

func (r *assistantIDRelay) wait() {
	if r == nil || r.done == nil {
		return
	}
	<-r.done
}

func concatModelCallbackStream(out *schema.StreamReader[callbacks.CallbackOutput]) (*schema.Message, error) {
	if out == nil {
		return nil, nil
	}
	messages := schema.StreamReaderWithConvert(out, func(item callbacks.CallbackOutput) (*schema.Message, error) {
		mo := einomodel.ConvCallbackOutput(item)
		if mo == nil || mo.Message == nil {
			return nil, schema.ErrNoValue
		}
		return mo.Message, nil
	})
	return schema.ConcatMessageStream(messages)
}

// Deps bundles every dependency the default callback chain needs.
// Each handler treats nil dependencies as "skip me" — pass partial
// Deps to wire only some handlers (e.g. tests that only want metrics).
//
// Reference: Callback 链 + 主参考图 callback 链.
type Deps struct {
	// AlertDraftGuard blocks model-only alert-rule drafts before they are
	// persisted or streamed. It must run before Persistence/SSE.
	AlertDraftGuard AlertDraftGuardDeps

	// Persistence wiring (chat_messages / chat_tool_calls writes).
	Persistence PersistenceDeps

	// SSE emitter (when nil, no SSE handler is wired). Cutover layer
	// builds an emitter that pipes into the existing http.go writeSSE.
	SSE SSEEmitter

	// Audit (slog INFO records). Logger required.
	Audit AuditDeps

	// Metrics (Prom counters). Registerer required.
	Metrics MetricsDeps

	// Budget gate (PR-1). Both fields required to wire it.
	BudgetChecker llm.BudgetChecker
	BudgetUserID  uint64
}

// NewDefaultHandlers builds the default ordered callback chain for an
// agent graph run. Order is incidental — eino does not document a
// guaranteed handler ordering — but the slice is stable across calls
// so tests can assert positional indices.
//
// Handlers with nil dependencies are skipped, so passing a partial
// Deps yields a sparser chain. The PR-1 BudgetCallbackHandler is
// included whenever Deps.BudgetChecker is non-nil.
//
// Cutover layer (NEXT PR) calls this once per request, threads the
// returned slice into compose.WithCallbacks at Invoke time, and
// discards the handlers when the request finishes (so per-call state
// is bounded by the request lifetime).
func NewDefaultHandlers(deps Deps) []callbacks.Handler {
	out := make([]callbacks.Handler, 0, 6)

	if h := NewAlertDraftGuardHandler(deps.AlertDraftGuard); h != nil {
		out = append(out, h)
	}

	// Shared relay lets the persistence stream copy announce completion to
	// SSE's independent stream copy. Stream handlers drain concurrently, so
	// registration order alone is not a safe message-id handoff.
	var relay *assistantIDRelay
	if h := NewPersistenceHandler(deps.Persistence); h != nil {
		relay = &assistantIDRelay{done: make(chan struct{})}
		h.assistantIDRelay = relay
		out = append(out, h)
	}
	if h := NewSSEHandler(deps.SSE); h != nil {
		h.assistantIDRelay = relay
		out = append(out, h)
	}
	if h := NewAuditHandler(deps.Audit); h != nil {
		out = append(out, h)
	}
	if h := NewMetricsHandler(deps.Metrics); h != nil {
		out = append(out, h)
	}
	if deps.BudgetChecker != nil {
		out = append(out, llm.NewBudgetCallbackHandler(deps.BudgetChecker, deps.BudgetUserID))
	}
	return out
}

// SSEHandlerFromHandlers returns the request-scoped SSE handler, if wired.
func SSEHandlerFromHandlers(handlers []callbacks.Handler) *SSEHandler {
	for _, handler := range handlers {
		if sse, ok := handler.(*SSEHandler); ok {
			return sse
		}
	}
	return nil
}

// EnableSynchronousToolPersistence returns the request-scoped persistence
// handler after switching its tool writes to graph's adapter boundary. This
// is required because nested ToolsNode callbacks are not guaranteed to reach
// the outer callback chain in the supported Eino runtime.
func EnableSynchronousToolPersistence(handlers []callbacks.Handler) *PersistenceHandler {
	for _, handler := range handlers {
		if p, ok := handler.(*PersistenceHandler); ok {
			p.directToolPersistence = true
			return p
		}
	}
	return nil
}

// FinalizeBatches runs end-of-request bookkeeping on every handler in
// the chain that wants one. Currently this is just PersistenceHandler:
// see flushIncompleteBatch — the ChatModel.OnStart hook autoheals the
// previous batch DURING a session, but a session that ends mid-batch
// (user closes browser, request cancels) never gets the next OnStart.
// chatruntime defers FinalizeBatches after compose.Invoke returns so
// the batch always gets a final flush.
func FinalizeBatches(ctx context.Context, handlers []callbacks.Handler) {
	for _, h := range handlers {
		if p, ok := h.(*PersistenceHandler); ok {
			p.FinalizeBatch(ctx)
		}
	}
}

// ErrNoHandlers is returned by helpers that require at least one
// handler in the chain. Reserved for future use; PR-6 callers tolerate
// an empty slice.
var ErrNoHandlers = errors.New("graph/callbacks: no handlers configured")

// registerOrExisting is the package-private register helper. Mirrors
// llm/metrics.go's registerOrExisting + tools/decorators/metric.go's
// regOrExist; lives here so persistence.go can use it without
// triggering an import cycle (both packages are siblings of llm but
// llm/metrics.go's helper is unexported).
func registerOrExisting(reg prometheus.Registerer, c prometheus.Collector) prometheus.Collector {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
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
