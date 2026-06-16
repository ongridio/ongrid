// Package callbacks bundles the eino callbacks.Handler implementations
// that wire ongrid cross-cutting concerns into the new compose.Graph
// agent kernel: persistence, SSE streaming, audit, metrics, and
// (carried over from PR-1) budget gating. The handler set is documented
// in (Callback 链) + (主参考图 — callback chain
// 横切区块).
//
// Each file in this package owns exactly one handler; chain.go combines
// them into a default list. Handlers are wired at Invoke time via
// compose.WithCallbacks (see graph.BuildReActGraph header comment) — no
// global registration is performed by these constructors so a graph
// run cannot accidentally pick up handlers from another tenant or
// session. PR-6 of scaffolding only — the cutover layer (NEXT
// PR) will assemble Deps and pass the chain through compose.Invoke.
package callbacks

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// PersistenceDeps bundles the persistence handler's collaborators.
// SessionID identifies which chat row to write into. Repo is the
// SessionRepo binding (— write chat_messages and
// chat_tool_calls). Logger is optional — used to record best-effort
// persistence failures so they don't fail user-visible requests but
// stay observable. Registerer is optional — when set, the handler
// increments ongrid_persist_errors_total{kind=...} for any persist
// failure (错误处理 — persist 失败不阻断 graph).
type PersistenceDeps struct {
	SessionID  string
	Repo       biz.SessionRepo
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// Model is the LLM model id the cutover layer routed this run to.
	// Persisted on each role=assistant chat_messages row so the SPA can
	// surface per-message provenance. Empty → column stays NULL.
	Model string
}

const persistenceWriteTimeout = 5 * time.Second

func persistenceWriteContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), persistenceWriteTimeout)
}

