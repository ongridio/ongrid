// runtime.go is the in-process orchestration entry the new graph-based
// agent path goes through. PR-9 of — the cutover layer that
// pre-PR-9 callers (legacy agent.go) bypass entirely. After this PR
// the manager's HTTP service can pick which path runs based on the
// ONGRID_AGENT_KERNEL feature flag (default = legacy).
//
// Responsibilities:
//
//  1. Ownership check (caller user_id == session.user_id; admin bypass
//     happens upstream — by the time Handle is called the service has
//     already resolved the owning user_id).
//  2. Skill resolution against the user query (SkillRegistry.Resolve)
//     — the active skills shape the system prompt.
//  3. System prompt assembly via ComposeSystemPrompt — base prompt +
//     active skill prompts + (optional) agent persona.
//  4. @-mention inlining — same markdown-bullet preamble the legacy
//     agent.go produces, so chat history replay is byte-identical
//     across kernels (legacy mention text persists into the new path
//     unchanged).
//  5. User-message persistence (chat_messages role=user). Mirrors the
//     legacy "persist before LLM call" invariant from agent.go so a
//     downstream crash leaves the user turn on disk.
//  6. Graph invoke with the default callback chain
//     (callbacks.NewDefaultHandlers) wired via compose.WithCallbacks.
//     The callback chain handles persistence (assistant + tool rows),
//     SSE streaming, audit, metrics, and budget gating.
//  7. Reply translation — a chatruntime.Reply that the service layer
//     translates back to agent.Reply for HTTP response shape parity.
//
// Behaviour parity with legacy agent.go is the explicit goal — the
// SPA, the persistence schema, and downstream reports must not see
// the kernel switch. Where exact parity isn't reachable (e.g. eino's
// graph runs the loop internally so we don't see per-iteration
// AssistantEvent counts), the value is best-effort and documented
// inline.
package chatruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph/callbacks"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/toolreplay"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// Mention mirrors the legacy agent.Mention shape so chatruntime can
// be called without dragging the manager/biz/aiops/agent package as
// a dependency. The service-layer adapter does the type translation.
type Mention struct {
	Type  string
	ID    string
	Label string
}

// MentionResolver hydrates a list of @-mentions into one bullet line
// each. Mirrors agent.MentionResolver. nil = no inlining.
type MentionResolver interface {
	Resolve(ctx context.Context, mentions []Mention) []string
}

// EventType enumerates streaming events emitted by the runtime. The
// values are byte-equal to the legacy agent.EventType strings so the
// SSE adapter at the service layer can route both kernels through
// the same wire format. New event names introduced in PR-6
// (assistant_start / assistant_delta / assistant_end) are NOT
// surfaced through this enum yet — the cutover keeps the legacy
// frame names so the SPA round-trips without changes.
type EventType string

const (
	// EventAssistant fires after every assistant turn is persisted.
	EventAssistant EventType = "assistant"
	// EventToolStart fires after a tool_call row is persisted as
	// pending, before the tool actually runs.
	EventToolStart EventType = "tool_start"
	// EventToolEnd fires once the tool finishes (success/error/timeout).
	EventToolEnd EventType = "tool_end"
	// EventDone fires once at terminal success with the final Reply.
	EventDone EventType = "done"
	// EventError fires on terminal failure.
	EventError EventType = "error"
)

// AssistantEvent describes one assistant turn's persisted row. Same
// shape as legacy agent.AssistantEvent.
type AssistantEvent struct {
	Iteration        int
	MessageID        string
	Content          string
	CreatedAt        time.Time
	PendingToolCalls int
}

// ToolEvent describes one tool_call lifecycle event. Same shape as
// legacy agent.ToolEvent.
type ToolEvent struct {
	ToolCallID string
	Name       string
	DeviceID   *uint64
	Status     string
	StartedAt  time.Time
	EndedAt    *time.Time
	DurationMs int64
	Error      string
	ArgsJSON   string
	ResultJSON string
}

// Event is the streaming envelope. Mirrors legacy agent.Event so the
// service-layer adapter can copy fields one-to-one without a switch.
//
// Notification carries the task_notification payload — set when
// Type == EventTaskNotification and a background worker reaches a
// terminal state. The SPA / SSE adapter renders it as a separate frame
// type next to assistant / tool / done.
type Event struct {
	Type         EventType
	Assistant    *AssistantEvent
	Tool         *ToolEvent
	Done         *Reply
	Error        string
	Notification *TaskNotification
}

// Emit is the streaming callback. nil-safe.
type Emit func(Event)

// Request bundles every per-call input the runtime needs.
type Request struct {
	SessionID string
	UserID    uint64
	// Role is the caller's system role (admin | user | viewer).
	// gates the toolbag against this — viewer drops Class!=read so the
	// LLM cannot reach mutating tools no matter what the persona allows.
	Role             string
	UserText         string
	Mentions         []Mention
	Provider         string
	Model            string
	WebSearchEnabled bool
	// Locale is the UI language ("en-US"/"zh-CN") the reply should use;
	// threaded into graph.Input so the assembler adds a language directive.
	Locale string
	// Emit is the streaming sink. nil = no streaming (blocking call).
	Emit Emit
}

// Reply is the terminal result returned by Handle. Mirrors the
// legacy agent.Reply shape so the service layer can fan it back out
// through the same DTO without translation losses.
type Reply struct {
	Message    *aiopsmodel.Message
	Usage      llm.Usage
	Iterations int
	ToolCalls  []*aiopsmodel.ToolCall
}

// Config tunes runtime behaviour. ToolBag is the wrapped (decorated)
// tool list; the runtime does NOT wrap tools itself — that's the
// caller's job at construction time so the decorator deps (audit
// sink, limiter, registerer) live exactly once per process.
type Config struct {
	// SkillRegistry holds the parsed SKILL.md descriptors. May be
	// empty (nil-safe ComposeSystemPrompt).
	SkillRegistry *SkillRegistry

	// AgentRegistry holds the parsed agent personas. May be empty.
	AgentRegistry *AgentRegistry

	// Sessions is the persistence binding (chat_sessions /
	// chat_messages / chat_tool_calls). Required.
	Sessions biz.SessionRepo

	// ChatModel is the eino model.ToolCallingChatModel. Required.
	// Production wiring passes RoutingChatModel from PR-1; tests
	// inject a scriptedChatModel.
	ChatModel model.ToolCallingChatModel

	// ToolBag is the pre-decorated BaseTool list the graph exposes
	// to the LLM. cmd/ongrid/main.go assembles this once via
	// Registry.BuildBaseTools + AppendHostFilesTools + Wrap.
	ToolBag []basetool.BaseTool

	// CoordinatorStubs are name-matching redirect stubs visible only
	// to the coordinator agent. They occupy hallucination-prone tool
	// names (host_bash, get_host_load, ...) and return a "use
	// AgentTool to dispatch to specialist-X" message instead of
	// executing the real query. Without them, the eino runtime
	// aborts with "tool not found in toolsNode indexes" the moment
	// the LLM picks a name not actually in the coordinator's filtered
	// bag — see internal/manager/biz/aiops/tools/redirect_stub.go.
	//
	// Workers (specialists) never see this slice. They have the real
	// tool under the same name in their own filtered bag.
	CoordinatorStubs []basetool.BaseTool

	// MentionResolver hydrates @-mentions into markdown bullets.
	// Optional.
	MentionResolver MentionResolver

	// BasePrompt is the universal preamble prepended to every
	// system prompt. Empty = none.
	BasePrompt string

	// HistoryLimit caps how many past messages we replay. 0 → 50
	// (legacy default).
	HistoryLimit int

	// GraphCfg tunes the graph engine (max iterations, temperature,
	// per-tool timeout).
	GraphCfg graph.Config

	// CallbackDeps are the cross-cutting deps wired into every graph
	// run. Persistence.SessionID is filled per-request from
	// Request.SessionID; the rest of Persistence + Audit + Metrics
	// + Budget are filled at construction.
	CallbackDeps callbacks.Deps

	// Logger may be nil.
	Logger *slog.Logger
}

