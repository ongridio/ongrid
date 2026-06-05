// worker.go implements — the coordinator/worker multi-agent
// path. The Runtime owns a small in-memory map of spawned workers; each
// worker is an independent graph.Invoke against a tool-bag filtered by
// the worker agent's persona (whitelist + disallowed_tools — black wins).
//
// Non-goals (first version):
//   - Worker nesting: a worker cannot spawn another worker. SpawnWorker
//     is exposed only on the Runtime, and AgentTool's wiring keeps it on
//     the coordinator.
//   - Cross-session memory sharing: each worker has its own logical
//     session id so its message history is opaque to its parent. The
//     parent → worker relationship is now persisted on chat_sessions
//     (agent_id / parent_session_id / background columns); the in-memory
//     map is still authoritative for live status until a worker tile UI
//     queries those columns directly.
//
// State machine (figure):
//
//	pending — set transiently between SpawnRequest accept and goroutine
//	           start (effectively never observable for sync spawns)
//	  ↓
//	running — goroutine has begun graph.Invoke; status flips here
//	           BEFORE the child graph emits its first event
//	  ↓
//	completed — graph.Invoke returned a non-nil AssistantMessage with
//	           no error; Result holds the final assistant content
//	failed — graph.Invoke returned err != nil OR the agent name was
//	            unknown / agent registry was nil
//	killed — StopWorker called while status was running; cancel()
//	            fires and the goroutine observes ctx.Done() then sets
//	            EndedAt + status=killed (without overwriting err)
//
// Background semantics — when SpawnRequest.Background is true SpawnWorker
// returns immediately with status=running and the graph.Invoke runs in a
// detached goroutine; the goroutine emits a "task_notification" SSE
// envelope back through req.ParentEmit upon terminal status. Callers
// (AgentTool) get the worker_id from the synchronous return and can
// answer the LLM with `{task_id, status: "pending"}` while the SPA
// renders a live tile that gets updated when the notification fires.
package chatruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph/callbacks"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// WorkerStatus enumerates the worker state machine. See package header
// for the transition diagram.
type WorkerStatus string

const (
	// WorkerStatusPending — accepted but not yet started. Observable
	// only in tests that intercept between SpawnWorker accept and goroutine
	// scheduling; in production this state is essentially transient.
	WorkerStatusPending WorkerStatus = "pending"
	// WorkerStatusRunning — graph.Invoke is in flight.
	WorkerStatusRunning WorkerStatus = "running"
	// WorkerStatusCompleted — graph.Invoke returned a final assistant
	// message with no error.
	WorkerStatusCompleted WorkerStatus = "completed"
	// WorkerStatusFailed — graph.Invoke returned an error, or the agent
	// name was unknown.
	WorkerStatusFailed WorkerStatus = "failed"
	// WorkerStatusKilled — explicitly stopped via StopWorker.
	WorkerStatusKilled WorkerStatus = "killed"
)

// Worker tracks a spawned sub-agent. Unique by ID within a Runtime
// instance.
type Worker struct {
	// ID is "agent-<8 hex>" — unique per Runtime, stable for the
	// worker's lifetime.
	ID string
	// AgentName is the persona name (frontmatter `name` field).
	AgentName string
	// SessionID is the worker's own session id (distinct from the
	// coordinator's session). For PR-1 of this is a synthetic
	// in-memory id; the chat_sessions row + parent_session_id column
	// migration is follow-up.
	SessionID string
	// ParentSessionID is the coordinator session that requested the
	// spawn. Tracked here so a future audit query can rebuild the parent
	// → worker tree without a schema change.
	ParentSessionID string
	// Prompt is the initial user message handed to the worker.
	Prompt string
	// Status is the lifecycle state. Read-only outside the runtime;
	// callers should always pull through GetWorker for a snapshot.
	Status WorkerStatus
	// Background indicates whether this is a fire-and-forget spawn.
	Background bool
	// StartedAt is the wall time the worker entered "running".
	StartedAt time.Time
	// EndedAt is the wall time of any terminal transition. nil while
	// running.
	EndedAt *time.Time
	// Result is the worker's final assistant content (when terminal =
	// completed). Empty for failed / killed.
	Result string
	// Err is the failure reason (when terminal = failed / killed).
	Err string

	// Internal fields — not surfaced to callers via copies.
	cancel context.CancelFunc
	mu     sync.Mutex
}