// PersistenceHandler writes chat_messages + chat_tool_calls rows as
// the graph executes. Mirrors the persistence side-effects the legacy
// agent.go for-loop performs synchronously; spec:
//
//	OnChatModelStart → flushIncompleteBatch (autoheal stubs for
//	                   any tool_call from the PREVIOUS assistant turn
//	                   whose OnEnd never fired — see currentBatch)
//	OnChatModelEnd → INSERT chat_messages (role=assistant)
//	OnToolStart → INSERT chat_tool_calls (status=pending)
//	OnToolEnd → UPDATE chat_tool_calls + INSERT chat_messages (role=tool)
//
// User-message persistence stays at the chatruntime entry point (as
// agent.go does today) — by the time the graph starts, the user's
// turn is already on disk. This handler is only responsible for the
// agent's own writes.
//
// Concurrency: handler instances are designed for ONE graph run each.
// The cutover layer constructs a fresh PersistenceHandler per request
// so the SessionID + per-call state stay scoped. Tool start times are
// stashed on context (per-call) which is goroutine-safe.
//
// Autoheal background (currentBatch). eino's ToolsNode runs sibling
// tool_calls from one assistant turn in parallel. In live production
// (.91, session b528bfb0 on 2026-06-06) we observed an assistant
// turn emitting 4 host_bash tool_calls where only 2 of the 4 OnEnd
// callbacks fired — chat_tool_calls had all 4 rows but chat_messages
// was missing 2 role=tool rows, with NO recorded persistence error.
// Replay then produced an envelope strict providers (DeepSeek v4+)
// reject with HTTP 400 "insufficient tool messages following
// tool_calls". The replay-side defense (toolreplay precheck +
// MarkDependentToolsForSkip) keeps the user out of the loop, but the
// dropped tool results are still lost. To stop this from happening
// silently we track per-assistant batches here: persistAssistant
// records the expected llm_call_id set; persistToolEnd marks each
// seen; the next ChatModel.OnStart (signalling the previous batch is
// terminally done) flushes any leftover llm_call_ids as autoheal
// stub role=tool rows so the next replay envelope is structurally
// valid AND the LLM gets honest "tool result was not captured"
// signal instead of a fabricated success.
//
// Errors: persist failures NEVER abort the graph — the handler logs +
// counts and returns ctx unchanged. 可观测性: audit/
// persist best-effort.
type PersistenceHandler struct {
	deps PersistenceDeps

	// errCounter is the lazy collector for persist failures. Resolved
	// at construction; nil when Registerer is nil.
	errCounter *prometheus.CounterVec
	// lossCounter is the autoheal observability collector. Labels:
	//   outcome="autoheal_stub" — a stub row was inserted
	//   tool_name=<the tool whose response was lost>
	// Cardinality: tool_name set is small (bounded by registered tools).
	lossCounter *prometheus.CounterVec

	// toolCalls maps eino tool_call_id → the chat_tool_calls row id we
	// inserted on OnStart, so OnEnd can update the same row. eino
	// guarantees the tool_call_id is unique within a graph run; we
	// scope the map to this handler instance, not globally.
	// Entries are deleted on OnEnd/OnError; what remains here at flush
	// time is exactly the set of tool calls whose end-callback never
	// fired — those are what flushIncompleteBatch heals.
	toolCalls   map[string]toolCallEntry
	toolCallsMu sync.Mutex

	// asstMu protects iteration-counting state used by OnEnd to
	// distinguish the terminal assistant turn from intermediate ones.
	asstMu     sync.Mutex
	asstWrites int

	// assistantIDRelay (optional) is the cross-handler share with
	// SSEHandler — see chain.go. Set by NewDefaultHandlers; nil when
	// tests use NewPersistenceHandler directly.
	assistantIDRelay *assistantIDRelay

	// lastIDsMu protects the two pieces of cross-callback state below.
	// Both flow ChatModel.OnEnd → Tool.OnStart/OnEnd within a single
	// ReAct iteration, which is serial — no two tool calls overlap —
	// so a plain FIFO + scalar is correct.
	lastIDsMu sync.Mutex
	// lastAssistantID is the chat_messages.id of the most recent
	// assistant row this handler wrote. Used as MessageID for the
	// chat_tool_calls rows the FOLLOWING tools persist; MessageID is
	// NOT NULL on chat_tool_calls so without this every insert
	// silently fails the NOT NULL check and the table stays empty —
	// the exact state observed on test sessions pre-fix.
	lastAssistantID string
	// pendingLLMCalls is the FIFO of {llm_call_id, tool_name} pairs
	// extracted from ChatModel.OnEnd's msg.ToolCalls. Tool.OnStart
	// pops the head to associate the upcoming chat_tool_calls row
	// with the real LLM-assigned id (e.g. "call_abc123"). Without
	// this, persistToolEnd writes the synthetic
	// "<tool_name>|<tool_type>" fallback into chat_messages.tool_call_id
	// and history replay loses the linkage strict providers need.
	pendingLLMCalls []pendingLLMCall

	// batchMu guards currentBatch. Held only while opening / sealing /
	// flushing — individual mark-seen calls inside persistToolEnd take
	// it just long enough to update one map. Tool callbacks fire in
	// parallel from eino's ToolsNode so the mutex is non-trivial.
	batchMu      sync.Mutex
	currentBatch *assistantBatch
}

// assistantBatch tracks the tool_call → tool_response pairing for one
// assistant turn so flushIncompleteBatch can emit stub responses for
// any tool_call whose OnEnd never fired. expected is the set of
// LLM-assigned call ids the assistant emitted; seen accumulates as
// persistToolEnd writes role=tool rows; toolName lets the stub carry
// the right tool_name into chat_messages for replay parity. Per the
// per-request handler scoping (see handler doc), at most one batch
// is live at a time.
type assistantBatch struct {
	assistantID string
	expected    map[string]string // llmCallID → toolName (recorded on register)
	seen        map[string]struct{}
	openedAt    time.Time
}

// pendingLLMCall carries the LLM-assigned call id (e.g.
// "call_abc123") + the tool name from the assistant turn that emitted
// the tool_calls slot. ReAct serializes tool execution so we can
// dequeue by order; the toolName is checked for sanity (mismatch =
// graph wiring change, surface as a recorded error).
type pendingLLMCall struct {
	llmCallID string
	toolName  string
}

// toolCallEntry tracks per-call state across OnStart → OnEnd.
type toolCallEntry struct {
	rowID      string
	startedAt  time.Time
	toolName   string
	argsJSON   string
	toolCallID string // eino's per-graph-run tool_call_id (or synthetic fallback)
	// llmCallID is the LLM-assigned id (e.g. "call_abc123") flowed
	// from ChatModel.OnEnd via the handler's pendingLLMCalls FIFO.
	// Empty when the wiring didn't propagate (synthetic fallback);
	// persistToolEnd uses it for chat_messages.tool_call_id so history
	// replay can pair the tool turn with the assistant tool_calls slot.
	llmCallID string
}