// ToolBagProvider is the local mirror of tools.DeferredToolBagProvider —
// chatruntime cannot import the tools package (cyclic: tools imports
// nothing from chatruntime, but cmd-side wiring goes the other
// direction and would close the loop). The shape is intentionally
// identical so a *tools.ToolBag value satisfies this interface
// directly without an adapter.
//
// — the runtime needs a handle on the unredacted tool
// universe so future code paths (e.g. logging the active toolBag at
// boot, exposing a /metrics list of registered tools) can introspect
// what the LLM has access to even when deferral is on. Today the
// handle is read-only — set once via SetToolBag, never mutated.
type ToolBagProvider interface {
	DeferredTools() []basetool.BaseTool
	AllTools() []basetool.BaseTool
}

// Runtime is the cutover entry. Construct once at boot via
// NewRuntime; call Handle per HTTP request.
//
// workers holds the in-process map of spawned sub-agents keyed by
// worker id. Lifetime: bounded by the Runtime's lifetime — there's no
// auto-eviction yet (a follow-up PR adds a TTL sweeper once we move
// chat_sessions to track parent_session_id; until then workers persist
// in memory until process restart, which matches a fresh sandbox per
// chat in dev/test).
type Runtime struct {
	cfg Config
	log *slog.Logger

	workersMu sync.Mutex
	workers   map[string]*Worker

	// bag is the unredacted ToolBag handle wired by SetToolBag. nil
	// when the caller wires the runtime without calling SetToolBag —
	// not all code paths need it (legacy callers only feed
	// cfg.ToolBag through the graph).
	bag ToolBagProvider
}

// NewRuntime builds a Runtime. Returns an error when required deps
// are missing — Sessions and ChatModel.
func NewRuntime(cfg Config) (*Runtime, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("chatruntime: Sessions is required")
	}
	if cfg.ChatModel == nil {
		return nil, errors.New("chatruntime: ChatModel is required")
	}
	if cfg.HistoryLimit <= 0 {
		cfg.HistoryLimit = 50
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Runtime{cfg: cfg, log: log}, nil
}

// SetMentionResolver wires the @-mention hydrator post-construction.
// Optional — when unset the runtime runs with no mention inlining
// (matches the legacy agent's nil-resolver behaviour).
func (rt *Runtime) SetMentionResolver(r MentionResolver) {
	if rt == nil {
		return
	}
	rt.cfg.MentionResolver = r
}

// SetToolBag wires the unredacted ToolBag handle onto the Runtime so
// downstream introspection (ToolSearch, future /metrics
// surfaces) can query the full tool universe even when the LLM-facing
// view is the deferred / redacted one. Idempotent; nil is a no-op so
// tests can opt out without a panic.
//
// Note: the bag is INFORMATIONAL only. The graph still feeds the
// LLM via cfg.ToolBag, which the caller assembles from
// bag.SchemasForLLM() before calling NewRuntime — see
// cmd/ongrid/main.go's buildAIOpsRuntime for the wiring.
func (rt *Runtime) SetToolBag(bag ToolBagProvider) {
	if rt == nil {
		return
	}
	rt.bag = bag
}

// AppendToolBag appends extra BaseTools to the coordinator tool bag.
// — used by main.go to bolt the coordinator-only AgentTool
// / SendMessageTool / TaskStopTool onto the Runtime AFTER construction
// (the chicken-and-egg: those tools need the Runtime as their spawner,
// the Runtime needs the tool bag at NewRuntime time). Workers do NOT
// observe these added tools — filterToolsForAgent strips them
// unconditionally via coordinatorOnlyTools (see worker.go).
//
// This is intentionally distinct from a generic "AddTool" method: it
// signals "these are coordinator-only" so a future reader of main.go
// understands why three more tools appear after NewRuntime returned.
func (rt *Runtime) AppendToolBag(tools []basetool.BaseTool) {
	if rt == nil || len(tools) == 0 {
		return
	}
	rt.cfg.ToolBag = append(rt.cfg.ToolBag, tools...)
}

// AgentRegistry exposes the registry for narrow read-only callers
// (cmd/main.go uses this when building AgentTool's subagent registry
// shim). nil-safe.
func (rt *Runtime) AgentRegistry() *AgentRegistry {
	if rt == nil {
		return nil
	}
	return rt.cfg.AgentRegistry
}

// SkillRegistry exposes the skill registry for narrow read-only
// callers — today the marketplace usecase wires Reload calls
// against it on install/uninstall. nil-safe.
func (rt *Runtime) SkillRegistry() *SkillRegistry {
	if rt == nil {
		return nil
	}
	return rt.cfg.SkillRegistry
}

// ToolCount returns how many tools are bound into the toolBag. Used
// at boot time + tests for visibility (the spec asks us to log this
// when ONGRID_AGENT_KERNEL=graph).
func (rt *Runtime) ToolCount() int {
	if rt == nil {
		return 0
	}
	return len(rt.cfg.ToolBag)
}

// ToolNames returns the resolved tool names (best-effort — Info()
// errors are skipped). Production wiring logs this at boot so the
// operator sees what the LLM is exposed to.
func (rt *Runtime) ToolNames(ctx context.Context) []string {
	if rt == nil {
		return nil
	}
	out := make([]string, 0, len(rt.cfg.ToolBag))
	for _, t := range rt.cfg.ToolBag {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		out = append(out, info.Name)
	}
	return out
}

