package decorators

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// ReviewGate is the SOP-double-sign decorator (+ mutating-class gate).
// When the wrapped BaseTool's Class is "write" or "destructive", the
// decorator intercepts the call. A narrow set of config writes can be
// approved by deterministic policy; everything else spawns a reviewer
// worker (agents/reviewer.md) and only forwards to the inner tool if
// the reviewer returns "Decision: approve". On reject the decorator
// returns an error wrapping the reviewer's reason so the coordinator
// chat model can explain the situation to the user.
//
// Why ReviewGate is its own decorator (instead of folding it into the
// agent loop or registry):
//
//   - Decorators compose: ReviewGate sees ALL mutating tool calls
//     regardless of which kernel (legacy / graph) drives them, AS
//     LONG AS the wiring code wraps the tool with chain.Wrap. That
//     gives one chokepoint to enforce
//   - Class-driven dispatch is invisible at the agent loop layer —
//     the loop calls InvokableRun and either gets a real result (if
//     class=read or reviewer approved) or an error (if reviewer
//     rejected). No special-casing of mutating tools at the loop
//     level.
//   - Tests can unit-cover the decorator without standing up a chat
//     runtime: we inject a fake Spawner that returns canned reviewer
//     output.
//
// Position in the chain (chain.go's Wrap):
//
//	tenant_bind → REVIEW_GATE → timeout → audit → ratelimit → metric
//
// Why outside timeout: the reviewer worker is itself a graph.Invoke
// against an LLM with its own internal turn budget; wrapping ReviewGate
// inside timeout would force the reviewer to finish within the inner
// tool's 15s budget, which is unrealistic. ReviewGate carries its own
// independent Timeout (default 60s) for the reviewer round-trip; once
// approve fires, the inner tool runs under its own timeout decorator.
//
// Why outside audit: the audit row records the **execution** — for a
// rejected proposal there is no execution to audit (we have a row in
// chat_mutating_proposals instead). Putting ReviewGate inside audit
// would write a synthetic execution row for every reject; we don't
// want that pollution.
//
// Why outside tenant_bind: the proposal payload includes the operator
// user id which tenant_bind resolves from ctx; ReviewGate reads from
// the same opts so it must run AFTER tenant_bind populates them.
type ReviewGate struct {
	inner basetool.BaseTool

	// reviewerAgent is the persona name the gate spawns. Almost always
	// "reviewer" today; configurable so future per-tool reviewers
	// (db-specific, k8s-specific) can be wired without changing this
	// decorator.
	reviewerAgent string

	// spawner is the seam to the chatruntime.Runtime. Concrete
	// production binding lives at the cmd/main.go boundary.
	spawner ReviewSpawner

	// sink writes audit rows. Nil-safe: when nil the decorator skips
	// persistence and the coordinator gets the same in-memory
	// approve/reject behaviour. Production wiring sets this.
	sink MutatingProposalSink

	// timeout caps the reviewer round-trip. Independent from the
	// inner-tool timeout (which the timeout decorator imposes).
	timeout time.Duration

	// nowFn is injected for deterministic tests; defaults to time.Now.
	nowFn func() time.Time
}

// DefaultReviewerAgent is the persona name spawned when no override is
// supplied. Mirrors agents/reviewer.md `name: reviewer`.
const DefaultReviewerAgent = "reviewer"

// DefaultReviewerTimeout is the per-call ceiling for the reviewer
// round-trip. 60s is generous: the reviewer has up to 5 turns of LLM
// + tool calls to make a decision. Longer than the inner-tool
// timeout (15s) on purpose — see package doc.
const DefaultReviewerTimeout = 60 * time.Second

// ErrReviewRejected wraps the reviewer's rejection. The agent loop /
// LLM sees "review rejected: <reviewer reason>" and the coordinator
// chat model is expected to explain the situation to the user.
// errors.Is checks let upstream code distinguish rejections from
// generic tool failures.
var ErrReviewRejected = errors.New("review rejected")

// ErrReviewUndecided fires when the reviewer's output cannot be
// parsed for a "Decision: approve|reject" line. Treated as a reject
// (default-safe posture per agents/reviewer.md "reject is the safe
// default"); the wrapper errors so the coordinator can surface a
// clean message instead of approving an ambiguous response.
var ErrReviewUndecided = errors.New("reviewer returned no decision")

// ErrReviewerSpawn fires when the spawner itself errors (agent not
// found, runtime down). Treated as a reject for safety — when the
// review system is broken, no mutation runs.
var ErrReviewerSpawn = errors.New("reviewer spawn failed")