// NewPersistenceHandler builds the handler. Returns nil if SessionID
// or Repo is missing — the cutover layer treats nil as "persistence
// disabled" (matching the existing optional-handler pattern).
func NewPersistenceHandler(deps PersistenceDeps) *PersistenceHandler {
	if deps.SessionID == "" || deps.Repo == nil {
		return nil
	}
	h := &PersistenceHandler{
		deps:      deps,
		toolCalls: make(map[string]toolCallEntry),
	}
	if deps.Registerer != nil {
		h.errCounter = registerOrExisting(deps.Registerer, prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ongrid_persist_errors_total",
				Help: "Total ongrid AIOps persistence failures (chat_messages / chat_tool_calls writes).",
			},
			[]string{"kind"},
		)).(*prometheus.CounterVec)
		h.lossCounter = registerOrExisting(deps.Registerer, prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ongrid_chat_tool_response_loss_total",
				Help: "Total ongrid AIOps tool responses lost (OnEnd never fired) and autohealed via stub.",
			},
			[]string{"outcome", "tool_name"},
		)).(*prometheus.CounterVec)
	}
	return h
}

// Needed signals which timings to wire so eino can short-circuit the
// expensive ones we don't use.
//
// ChatModel.OnStart is wired (in addition to OnEnd) so flushIncompleteBatch
// can autoheal the previous turn before the next LLM call goes out — its
// position in the callback stream is the cleanest "previous batch is
// terminally done" signal we have.
func (h *PersistenceHandler) Needed(_ context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	if h == nil {
		return false
	}
	if info == nil {
		return false
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		return timing == callbacks.TimingOnStart || timing == callbacks.TimingOnEnd
	case components.ComponentOfTool:
		return timing == callbacks.TimingOnStart || timing == callbacks.TimingOnEnd || timing == callbacks.TimingOnError
	default:
		return false
	}
}

// OnStart is the hook fired before a component runs. For PersistenceHandler:
//   - ChatModel start: flush any incomplete tool batch from the previous
//     assistant turn (autoheal stub) BEFORE this LLM call goes out. Also
//     traces the entry for instrumentation.
//   - Tool start: write a chat_tool_calls row in pending state and
//     stash the row id on the handler so OnEnd can find it.
func (h *PersistenceHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	if info.Component == components.ComponentOfChatModel {
		h.trace(ctx, "chat_model_start", info, "")
		h.flushIncompleteBatch(ctx)
		return ctx
	}
	if info.Component != components.ComponentOfTool {
		return ctx
	}
	tin := einotool.ConvCallbackInput(input)
	if tin == nil {
		h.trace(ctx, "tool_start_skip_nil_input", info, "")
		return ctx
	}
	h.trace(ctx, "tool_start", info, "")
	startedAt := time.Now().UTC()
	// Resolve MessageID + LLM call id from the assistant turn that
	// just emitted this tool_call. Explicit ctx seams (WithMessageID /
	// WithToolCallID) take precedence for test isolation; production
	// falls back to the handler's internal tracking populated in
	// persistAssistant.
	messageID := messageIDFromCtx(ctx)
	if messageID == "" {
		messageID = h.currentAssistantID()
	}
	// ROOT FIX: eino's ToolsNode stamps the authoritative LLM call id
	// (call_XX) onto ctx before each tool runs — compose.GetToolCallID.
	// Read it directly. It's exact and order-independent, so we never
	// reconstruct the id from completion order (which mis-pairs under
	// parallel out-of-order completion and was the source of the
	// synthetic-id orphans → provider 400). The ctx seam + FIFO are kept
	// only as defensive fallbacks for tools invoked outside a ToolsNode.
	llmCallID := compose.GetToolCallID(ctx)
	if llmCallID == "" {
		if v := ctxToolCallID(ctx); v != "" && v != fallbackToolCallID(info) {
			llmCallID = v
		} else {
			llmCallID = h.popPendingLLMCall(info.Name)
		}
	}
	row := &model.ToolCall{
		MessageID:     messageID,
		ToolName:      info.Name,
		ArgumentsJSON: tin.ArgumentsInJSON,
		Status:        model.StatusPending,
		StartedAt:     startedAt,
		CreatedAt:     startedAt,
	}
	if llmCallID != "" {
		id := llmCallID
		row.LLMCallID = &id
	}
	writeCtx, cancel := persistenceWriteContext(ctx)
	defer cancel()
	if err := h.deps.Repo.CreateToolCall(writeCtx, row); err != nil {
		h.recordErr("tool_call_insert", err)
		return ctx
	}
	// stash by the eino tool_call_id so OnEnd can update the same row;
	// llmCallID (if any) flows downstream so the role=tool chat_messages
	// row carries the real id strict providers expect.
	einoTCID := toolCallIDFromCtx(ctx, info)
	h.toolCallsMu.Lock()
	h.toolCalls[einoTCID] = toolCallEntry{
		rowID:      row.ID,
		startedAt:  startedAt,
		toolName:   info.Name,
		argsJSON:   tin.ArgumentsInJSON,
		toolCallID: einoTCID,
		llmCallID:  llmCallID,
	}
	h.toolCallsMu.Unlock()
	return ctx
}