// SpawnRequest describes a worker to spawn. See AgentTool.InvokableRun
// for the LLM-facing argument shape that produces this struct.
type SpawnRequest struct {
	// AgentName is the persona name (frontmatter `name`). Must be
	// resolvable through the Runtime's AgentRegistry.
	AgentName string
	// Prompt is the initial user message for the worker.
	Prompt string
	// Background = true: fire-and-forget; SpawnWorker returns immediately
	// with status=running. Background = false: SpawnWorker blocks until
	// the worker reaches a terminal state.
	Background bool
	// ParentSession is the coordinator's chat session id.
	ParentSession string
	// ParentEmit is the SSE emitter the coordinator runtime threads
	// through. Used to deliver the task_notification frame when the
	// worker (background=true) finishes. nil = no notification (sync
	// callers don't need it).
	ParentEmit Emit
	// SessionKind overrides the persisted chat_sessions.kind for the
	// worker session. Empty = "user" (default). The investigator usecase
	// sets "investigation" so auto-spawned RCA transcripts stay out of
	// the /chat list.
	SessionKind string
	// OwnerUserID overrides the persisted chat_sessions.user_id when
	// the worker has no ParentSession to inherit from. The investigator
	// usecase passes 0 to mark these rows as "system-owned" — they
	// don't belong to any operator and the /chat filter (which checks
	// user_id) won't surface them.
	OwnerUserID uint64
	// Locale is the UI language ("en", "zh-CN", ...) the worker's reply
	// should use. Threaded from the parent coordinator's request so a
	// sub-agent answers in the same language the user is typing in
	// (otherwise GLM defaults to zh on English questions). Empty = no
	// directive, back-compat with investigator/auto-spawned workers.
	Locale string
	// Provider + Model are the coordinator's resolved LLM choice. Threaded
	// into runWorker's g.Invoke as chatModelOpts so the sub-agent runs
	// against the same LLM the user picked for the coordinator — without
	// this, runWorker falls through to the RoutingChatModel default and
	// installs without an OpenAI key see specialists fail with
	// `provider "openai" not configured`. Empty = no override; the worker
	// uses whatever the routing default is.
	Provider string
	Model    string
}

// TaskNotification is the payload of a task_notification streaming
// frame. Mirrors the schema laid out in — coordinator's
// SSE listener fans this back through the legacy SSE envelope so the
// SPA only needs to learn one new event type.
type TaskNotification struct {
	TaskID  string         `json:"task_id"`
	Status  WorkerStatus   `json:"status"`
	Summary string         `json:"summary"`
	Result  string         `json:"result,omitempty"`
	Err     string         `json:"error,omitempty"`
	Usage   map[string]any `json:"usage,omitempty"`
}

// EventTaskNotification is the new streaming event type the runtime
// emits when a background worker reaches a terminal state. The SPA
// listens for this in addition to the existing assistant / tool /
// done frames.
const EventTaskNotification EventType = "task_notification"

// emitCtxKey is the unexported context key that threads the active
// per-request Emit through the graph layer down into AgentTool's
// InvokableRun via the WorkerSpawner shim. The shim reads it through
// EmitFromContext to populate SpawnRequest.ParentEmit so a background
// worker can fire its task_notification through the SSE channel that
// owns the user's chat session.
type emitCtxKeyT struct{}

var emitCtxKey = emitCtxKeyT{}

// withEmit returns ctx augmented with emit. Internal — Handle threads
// the request emitter in before invoking the graph.
func withEmit(ctx context.Context, emit Emit) context.Context {
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, emitCtxKey, emit)
}

// EmitFromContext retrieves the active streaming Emit from ctx, if any.
// Returns nil when no emitter was attached (e.g. blocking call without
// SSE). The wiring shim at the cmd/main.go boundary uses this to thread
// task_notification frames through the same SSE channel that owns the
// coordinator's chat session.
func EmitFromContext(ctx context.Context) Emit {
	v, _ := ctx.Value(emitCtxKey).(Emit)
	return v
}

// Locale ctx propagation lives in the tools package (next to AgentTool,
// the consumer). Runtime calls tools.WithLocale before g.Invoke; the
// tool reads it via tools.LocaleFromContext inside InvokableRun.

