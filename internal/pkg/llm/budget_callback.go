// budget_callback.go is the eino-side budget gate /
// (Agent eino skill framework). It exposes a callbacks.Handler
// that an eino graph (later PR) wires in via callbacks.AppendGlobalHandlers
// or compose.WithCallbacks so every ChatModel call passes through the same
// per-day token cap as the legacy Client → BudgetChecker path (client.go).
//
// Design intent:
//   - OnStart estimates prompt tokens (cheap rule-of-thumb: joined content
//     length / 4) and asks BudgetChecker.Check. On rejection we mark the
//     context so the graph's ChatModel wrapper can return ErrBudgetExceeded
//     before invoking the underlying model.
//   - OnEnd reads schema.ResponseMeta.Usage from the model's reply and
//     records it via BudgetChecker.Record.
//   - We do NOT touch the legacy budget gate inside openaiClient.Chat — it
//     keeps running for the existing call sites until PR-N migrates them.
//     If both gates run for the same call, double-counting is avoided
//     because the graph path goes through callbacks-only and the direct
//     path goes through openaiClient.Chat directly.
//
// Why not return an error from OnStart? eino's callback contract gives
// handlers no way to short-circuit a component. We attach the rejection
// to ctx so the graph's ChatModel wrapper can read it back and fail before
// either a normal model call or a tool-budget synthesis call.
package llm

import (
	"context"
	"sync/atomic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// budgetRejectKey is the context key carrying a budget-rejection error
// from OnStart to the graph node.
type budgetRejectKey struct{}

// BudgetRejectionFromContext returns the budget-rejection error attached
// to ctx by BudgetCallbackHandler.OnStart, or nil if none. The graph's
// ChatModel wrapper checks it before invoking the model, including Stream,
// to convert the callback signal into a hard error.
func BudgetRejectionFromContext(ctx context.Context) error {
	v := ctx.Value(budgetRejectKey{})
	if v == nil {
		return nil
	}
	if err, ok := v.(error); ok {
		return err
	}
	return nil
}

// BudgetCallbackHandler is an eino callbacks.Handler that gates ChatModel
// calls against a BudgetChecker. It implements TimingChecker so eino skips
// the timings we do not care about (stream variants, OnError).
//
// Construct with NewBudgetCallbackHandler. Safe for concurrent use; the
// hit/reject/record counters are atomic.
//
// Reference:
type BudgetCallbackHandler struct {
	checker BudgetChecker
	// userID is forwarded as the BudgetChecker scope. PR-N may swap in a
	// per-call resolver once tenancy lands; for now (single-tenant
	// pivot) a fixed bucket is fine.
	userID uint64

	// metrics-style counters exposed for tests.
	checks   atomic.Uint64
	rejects  atomic.Uint64
	records  atomic.Uint64
	tokensIn atomic.Uint64 // sum of TotalTokens reported by OnEnd
}

// NewBudgetCallbackHandler builds a handler. checker may be nil — in that
// case the handler is a no-op (all timings short-circuit). userID is the
// budget bucket; pass 0 for the global bucket (matches InMemoryBudget).
func NewBudgetCallbackHandler(checker BudgetChecker, userID uint64) *BudgetCallbackHandler {
	return &BudgetCallbackHandler{checker: checker, userID: userID}
}

// Needed lets eino skip stream/error timings we do not implement, plus
// the whole handler when no BudgetChecker is wired.
func (h *BudgetCallbackHandler) Needed(_ context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	if h == nil || h.checker == nil {
		return false
	}
	if info != nil && info.Component != "" && info.Component != components.ComponentOfChatModel {
		return false
	}
	switch timing {
	case callbacks.TimingOnStart, callbacks.TimingOnEnd:
		return true
	default:
		return false
	}
}

// OnStart estimates prompt tokens and calls BudgetChecker.Check. On
// rejection it stashes the error on ctx (see BudgetRejectionFromContext)
// instead of returning it — eino's Handler contract has no early-exit.
func (h *BudgetCallbackHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if h == nil || h.checker == nil {
		return ctx
	}
	if info != nil && info.Component != "" && info.Component != components.ComponentOfChatModel {
		return ctx
	}
	mi := einomodel.ConvCallbackInput(input)
	if mi == nil {
		return ctx
	}
	h.checks.Add(1)
	est := estimateEinoPromptTokens(mi.Messages)
	if err := h.checker.Check(ctx, h.userID, est); err != nil {
		h.rejects.Add(1)
		return context.WithValue(ctx, budgetRejectKey{}, err)
	}
	return ctx
}

// OnEnd parses ResponseMeta.Usage from the model output and records it.
// Recording failures are swallowed so they never fail a user-visible
// request — same policy as openaiClient.Chat (client.go:341).
func (h *BudgetCallbackHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	if h == nil || h.checker == nil {
		return ctx
	}
	if info != nil && info.Component != "" && info.Component != components.ComponentOfChatModel {
		return ctx
	}
	mo := einomodel.ConvCallbackOutput(output)
	if mo == nil {
		return ctx
	}
	usage := extractUsage(mo)
	if usage == nil {
		return ctx
	}
	h.records.Add(1)
	h.tokensIn.Add(uint64(usage.TotalTokens))
	_ = h.checker.Record(ctx, h.userID, *usage)
	return ctx
}

// OnError, OnStartWithStreamInput, OnEndWithStreamOutput are no-ops in
// PR-1. Stream accounting lands in a later PR.
func (h *BudgetCallbackHandler) OnError(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
	return ctx
}

func (h *BudgetCallbackHandler) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, in *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	if in != nil {
		// Drain & close so we don't leak goroutines — eino docs require it.
		in.Close()
	}
	return ctx
}

