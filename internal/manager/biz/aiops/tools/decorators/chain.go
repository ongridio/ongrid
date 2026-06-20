package decorators

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// Deps bundles the cross-cutting dependencies a decorator chain needs.
// Zero values fall back to safe defaults (DefaultTimeout, no audit,
// NoopLimiter, prometheus.DefaultRegisterer) so a minimal Wrap call
// still produces a well-behaved tool.
//
// ASCII diagram — these are the inputs to the standard
// decorator stack.
type Deps struct {
	// Timeout is the per-call ceiling. Zero -> DefaultTimeout (15s).
	Timeout time.Duration

	// Audit is the seam to chat_tool_calls. Nil -> audit disabled.
	Audit AuditSink

	// Limiter throttles per-(tool, user). Nil -> NoopLimiter.
	Limiter Limiter

	// Registerer is where the metric collectors register. Nil ->
	// prometheus.DefaultRegisterer.
	Registerer prometheus.Registerer

	// ReviewSpawner is the chatruntime seam ReviewGate uses to spawn
	// the reviewer worker for mutating tools that are not covered by a
	// deterministic approval policy. Nil → ReviewGate is NOT installed;
	// mutating tools (Class="write"|"destructive") will run unguarded.
	// cmd/main.go MUST wire this when the deployment includes the
	// reviewer agent persona — otherwise SOP gating is silently disabled.
	ReviewSpawner ReviewSpawner

	// ReviewSink writes chat_mutating_proposals rows. Nil-safe.
	ReviewSink MutatingProposalSink

	// ReviewerAgent overrides DefaultReviewerAgent. Empty falls back.
	ReviewerAgent string

	// ReviewerTimeout overrides DefaultReviewerTimeout. Zero falls back.
	ReviewerTimeout time.Duration
}

// Wrap returns inner wrapped by the standard decorator stack:
//
//	tenant_bind → review_gate → timeout → audit → ratelimit → metric
//
// Reading top-down (ASCII flow + SOP gating):
// a request hits tenant_bind first (it's the outermost wrapper),
// which resolves the tenant from ctx, then descends through
// review_gate (gates mutating tools by deterministic policy or by
// spawning a reviewer worker — PR-7), timeout (bound the inner call),
// audit (write start row), ratelimit (refuse after audit), and
// finally metric — which observes the actual inner tool execution
// time without timeout/audit/ratelimit overhead in the histogram.
//
// Why this order:
//
//   - tenant_bind OUTERMOST so its mutated args + opts flow into all
//     downstream layers (audit logs the rewritten args, ratelimit
//     keys on the resolved user id, review_gate sees the operator
//     user_id on the proposal).
//   - review_gate OUTSIDE timeout because the reviewer worker is
//     itself a graph.Invoke against an LLM with its own multi-turn
//     budget; constraining it to the inner tool's 15s timeout is
//     unrealistic. ReviewGate carries its own independent Timeout
//     (default 60s) for the reviewer round-trip; once approve fires,
//     the inner tool runs under the timeout decorator's 15s budget
//     normally.
//   - review_gate OUTSIDE audit because a rejected proposal has no
//     execution to audit — chat_tool_calls is the **execution** log,
//     chat_mutating_proposals is the **decision** log. Putting
//     review_gate inside audit would emit a synthetic execution row
//     for every reject, polluting the execution table.
//   - review_gate is a no-op for read-class tools (Info().Class !=
//     "write"|"destructive"), so applying it unconditionally costs
//     one Info() call per non-mutating invoke; we accept that for
//     the documentability win — a single chain order is easier to
//     reason about than per-tool conditional wrapping.
//   - timeout outside audit so an audit-pending row is still written
//     when the call times out (otherwise we lose the timeout
//     classification in chat_tool_calls).
//   - audit outside ratelimit so a rate-limited refusal is still
//     audited as a failed attempt.
//   - ratelimit outside metric so denied calls don't pollute the
//     duration histogram (which would double-count the rejection
//     overhead at the limiter as a near-zero "tool call").
//   - metric innermost so its histogram tracks the inner tool's
//     latency, not the decorator overhead.
//
// Any of these can be flipped if the SLO story changes — the order is
// documented here so it's reviewable as a single decision rather than
// scattered across construction sites.
func Wrap(inner basetool.BaseTool, deps Deps) basetool.BaseTool {
	if inner == nil {
		return nil
	}
	limiter := deps.Limiter
	if limiter == nil {
		limiter = NoopLimiter{}
	}
	tool := inner
	tool = WithMetric(tool, deps.Registerer)
	tool = WithRateLimit(tool, limiter)
	tool = WithAudit(tool, deps.Audit) // nil-safe: pass-through
	tool = WithTimeout(tool, deps.Timeout)
	if deps.ReviewSpawner != nil {
		tool = WithReviewGate(tool, deps.ReviewSpawner, ReviewGateConfig{
			ReviewerAgent: deps.ReviewerAgent,
			Sink:          deps.ReviewSink,
			Timeout:       deps.ReviewerTimeout,
		})
	}
	tool = WithTenantBind(tool)
	return tool
}