// ReviewSpawnRequest is the seam-side spawn shape passed to
// ReviewSpawner.SpawnReviewer. We deliberately do NOT import
// chatruntime types — the decorator package must stay free of
// chat-runtime coupling (module boundaries).
type ReviewSpawnRequest struct {
	// AgentName is the reviewer persona to spawn ("reviewer" by
	// default; configurable for future per-tool reviewers).
	AgentName string

	// Prompt is the markdown brief assembled by ReviewGate from the
	// proposal envelope. The reviewer worker treats it as its first
	// user message.
	Prompt string
}

// ReviewSpawnResult is the seam-side projection of the reviewer's
// final assistant message. ReviewGate parses Result line-by-line for
// "Decision: approve" / "Decision: reject"; TaskID is recorded on
// the audit row for cross-reference.
type ReviewSpawnResult struct {
	// TaskID is the chatruntime.Worker.ID — opaque to the decorator
	// but persisted on the audit row.
	TaskID string

	// Result is the reviewer's final assistant content. ReviewGate
	// scans this for the decision marker.
	Result string

	// Err is non-empty when the worker reached a terminal failure
	// state (graph error, out-of-budget). Treated as a reject for
	// safety.
	Err string
}

// ReviewSpawner is the narrow seam ReviewGate needs from the chat
// runtime. Production binding wraps *chatruntime.Runtime via a thin
// shim at the cmd/main.go boundary; tests inject a fake that returns
// canned results.
//
// Synchronous semantics: SpawnReviewer blocks until the reviewer
// reaches a terminal state. We pass background=false to the runtime
// because the gate is itself a synchronous call from the agent loop's
// perspective — the LLM is waiting for the tool result.
type ReviewSpawner interface {
	SpawnReviewer(ctx context.Context, req ReviewSpawnRequest) (*ReviewSpawnResult, error)
}

// MutatingProposalSink is the audit seam. ReviewGate inserts a row
// before spawning the reviewer (so an interrupted round-trip leaves
// a "pending" record), updates it with the decision, and stamps
// executed_at after the inner tool returns. Production binding writes
// to chat_mutating_proposals; tests use an in-memory fake.
//
// Why a sink interface (vs importing the repo): same reason as the
// AuditSink seam in audit.go — decorators stay free of biz repo
// coupling.
type MutatingProposalSink interface {
	// Insert records the intercepted proposal in DecisionPending state.
	// Returns the row id so subsequent UpdateDecision / MarkExecuted
	// calls can target it. Implementations MAY use the supplied id
	// (when non-empty) or assign their own.
	Insert(ctx context.Context, ev MutatingProposalEvent) (id string, err error)

	// UpdateDecision flips the row to approve / reject and records
	// the reviewer's reason. Errors are logged but MUST NOT propagate
	// to the caller — audit failures don't fail the tool decision.
	UpdateDecision(ctx context.Context, id, decision string, reason string) error

	// MarkExecuted stamps the row when the inner tool's InvokableRun
	// returns. Best-effort.
	MarkExecuted(ctx context.Context, id string, t time.Time) error
}

// MutatingProposalEvent is the data passed to the sink at intercept
// time. Mirrors model.MutatingProposal field names.
type MutatingProposalEvent struct {
	ToolName       string
	ArgsJSON       string
	ToolClass      string
	ReviewerAgent  string
	ReviewerTaskID string
	OperatorUserID uint64
	SessionID      string
	CreatedAt      time.Time
}

// ReviewGateConfig parameterises the decorator. Zero values fall back
// to safe defaults so a minimal WithReviewGate(spawner) call still
// produces a well-behaved decorator.
type ReviewGateConfig struct {
	// ReviewerAgent overrides DefaultReviewerAgent.
	ReviewerAgent string

	// Sink is the audit binding (chat_mutating_proposals row writer).
	// Nil = audit disabled.
	Sink MutatingProposalSink

	// Timeout overrides DefaultReviewerTimeout.
	Timeout time.Duration
}

const deterministicPolicyReviewerAgent = "deterministic_policy"

const alertRuleCreateDeterministicApprovalReason = "deterministic policy approved alert_rule/create: apply_config_change validates confirmed=true, admin role, payload, draft_hash, draft_id, compiler constraints, and rule uniqueness before persisting"