func (h *BudgetCallbackHandler) OnEndWithStreamOutput(ctx context.Context, _ *callbacks.RunInfo, out *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	if out != nil {
		out.Close()
	}
	return ctx
}

// Stats returns a snapshot of the handler counters. Exposed for tests
// and admin debug pages; do not rely on the field shape long-term.
type BudgetCallbackStats struct {
	Checks   uint64
	Rejects  uint64
	Records  uint64
	TokensIn uint64
}

// Stats returns the current counters.
func (h *BudgetCallbackHandler) Stats() BudgetCallbackStats {
	return BudgetCallbackStats{
		Checks:   h.checks.Load(),
		Rejects:  h.rejects.Load(),
		Records:  h.records.Load(),
		TokensIn: h.tokensIn.Load(),
	}
}

// Compile-time interface assertions.
var (
	_ callbacks.Handler       = (*BudgetCallbackHandler)(nil)
	_ callbacks.TimingChecker = (*BudgetCallbackHandler)(nil)
)

// estimateEinoPromptTokens mirrors estimatePromptTokens (client.go:465)
// but reads the eino-typed message slice directly. Kept private so it
// stays a private rule-of-thumb; real billing is the Usage that comes
// back on OnEnd.
func estimateEinoPromptTokens(msgs []*schema.Message) int {
	const perMsgOverhead = 4
	total := 0
	for _, m := range msgs {
		if m == nil {
			continue
		}
		total += perMsgOverhead
		total += len(m.Content) / 4
		total += len(m.ReasoningContent) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Name) / 4
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

// extractUsage pulls a Usage value out of an eino model.CallbackOutput.
// Returns nil if no usage is reported (some providers omit it).
func extractUsage(mo *einomodel.CallbackOutput) *Usage {
	if mo == nil {
		return nil
	}
	// Prefer the typed TokenUsage on the callback output.
	if mo.TokenUsage != nil {
		return &Usage{
			PromptTokens:     mo.TokenUsage.PromptTokens,
			CompletionTokens: mo.TokenUsage.CompletionTokens,
			TotalTokens:      mo.TokenUsage.TotalTokens,
		}
	}
	// Fallback: the message itself may carry ResponseMeta.Usage (some
	// integrations populate one but not the other).
	if mo.Message != nil && mo.Message.ResponseMeta != nil && mo.Message.ResponseMeta.Usage != nil {
		u := mo.Message.ResponseMeta.Usage
		return &Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
		}
	}
	return nil
}