// SpawnWorker starts a worker. See package header for state machine.
//
// Background=false: blocks until the worker reaches completed / failed /
// killed; the returned *Worker has Result + Err filled.
//
// Background=true: returns immediately with a Worker whose Status =
// running; the graph runs in a detached goroutine and the terminal
// transition emits a task_notification through req.ParentEmit.
func (rt *Runtime) SpawnWorker(ctx context.Context, req SpawnRequest) (*Worker, error) {
	if rt == nil {
		return nil, errors.New("chatruntime: nil runtime")
	}
	if rt.cfg.ChatModel == nil {
		return nil, errors.New("chatruntime: ChatModel is required")
	}
	if rt.cfg.AgentRegistry == nil {
		return nil, fmt.Errorf("chatruntime: agent %q: registry not wired", req.AgentName)
	}
	agentDef, ok := rt.cfg.AgentRegistry.ByName(req.AgentName)
	if !ok || agentDef == nil {
		return nil, fmt.Errorf("chatruntime: agent %q not found", req.AgentName)
	}

	id := newWorkerID()
	sessID := newWorkerSessionID(id)

	// Persist the worker session row up-front so audit + worker-tree
	// queries can resolve the (parent → worker) relationship without
	// waiting for the worker to finish. We need the parent's UserID for
	// row-level scoping; missing parent → worker session is orphan-able
	// (still persisted, but parent_session_id=nil) so the LLM-driven
	// flow remains usable in tests / sync calls without a parent row.
	var ownerUserID uint64
	var parentRefForRow *string
	if req.ParentSession != "" && rt.cfg.Sessions != nil {
		parentSess, err := rt.cfg.Sessions.GetSession(ctx, req.ParentSession)
		if err == nil && parentSess != nil {
			ownerUserID = parentSess.UserID
			pid := req.ParentSession
			parentRefForRow = &pid
		}
		// On lookup failure we proceed without a parent ref so the
		// worker can still run; the audit row simply has parent_session_id
		// = nil. Logged at info — not fatal.
		if err != nil && rt.log != nil {
			rt.log.Info("chatruntime: worker spawn — parent session lookup failed",
				"parent_session_id", req.ParentSession,
				"err", err.Error())
		}
	}
	if rt.cfg.Sessions != nil {
		agentName := agentDef.Name
		// Optional caller-supplied overrides: SessionKind labels the
		// transcript so /chat list can filter; OwnerUserID is used
		// when there's no ParentSession to inherit from (e.g. the
		// alert investigator spawns system-owned workers).
		kind := aiopsmodel.SessionKindUser
		if req.SessionKind != "" {
			kind = req.SessionKind
		}
		effectiveOwner := ownerUserID
		if effectiveOwner == 0 && req.OwnerUserID != 0 {
			effectiveOwner = req.OwnerUserID
		}
		row := &aiopsmodel.Session{
			ID:              sessID,
			UserID:          effectiveOwner,
			Title:           fmt.Sprintf("Worker: %s", agentDef.Name),
			AgentID:         &agentName,
			ParentSessionID: parentRefForRow,
			Background:      req.Background,
			Kind:            kind,
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		}
		if err := rt.cfg.Sessions.CreateSession(ctx, row); err != nil {
			return nil, fmt.Errorf("chatruntime: worker session create: %w", err)
		}
	}

	// Background spawn always derives from the long-lived runtime context
	// so a finishing HTTP request can't tear down the worker mid-run.
	// Sync spawn inherits the caller's ctx so a cancel propagates.
	parent := ctx
	if req.Background {
		parent = context.Background()
	}
	workerCtx, cancel := context.WithCancel(parent)

	w := &Worker{
		ID:              id,
		AgentName:       agentDef.Name,
		SessionID:       sessID,
		ParentSessionID: req.ParentSession,
		Prompt:          req.Prompt,
		Status:          WorkerStatusPending,
		Background:      req.Background,
		cancel:          cancel,
	}

	rt.workersMu.Lock()
	if rt.workers == nil {
		rt.workers = map[string]*Worker{}
	}
	rt.workers[id] = w
	rt.workersMu.Unlock()

	run := func() {
		// Close the session row when the goroutine terminates by any
		// path (normal completion, error, kill, panic). Without this
		// the chat_sessions row stays closed_at=NULL forever and
		// every SpawnWorker call leaks one "looks-active" row — see
		// the orphan accumulation that hit 161 rows on the test env
		// before this fix landed. We use context.Background so the
		// close fires even when the caller's ctx is already cancelled
		// (which is the common case for the cancel-killed branch).
		defer func() {
			if rt.cfg.Sessions != nil && sessID != "" {
				if err := rt.cfg.Sessions.CloseSession(context.Background(), sessID); err != nil && rt.log != nil {
					rt.log.Warn("chatruntime: worker session close failed",
						"session_id", sessID, "err", err.Error())
				}
			}
		}()

		w.mu.Lock()
		w.Status = WorkerStatusRunning
		w.StartedAt = time.Now().UTC()
		w.mu.Unlock()

		// Stamp the coordinator's LLM choice onto workerCtx so runWorker
		// can read it back as chatModelOpts. Without this the sub-agent
		// falls through to the routing default ("openai") even though
		// the user picked something else (e.g. deepseek) for the
		// coordinator. SendToWorker's runWorker callsite uses ctx as-is
		// because SendTo inherits the coordinator's ctx directly.
		workerCtx = basetool.WithLLMChoice(workerCtx, req.Provider, req.Model)
		result, err := rt.runWorker(workerCtx, agentDef, sessID, req.Prompt, req.Locale)

		w.mu.Lock()
		end := time.Now().UTC()
		w.EndedAt = &end
		switch {
		case workerCtx.Err() != nil && w.Status == WorkerStatusRunning && err != nil:
			// Killed via cancel(); preserve the cancellation classification
			// over a stray invoke error.
			w.Status = WorkerStatusKilled
			w.Err = workerCtx.Err().Error()
		case err != nil:
			w.Status = WorkerStatusFailed
			w.Err = err.Error()
		default:
			w.Status = WorkerStatusCompleted
			w.Result = result
		}
		w.mu.Unlock()

		if req.Background && req.ParentEmit != nil {
			req.ParentEmit(rt.notificationFor(w))
		}
	}

	if req.Background {
		go run()
		return rt.snapshotWorker(w), nil
	}

	run()
	return rt.snapshotWorker(w), nil
}

