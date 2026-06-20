package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// SessionRepo is the GORM-backed biz/aiops.SessionRepo.
type SessionRepo struct {
	db *gorm.DB
}

// NewSessionRepo constructs the repo around an opened *gorm.DB.
func NewSessionRepo(db *gorm.DB) *SessionRepo { return &SessionRepo{db: db} }

// NewBizRepo is the wire-ready constructor. cmd/ongrid binds this at
// assembly time to obtain a biz.SessionRepo without exposing the concrete
// type to the composition root.
func NewBizRepo(db *gorm.DB) biz.SessionRepo { return NewSessionRepo(db) }

var _ biz.SessionRepo = (*SessionRepo)(nil)

// CreateSession inserts s.
func (r *SessionRepo) CreateSession(ctx context.Context, s *model.Session) error {
	if s == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(s).Error
}

// GetSession returns the session by id.
func (r *SessionRepo) GetSession(ctx context.Context, id string) (*model.Session, error) {
	var s model.Session
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

// ListByParent returns every session row whose parent_session_id == parentID,
// ordered by created_at ASC (chronological spawn order). Used by the
// SPA worker-tree view and audit queries that walk a coordinator's
// fan-out. Empty slice (never nil) when parentID has no children.
func (r *SessionRepo) ListByParent(ctx context.Context, parentID string) ([]*model.Session, error) {
	out := make([]*model.Session, 0)
	if parentID == "" {
		return out, nil
	}
	tx := r.db.WithContext(ctx).
		Model(&model.Session{}).
		Where("parent_session_id = ?", parentID).
		Order("created_at ASC, id ASC")
	if err := tx.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListSessions returns sessions for userID ordered by id DESC. When
// relatedIncidentID is non-nil only sessions linked to that incident
// are returned — used by the IncidentDetail agent-timeline panel.
func (r *SessionRepo) ListSessions(ctx context.Context, userID uint64, limit, offset int, relatedIncidentID *uint64) ([]*model.Session, error) {
	// kind='user' only — investigation sessions are auto-spawned per
	// alert RCA and live in the same table for audit, but the chat-list
	// surface should never show them. The IncidentDetail page reads
	// investigation_reports directly, not this list.
	tx := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ?", userID).
		Where("kind = ?", model.SessionKindUser)
	if relatedIncidentID != nil {
		tx = tx.Where("related_incident_id = ?", *relatedIncidentID)
	}
	if limit > 0 {
		tx = tx.Limit(limit)
	}
	if offset > 0 {
		tx = tx.Offset(offset)
	}
	var out []*model.Session
	if err := tx.Order("created_at DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// CloseSession sets closed_at = now. Idempotent: re-closing a closed session
// is a no-op (the column is overwritten with a new timestamp, which is
// acceptable for the soft-close semantic we need).
func (r *SessionRepo) CloseSession(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Session{}).Where("id = ?", id).Update("closed_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// RenameSession updates the session title. Used by the Sidebar's
// inline-rename action. Bumps updated_at so the list re-orders to top
// (matches user expectation: editing brings it back to recent).
func (r *SessionRepo) RenameSession(ctx context.Context, id string, title string) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Session{}).Where("id = ?", id).
		Updates(map[string]any{"title": title, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// DeleteSession permanently removes the session and every row that
// hangs off it (messages, tool_calls). Used by the UI's "delete chat"
// action; soft-close (CloseSession) is kept for callers that want to
// preserve audit history.
//
// Implementation: a single transaction so a partial delete can't leave
// orphaned messages. We rely on chat_tool_calls(message_id) → chat_messages
// and chat_messages(session_id) → chat_sessions ordering; no FKs are
// declared in our schema, so cascade is manual.
func (r *SessionRepo) DeleteSession(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if res := tx.
			Where("message_id IN (?)", tx.Model(&model.Message{}).Select("id").Where("session_id = ?", id)).
			Delete(&model.ToolCall{}); res.Error != nil {
			return res.Error
		}
		if res := tx.Where("session_id = ?", id).Delete(&model.Message{}); res.Error != nil {
			return res.Error
		}
		res := tx.Where("id = ?", id).Delete(&model.Session{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}
		return nil
	})
}

// AppendMessage inserts m.
func (r *SessionRepo) AppendMessage(ctx context.Context, m *model.Message) error {
	if m == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(m).Error
}

// ListMessages returns messages for sessionID ordered by created_at ASC.
// A non-positive limit returns all rows. A positive limit returns the most
// recent N messages, still ordered ascending for history replay.
//
// Hydrates message.ToolCalls from chat_tool_calls in a single batched
// query keyed on assistant message IDs — without this, replay drops
// any assistant turn whose Content is NULL (tool-call-only turns) and
// the following role=tool messages become orphans, which strict LLM
// providers reject with HTTP 400 "tool must follow tool_calls".
func (r *SessionRepo) ListMessages(ctx context.Context, sessionID string, limit int) ([]*model.Message, error) {
	tx := r.db.WithContext(ctx).Model(&model.Message{}).Where("session_id = ?", sessionID).Order("created_at ASC, id ASC")
	if limit > 0 {
		tx = r.db.WithContext(ctx).
			Model(&model.Message{}).
			Where("session_id = ?", sessionID).
			Order("created_at DESC, id DESC").
			Limit(limit)
	}
	var out []*model.Message
	if err := tx.Find(&out).Error; err != nil {
		return nil, err
	}
	if limit > 0 {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	if err := r.hydrateToolCalls(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// hydrateToolCalls batches one SELECT against chat_tool_calls keyed on
// the assistant message ids in msgs, and attaches the rows to each
// message in-place. Order within a single assistant turn is preserved
// by sorting on (created_at, id) — the agent persists tool_calls one
// at a time so created_at is monotonic, and id is the stable tiebreak.
func (r *SessionRepo) hydrateToolCalls(ctx context.Context, msgs []*model.Message) error {
	assistantIDs := make([]string, 0, len(msgs))
	byID := make(map[string]*model.Message, len(msgs))
	for _, m := range msgs {
		if m.Role != model.RoleAssistant {
			continue
		}
		assistantIDs = append(assistantIDs, m.ID)
		byID[m.ID] = m
	}
	if len(assistantIDs) == 0 {
		return nil
	}
	var tcs []model.ToolCall
	if err := r.db.WithContext(ctx).
		Where("message_id IN ?", assistantIDs).
		Order("created_at ASC, id ASC").
		Find(&tcs).Error; err != nil {
		return err
	}
	for i := range tcs {
		tc := tcs[i]
		if m, ok := byID[tc.MessageID]; ok {
			m.ToolCalls = append(m.ToolCalls, tc)
		}
	}
	return nil
}

// CreateToolCall inserts tc.
func (r *SessionRepo) CreateToolCall(ctx context.Context, tc *model.ToolCall) error {
	if tc == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(tc).Error
}

// SumTokensSince aggregates prompt_tokens / completion_tokens / request
// count across all assistant messages with created_at >= since. NULL token
// columns count as zero (COALESCE). Implemented as a single raw query so
// the gorm chain stays cheap and the SQL is auditable.
func (r *SessionRepo) SumTokensSince(ctx context.Context, since time.Time) (biz.TokenSums, error) {
	var out biz.TokenSums
	const q = `
SELECT
  COALESCE(SUM(prompt_tokens), 0)     AS prompt_tokens,
  COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
  COUNT(*)                            AS requests
FROM chat_messages
WHERE role = ? AND created_at >= ?`
	if err := r.db.WithContext(ctx).Raw(q, model.RoleAssistant, since).Scan(&out).Error; err != nil {
		return biz.TokenSums{}, err
	}
	return out, nil
}

// UpdateToolCallResult fills status/result/error/ended_at for an existing
// pending tool-call row. status SHOULD be one of model.StatusSuccess /
// StatusError / StatusTimeout.
func (r *SessionRepo) UpdateToolCallResult(
	ctx context.Context,
	id string,
	status string,
	resultJSON *string,
	errStr *string,
	endedAt time.Time,
) error {
	updates := map[string]any{
		"status":      status,
		"ended_at":    endedAt,
		"result_json": resultJSON,
		"error":       errStr,
	}
	res := r.db.WithContext(ctx).Model(&model.ToolCall{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