// Handle runs one user turn end-to-end. Mirrors legacy
// agent.runInternal: ownership check → mention inline → user message
// persistence → graph invoke with callback chain → reply.
func (rt *Runtime) Handle(ctx context.Context, req *Request) (*Reply, error) {
	if rt == nil || rt.cfg.Sessions == nil || rt.cfg.ChatModel == nil {
		return nil, errs.ErrNotWiredYet
	}
	if req == nil {
		return nil, fmt.Errorf("%w: request required", errs.ErrInvalid)
	}

	emit := req.Emit
	if emit == nil {
		emit = func(Event) {}
	}
	// Thread the active emitter into ctx so AgentTool's InvokableRun
	// (deep inside the graph) can fire a task_notification SSE frame
	// when a background worker terminates. /
	ctx = withEmit(ctx, req.Emit)

	// 1. Ownership check.
	sess, err := rt.cfg.Sessions.GetSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if sess.UserID != req.UserID {
		// Match legacy behaviour: surface ErrNotFound (not
		// ErrForbidden) so non-owners can't fingerprint sessions.
		return nil, errs.ErrNotFound
	}

	// 2. Render @-mention bullets.
	mentionsRendered := ""
	augmentedUserText := req.UserText
	if len(req.Mentions) > 0 && rt.cfg.MentionResolver != nil {
		bullets := rt.cfg.MentionResolver.Resolve(ctx, req.Mentions)
		if len(bullets) > 0 {
			// Same prefix the legacy agent.go uses — keeps history
			// replay byte-identical across kernels.
			mentionsRendered = "用户在消息中引用了以下平台对象，请在回答时考虑：\n" +
				strings.Join(bullets, "\n")
			augmentedUserText = mentionsRendered + "\n\n" + req.UserText
		}
	}

	// 3. Persist the user message first (legacy invariant — survives
	//    a graph crash).
	userContent := augmentedUserText
	userMsg := &aiopsmodel.Message{
		SessionID: sess.ID,
		Role:      aiopsmodel.RoleUser,
		Content:   &userContent,
		CreatedAt: time.Now().UTC(),
	}
	if err := rt.cfg.Sessions.AppendMessage(ctx, userMsg); err != nil {
		return nil, fmt.Errorf("chatruntime: persist user msg: %w", err)
	}

	// 4. Load history (includes the just-appended user message).
	history, err := rt.cfg.Sessions.ListMessages(ctx, sess.ID, rt.cfg.HistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("chatruntime: load history: %w", err)
	}
	if reply, handled := rt.tryApplyConfirmedConfigDraft(ctx, req, sess, history, emit); handled {
		return reply, nil
	}

	// 5. Resolve active skills + compose the system prompt.
	policy := Policy{AllowedClasses: []string{"*"}}
	var activeSkills []*Skill
	if rt.cfg.SkillRegistry != nil {
		activeSkills = rt.cfg.SkillRegistry.Resolve(req.UserText, policy)
	}
	// 5a. Top-level persona (Phase 2 of the user-Agent initiative): when
	// the session was created with a chosen agent (sess.AgentID set), use
	// that persona's SystemPrompt as the base instead of the global
	// BasePrompt and filter the ToolBag through its allowed/disallowed
	// list. Worker spawn already does this for sub-agents (see worker.go);
	// the coordinator path now honors it too. Stale agent ids fall back
	// to the global default with a log line — never crash a session.
	basePrompt := rt.cfg.BasePrompt
	sessionToolBag := rt.cfg.ToolBag
	agentReminderForPersona := ""
	// Coordinator iff session is pinned to "default" or has no
	// AgentID at all. Specialist personas (= anything else in the
	// registry) are workers, not coordinators.
	isCoordinator := sess.AgentID == nil || *sess.AgentID == "" || *sess.AgentID == "default"
	// viewer downgrade — drop every Class!=read tool out of the
	// toolbag before persona / control filtering. This applies even when
	// no persona is loaded, so the LLM never sees host_bash / AgentTool /
	// restart_service via the viewer's chat session.
	viewerOnly := req.Role == "viewer"
	if viewerOnly {
		sessionToolBag = filterToolsForAgentRole(rt.cfg.ToolBag, nil, isCoordinator, true)
	}
	// Resolve the active persona. Sessions with no AgentID still
	// route through the "default" persona — that's where the curated
	// coordinator toolbag lives. Without this lookup, a CreateSession
	// without an explicit agent_id would see the full toolbag and
	// the rule-1 dispatch policy becomes advisory again.
	personaName := "default"
	if sess.AgentID != nil && *sess.AgentID != "" {
		personaName = *sess.AgentID
	}
	if rt.cfg.AgentRegistry != nil {
		if persona, ok := rt.cfg.AgentRegistry.ByName(personaName); ok {
			if strings.TrimSpace(persona.SystemPrompt) != "" {
				basePrompt = persona.SystemPrompt
			}
			// Apply the persona's Tools whitelist even for the
			// "default" coordinator — so a curated coordinator
			// toolbag (AgentTool + a handful of triage tools) can
			// force dispatch instead of letting the LLM see and
			// reach for every deep-dive tool itself. The
			// coordinatorOnlyTools (AgentTool/SendMessage/TaskStop)
			// survive the strip via isCoordinator=true.
			sessionToolBag = filterToolsForAgentRole(rt.cfg.ToolBag, persona, isCoordinator, viewerOnly)
			agentReminderForPersona = persona.CriticalReminder
		} else if rt.log != nil && personaName != "default" {
			rt.log.Info("chatruntime: session agent_id not in registry — using default",
				slog.String("session_id", sess.ID),
				slog.String("agent_id", personaName))
		}
	}
	// Coordinator gets the redirect-stub overlay: same-name shadows
	// for hallucination-prone tool names that hand the LLM a
	// "dispatch to specialist-X via AgentTool" message instead of
	// crashing the graph with "tool not found in toolsNode". Stubs
	// must come AFTER filterToolsForAgent so they survive any name
	// collision with the (already-stripped) real tools.
	if isCoordinator && len(rt.cfg.CoordinatorStubs) > 0 {
		sessionToolBag = append(sessionToolBag, rt.cfg.CoordinatorStubs...)
	}
	// AgentID="default" is the virtual top-level persona — same wiring
	// as the no-agent coordinator (BasePrompt + full toolBag + agent
	// catalog), but the session keeps "default" so the SPA shows the
	// persona badge and the user can see / pick it on /agents.
	// 5b. Multi-agent catalog: when the session is the coordinator
	// (no specific persona pinned), append a markdown list of available
	// specialist personas so the LLM knows what subagent_type values
	// AgentTool will accept. Workers don't get the catalog (they can't
	// spawn nested workers — see coordinatorOnlyTools).
	if isCoordinator && rt.cfg.AgentRegistry != nil {
		if catalog := buildAgentCatalog(rt.cfg.AgentRegistry); catalog != "" {
			basePrompt = basePrompt + "\n\n" + catalog
		}
	}
	systemPrompt := ComposeSystemPrompt(basePrompt, activeSkills, nil)

	// 6. Build the eino history slice. Convert persisted rows into
	//    schema.Message so the graph assembler can replay them.
	einoHistory := buildEinoHistory(history)

	// 7. Build the graph (per-request — eino graphs are cheap to
	//    compose and the inner ReAct agent is rebuilt on every call
	//    today; an in-memory cache keyed on (toolBag identity, cfg)
	//    is a future optimisation).
	graphCfg := rt.cfg.GraphCfg
	// Persona-level MaxTurns override — already honoured for workers
	// via worker.go runWorker; mirror it here for the coordinator
	// path so the default persona can have a tighter cap. Without
	// this, the global 30-iteration cap lets a misbehaving
	// coordinator chain 120+ tool calls (we observed exactly this
	// pattern in the E2E eval before adding the override).
	if rt.cfg.AgentRegistry != nil {
		if persona, ok := rt.cfg.AgentRegistry.ByName(personaName); ok && persona.MaxTurns > 0 {
			graphCfg.MaxIterations = persona.MaxTurns
		}
	}
	g, err := graph.BuildReActGraph(rt.cfg.ChatModel, sessionToolBag, graphCfg)
	if err != nil {
		return nil, fmt.Errorf("chatruntime: build graph: %w", err)
	}

	// 8. Wire the per-request callback chain. Persistence's
	//    SessionID is filled in here; everything else lives on
	//    rt.cfg.CallbackDeps. The SSE handler is appended via the
	//    cutover layer's emitter when req.Emit != nil.
	deps := rt.cfg.CallbackDeps
	deps.AlertDraftGuard.UserText = req.UserText
	deps.Persistence.SessionID = sess.ID
	deps.Persistence.Model = strings.TrimSpace(req.Model)
	if deps.Persistence.Repo == nil {
		deps.Persistence.Repo = rt.cfg.Sessions
	}
	if req.Emit != nil {
		deps.SSE = rt.toCallbackEmitter(req.Emit, sess.ID)
	} else {
		deps.SSE = nil
	}
	handlers := callbacks.NewDefaultHandlers(deps)

	// 9. Compute per-turn dynamic hints from history + persona's
	//    critical_reminder (if a worker persona is active for this turn).
	// — these get inlined into the per-turn
	//    <system-reminder> block by graph.assembleMessages.
	dynamicHints := rt.calcDynamicHints(history)
	// AgentReminder is populated when the session is pinned to a
	// persona (Phase 2). — anti-drift reminder gets
	// inlined into the per-turn <system-reminder> block. Worker spawns
	// have always passed CriticalReminder; the coordinator path now
	// does too when sess.AgentID is set.
	agentReminder := agentReminderForPersona

	// 10. Invoke. The graph's outer Output carries the assistant
	//     message + usage; the persistence callback already wrote the
	//     assistant + tool rows by the time Invoke returns.
	//
	//     Per-request model selection (SPA model picker): thread
	//     req.Provider/req.Model into the ChatModel node as eino model
	//     options. RoutingChatModel.pick consumes WithProvider; the inner
	//     clientChatModel honours WithModel. Without this the graph kernel
	//     silently ignored the picker and always used the routing default
	//     — so a user who chose e.g. claude-opus still got the default
	//     model. When both are empty no option is added (default path
	//     unchanged).
	invokeOpts := []compose.Option{compose.WithCallbacks(handlers...)}
	if mopts := chatModelOpts(req); len(mopts) > 0 {
		invokeOpts = append(invokeOpts, compose.WithChatModelOption(mopts...))
	}
	invokeOpts = append(invokeOpts, compose.WithToolsNodeOption(
		compose.WithToolOption(graph.WithInvokeOpts(basetool.WithUserText(req.UserText))),
	))
	// Thread the UI locale onto ctx so AgentTool can pick it up and
	// forward it into the sub-agent's SpawnRequest. Without this, a
	// coordinator that handles an English question hands off to a
	// specialist that answers in zh (GLM default) — see
	// feedback_ai_output_locale.md regression 2026-06-02.
	ctx = basetool.WithLocale(ctx, req.Locale)
	// Same idea for the LLM choice: stamp the coordinator's resolved
	// provider+model so AgentTool can plumb them into the sub-agent's
	// SpawnRequest. runWorker reads them back as chatModelOpts.
	// Without this, sub-agents fall through to the routing default
	// ("openai"), and installs without an OpenAI key see specialists
	// fail with `provider "openai" not configured`.
	ctx = basetool.WithLLMChoice(ctx, req.Provider, req.Model)
	// Always autoheal any in-flight tool batch on the way out — covers
	// the "user closed browser mid-tool-batch" case the in-session
	// ChatModel.OnStart flush can't reach. Defer with a background-rooted
	// ctx so a cancelled request ctx doesn't block the stub inserts.
	defer func() {
		flushCtx := context.WithoutCancel(ctx)
		callbacks.FinalizeBatches(flushCtx, handlers)
	}()
	out, invokeErr := g.Invoke(ctx, &graph.Input{
		SystemPrompt:     systemPrompt,
		History:          einoHistory,
		UserText:         req.UserText,
		WebSearchEnabled: req.WebSearchEnabled,
		MentionsRendered: mentionsRendered,
		AgentReminder:    agentReminder,
		DynamicHints:     dynamicHints,
		Locale:           req.Locale,
	}, invokeOpts...)
	if invokeErr != nil {
		// Soft-fail graph-level errors: write an apology assistant
		// message, emit it like a normal turn, and emit Done. Keeps
		// SPA experience consistent with legacy agent.go's
		// MaxIterations apology path. Without this, errors like
		// eino's ErrExceededMaxSteps surface as "stream error" in
		// the user's chat instead of an actionable message.
		apology := buildGraphErrorApology(invokeErr)
		fallbackMsg := &aiopsmodel.Message{
			SessionID: sess.ID,
			Role:      aiopsmodel.RoleAssistant,
			Content:   &apology,
			CreatedAt: time.Now().UTC(),
		}
		// Best-effort persist; if it fails we still emit so the user
		// sees something instead of a stream error.
		if persistErr := rt.cfg.Sessions.AppendMessage(ctx, fallbackMsg); persistErr != nil && rt.log != nil {
			rt.log.Warn("chatruntime: persist apology failed",
				slog.String("err", persistErr.Error()))
		}
		emit(Event{Type: EventAssistant, Assistant: &AssistantEvent{
			MessageID: fallbackMsg.ID,
			Content:   apology,
			CreatedAt: fallbackMsg.CreatedAt,
		}})
		reply := &Reply{
			Message: fallbackMsg,
		}
		emit(Event{Type: EventDone, Done: reply})
		if rt.log != nil {
			rt.log.Warn("chatruntime: graph invoke failed (apology emitted)",
				slog.String("err", invokeErr.Error()))
		}
		return reply, nil
	}

	// 10. Translate Output → Reply. The graph's Output owns the
	//     assistant message and best-effort iterations + usage; the
	//     persistence handler has already written the chat_messages
	//     row, so we re-fetch the recently-persisted assistant message
	//     ID for handoff to the SSE adapter when needed.
	reply := &Reply{Usage: out.Usage, Iterations: out.Iterations}
	if out.AssistantMessage != nil {
		// Build a synthetic *aiopsmodel.Message so the upper layer
		// can re-render the legacy postMessageResp shape. The
		// canonical row written by PersistenceHandler lives in
		// chat_messages; we mirror its fields here for the wire DTO.
		content := out.AssistantMessage.Content
		if guard := callbacks.AlertDraftGuardFromHandlers(handlers); guard != nil {
			content = guard.SanitizeAssistantContent(content)
		}
		var contentPtr *string
		if content != "" {
			contentPtr = &content
		}
		reply.Message = &aiopsmodel.Message{
			SessionID: sess.ID,
			Role:      aiopsmodel.RoleAssistant,
			Content:   contentPtr,
			CreatedAt: time.Now().UTC(),
		}
	}

	// EventDone — keep parity with legacy agent.go's terminal
	// emission. PersistenceHandler may have already written the
	// assistant row; the legacy contract is "Done frame fires
	// exactly once at terminal success".
	emit(Event{Type: EventDone, Done: reply})
	return reply, nil
}