// WithReviewGate returns inner wrapped so InvokableRun first checks
// Class:
//
//   - Class != "write" && Class != "destructive" → pass-through.
//     The decorator is a no-op overhead-wise (one Info call); we
//     could conditionally apply at chain.Wrap time but applying
//     unconditionally keeps the chain order documentable in one
//     place.
//   - Class == "write" || "destructive" → deterministic approval when
//     narrowly allowed; otherwise spawn reviewer worker, parse
//     decision, gate the inner call.
//
// A nil spawner falls back to the "always reject" safe posture: the
// decorator returns ErrReviewerSpawn so a misconfigured deployment
// can't accidentally fire mutating tools without review.
func WithReviewGate(inner basetool.BaseTool, spawner ReviewSpawner, cfg ReviewGateConfig) basetool.BaseTool {
	if inner == nil {
		return nil
	}
	agent := cfg.ReviewerAgent
	if strings.TrimSpace(agent) == "" {
		agent = DefaultReviewerAgent
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultReviewerTimeout
	}
	return &ReviewGate{
		inner:         inner,
		reviewerAgent: agent,
		spawner:       spawner,
		sink:          cfg.Sink,
		timeout:       timeout,
		nowFn:         time.Now,
	}
}

// Info passes through unchanged — schema is invariant under review.
// The Class field is what ReviewGate reads to decide whether to gate.
func (g *ReviewGate) Info(ctx context.Context) (*basetool.ToolInfo, error) {
	return g.inner.Info(ctx)
}

// InvokableRun is the gate. See package doc.
func (g *ReviewGate) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	info, err := g.inner.Info(ctx)
	if err != nil || info == nil {
		// Pass-through on missing info so an Info-broken tool surfaces
		// its own error from the inner call rather than confusing the
		// reviewer with a malformed proposal. Defensive only — every
		// production tool returns a valid Info.
		return g.inner.InvokableRun(ctx, argsJSON, opts...)
	}

	if !isMutatingClass(info.Class) {
		return g.inner.InvokableRun(ctx, argsJSON, opts...)
	}

	if reason, ok := deterministicReviewApproval(info.Name, argsJSON); ok {
		return g.runWithDeterministicApproval(ctx, info, argsJSON, reason, opts...)
	}

	if g.spawner == nil {
		return "", fmt.Errorf("%w: spawner not wired (mutating tool %q requires review)", ErrReviewerSpawn, info.Name)
	}

	resolved := basetool.ResolveOptions(opts)

	// 1) Persist the pending proposal (best-effort; sink errors are
	//    logged but don't block the call — an audit outage shouldn't
	//    halt the reviewer flow).
	createdAt := g.nowFn().UTC()
	proposalID := ""
	if g.sink != nil {
		id, sinkErr := g.sink.Insert(ctx, MutatingProposalEvent{
			ToolName:       info.Name,
			ArgsJSON:       argsJSON,
			ToolClass:      info.Class,
			ReviewerAgent:  g.reviewerAgent,
			ReviewerTaskID: "", // filled after spawn
			OperatorUserID: resolved.UserID,
			SessionID:      resolved.Tenant, // tenant carries session/user-derived id
			CreatedAt:      createdAt,
		})
		if sinkErr == nil {
			proposalID = id
		}
	}

	// 2) Spawn the reviewer. Synchronous from the gate's perspective
	//    (the agent loop is waiting for this tool to return).
	prompt := buildReviewerPrompt(info.Name, info.Class, argsJSON, resolved.UserID)
	spawnCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	res, spawnErr := g.spawner.SpawnReviewer(spawnCtx, ReviewSpawnRequest{
		AgentName: g.reviewerAgent,
		Prompt:    prompt,
	})
	if spawnErr != nil {
		g.recordDecision(ctx, proposalID, model_DecisionReject, "spawn error: "+spawnErr.Error())
		return "", fmt.Errorf("%w: %v", ErrReviewerSpawn, spawnErr)
	}
	if res == nil {
		g.recordDecision(ctx, proposalID, model_DecisionReject, "spawner returned nil result")
		return "", fmt.Errorf("%w: spawner returned nil", ErrReviewerSpawn)
	}
	if res.Err != "" {
		g.recordDecision(ctx, proposalID, model_DecisionReject, "worker error: "+res.Err)
		return "", fmt.Errorf("%w: worker error: %s", ErrReviewerSpawn, res.Err)
	}

	// 3) Parse the reviewer's output for "Decision: approve|reject".
	decision, reason := parseReviewerDecision(res.Result)
	switch decision {
	case "approve":
		g.recordDecision(ctx, proposalID, model_DecisionApprove, reason)
		// 4) Run the inner tool.
		out, runErr := g.inner.InvokableRun(ctx, argsJSON, opts...)
		g.markExecuted(ctx, proposalID)
		return out, runErr
	case "reject":
		g.recordDecision(ctx, proposalID, model_DecisionReject, reason)
		if reason == "" {
			reason = "no reason provided"
		}
		return "", fmt.Errorf("%w: %s", ErrReviewRejected, reason)
	default:
		g.recordDecision(ctx, proposalID, model_DecisionReject, "undecided: "+truncate(res.Result, 200))
		return "", fmt.Errorf("%w: %v", ErrReviewUndecided, "missing 'Decision: approve|reject' line")
	}
}

