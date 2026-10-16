// Package contract defines version-independent runtime inputs, results and events.
// Execution policy and mutable worker state remain inside each version.
package contract

import (
	"context"
	"time"

	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// Mention 是运行时的 mention 输入；Service 从 chatprotocol 转换。
type Mention struct {
	Type  string
	ID    string
	Label string
}

// MentionResolver hydrates each @-mention into one bullet line. nil disables inlining.
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
	// Approval carries the approval_pending payload — set when
	// Type == EventApprovalPending and a blocking tool (cloud_bash) has
	// queued a proposal and is awaiting the human decision (HLD-021).
	Approval *ApprovalPending
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
	Attachments      []aiopsmodel.Attachment
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
	// worker session. Empty = "work". The investigator usecase
	// sets "investigation" so auto-spawned RCA transcripts stay out of
	// the /chat list.
	SessionKind string
	// OwnerUserID overrides the persisted chat_sessions.user_id when
	// the worker has no ParentSession to inherit from. The investigator
	// usecase passes 0 to mark these rows as "system-owned" — they
	// don't belong to any operator and the internal audience prevents
	// them from appearing in the primary chat list.
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

// EventApprovalPending fires when a synchronous-blocking tool (HLD-021,
// e.g. cloud_bash) has queued a human-approval proposal and is about to
// block waiting for the decision. The frame surfaces the inline approve/
// reject card LIVE — the tool no longer returns a pending_approval result
// blob (it blocks, then returns the real executor result), so the card
// must be driven by this frame instead. ToolCallID ties the card to the
// tool call's existing streaming card so the UI shows a single card.
const EventApprovalPending EventType = "approval_pending"

// ApprovalPending is the EventApprovalPending payload. Emitted by the
// proposer shim (cmd/main.go) via EmitFromContext while a blocking tool
// awaits the human decision.
type ApprovalPending struct {
	ApprovalID  string
	ToolCallID  string
	Kind        string
	ToolName    string
	Command     string
	Credentials []string
}