// toCallbackEmitter adapts a chatruntime.Emit into the
// callbacks.SSEEmitter the persistence/SSE chain consumes. The
// adapter normalises the new event names back to the legacy ones
// (SSEEventAssistantStart / SSEEventAssistantEnd → "assistant";
// SSEEventAssistantDelta dropped — see PR-9 spec note about
// IncludeDelta=false until the SPA catches up).
func (rt *Runtime) toCallbackEmitter(emit Emit, sessionID string) callbacks.SSEEmitter {
	return func(ev callbacks.SSEEvent) {
		switch ev.Type {
		case callbacks.SSEEventAssistantEnd:
			if ev.Assistant == nil {
				return
			}
			emit(Event{Type: EventAssistant, Assistant: &AssistantEvent{
				Iteration:        ev.Assistant.Iteration,
				MessageID:        ev.Assistant.MessageID,
				Content:          ev.Assistant.Content,
				CreatedAt:        ev.Assistant.CreatedAt,
				PendingToolCalls: ev.Assistant.PendingToolCalls,
			}})
		case callbacks.SSEEventAssistantStart:
			// Legacy frontend doesn't have an "assistant_start"
			// frame; suppress it so the SPA doesn't see a phantom
			// empty assistant bubble. The assistant_end frame
			// (above) carries the full content + pending tool
			// count which matches the legacy "assistant" frame.
			return
		case callbacks.SSEEventAssistantDelta:
			// Token-level streaming is gated behind a feature flag
			// pending SPA support. Drop for now — see PR-9 spec.
			return
		case callbacks.SSEEventToolStart:
			if ev.Tool == nil {
				return
			}
			emit(Event{Type: EventToolStart, Tool: &ToolEvent{
				ToolCallID: ev.Tool.ToolCallID,
				Name:       ev.Tool.Name,
				ArgsJSON:   ev.Tool.ArgsJSON,
				Status:     ev.Tool.Status,
				StartedAt:  ev.Tool.StartedAt,
			}})
		case callbacks.SSEEventToolEnd:
			if ev.Tool == nil {
				return
			}
			emit(Event{Type: EventToolEnd, Tool: &ToolEvent{
				ToolCallID: ev.Tool.ToolCallID,
				Name:       ev.Tool.Name,
				ArgsJSON:   ev.Tool.ArgsJSON,
				Status:     ev.Tool.Status,
				StartedAt:  ev.Tool.StartedAt,
				EndedAt:    ev.Tool.EndedAt,
				DurationMs: ev.Tool.DurationMs,
				Error:      ev.Tool.Error,
				ResultJSON: ev.Tool.ResultJSON,
			}})
		case callbacks.SSEEventDone:
			// Done is emitted by Handle directly so the chatruntime
			// caller sees a single, well-typed Done event with the
			// full Reply. The graph's terminal "done" callback fires
			// without a Reply payload, which is unhelpful for the
			// adapter. Drop the empty version to avoid double-fire.
			return
		case callbacks.SSEEventError:
			msg := ""
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			emit(Event{Type: EventError, Error: msg})
		}
	}
}