func (g *ReviewGate) runWithDeterministicApproval(ctx context.Context, info *basetool.ToolInfo, argsJSON, reason string, opts ...basetool.InvokeOption) (string, error) {
	proposalID := ""
	if g.sink != nil {
		resolved := basetool.ResolveOptions(opts)
		id, sinkErr := g.sink.Insert(ctx, MutatingProposalEvent{
			ToolName:       info.Name,
			ArgsJSON:       argsJSON,
			ToolClass:      info.Class,
			ReviewerAgent:  deterministicPolicyReviewerAgent,
			ReviewerTaskID: "",
			OperatorUserID: resolved.UserID,
			SessionID:      resolved.Tenant,
			CreatedAt:      g.nowFn().UTC(),
		})
		if sinkErr == nil {
			proposalID = id
			g.recordDecision(ctx, proposalID, model_DecisionApprove, reason)
		}
	}

	out, runErr := g.inner.InvokableRun(ctx, argsJSON, opts...)
	g.markExecuted(ctx, proposalID)
	return out, runErr
}

// recordDecision is the best-effort sink update. We swallow errors so
// audit outages don't fail the tool decision; production wires the
// sink to log internally.
func (g *ReviewGate) recordDecision(ctx context.Context, id, decision, reason string) {
	if g.sink == nil || id == "" {
		return
	}
	if err := g.sink.UpdateDecision(ctx, id, decision, reason); err != nil {
		// Best-effort audit sink: the review decision remains authoritative.
		return
	}
}

func (g *ReviewGate) markExecuted(ctx context.Context, id string) {
	if g.sink == nil || id == "" {
		return
	}
	if err := g.sink.MarkExecuted(ctx, id, g.nowFn().UTC()); err != nil {
		// Best-effort audit sink: do not change the already-computed tool result.
		return
	}
}

// isMutatingClass returns true for tool classes that require review.
// Mirrors basetool.ToolInfo.Class taxonomy: "read" / "write" /
// "destructive". We treat empty as "read" (default-permissive on the
// gate side; the audit/timeout decorators still apply).
func isMutatingClass(class string) bool {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "write", "destructive":
		return true
	default:
		return false
	}
}

func deterministicReviewApproval(toolName, argsJSON string) (string, bool) {
	if toolName != "apply_config_change" {
		return "", false
	}
	var args struct {
		Domain  string          `json:"domain"`
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", false
	}
	if !isAlertRuleConfigDomain(args.Domain) {
		return "", false
	}
	action := args.Action
	if strings.TrimSpace(action) == "" && len(args.Payload) > 0 {
		var payload struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args.Payload, &payload); err == nil {
			action = payload.Action
		}
	}
	if !isCreateConfigAction(action) {
		return "", false
	}
	return alertRuleCreateDeterministicApprovalReason, true
}

func isAlertRuleConfigDomain(domain string) bool {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "alert_rule", "alert", "alert_rule_config":
		return true
	default:
		return false
	}
}

func isCreateConfigAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "create":
		return true
	default:
		return false
	}
}