// CountWorkersByStatus returns the live worker counts split by status.
// Used by the ADR-026 self-obs sampler ticker (cmd/ongrid/main.go) to
// keep ongrid_chatruntime_worker_sessions{status="running|pending"}
// fresh. Holds the runtime mutex briefly; safe at any interval.
func (rt *Runtime) CountWorkersByStatus() (running, pending int) {
	if rt == nil {
		return 0, 0
	}
	rt.workersMu.Lock()
	defer rt.workersMu.Unlock()
	for _, w := range rt.workers {
		if w == nil {
			continue
		}
		w.mu.Lock()
		switch w.Status {
		case WorkerStatusRunning:
			running++
		case WorkerStatusPending:
			pending++
		}
		w.mu.Unlock()
	}
	return
}

// SendToWorker continues a worker by appending a follow-up user message.
// The worker must be running or already in a terminal state — claude-code
// allows continuing a completed worker so a coordinator can refine the
// task without a fresh spawn (figure: SendMessageTool
// targets either state). For a running worker the message is queued
// until the worker yields; for a completed worker we re-run with the
// follow-up prompt.
//
// First version: we run the follow-up synchronously in the current
// goroutine (no queueing), and disallow continuing a killed worker —
// the LLM should spawn a new one in that case.
func (rt *Runtime) SendToWorker(ctx context.Context, workerID, message string) error {
	if rt == nil {
		return errors.New("chatruntime: nil runtime")
	}
	rt.workersMu.Lock()
	w := rt.workers[workerID]
	rt.workersMu.Unlock()
	if w == nil {
		return fmt.Errorf("chatruntime: worker %q not found", workerID)
	}
	w.mu.Lock()
	status := w.Status
	agentName := w.AgentName
	w.mu.Unlock()
	switch status {
	case WorkerStatusKilled:
		return fmt.Errorf("chatruntime: worker %q is killed; spawn a fresh one", workerID)
	case WorkerStatusFailed:
		return fmt.Errorf("chatruntime: worker %q is failed; spawn a fresh one", workerID)
	}
	if rt.cfg.AgentRegistry == nil {
		return errors.New("chatruntime: agent registry not wired")
	}
	agentDef, ok := rt.cfg.AgentRegistry.ByName(agentName)
	if !ok || agentDef == nil {
		return fmt.Errorf("chatruntime: agent %q vanished from registry", agentName)
	}

	w.mu.Lock()
	w.Status = WorkerStatusRunning
	sessID := w.SessionID
	w.mu.Unlock()

	// SendToWorker has no SpawnRequest; inherit locale from ctx
	// (chat coordinator threads it via basetool.WithLocale).
	result, err := rt.runWorker(ctx, agentDef, sessID, message, basetool.LocaleFromContext(ctx))

	w.mu.Lock()
	defer w.mu.Unlock()
	end := time.Now().UTC()
	w.EndedAt = &end
	if err != nil {
		w.Status = WorkerStatusFailed
		w.Err = err.Error()
		return err
	}
	w.Status = WorkerStatusCompleted
	w.Result = result
	return nil
}