// ctxToolCallID returns the WithToolCallID seam value, or "" when
// unset. Pulled out so OnStart's logic can ignore the seam when its
// only value is the synthetic "name|type" fallback (which carries no
// real id information).
func ctxToolCallID(ctx context.Context) string {
	v, _ := ctx.Value(toolCallIDCtxKey{}).(string)
	return v
}

// fallbackToolCallID is the synthetic id toolCallIDFromCtx returns
// when no ctx value is set. Exposed for the comparison inside OnStart
// so we don't mistake the fallback for a real LLM-assigned id.
func fallbackToolCallID(info *callbacks.RunInfo) string {
	if info == nil {
		return ""
	}
	return info.Name + "|" + info.Type
}

// OnEnd is the hook fired after a component succeeds.
//   - ChatModel end: append a chat_messages row (role=assistant) with
//     the model's content + token usage.
//   - Tool end: update the chat_tool_calls row with status=success +
//     the result JSON, AND insert a chat_messages row (role=tool) so
//     history replay sees the tool result on the next turn.
func (h *PersistenceHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		mo := einomodel.ConvCallbackOutput(output)
		if mo == nil || mo.Message == nil {
			h.trace(ctx, "chat_model_end_skip_nil", info, "")
			return ctx
		}
		h.trace(ctx, "chat_model_end", info, "")
		h.persistAssistant(ctx, mo)
	case components.ComponentOfTool:
		tout := einotool.ConvCallbackOutput(output)
		if tout == nil {
			h.trace(ctx, "tool_end_skip_nil_output", info, "")
			return ctx
		}
		h.trace(ctx, "tool_end_success", info, toolCallIDFromCtx(ctx, info))
		h.persistToolEnd(ctx, info, tout, nil)
	}
	return ctx
}

// OnError fires when a component returns a non-nil error. For tool
// errors we still update the chat_tool_calls row to status=error so
// the audit trail captures the failure; ChatModel errors are recorded
// by the audit handler (this layer doesn't produce a chat row for
// them — there's no message to persist).
func (h *PersistenceHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	if h == nil || info == nil || err == nil {
		return ctx
	}
	if info.Component != components.ComponentOfTool {
		return ctx
	}
	h.trace(ctx, "tool_end_error", info, toolCallIDFromCtx(ctx, info))
	h.persistToolEnd(ctx, info, nil, err)
	return ctx
}

// OnStartWithStreamInput is a no-op (we only consume the eventual end value).
func (h *PersistenceHandler) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, in *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	if in != nil {
		in.Close()
	}
	return ctx
}

// OnEndWithStreamOutput is a no-op for PR-6. token-level streaming
// persistence (writing the final assembled message at stream-end) is
// owned by the cutover layer in PR-7; for now we drain + close so we
// don't leak goroutines.
func (h *PersistenceHandler) OnEndWithStreamOutput(ctx context.Context, _ *callbacks.RunInfo, out *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	if out != nil {
		out.Close()
	}
	return ctx
}