// buildEinoHistory translates persisted aiopsmodel.Message rows into
// schema.Message.
//
// Tool-call replay: tool-only assistant rows (Content=NULL, with hydrated
// ToolCalls) are emitted as {role:assistant, content:"", tool_calls:[...]}
// so the following role=tool messages remain paired. Strict providers
// (DeepSeek v4+) reject orphan tool messages with HTTP 400; OpenAI silently
// tolerated them. See toolreplay.Resolve for the LLM-call-id recovery
// rules (post-fix llm_call_id column + back-compat pair-by-order).
//
// The graph assembler appends the user turn separately (from Input.UserText);
// to avoid the LLM seeing the same turn twice we strip a trailing role=user
// row from the persisted history.
func buildEinoHistory(rows []*aiopsmodel.Message) []*schema.Message {
	if len(rows) == 0 {
		return nil
	}
	end := len(rows)
	if end > 0 && rows[end-1].Role == aiopsmodel.RoleUser {
		end--
	}
	// Resolve against the WHOLE history (not just rows[:end]) so the
	// trailing-user trim doesn't accidentally break pair-by-order
	// resolution for an assistant just before the trimmed user.
	callIDs := toolreplay.Resolve(rows)
	// Index role=tool rows by tool_call_id so an assistant with tool_calls
	// can hoist its responses to the immediate-next position regardless
	// of the persisted created_at order. Without this, long-running
	// AgentTool sub-agent spawns leave their parent assistant's tool_call
	// slot dangling — strict providers (DeepSeek v4+) reject with
	// HTTP 400 "insufficient tool messages following tool_calls message".
	toolIdx := toolreplay.IndexToolMessagesByCallID(rows)
	skipTool := make(map[int]bool)
	out := make([]*schema.Message, 0, end)

	// emitToolByCallID looks up the tool row matching callID in toolIdx,
	// appends it to out (as schema.Message), and marks it skipped so the
	// natural-order iterator below doesn't re-emit it.
	emitToolByCallID := func(callID string) {
		j, ok := toolIdx[callID]
		if !ok || j >= end {
			// Tool result wasn't persisted (in flight) or sits in the
			// trimmed trailing user range. Skip silently — the assistant
			// turn that emitted this tool_call may need to be dropped
			// upstream; here we simply omit this slot.
			return
		}
		tm := rows[j]
		content := ""
		if tm.Content != nil {
			content = sanitizeToolReplayContent(*tm.Content)
		}
		tcID := ""
		if tm.ToolCallID != nil {
			tcID = *tm.ToolCallID
		}
		tname := ""
		if tm.ToolName != nil {
			tname = *tm.ToolName
		}
		out = append(out, &schema.Message{
			Role:       schema.RoleType(aiopsmodel.RoleTool),
			Content:    content,
			ToolCallID: tcID,
			ToolName:   tname,
		})
		skipTool[j] = true
	}
	for i := 0; i < end; i++ {
		m := rows[i]
		switch m.Role {
		case aiopsmodel.RoleUser, aiopsmodel.RoleSystem:
			if m.Content == nil {
				continue
			}
			out = append(out, &schema.Message{
				Role:    schema.RoleType(m.Role),
				Content: *m.Content,
			})
		case aiopsmodel.RoleAssistant:
			calls, ok := callIDs[m.ID]
			if len(m.ToolCalls) > 0 && !ok {
				toolreplay.MarkDependentToolsForSkip(rows, i, len(m.ToolCalls), skipTool)
				continue
			}
			// Precheck: every tool_call we'd emit must have a hoistable
			// response inside the current window. If any is missing
			// (parallel ToolsNode dropped an OnEnd → chat_tool_calls
			// row written but no role=tool chat_messages row), drop the
			// whole assistant turn + its dependent tools rather than
			// send an envelope strict providers reject with HTTP 400
			// "insufficient tool messages following tool_calls".
			if ok && len(calls) > 0 {
				complete := true
				for _, tc := range calls {
					if j, found := toolIdx[tc.ID]; !found || j >= end {
						complete = false
						break
					}
				}
				if !complete {
					toolreplay.MarkDependentToolsForSkip(rows, i, len(calls), skipTool)
					continue
				}
			}
			content := ""
			if m.Content != nil {
				content = *m.Content
			}
			msg := &schema.Message{
				Role:    schema.RoleType(m.Role),
				Content: content,
			}
			if ok {
				msg.ToolCalls = make([]schema.ToolCall, 0, len(calls))
				for _, tc := range calls {
					msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: schema.FunctionCall{
							Name:      tc.Name,
							Arguments: tc.ArgsJSON,
						},
					})
				}
			}
			if content == "" && len(msg.ToolCalls) == 0 {
				// Polluted-data case: assistant with no replayable
				// signal AND no hydrated tool_calls. Drop assistant
				// + dependent tool rows so the LLM never sees orphans.
				toolreplay.MarkAllFollowingToolsForSkip(rows, i, skipTool)
				continue
			}
			out = append(out, msg)
			// HOIST: for every tool_call in this assistant's slot,
			// look up its response by tool_call_id and emit it RIGHT
			// HERE so the LLM API's "tool messages must immediately
			// follow tool_calls" invariant holds — even when the
			// natural created_at order interleaves another assistant
			// (e.g. long-running AgentTool finishing after later
			// short tools).
			for _, tc := range msg.ToolCalls {
				emitToolByCallID(tc.ID)
			}
		case aiopsmodel.RoleTool:
			// Tool messages are emitted ONLY via hoisting (emitToolByCallID,
			// right after their assistant's tool_calls — which sets
			// skipTool for that row). A role=tool row reaching this branch
			// un-hoisted is an ORPHAN: its tool_call_id matches no preceding
			// assistant tool_call. That happens when an out-of-order
			// parallel completion persisted the response under a synthetic
			// "<name>|einoToolAdapter" id (the autoheal stub then filled the
			// real id slot, so the assistant turn survived the precheck and
			// this real response is left dangling). Emitting it bare yields
			// the provider 400 "Messages with role 'tool' must be a response
			// to a preceding message with 'tool_calls'". Drop it — the
			// assistant's slot is already satisfied by the hoisted row.
			continue
		}
	}
	return out
}