// StopWorker cancels a running worker. Returns nil even when the worker
// is already terminal — stopping is idempotent.
func (rt *Runtime) StopWorker(_ context.Context, workerID string) error {
	if rt == nil {
		return errors.New("chatruntime: nil runtime")
	}
	rt.workersMu.Lock()
	w := rt.workers[workerID]
	rt.workersMu.Unlock()
	if w == nil {
		return fmt.Errorf("chatruntime: worker %q not found", workerID)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Status == WorkerStatusRunning || w.Status == WorkerStatusPending {
		if w.cancel != nil {
			w.cancel()
		}
		// The goroutine flips Status to killed when it observes ctx.Done().
		// For pending workers there's no goroutine yet; stamp here.
		if w.Status == WorkerStatusPending {
			w.Status = WorkerStatusKilled
			end := time.Now().UTC()
			w.EndedAt = &end
		}
	}
	return nil
}

// GetWorker returns a snapshot copy of the worker by id. The returned
// *Worker is safe to read without locks; callers must NOT mutate it.
// (false, nil) when not found.
func (rt *Runtime) GetWorker(workerID string) (*Worker, bool) {
	if rt == nil {
		return nil, false
	}
	rt.workersMu.Lock()
	w, ok := rt.workers[workerID]
	rt.workersMu.Unlock()
	if !ok {
		return nil, false
	}
	return rt.snapshotWorker(w), true
}

// runWorker is the inner spawn-and-invoke routine shared by SpawnWorker
// and SendToWorker. It builds a per-worker tool bag (whitelist + black-
// list filtered against the runtime's tool bag), composes the worker's
// system prompt, and drives one graph.Invoke against the agent's
// configured Model + MaxTurns.
//
// sessID is the worker's own chat_sessions row id. It is threaded into
// the persistence callback so worker assistant + tool messages are
// written into chat_messages under the worker's session (not the
// coordinator's). The user-role prompt is persisted up-front here,
// matching Handle()'s "user message lands on disk before the LLM call"
// invariant — same survival semantics on a graph crash.
func (rt *Runtime) runWorker(ctx context.Context, agentDef *Agent, sessID, userText, locale string) (string, error) {
	workerTools := filterToolsForAgent(rt.cfg.ToolBag, agentDef, false)

	systemPrompt := ComposeSystemPrompt(rt.cfg.BasePrompt, nil, agentDef)

	// KB-first prologue. Weak coordinator models (GLM-4 etc) don't
	// reliably follow "rule 0 — query_knowledge before any other
	// tool" written in the persona prompt; we observed specialists
	// jumping straight to host_du_summary / query_promql. So when
	// the persona declares query_knowledge in its Tools whitelist,
	// we call it ONCE here with the user task as the query and
	// prepend the top match (if score ≥ 0.6) to userText as
	// explicit context. The worker LLM then sees the KB material as
	// part of its input — no choice required.
	//
	// Cost: one extra qdrant query per worker spawn (~50ms typical),
	// saves an entire ReAct turn when the rule would have been
	// followed, and forces RAG-first behavior when it wouldn't have.
	if hasTool(workerTools, "query_knowledge") {
		if kbCtx := rt.prologueKBLookup(ctx, workerTools, userText); kbCtx != "" {
			userText = kbCtx + "\n\n---\n\n用户原始任务：\n" + userText
		}
	}

	cfg := rt.cfg.GraphCfg
	if agentDef.MaxTurns > 0 {
		cfg.MaxIterations = agentDef.MaxTurns
	}
	if agentDef.Model != "" {
		cfg.Model = agentDef.Model
	}

	g, err := graph.BuildReActGraph(rt.cfg.ChatModel, workerTools, cfg)
	if err != nil {
		return "", fmt.Errorf("chatruntime: build worker graph: %w", err)
	}

	// Persist the user-role prompt under the worker's session id. Same
	// invariant Handle() honours on the coordinator path — the user turn
	// lives on disk before the graph runs so a crash mid-invoke leaves
	// an auditable trace.
	var handlers []einocallbacks.Handler
	if rt.cfg.Sessions != nil && sessID != "" {
		content := userText
		userMsg := &aiopsmodel.Message{
			SessionID: sessID,
			Role:      aiopsmodel.RoleUser,
			Content:   &content,
			CreatedAt: time.Now().UTC(),
		}
		if err := rt.cfg.Sessions.AppendMessage(ctx, userMsg); err != nil {
			return "", fmt.Errorf("chatruntime: persist worker user msg: %w", err)
		}

		// Wire the per-worker persistence callback. Reuse the runtime's
		// CallbackDeps for cross-cutting context (logger, registerer)
		// but override Persistence.SessionID so writes land on the
		// worker row. SSE / Audit / Metrics inherit from the runtime
		// config (Audit + Metrics are session-id-aware via their own
		// deps; we keep the worker's session id flowing into them too).
		deps := rt.cfg.CallbackDeps
		deps.Persistence.SessionID = sessID
		deps.Persistence.Model = agentDef.Model
		if deps.Persistence.Repo == nil {
			deps.Persistence.Repo = rt.cfg.Sessions
		}
		// Workers don't stream to the parent SSE channel directly; their
		// terminal status fans back through req.ParentEmit's task_notification.
		deps.SSE = nil
		handlers = callbacks.NewDefaultHandlers(deps)
	}

	// Thread the coordinator's resolved LLM choice (stamped on ctx via
	// basetool.WithLLMChoice at the SpawnWorker boundary) onto the
	// graph's ChatModel as eino model options. Without these, the
	// RoutingChatModel falls through to its built-in default — which
	// is "openai" — and installs with no OpenAI key see the worker
	// fail with `provider "openai" not configured`. Empty fields add
	// no option, preserving back-compat with auto-spawn paths
	// (investigator) that don't carry a coordinator choice.
	invokeOpts := []compose.Option{compose.WithCallbacks(handlers...)}
	if mopts := workerChatModelOpts(ctx); len(mopts) > 0 {
		invokeOpts = append(invokeOpts, compose.WithChatModelOption(mopts...))
	}
	out, err := g.Invoke(ctx, &graph.Input{
		SystemPrompt: systemPrompt,
		History:      nil,
		UserText:     userText,
		Locale:       locale,
	}, invokeOpts...)
	if err != nil {
		return "", err
	}
	if out == nil || out.AssistantMessage == nil {
		return "", nil
	}
	return out.AssistantMessage.Content, nil
}

// notificationFor produces a task_notification Event for the given
// terminal worker. Field shape mirrors
func (rt *Runtime) notificationFor(w *Worker) Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	summary := fmt.Sprintf("Agent %s %s", w.AgentName, w.Status)
	usage := map[string]any{}
	if !w.StartedAt.IsZero() && w.EndedAt != nil {
		usage["duration_ms"] = w.EndedAt.Sub(w.StartedAt).Milliseconds()
	}
	notif := &TaskNotification{
		TaskID:  w.ID,
		Status:  w.Status,
		Summary: summary,
		Result:  w.Result,
		Err:     w.Err,
		Usage:   usage,
	}
	return Event{Type: EventTaskNotification, Notification: notif}
}

