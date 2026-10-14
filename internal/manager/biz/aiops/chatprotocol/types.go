// Package chatprotocol 定义聊天入口共享的请求、回复和事件，不包含 Agent 执行逻辑。
package chatprotocol

import (
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"time"
)

// Reply is the terminal result returned to the caller.
type Reply struct {
	Message    *model.Message
	Usage      llm.Usage
	Iterations int
	ToolCalls  []*model.ToolCall
}

// EventType enumerates streaming events emitted during a run. Each event
// fires exactly once per logical phase, in the same order the agent makes
// progress, so the UI can render incrementally without polling.
type EventType string

const (
	// EventAssistant fires after every assistant turn is persisted. For
	// tool-calling turns Content is empty but PendingToolCalls is non-zero
	// — the UI should render placeholders for that many tool calls.
	EventAssistant EventType = "assistant"
	// EventToolStart fires after a tool_call row is persisted as pending,
	// before the tool actually runs.
	EventToolStart EventType = "tool_start"
	// EventToolEnd fires once the tool finishes (success/error/timeout).
	EventToolEnd EventType = "tool_end"
	// EventDone fires once at terminal success with the final Reply.
	EventDone EventType = "done"
	// EventTaskNotification carries completed background-worker events.
	EventTaskNotification EventType = "task_notification"
	// EventApprovalPending fires when a synchronous-blocking tool
	// (HLD-021, e.g. execute_k8s_action) queues a human-approval proposal and is
	// about to block on the decision. Carries the live approve/reject card
	// payload. The runtime adapter preserves the existing SSE frame.
	EventApprovalPending EventType = "approval_pending"
)

// Event is the streaming envelope. Exactly one of the optional pointer
// fields is set, depending on Type.
type Event struct {
	Type         EventType
	Assistant    *AssistantEvent
	Tool         *ToolEvent
	Done         *Reply
	Notification *TaskNotificationEvent
	Approval     *ApprovalPendingEvent
}

// ApprovalPendingEvent carries the approval_pending payload. Mirrors the
// chatruntime.ApprovalPending shape; translation happens in
// service/aiops/service.go::translateRuntimeEvent. Fields stay exported so
// the http SSE renderer can copy them into JSON without reaching into
// chatruntime.
type ApprovalPendingEvent struct {
	ApprovalID  string
	ToolCallID  string
	Kind        string
	ToolName    string
	Command     string
	Credentials []string
}

// TaskNotificationEvent carries the task_notification payload.
// Mirrors the chatruntime.TaskNotification shape — translation happens
// in service/aiops/service.go::translateRuntimeEvent. The fields are
// kept exported so the http SSE renderer can copy them into JSON
// without reaching into chatruntime.
type TaskNotificationEvent struct {
	TaskID  string
	Status  string
	Summary string
	Result  string
	Err     string
	Usage   map[string]any
}

// AssistantEvent describes one assistant turn's persisted row.
type AssistantEvent struct {
	Iteration        int
	MessageID        string
	Content          string
	CreatedAt        time.Time
	PendingToolCalls int
}

// ToolEvent describes one tool_call lifecycle event. EndedAt, Status,
// DurationMs and ResultJSON are only meaningful on EventToolEnd; ArgsJSON
// is set on EventToolStart so the UI can show what the agent asked for.
type ToolEvent struct {
	ToolCallID string
	Name       string
	DeviceID   *uint64
	Status     string // pending | success | error | timeout
	StartedAt  time.Time
	EndedAt    *time.Time
	DurationMs int64
	Error      string
	ArgsJSON   string
	ResultJSON string
}

// Emit is the streaming callback. nil is a no-op (used by Run, which is
// the blocking path); RunStream passes a real fn that fans events out
// over SSE/etc.
type Emit func(Event)

// Mention is the wire-shape reference the agent receives from the chat
// send endpoint. Mirrors mentions.Mention; declared locally so this
// package doesn't import biz/aiops/mentions (which itself imports
// biz/device + biz/alert and would form a tight coupling chain).
type Mention struct {
	Type  string
	ID    string
	Label string
}

// RunOptions bundles per-call overrides — provider/model selection,
// mentions to inline, etc. Empty fields fall back to the agent's
// configured defaults. Pass &RunOptions{} to keep behaviour identical
// to the no-options Run.
//
// WebSearchEnabled toggles whether the agent exposes the manager-scoped
// `web_search` skill to the LLM on this turn. Default false — the
// model will not gratuitously search the public web on every metric
// question. When the user clicks the SPA's globe toggle the SPA
// flips this to true, which makes the tool visible to the model AND
// permits invocation. (Filtering both the schema and the dispatch
// keeps the contract honest: a stale tool_call from history replay
// never silently slips through.)
type RunOptions struct {
	Provider         string
	Model            string
	Mentions         []Mention
	Attachments      []model.Attachment
	WebSearchEnabled bool
	// Locale is the UI language the reply should be in ("en-US"/"zh-CN").
	// Threaded to the graph kernel's prompt assembler. Empty = no directive.
	Locale string
}