func (h *PersistenceHandler) persistAssistant(ctx context.Context, mo *einomodel.CallbackOutput) {
	msg := mo.Message
	row := &model.Message{
		SessionID: h.deps.SessionID,
		Role:      string(msg.Role),
		CreatedAt: time.Now().UTC(),
	}
	if h.deps.Model != "" {
		m := h.deps.Model
		row.Model = &m
	}
	if msg.Content != "" {
		c := msg.Content
		row.Content = &c
	}
	if mo.TokenUsage != nil {
		pt := mo.TokenUsage.PromptTokens
		ct := mo.TokenUsage.CompletionTokens
		row.PromptTokens = &pt
		row.CompletionTokens = &ct
	} else if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		pt := msg.ResponseMeta.Usage.PromptTokens
		ct := msg.ResponseMeta.Usage.CompletionTokens
		row.PromptTokens = &pt
		row.CompletionTokens = &ct
	}
	writeCtx, cancel := persistenceWriteContext(ctx)
	defer cancel()
	if err := h.deps.Repo.AppendMessage(writeCtx, row); err != nil {
		h.recordErr("assistant_insert", err)
		return
	}
	// Hand the persisted row id to SSEHandler so the assistant_end
	// frame can ship it. AppendMessage stamps row.ID via gorm's auto-
	// generated string id (see model.Message ID field).
	h.assistantIDRelay.store(row.ID)
	h.asstMu.Lock()
	h.asstWrites++
	h.asstMu.Unlock()

	// Tool-call linkage for downstream Tool.OnStart:
	//   - lastAssistantID is the MessageID the next chat_tool_calls
	//     rows should attach to.
	//   - pendingLLMCalls carries the real LLM call ids emitted in
	//     msg.ToolCalls so the FOLLOWING tool callbacks can persist
	//     them as llm_call_id (and replay can pair correctly).
	// Both reset on every assistant turn — the next assistant
	// supersedes the previous turn's tool_calls slot.
	pending := make([]pendingLLMCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		if tc.ID == "" || tc.Function.Name == "" {
			continue
		}
		pending = append(pending, pendingLLMCall{
			llmCallID: tc.ID,
			toolName:  tc.Function.Name,
		})
	}
	h.lastIDsMu.Lock()
	h.lastAssistantID = row.ID
	h.pendingLLMCalls = pending
	h.lastIDsMu.Unlock()
	// Open a new batch tracker — flushIncompleteBatch will heal it on
	// the next ChatModel.OnStart. If the assistant emitted no tool_calls
	// (terminal text answer), expected is empty and the batch is a
	// no-op on flush.
	h.openBatch(row.ID, pending)
}

// openBatch starts tracking the tool-call → tool-response pairing for
// a fresh assistant turn. Replaces any prior batch (which means the
// previous batch was abandoned without its OnEnds firing AND without
// a ChatModel.OnStart flushing it — drop counter so we can see it).
func (h *PersistenceHandler) openBatch(assistantID string, calls []pendingLLMCall) {
	expected := make(map[string]string, len(calls))
	for _, c := range calls {
		if c.llmCallID == "" {
			continue
		}
		expected[c.llmCallID] = c.toolName
	}
	h.batchMu.Lock()
	if h.currentBatch != nil && len(h.currentBatch.expected) > 0 {
		// Surface as recordErr so we know if openBatch ever races with
		// an unflushed prior batch (would mean ChatModel.OnEnd fired
		// twice without an intervening OnStart — very unusual).
		h.recordErr("batch_overwrite_unflushed", errBatchOverwrite{
			prevAssistant: h.currentBatch.assistantID,
			newAssistant:  assistantID,
			lost:          len(h.currentBatch.expected) - len(h.currentBatch.seen),
		})
	}
	h.currentBatch = &assistantBatch{
		assistantID: assistantID,
		expected:    expected,
		seen:        make(map[string]struct{}, len(expected)),
		openedAt:    time.Now().UTC(),
	}
	h.batchMu.Unlock()
}

// markSeen records that a tool_call_id's response has been persisted.
// Called from persistToolEnd AFTER the AppendMessage succeeds.
func (h *PersistenceHandler) markSeen(llmCallID string) {
	if llmCallID == "" {
		return
	}
	h.batchMu.Lock()
	if h.currentBatch != nil {
		h.currentBatch.seen[llmCallID] = struct{}{}
	}
	h.batchMu.Unlock()
}