// coordinatorOnlyTools is the implicit blacklist applied to every worker
// in this first-version implementation: the control tools live on
// the coordinator only. Even if a worker persona accidentally whitelists
// "AgentTool" we still strip it here, so workers cannot spawn nested
// workers / send messages / stop tasks.
var coordinatorOnlyTools = map[string]bool{
	"AgentTool":   true,
	"SendMessage": true,
	"TaskStop":    true,
}

// filterToolsForAgent applies the agent persona's whitelist + blacklist
// against the runtime tool bag. Black wins :
//
//   - if Tools is empty, every tool is initially allowed (inherit from
//     policy); otherwise only tools whose Info().Name appears in the
//     whitelist are allowed.
//   - DisallowedTools is then subtracted. Pattern "*_skill" is a
//     suffix wildcard supported for ergonomic blacklists (
//     example).
//   - The control tools (AgentTool / SendMessage / TaskStop) are
//     stripped only for workers (isCoordinator=false) — workers cannot
//     spawn nested workers in this first version. Coordinator passes
//     isCoordinator=true so AgentTool stays visible even when the
//     coordinator persona's Tools whitelist is non-empty.
//   - viewerOnly==true drops every tool whose Info().Class is
//     not "read". Viewer chat is degraded to single-agent read-only,
//     which is fine — the coordinator can still answer with read tools
//     directly (no need to dispatch since there's nothing mutating to do).
func filterToolsForAgent(bag []basetool.BaseTool, agentDef *Agent, isCoordinator bool) []basetool.BaseTool {
	return filterToolsForAgentRole(bag, agentDef, isCoordinator, false)
}

