package aiops

import (
	"context"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// TokenSums is the aggregated token / request count returned by
// SessionRepo.SumTokensSince. NULL token columns count as zero. Requests is
// the count of role='assistant' messages in the window — it approximates the
// number of LLM round-trips.
type TokenSums struct {
	PromptTokens     int64
	CompletionTokens int64
	Requests         int64 // count of assistant messages
}

// SessionRepo persists chat sessions, messages, and tool-call rows.
// Implemented in internal/manager/data/aiops/store.
//
// All reads scoped to a single session return messages in ascending order by
// id (chronological). ListSessions returns sessions ordered by id DESC (most
// recent first).
type SessionRepo interface {
	// Sessions ------------------------------------------------------------
	CreateSession(ctx context.Context, s *model.Session) error
	GetSession(ctx context.Context, id string) (*model.Session, error)
	// ListSessions returns sessions for userID. When relatedIncidentID is
	// non-nil only sessions linked to that incident are returned (the
	// IncidentDetail agent-timeline panel uses this).
	ListSessions(ctx context.Context, userID uint64, limit, offset int, relatedIncidentID *uint64) ([]*model.Session, error)
	// ListByParent returns every session whose parent_session_id == parentID,
	// ordered by created_at ASC. Used by the SPA worker-tree view (a future
	// PR) and by audit queries that walk a coordinator → worker fan-out.
	// Returns an empty slice when parentID has no children — never nil.
	ListByParent(ctx context.Context, parentID string) ([]*model.Session, error)
	CloseSession(ctx context.Context, id string) error
	// RenameSession updates the session title; bumps updated_at.
	// Returns ErrNotFound when no row matches id.
	RenameSession(ctx context.Context, id string, title string) error
	// DeleteSession hard-deletes the session and every dependent row
	// (messages + tool calls). Used by the UI delete action.
	DeleteSession(ctx context.Context, id string) error

	// Messages ------------------------------------------------------------
	AppendMessage(ctx context.Context, m *model.Message) error
	// ListMessages returns up to limit recent messages for sessionID, ordered
	// by created_at ascending. A non-positive limit returns all messages.
	ListMessages(ctx context.Context, sessionID string, limit int) ([]*model.Message, error)

	// Tool calls ----------------------------------------------------------
	CreateToolCall(ctx context.Context, tc *model.ToolCall) error
	UpdateToolCallResult(
		ctx context.Context,
		id string,
		status string,
		resultJSON *string,
		errStr *string,
		endedAt time.Time,
	) error

	// Usage ---------------------------------------------------------------
	// SumTokensSince aggregates prompt_tokens / completion_tokens / request
	// count across all assistant messages with created_at >= since. NULL
	// token columns are treated as zero. The result is global (no user
	// scoping) — caller-side filtering is the right place to add scopes.
	SumTokensSince(ctx context.Context, since time.Time) (TokenSums, error)
}