// flushIncompleteBatch is the autoheal step. Called at the start of
// the NEXT ChatModel call (and from FinalizeBatch on handler teardown),
// which is the cleanest signal that the previous assistant turn's
// tool batch is terminally done. For every expected llm_call_id that
// never landed a seen mark, write a stub role=tool row so the next
// replay envelope is structurally valid AND the LLM gets honest
// signal that the tool result was lost.
//
// Idempotent: clears currentBatch on exit, so a redundant call is a
// no-op.
func (h *PersistenceHandler) flushIncompleteBatch(ctx context.Context) {
	h.batchMu.Lock()
	batch := h.currentBatch
	h.currentBatch = nil
	h.batchMu.Unlock()
	if batch == nil || len(batch.expected) == 0 {
		return
	}
	missing := make(map[string]string) // llmCallID → toolName
	for id, tname := range batch.expected {
		if _, ok := batch.seen[id]; ok {
			continue
		}
		missing[id] = tname
	}
	if len(missing) == 0 {
		return
	}
	if h.deps.Logger != nil {
		h.deps.Logger.Warn("tool response loss detected; autohealing with stub rows",
			slog.String("session_id", h.deps.SessionID),
			slog.String("assistant_id", batch.assistantID),
			slog.Int("expected", len(batch.expected)),
			slog.Int("seen", len(batch.seen)),
			slog.Int("missing", len(missing)),
			slog.Int64("batch_age_ms", time.Since(batch.openedAt).Milliseconds()),
		)
	}
	for callID, tname := range missing {
		body := `{"error":"tool response was not persisted (eino ToolsNode OnEnd loss); placeholder synthesised by manager to keep history envelope valid","autoheal":true}`
		cid := callID
		tn := tname
		row := &model.Message{
			SessionID:  h.deps.SessionID,
			Role:       model.RoleTool,
			Content:    &body,
			ToolCallID: &cid,
			ToolName:   &tn,
			CreatedAt:  time.Now().UTC(),
		}
		writeCtx, cancel := persistenceWriteContext(ctx)
		err := h.deps.Repo.AppendMessage(writeCtx, row)
		cancel()
		if err != nil {
			h.recordErr("autoheal_stub_insert", err)
			continue
		}
		if h.lossCounter != nil {
			h.lossCounter.WithLabelValues("autoheal_stub", tname).Inc()
		}
	}
}

// FinalizeBatch flushes any in-flight batch before the handler is
// retired. Called from chatruntime after the graph invoke returns so
// "user closed browser mid-batch" doesn't leave orphan tool_calls
// behind (the ChatModel.OnStart hook only catches in-session cases).
func (h *PersistenceHandler) FinalizeBatch(ctx context.Context) {
	if h == nil {
		return
	}
	h.flushIncompleteBatch(ctx)
}

type errBatchOverwrite struct {
	prevAssistant, newAssistant string
	lost                        int
}

func (e errBatchOverwrite) Error() string {
	return "batch overwrite prev=" + e.prevAssistant + " new=" + e.newAssistant
}

// popPendingLLMCall returns the head of the FIFO matching toolName,
// or "" if the queue is empty / next entry doesn't match (which would
// indicate a graph wiring change we want to surface, not silently
// mispair).
func (h *PersistenceHandler) popPendingLLMCall(toolName string) string {
	h.lastIDsMu.Lock()
	defer h.lastIDsMu.Unlock()
	if len(h.pendingLLMCalls) == 0 {
		return ""
	}
	// Match by tool name ANYWHERE in the queue, not just the head.
	// Parallel tool calls complete out of issue-order (e.g. query_promql
	// finishes before get_host_processes even though it was listed
	// second), so a strict head match mis-pairs and the row falls back to
	// a synthetic "<name>|einoToolAdapter" id — which has no matching
	// tool_calls on the assistant turn, so replay hits the provider's
	// 400 "tool must follow tool_calls". Pop the first pending entry with
	// this name; duplicate names still pair in issue order (FIFO among
	// same-named peers).
	for i, c := range h.pendingLLMCalls {
		if c.toolName == toolName {
			id := c.llmCallID
			h.pendingLLMCalls = append(h.pendingLLMCalls[:i:i], h.pendingLLMCalls[i+1:]...)
			return id
		}
	}
	// Genuinely no pending call for this tool — a real wiring drift.
	// Surface it; the row falls back to the synthetic id and the replay
	// builder drops the orphan tool message so the turn stays valid.
	h.recordErr("tool_call_fifo_mismatch", errToolFIFOMismatch{
		expected: h.pendingLLMCalls[0].toolName,
		got:      toolName,
	})
	return ""
}