func filterToolsForAgentRole(bag []basetool.BaseTool, agentDef *Agent, isCoordinator bool, viewerOnly bool) []basetool.BaseTool {
	whitelist := map[string]bool{}
	blacklist := []string(nil)
	if agentDef != nil {
		for _, n := range agentDef.Tools {
			whitelist[strings.TrimSpace(n)] = true
		}
		blacklist = agentDef.DisallowedTools
	}

	out := make([]basetool.BaseTool, 0, len(bag))
	for _, t := range bag {
		if t == nil {
			continue
		}
		info, err := t.Info(context.Background())
		if err != nil || info == nil {
			continue
		}
		// Viewer downgrade: drop any tool that is not Class="read".
		// Applies BEFORE persona / control-tool checks so that even the
		// coordinator chat for a viewer cannot dispatch (AgentTool is
		// Class="write") or mutate (host_restart_service, etc.).
		if viewerOnly && info.Class != "read" {
			continue
		}
		// Coordinator-only tools survive the strip when we ARE the
		// coordinator. They're always in scope for the coordinator
		// regardless of any persona whitelist — control plane.
		if coordinatorOnlyTools[info.Name] {
			if !isCoordinator {
				continue
			}
		} else if agentDef != nil && len(whitelist) > 0 && !whitelist[info.Name] {
			continue
		}
		if matchesAny(info.Name, blacklist) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// matchesAny returns true when name matches any pattern in patterns.
// Patterns: exact name, or "*<suffix>" / "<prefix>*" wildcard.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch {
		case p == name:
			return true
		case strings.HasPrefix(p, "*") && strings.HasSuffix(name, p[1:]):
			return true
		case strings.HasSuffix(p, "*") && strings.HasPrefix(name, p[:len(p)-1]):
			return true
		}
	}
	return false
}

// snapshotWorker returns a copy of w safe to publish to a caller. The
// internal lock + cancel func are not copied — the returned *Worker is
// strictly read-only.
func (rt *Runtime) snapshotWorker(w *Worker) *Worker {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := &Worker{
		ID:              w.ID,
		AgentName:       w.AgentName,
		SessionID:       w.SessionID,
		ParentSessionID: w.ParentSessionID,
		Prompt:          w.Prompt,
		Status:          w.Status,
		Background:      w.Background,
		StartedAt:       w.StartedAt,
		Result:          w.Result,
		Err:             w.Err,
	}
	if w.EndedAt != nil {
		t := *w.EndedAt
		cp.EndedAt = &t
	}
	return cp
}

// newWorkerID returns a fresh "agent-<8hex>" identifier. crypto/rand
// keeps collisions astronomically unlikely; callers don't need to retry.
func newWorkerID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is treated as fatal in the Go stdlib
		// design; fall back to a timestamp-derived id to avoid panic.
		return fmt.Sprintf("agent-%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "agent-" + hex.EncodeToString(b[:])
}

