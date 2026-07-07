package aiops

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MutatingProposal is one row per intercepted mutating tool_call. The
// ReviewGate decorator (manager/biz/aiops/tools/decorators/review_gate.go)
// inserts a row at intercept time, fires the reviewer worker, and updates
// the row with the reviewer's decision once the worker returns. The
// table is the **audit source of truth** for
// every mutating proposal — approved or rejected — leaves a row.
//
// Why a separate table instead of reusing chat_tool_calls:
//
//   - chat_tool_calls represents a tool **execution**; mutating
//     proposals can REJECT, in which case there is no execution row
//     (the tool never ran). Reject rows would otherwise need a
//     synthetic chat_tool_calls row, polluting the execution table.
//   - The reviewer's reasoning + gates_passed + missing_gates are
//     audit data the SPA approval UI needs to surface; cramming them
//     into chat_tool_calls.result_json would force a JSON parse on
//     every render.
//   - chat_tool_calls is per-message-scoped; mutating proposals are
//     per-session-scoped (SOP says "no parallel mutating in 5min" —
//     the reviewer queries this table to enforce).
//
// First version is intentionally narrow:
//   - id (UUID)
//   - session_id / message_id / tool_call_id — link back to the
//     coordinator chat that issued the proposal
//   - tool_name + args_json — captures WHAT was proposed
//   - reviewer_task_id — the worker id that did the review (links to
//     chatruntime.Worker.ID via the in-memory map, NOT a FK; the
//     workers table is in-memory only in PR-7)
//   - decision ("pending" / "approve" / "reject") + decision_reason
//   - decided_at — the reviewer's verdict
//   - operator_user_id — who triggered the proposal (the chat owner)
//   - approver_user_id — reserved for the SPA dual-sign UI follow-up;
//     the reviewer agent is a software approver, not a human
type MutatingProposal struct {
	ID string `gorm:"primaryKey;type:char(36);column:id"`

	// SessionID / MessageID / ToolCallID link back to the coordinator
	// chat that issued the proposal. SessionID is required (every
	// proposal originates from a chat session); MessageID + ToolCallID
	// are required when the chat persistence layer is wired but
	// fallback-empty when the decorator runs without a session
	// context (legacy / direct-invocation tests).
	SessionID  string  `gorm:"index;type:char(36);not null;column:session_id"`
	MessageID  *string `gorm:"index;type:char(36);column:message_id"`
	ToolCallID *string `gorm:"index;type:char(36);column:tool_call_id"`

	// ToolName + ArgsJSON record WHAT was proposed. Mirrors
	// chat_tool_calls.tool_name / arguments_json; the schema is
	// independent so the reviewer can audit the proposal even after
	// the chat row is purged.
	ToolName string `gorm:"size:64;not null;index:idx_chat_mutating_tool_created,priority:1;column:tool_name"`
	ArgsJSON string `gorm:"type:text;not null;column:args_json"`

	// ToolClass is "write" | "destructive" — the value ReviewGate
	// observed at intercept time. Recorded so an audit query can
	// re-derive the class without re-loading the tool registry.
	ToolClass string `gorm:"size:16;not null;column:tool_class"`

	// ReviewerAgent is the persona name the reviewer worker ran as
	// (frontmatter `name` field). Almost always "reviewer" today;
	// stored explicitly so future per-tool reviewers (e.g. a
	// db-specific reviewer for "drop_table") leave a clean trail.
	ReviewerAgent string `gorm:"size:64;not null;column:reviewer_agent"`

	// ReviewerTaskID is the chatruntime.Worker.ID of the spawn. Useful
	// for cross-referencing the worker's transcript in future PRs that
	// persist chat_sessions for sub-agents.
	ReviewerTaskID string `gorm:"size:64;not null;column:reviewer_task_id"`

	// Decision is the reviewer's verdict. Constants below.
	Decision string `gorm:"size:16;not null;default:pending;check:decision IN ('pending','approve','reject');index:idx_chat_mutating_decision_created,priority:1;column:decision"`

	// DecisionReason is the reviewer's rationale (the "Notes" / "Gates"
	// block from the reviewer.md output). Stored verbatim so SPA can
	// render markdown.
	DecisionReason *string `gorm:"type:text;column:decision_reason"`

	// OperatorUserID is the user_id of the chat owner who triggered
	// the proposal. Required when the call carries a user via the
	// InvokeOption WithUserID; 0 falls back to "anonymous" for tests.
	OperatorUserID uint64 `gorm:"index;not null;default:0;column:operator_user_id"`

	// ApproverUserID is reserved for the SPA dual-sign follow-up
	//Today the reviewer is a software approver only
	// — the column stays NULL until a human signs off.
	ApproverUserID *uint64 `gorm:"column:approver_user_id"`

	// CreatedAt is the intercept time (decorator entered Run);
	// DecidedAt is when the reviewer returned. Two columns so the
	// "reviewer round-trip duration" SLO can be computed without
	// joining other tables.
	CreatedAt  time.Time  `gorm:"index:idx_chat_mutating_tool_created,priority:2;index:idx_chat_mutating_decision_created,priority:2"`
	DecidedAt  *time.Time `gorm:"column:decided_at"`
	ExecutedAt *time.Time `gorm:"column:executed_at"`
}

// TableName pins the SQLite / MySQL table name.
func (MutatingProposal) TableName() string { return "chat_mutating_proposals" }

// BeforeCreate auto-fills ID with a fresh UUIDv4 when the caller didn't
// pre-set one. Keeps id-generation in one place.
func (p *MutatingProposal) BeforeCreate(*gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

// MutatingProposal Decision constants.
const (
	// DecisionPending — proposal recorded, reviewer not yet decided.
	DecisionPending = "pending"
	// DecisionApprove — reviewer said yes; tool dispatched.
	DecisionApprove = "approve"
	// DecisionReject — reviewer said no; tool NOT dispatched, error
	// surfaced to the coordinator chat.
	DecisionReject = "reject"
)