// buildReviewerPrompt assembles the markdown brief the reviewer worker
// receives as its first user message. Keep this terse — the
// reviewer.md system prompt drives the workflow; this prompt only
// supplies the proposal payload + a reminder to emit the decision
// marker.
//
// Format is intentionally simple JSON-in-fence so the reviewer can
// parse it with one json.Unmarshal call if it wants structured access,
// while a plain LLM still reads it as human-friendly text. The
// reminder line is critical: it tells the reviewer to emit
// "Decision: approve|reject" exactly so this decorator's parser
// works.
func buildReviewerPrompt(toolName, toolClass, argsJSON string, operatorUID uint64) string {
	// Re-encode argsJSON through json.Indent for readability; if the
	// input isn't valid JSON, fall back verbatim.
	var pretty []byte
	if buf, err := indentJSON([]byte(argsJSON)); err == nil {
		pretty = buf
	} else {
		pretty = []byte(argsJSON)
	}
	var b strings.Builder
	b.WriteString("# 二审 proposal\n\n")
	b.WriteString("收到 mutating tool_call 提案，请按你的 system prompt 工作流审查并决议。\n\n")
	b.WriteString("**Tool**: `")
	b.WriteString(toolName)
	b.WriteString("`  \n")
	b.WriteString("**Class**: `")
	b.WriteString(toolClass)
	b.WriteString("`  \n")
	b.WriteString(fmt.Sprintf("**Operator user_id**: %d\n\n", operatorUID))
	b.WriteString("**Args**:\n```json\n")
	b.Write(pretty)
	if len(pretty) == 0 || pretty[len(pretty)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("```\n\n")
	b.WriteString("---\n\n")
	b.WriteString("**输出格式硬性要求**: 你的最终消息必须包含一行 `Decision: approve` 或 `Decision: reject`，")
	b.WriteString("否则 ReviewGate 会按 reject 处理（safe default）。reject 时把理由写在 Decision 行下方，")
	b.WriteString("approve 时也写一段 1-2 句风险提示。\n\n")
	b.WriteString("如果没有 `get_sop_text` 工具，按 SRE 通用经验判断：")
	b.WriteString("有 SOP 工具就调；没有就基于 reviewer.md system prompt 的三条门控（SOP / 并行操作 / 回滚路径）做判断。\n")
	return b.String()
}

// indentJSON pretty-prints raw JSON for embedding in the prompt;
// returns the original bytes when input isn't valid JSON.
func indentJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// parseReviewerDecision scans the reviewer's output for a line
// starting with "Decision:" (case-insensitive). The first such line
// wins; the rest of the message after that line (or below it) becomes
// the reason (best-effort — we just take the next non-empty paragraph
// or the trailing 200 chars).
//
// Why line-scan and not LLM re-parse: a second LLM call would double
// the cost and add another failure mode (re-parsing an already-parsed
// decision). The reviewer.md prompt explicitly demands the marker
// line; if a future reviewer flavour decides to emit JSON we'd add a
// json fence detector here, not a second LLM call.
//
// Returns (decision, reason) where decision ∈ {"approve","reject",""}.
func parseReviewerDecision(text string) (decision, reason string) {
	if strings.TrimSpace(text) == "" {
		return "", ""
	}
	// Walk lines once.
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		matched     bool
		reasonLines []string
	)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		// Strip leading markdown emphasis markers (** _ etc.) so
		// "**Decision: approve**" matches the same as "Decision: approve".
		stripped := strings.Trim(line, "*_# ")
		if !matched {
			low := strings.ToLower(stripped)
			if strings.HasPrefix(low, "decision:") {
				rest := strings.TrimSpace(stripped[len("decision:"):])
				rest = strings.Trim(rest, "*_ ")
				rest = strings.ToLower(rest)
				switch {
				case strings.HasPrefix(rest, "approve"):
					decision = "approve"
				case strings.HasPrefix(rest, "reject"):
					decision = "reject"
				default:
					// Keep scanning — maybe the reviewer wrote a
					// preamble like "Decision: pending" we can ignore
					// in favour of a later definitive line.
					continue
				}
				matched = true
			}
		} else {
			// After the decision line, accumulate non-empty content
			// as the reason. Cap to keep the audit row tidy.
			if line != "" {
				reasonLines = append(reasonLines, line)
				if len(reasonLines) >= 8 {
					break
				}
			}
		}
	}
	if !matched {
		// Fallback: maybe the reviewer wrote "approve"/"reject"
		// without the marker. Treat absence as undecided (caller
		// converts to reject).
		return "", ""
	}
	reason = strings.Join(reasonLines, "\n")
	return decision, truncate(reason, 1024)
}

// truncate caps s to n runes, appending an ellipsis when truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// model_DecisionApprove / Reject mirror model.DecisionApprove /
// DecisionReject without forcing this package to import
// internal/manager/model/aiops (which would create a manager-side
// dependency tree the decorator package must stay clear of —
// The string values are the source-of-truth contract;
// the model file's constants must equal these.
const (
	model_DecisionApprove = "approve"
	model_DecisionReject  = "reject"
)