const (
	toolBudgetExceededStatus       = "call_budget_exceeded"
	toolBudgetExpiredPreviousTurn  = "expired_previous_turn"
	toolBudgetExpiredReplayMessage = "This per-turn tool budget result belonged to a previous user turn and has expired. The tool may be called again in the current user turn if needed."
)

type toolBudgetReplayEnvelope struct {
	Status      string `json:"status"`
	Scope       string `json:"scope,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Calls       int    `json:"calls,omitempty"`
	Instruction string `json:"instruction"`
}

func sanitizeToolReplayContent(content string) string {
	var env toolBudgetReplayEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return content
	}
	if env.Status != toolBudgetExceededStatus {
		return content
	}
	env.Scope = toolBudgetExpiredPreviousTurn
	env.Instruction = toolBudgetExpiredReplayMessage
	b, err := json.Marshal(env)
	if err != nil {
		return content
	}
	return string(b)
}

// chatModelOpts turns the per-request model selection (SPA picker) into eino
// model options for the graph's ChatModel node. Empty fields add no option,
// so the routing default applies (unchanged default path). Provider routes
// via RoutingChatModel.pick (WithProvider); model name is honoured by the
// inner clientChatModel (WithModel).
func chatModelOpts(req *Request) []model.Option {
	if req == nil {
		return nil
	}
	var opts []model.Option
	if p := strings.TrimSpace(req.Provider); p != "" {
		opts = append(opts, llm.WithProvider(p))
	}
	if m := strings.TrimSpace(req.Model); m != "" {
		opts = append(opts, model.WithModel(m))
	}
	return opts
}

// calcDynamicHints produces the per-turn hint bullets that get injected
// into the <system-reminder> block. Pure: depends only
// on the persisted history rows; no LLM, no I/O. Returns nil when no
// hint applies for this turn.
//
// Current heuristics (kept intentionally narrow — false positives on a
// reminder we re-show every turn corrode the LLM's trust in the block):
//
//	(a) consecutive same-tool failures ≥ 2 → "switch tools or ask user"
//	(b) repeated (tool, args) call ≥ 3 times → "stop repeating the same call"
func (rt *Runtime) calcDynamicHints(history []*aiopsmodel.Message) []string {
	if rt == nil {
		return nil
	}
	var hints []string
	if name, n := consecutiveFailedTool(history, 2); n >= 2 {
		hints = append(hints, fmt.Sprintf("%s 已连续失败 %d 次：换工具，或问用户澄清", name, n))
	}
	// Repeat-call detection — same tool with similar args ≥ 3 times in
	// the trailing window. This was the dominant failure mode in the
	// "self-loop diagnose" 30-iter loop: query_promql kept getting called
	// with slightly different metric names but no narrative progress.
	if name, args, n := repeatedToolCall(history, 3); n >= 3 {
		hints = append(hints, fmt.Sprintf("%s 已重复调用 %d 次（args: %s）：从你已有的数据下结论，不要再调用同款工具", name, n, args))
	}
	if alertDraftGuardNeedsDraftRetry(history) {
		hints = append(hints, "上一轮告警草案被安全闸门拦截：当前用户消息仍在继续创建告警时，直接调用 draft_config_change 生成 config_draft/draft_hash；metric 类规则需要先用 list_metric_catalog 获取当前指标。不要要求用户重新发送，不要输出文字草案，不要仅解释流程")
	}
	// "Promise-without-execution" detection — see ongridBasePrompt()
	// 中等档位的 LLM (e.g. glm-4-plus) 偶尔写 "让我..." / "我先..." 这种
	// 计划句但没真发出 tool_call，导致 graph 提前 END，用户体验是消息
	// 戛然而止。这条 hint 在下一轮把 LLM 重新拽回来 — 但只在用户给了
	// 续聊指令（如 "继续"）的时候触发，避免在用户主动终止对话时打扰。
	if excerpt, found := detectUnfollowedPromise(history); found {
		hints = append(hints, fmt.Sprintf(`上一轮你说"%s"但没真发 tool_call。如要继续探索请直接发 tool_call，不要再写计划句；如已有结论请直接给用户答复`, excerpt))
	}
	return hints
}

// repeatedToolCall walks the previous user turn's tool messages looking for the
// most frequent tool. Returns (name, argsExcerpt, n) where n is occurrence
// count. Args excerpt = first 80 bytes of the latest call's args. n < minN is
// reported as 0.
func repeatedToolCall(history []*aiopsmodel.Message, minN int) (string, string, int) {
	if minN <= 0 {
		minN = 3
	}
	start, end := previousUserTurnBounds(history)
	counts := map[string]int{}
	args := map[string]string{}
	for i := end - 1; i >= start; i-- {
		m := history[i]
		if m == nil || m.Role != aiopsmodel.RoleTool {
			continue
		}
		name := ""
		if m.ToolName != nil {
			name = *m.ToolName
		}
		if name == "" {
			continue
		}
		counts[name]++
		// snapshot args for highest-count latest occurrence
		if _, ok := args[name]; !ok && m.Content != nil {
			c := *m.Content
			if len(c) > 80 {
				c = c[:80]
			}
			args[name] = c
		}
	}
	var topName string
	var topN int
	for name, n := range counts {
		if n > topN {
			topName = name
			topN = n
		}
	}
	if topN < minN {
		return "", "", 0
	}
	return topName, args[topName], topN
}

func previousUserTurnBounds(history []*aiopsmodel.Message) (int, int) {
	if len(history) == 0 {
		return 0, 0
	}
	currentUserIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m != nil && m.Role == aiopsmodel.RoleUser {
			currentUserIdx = i
			break
		}
	}
	if currentUserIdx < 0 {
		return 0, len(history)
	}
	prevUserIdx := -1
	for i := currentUserIdx - 1; i >= 0; i-- {
		m := history[i]
		if m != nil && m.Role == aiopsmodel.RoleUser {
			prevUserIdx = i
			break
		}
	}
	if prevUserIdx < 0 {
		return 0, currentUserIdx
	}
	return prevUserIdx + 1, currentUserIdx
}

// consecutiveFailedTool walks history backwards looking at the trailing
// run of tool messages. Returns (toolName, n) where n is how many of the
// most-recent tool turns were the same tool AND each one's persisted
// content carries a failure marker. n < minN is reported as 0.
//
// Failure marker (deliberately simple): the persisted Content JSON
// contains the substring `"error"` — every tool decorator (PR-3) writes
// `{"error":"..."}` on tool failure (timeout, panic, returned error),
// and successful payloads do not happen to ship an `"error"` field. We
// keep the parse loose so a future tool that reports partial failures
// inside a richer JSON still gets caught.
//
// Boundary cases this honours:
//   - the trailing tool block must be unbroken — interleaved
//     user/assistant turns reset the counter (n = 0)
//   - if the trailing tool block has mixed tool names, only the
//     most-recent tool's run is counted (we walk backwards; on a
//     name-mismatch we stop)
//   - a single failure (n == 1) returns ("", 0) when minN == 2 — we do
//     NOT want to nag on every transient error
func consecutiveFailedTool(history []*aiopsmodel.Message, minN int) (string, int) {
	if minN <= 0 {
		minN = 2
	}
	name := ""
	n := 0
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m == nil {
			continue
		}
		if m.Role != aiopsmodel.RoleTool {
			break
		}
		// Tool message — extract name + check failure.
		curName := ""
		if m.ToolName != nil {
			curName = *m.ToolName
		}
		if curName == "" {
			break
		}
		if name == "" {
			name = curName
		} else if curName != name {
			// different tool — stop counting; we only flag a single
			// stuck-loop tool, not a pattern across multiple tools.
			break
		}
		content := ""
		if m.Content != nil {
			content = *m.Content
		}
		if !looksLikeToolFailure(content) {
			break
		}
		n++
	}
	if n < minN {
		return "", 0
	}
	return name, n
}

func alertDraftGuardNeedsDraftRetry(history []*aiopsmodel.Message) bool {
	currentIdx, currentUser := latestUserMessage(history)
	if currentIdx < 0 || strings.TrimSpace(currentUser) == "" {
		return false
	}
	if !callbacks.LooksLikeAlertRuleCreationText(currentUser) && !looksLikeAlertDraftContinuation(currentUser) {
		return false
	}
	for i := currentIdx - 1; i >= 0; i-- {
		m := history[i]
		if m == nil {
			continue
		}
		switch m.Role {
		case aiopsmodel.RoleAssistant:
			if m.Content == nil {
				return false
			}
			return callbacks.LooksLikeAlertDraftGuardBlockedMessage(*m.Content)
		case aiopsmodel.RoleUser:
			return false
		}
	}
	return false
}

func latestUserMessage(history []*aiopsmodel.Message) (int, string) {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m == nil || m.Role != aiopsmodel.RoleUser || m.Content == nil {
			continue
		}
		return i, *m.Content
	}
	return -1, ""
}

func looksLikeAlertDraftContinuation(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(trimmed, "为什么") ||
		strings.Contains(trimmed, "原因") ||
		strings.Contains(lower, "why") {
		return false
	}
	return containsAnyString(trimmed, lower, []string{
		"继续", "继续创建", "直接创建", "生成草案", "生成 draft", "创建 draft", "draft_config_change",
		"continue", "create it", "draft it", "generate draft",
	})
}

func containsAnyString(text, lowerText string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(lowerText, needle) || strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// looksLikeToolFailure is the heuristic used by consecutiveFailedTool.
// True when the persisted tool message Content carries an "error" JSON
// key — the decorator chain's standard failure shape.
func looksLikeToolFailure(content string) bool {
	if content == "" {
		return false
	}
	// Cheap substring scan — JSON validity is enforced by the
	// decorator path, and we only need a yes/no signal here.
	return strings.Contains(content, `"error"`)
}

// promiseMarkers are the substrings that indicate the assistant wrote a
// plan句 ("let me...") but did not actually emit a tool_call. Detected
// in the most recent assistant message; if matched + no tool message
// follows before the next user, calcDynamicHints injects a nudge for
// the next turn (see ongridBasePrompt).
var promiseMarkers = []string{
	"让我", "我先", "我来", "我会", "我将", "接下来", "首先", "先让我", "让我先",
}

// detectUnfollowedPromise walks history backwards looking for the last
// assistant message. Returns (excerpt, true) when:
//
//  1. that message contains any promiseMarker token, AND
//  2. no role=tool message follows it before the next role=user message
//     (or before end of history).
//
// Returns ("", false) otherwise. The excerpt is a 30-rune prefix of the
// matched assistant content for inclusion in the hint.
func detectUnfollowedPromise(history []*aiopsmodel.Message) (string, bool) {
	if len(history) == 0 {
		return "", false
	}
	// Find last assistant message.
	asstIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == aiopsmodel.RoleAssistant {
			asstIdx = i
			break
		}
	}
	if asstIdx < 0 || history[asstIdx].Content == nil {
		return "", false
	}
	content := strings.TrimSpace(*history[asstIdx].Content)
	if callbacks.LooksLikeAlertDraftGuardBlockedMessage(content) {
		return "", false
	}
	if content == "" || !containsAnyPromiseMarker(content) {
		return "", false
	}
	// Look forward from asstIdx+1: did a tool message follow before the
	// next user message?
	for j := asstIdx + 1; j < len(history); j++ {
		switch history[j].Role {
		case aiopsmodel.RoleTool:
			return "", false // promise WAS executed
		case aiopsmodel.RoleUser:
			// User came in before any tool ran — promise unfollowed.
			break
		}
	}
	// Build a clean excerpt: strip leading bullet/whitespace, take 30 runes.
	excerpt := content
	if r := []rune(excerpt); len(r) > 30 {
		excerpt = string(r[:30]) + "..."
	}
	return excerpt, true
}

func containsAnyPromiseMarker(content string) bool {
	for _, m := range promiseMarkers {
		if strings.Contains(content, m) {
			return true
		}
	}
	return false
}

// buildGraphErrorApology turns a graph-level error into a user-facing
// markdown apology, picking different wording per error class so the user
// (and downstream Ant audit) can tell what happened. Mirrors the apology
// style of legacy agent.go's MaxIterations fallback.
func buildGraphErrorApology(err error) string {
	if err == nil {
		return "抱歉，处理消息时遇到未知错误。请换个问法再问一次。"
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	// LLM hallucinated a tool name not in its actual schema (e.g.
	// model "remembers" `get_host_load` from training even though
	// only `query_devices / query_knowledge / ...` were presented).
	// Eino's tool router can't dispatch → whole graph aborts. Map
	// to a friendly retry message that nudges toward the right
	// flow (AgentTool dispatch).
	case strings.Contains(low, "not found in toolsnode"),
		strings.Contains(low, "tool") && strings.Contains(low, "not found"):
		return "这个问题需要的深度查询能力不在我（协调员）的工具范围内 — 那些工具都在专家手里。请直接告诉我你的具体诉求（例如「看 CPU/内存」「检查磁盘容量」「网络连通性」），我会派对应的 specialist 处理。"
	// Match both eino's wording shapes: "exceeds max steps" (current
	// runtime) and the legacy "exceeded max iterations" / "maxsteps"
	// variants. The bug we ate before: matcher said "exceeded" (past
	// tense) but eino emits "exceeds" (present tense), so every
	// max-steps abort dumped raw "[GraphRunError]…" to the user.
	case strings.Contains(low, "exceeds max"),
		strings.Contains(low, "exceeded max"),
		strings.Contains(low, "max steps"),
		strings.Contains(low, "maxstep"),
		strings.Contains(low, "max iterations"):
		return "我跑了很多轮工具调用还是没收敛 — 大概率是搜索路径走偏了。上面工具调用区已经有阶段性数据，可以直接看；也可以这样收敛：\n• 把问题缩小成「看 X 指标的 Y 现象」——给具体 metric / device 让我聚焦\n• 或者告诉我「已经看到 A / B / C，下一步从 D 这条线索查」\n• 也可以直接 @ 一个 specialist（如 @specialist-compute 看 CPU/内存）让专家接手"
	case strings.Contains(low, "context canceled"), strings.Contains(low, "context deadline"):
		return "本次请求超时或被取消。一般是上游 LLM / 设备响应慢。请稍后重试，或换简单的问法。"
	case strings.Contains(low, "budget"):
		return "今日 LLM 预算已用完。请联系 admin 调整配额或明天再试。"
	// History replay produced a tool_calls envelope that the upstream
	// provider rejected because one or more tool responses are missing
	// from chat_messages (eino ToolsNode dropped an OnEnd in parallel
	// execution; the chat_tool_calls row is there but the role=tool
	// chat_messages row is not). Distinct user message — this is a
	// dirty-session issue, not a provider or quota problem; new session
	// is the clean way out.
	case strings.Contains(low, "insufficient tool messages"),
		strings.Contains(low, "tool_calls must be followed"),
		strings.Contains(low, "tool messages responding"):
		return "本会话历史里有一轮工具调用的响应没落库（并行调用写入丢失），后续每条消息送上游都会被拒。请新开一个会话继续，这个会话先放一放——后端会再修。"
	// LLM provider quota / rate-limit / insufficient balance. Substring
	// covers OpenAI ("insufficient_quota"), Anthropic ("credit balance"),
	// Zhipu ("余额不足"), generic 429 wording. Operator-side issues, not
	// user-side. The `insufficient` substring is intentionally narrow —
	// the bare word also appears in protocol error strings like
	// "insufficient tool messages following tool_calls", which used to
	// land here and mislead the user into chasing a quota that was fine.
	case strings.Contains(low, "429"),
		strings.Contains(low, "too many requests"),
		strings.Contains(low, "余额不足"),
		strings.Contains(low, "insufficient_quota"),
		strings.Contains(low, "insufficient balance"),
		strings.Contains(low, "insufficient credit"),
		strings.Contains(low, "insufficient funds"),
		strings.Contains(low, "credit balance is too low"),
		strings.Contains(low, "rate limit"),
		strings.Contains(low, "quota"):
		return "LLM provider 当前不可用——可能是配额用尽 / 限流 / 余额不足。请到「设置 → 模型」检查 provider 的状态和余额；问题在配置层面，重试不会解决。"
	// Other LLM provider errors (auth failure, model not found, etc.)
	// land here so the user gets a hint to check provider config
	// instead of a 200-char raw API error dump.
	case strings.Contains(low, "llm: chat completion"),
		strings.Contains(low, "openai api"),
		strings.Contains(low, "api error"):
		return "LLM provider 报错（可能是 API key / 模型名 / 网络）。请到「设置 → 模型」检查 provider 配置；详细 raw 错误请看 manager 日志。"
	default:
		// 非已知 class：给一段保守的原文摘要，让用户能截图反馈。
		short := msg
		if len(short) > 200 {
			short = short[:200] + "..."
		}
		return "抱歉，处理消息时遇到错误：\n```\n" + short + "\n```\n请换个问法再试，或截图反馈给我们。"
	}
}