func (h *PersistenceHandler) currentAssistantID() string {
	h.lastIDsMu.Lock()
	defer h.lastIDsMu.Unlock()
	return h.lastAssistantID
}

type errToolFIFOMismatch struct {
	expected, got string
}

func (e errToolFIFOMismatch) Error() string {
	return "tool_call FIFO head=" + e.expected + " but tool=" + e.got
}

func (h *PersistenceHandler) persistToolEnd(ctx context.Context, info *callbacks.RunInfo, tout *einotool.CallbackOutput, execErr error) {
	tcID := toolCallIDFromCtx(ctx, info)
	h.toolCallsMu.Lock()
	entry, ok := h.toolCalls[tcID]
	delete(h.toolCalls, tcID)
	h.toolCallsMu.Unlock()
	if !ok {
		// No matching OnStart — chatruntime didn't fire it OR the same
		// tcID was double-deleted by a callback race. Used to be a
		// silent return; record it now so we can correlate against
		// flushIncompleteBatch stub events.
		h.recordErr("tool_call_lookup_miss", errToolCallLookupMiss{
			toolCallID: tcID,
			toolName:   runInfoName(info),
		})
		return
	}

	endedAt := time.Now().UTC()
	status := model.StatusSuccess
	var errStr *string
	var resultPtr *string
	if execErr != nil {
		s := execErr.Error()
		errStr = &s
		status = model.StatusError
		if errors.Is(execErr, context.DeadlineExceeded) {
			status = model.StatusTimeout
		}
	} else if tout != nil {
		s := tout.Response
		resultPtr = &s
	}

	writeCtx, cancel := persistenceWriteContext(ctx)
	defer cancel()
	if err := h.deps.Repo.UpdateToolCallResult(writeCtx, entry.rowID, status, resultPtr, errStr, endedAt); err != nil {
		h.recordErr("tool_call_update", err)
	}

	// Append a role=tool message so history replay sees the tool
	// result. Use the LLM-assigned call id (entry.llmCallID) when
	// available — that's what strict providers (DeepSeek v4+) match
	// against the assistant turn's tool_calls slot. Falls back to the
	// eino-internal tool_call_id only when the wiring didn't capture
	// the real id (e.g. test or pre-fix data); toolreplay.Resolve
	// drops the assistant turn for those, preserving correctness over
	// orphan tool replay.
	tname := entry.toolName
	tcallID := entry.llmCallID
	if tcallID == "" {
		tcallID = entry.toolCallID
	}
	body := ""
	if resultPtr != nil {
		body = *resultPtr
	} else if errStr != nil {
		body = `{"error":"` + *errStr + `"}`
	} else {
		body = `{}`
	}
	row := &model.Message{
		SessionID:  h.deps.SessionID,
		Role:       model.RoleTool,
		Content:    &body,
		ToolCallID: &tcallID,
		ToolName:   &tname,
		CreatedAt:  endedAt,
	}
	if err := h.deps.Repo.AppendMessage(writeCtx, row); err != nil {
		h.recordErr("tool_msg_insert", err)
		return
	}
	// Mark seen AFTER AppendMessage succeeds so a failed insert doesn't
	// fool flushIncompleteBatch into thinking this call landed. Tracked
	// by llmCallID (what the batch's expected set is keyed on) — falls
	// back to the synthetic id if the wiring lost the real one, which
	// preserves the at-least-one-mark invariant for sanity.
	if entry.llmCallID != "" {
		h.markSeen(entry.llmCallID)
	} else {
		h.markSeen(entry.toolCallID)
	}
}

type errToolCallLookupMiss struct {
	toolCallID, toolName string
}

func (e errToolCallLookupMiss) Error() string {
	return "tool_call lookup miss id=" + e.toolCallID + " name=" + e.toolName
}