// newWorkerSessionID derives a session id from the worker id. Distinct
// from the parent session so worker chat history can be persisted
// independently. The "worker-<8hex>" shape mirrors the worker id's
// hex tail and stays under chat_sessions.id (char(36)) — short, prefix-
// searchable, and visually distinct from coordinator UUIDs.
func newWorkerSessionID(workerID string) string {
	// workerID is "agent-<8hex>"; rebrand the prefix to "worker-" so
	// the chat_sessions row id is self-describing in audit dumps.
	if strings.HasPrefix(workerID, "agent-") {
		return "worker-" + workerID[len("agent-"):]
	}
	return "worker-" + workerID
}

// hasTool reports whether `name` is present in the BaseTool slice. Used
// by runWorker to decide whether to run the KB prologue.
func hasTool(bag []basetool.BaseTool, name string) bool {
	for _, t := range bag {
		if t == nil {
			continue
		}
		info, err := t.Info(context.Background())
		if err != nil || info == nil {
			continue
		}
		if info.Name == name {
			return true
		}
	}
	return false
}

// prologueKBLookup runs `query_knowledge` once with the user's task as
// the natural-language query and, when the top hit has score ≥ 0.6,
// returns a formatted KB context block ready to prepend to userText.
//
// All failure modes (tool missing, decode error, no hits, low score)
// return "" — the caller falls back to the bare user prompt. This is
// deliberately silent: the LLM should never see a "KB lookup failed"
// message, just sometimes-richer context.
func (rt *Runtime) prologueKBLookup(ctx context.Context, bag []basetool.BaseTool, userText string) string {
	var kb basetool.BaseTool
	for _, t := range bag {
		if t == nil {
			continue
		}
		info, err := t.Info(context.Background())
		if err != nil || info == nil || info.Name != "query_knowledge" {
			continue
		}
		kb = t
		break
	}
	if kb == nil {
		return ""
	}
	// Schema across versions has used both "query" and "q"; send
	// "query" and let the tool ignore extras.
	args, _ := json.Marshal(map[string]any{
		"query":    userText,
		"top_k":    3,
		"min_score": 0.6,
	})
	out, err := kb.InvokableRun(ctx, string(args))
	if err != nil || out == "" {
		return ""
	}
	// Parse loosely — the response shape is {"items":[{"title":..,"preview":..,"score":..}, ...]}
	var parsed struct {
		Items []struct {
			Title   string  `json:"title"`
			Preview string  `json:"preview"`
			Path    string  `json:"path"`
			Score   float64 `json:"score"`
			URL     string  `json:"url"`
		} `json:"items"`
	}
	if jerr := json.Unmarshal([]byte(out), &parsed); jerr != nil {
		return ""
	}
	if len(parsed.Items) == 0 {
		return ""
	}
	top := parsed.Items[0]
	if top.Score < 0.6 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[KB 自动注入 · 协调员认为这是相关 playbook]\n")
	b.WriteString("Title: ")
	b.WriteString(top.Title)
	b.WriteString(fmt.Sprintf("  (score=%.2f)\n", top.Score))
	if top.Path != "" {
		b.WriteString("Path: ")
		b.WriteString(top.Path)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(top.Preview)
	if top.URL != "" {
		b.WriteString("\n\nSource: ")
		b.WriteString(top.URL)
	}
	b.WriteString("\n\n请基于这份 playbook 推进诊断；末尾在答案里标注 `（参考 KB: " + top.Title + "）`。如果 playbook 不适用，可以忽略并自由发挥。")
	return b.String()
}

// workerChatModelOpts mirrors chatModelOpts but reads the (provider,
// model) pair from ctx instead of a *Request, so SpawnWorker and
// SendToWorker (which has no Request) share the same plumbing.
// RoutingChatModel.pick consumes WithProvider; the inner
// clientChatModel honours WithModel. Empty fields add no option, so
// auto-spawn paths that don't carry a coordinator choice fall through
// to the routing default unchanged.
func workerChatModelOpts(ctx context.Context) []model.Option {
	var opts []model.Option
	if p := strings.TrimSpace(basetool.LLMProviderFromContext(ctx)); p != "" {
		opts = append(opts, llm.WithProvider(p))
	}
	if m := strings.TrimSpace(basetool.LLMModelFromContext(ctx)); m != "" {
		opts = append(opts, model.WithModel(m))
	}
	return opts
}