func runInfoName(info *callbacks.RunInfo) string {
	if info == nil {
		return ""
	}
	return info.Name
}

// trace emits a single info-level breadcrumb for one callback firing.
// Used to detect "OnEnd never fired" cases by absence in logs — every
// expected callback should emit exactly one trace line. Cheap (one
// slog.Info call); operators can drop the log level to filter out if
// noise becomes a concern.
func (h *PersistenceHandler) trace(_ context.Context, event string, info *callbacks.RunInfo, toolCallID string) {
	if h == nil || h.deps.Logger == nil {
		return
	}
	name := ""
	typ := ""
	if info != nil {
		name = info.Name
		typ = string(info.Component)
	}
	h.deps.Logger.Info("persistence callback",
		slog.String("event", event),
		slog.String("session_id", h.deps.SessionID),
		slog.String("component", typ),
		slog.String("name", name),
		slog.String("tool_call_id", toolCallID),
	)
}

func (h *PersistenceHandler) recordErr(kind string, err error) {
	if h.deps.Logger != nil {
		h.deps.Logger.Warn("persistence handler write failed",
			slog.String("kind", kind),
			slog.String("session_id", h.deps.SessionID),
			slog.String("error", err.Error()))
	}
	if h.errCounter != nil {
		h.errCounter.WithLabelValues(kind).Inc()
	}
}

// AssistantWriteCount reports how many assistant rows this handler
// has persisted. Exposed for tests; production callers should not
// depend on it.
func (h *PersistenceHandler) AssistantWriteCount() int {
	if h == nil {
		return 0
	}
	h.asstMu.Lock()
	defer h.asstMu.Unlock()
	return h.asstWrites
}

// Compile-time assertion.
var (
	_ callbacks.Handler       = (*PersistenceHandler)(nil)
	_ callbacks.TimingChecker = (*PersistenceHandler)(nil)
)

// --- ctx helpers ---------------------------------------------------------

// messageIDCtxKey is the context key carrying the assistant chat_messages
// row id under which tool calls should be parented. The cutover layer
// will set this; PR-6 leaves the field zero-valued when the key is
// absent (the SQL schema permits NULL message_id during the migration
// window — chatruntime will backfill).
type messageIDCtxKey struct{}

// toolCallIDCtxKey lets the chatruntime + assembler pass eino's
// tool_call_id (the ChatGPT-style call id) through to the persistence
// handler so OnStart and OnEnd correlate via the same key.
type toolCallIDCtxKey struct{}

// WithMessageID returns ctx with mid stashed as the parent assistant
// row id. Used by the cutover layer to link chat_tool_calls rows back
// to their owning assistant turn.
func WithMessageID(ctx context.Context, mid string) context.Context {
	if mid == "" {
		return ctx
	}
	return context.WithValue(ctx, messageIDCtxKey{}, mid)
}

func messageIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(messageIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// WithToolCallID returns ctx with the eino tool_call_id stashed so
// PersistenceHandler.OnStart / OnEnd can correlate using a stable key.
// In production wiring eino sets the per-call id internally; for tests
// we expose this seam so handlers can be exercised in isolation.
func WithToolCallID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDCtxKey{}, id)
}

// toolCallIDFromCtx prefers an explicit context value; otherwise it
// falls back to a tuple of (run-info name, run-info type) which is
// stable for the duration of a single tool invocation. eino guarantees
// the same RunInfo pointer is reused across OnStart/OnEnd of one call,
// but inspecting it via Type+Name keeps us decoupled from internal
// pointer identity.
func toolCallIDFromCtx(ctx context.Context, info *callbacks.RunInfo) string {
	// ROOT FIX: prefer eino's authoritative tool_call_id. The ToolsNode
	// sets it per call (compose.GetToolCallID), so OnStart/OnEnd of one
	// tool share the real call_XX and the h.toolCalls map pairs exactly —
	// even when parallel tools finish out of order. This is also what the
	// role=tool chat_messages row carries, so replay never sees a
	// synthetic-id orphan.
	if rid := compose.GetToolCallID(ctx); rid != "" {
		return rid
	}
	if v, ok := ctx.Value(toolCallIDCtxKey{}).(string); ok && v != "" {
		return v
	}
	if info == nil {
		return ""
	}
	return info.Name + "|" + info.Type
}
