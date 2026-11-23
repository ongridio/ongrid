package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// ReadArchivedChat returns at most three passages from visible conversation text.
// Read the committed boundary on each call so the tool also works in the turn that saved it.
func (r *SessionRepo) ReadArchivedChat(ctx context.Context, sessionID string, userID uint64, id, term string, offset int) ([]model.ArchivedChatPassage, error) {
	if (id == "" && strings.TrimSpace(term) == "") || offset < 0 || offset > 100_000_000 || utf8.RuneCountInString(term) > 128 || (offset > 0 && id == "") {
		return nil, fmt.Errorf("supply a message_id or a literal search term; pagination requires a message_id")
	}
	tx, err := r.archivedMessages(ctx, sessionID, userID)
	result := []model.ArchivedChatPassage{}
	if err != nil || tx == nil {
		return result, err
	}
	db := r.db.WithContext(ctx)
	tx = tx.Where("role IN ? AND content IS NOT NULL", []string{"user", "assistant"})
	if id != "" {
		tx = tx.Where("id = ?", id)
	}
	// INSTR and SUBSTR use character positions on both SQLite and MySQL.
	if term != "" {
		tx = tx.Where("INSTR(content, ?) > 0", term)
	}
	if term != "" && offset == 0 {
		tx = tx.Select("id, role, content, CASE WHEN INSTR(content, ?) > 100 THEN INSTR(content, ?) - 101 ELSE 0 END AS passage_start", term, term)
	} else {
		tx = tx.Select("id, role, content, ? AS passage_start", offset)
	}
	source := tx.Order("created_at DESC, id DESC").Limit(3)
	if err := db.Table("(?) AS hits", source).Select("id, role, passage_start AS start, SUBSTR(content, passage_start + 1, 901) AS text").Scan(&result).Error; err != nil {
		return nil, err
	}
	for i := range result {
		p := &result[i]
		chars := []rune(p.Text)
		if len(chars) > 900 {
			p.Text = string(chars[:900])
			p.Next = p.Start + 900
		}
	}
	return result, nil
}

func (r *SessionRepo) archivedMessages(ctx context.Context, sessionID string, userID uint64) (*gorm.DB, error) {
	db := r.db.WithContext(ctx)
	var session model.Session
	if err := db.Select("context_checkpoint").Where("id = ? AND user_id = ?", sessionID, userID).Take(&session).Error; err != nil {
		return nil, err
	}
	cp := session.ContextCheckpoint
	if cp == nil {
		return nil, nil
	}
	return db.Model(&model.Message{}).Where("session_id = ? AND (created_at < ? OR (created_at = ? AND id < ?))", sessionID, cp.FromTime, cp.FromTime, cp.FromID), nil
}

// Page oldest first so expanding the archive does not shift existing pages.
func (r *SessionRepo) ArchivedToolResults(ctx context.Context, sessionID string, userID uint64, name string, offset int) (model.ArchivedToolPage, error) {
	const pageSize = 16
	page := model.ArchivedToolPage{Results: []model.ArchivedToolReference{}}
	if offset < 0 || offset > 100_000_000 {
		return page, fmt.Errorf("invalid result offset")
	}
	tx, err := r.archivedMessages(ctx, sessionID, userID)
	if err != nil || tx == nil {
		return page, err
	}
	tx = tx.Select("result_id, tool_name, created_at").Where("role = ? AND result_id IS NOT NULL", model.RoleTool)
	if name != "" {
		tx = tx.Where("tool_name = ?", name)
	}
	if err := tx.Order("created_at ASC, id ASC").Offset(offset).Limit(pageSize + 1).Scan(&page.Results).Error; err != nil {
		return page, err
	}
	if len(page.Results) > pageSize {
		page.Results = page.Results[:pageSize]
		page.Next = offset + pageSize
	}
	return page, nil
}

// ContextHistory hydrates SQL associations in bounded batches and loads only
// the newest historical images that fit beside the current turn.
func (r *SessionRepo) ContextHistory(ctx context.Context, sess *model.Session, current *model.Message) ([]*model.Message, error) {
	db := r.db.WithContext(ctx)
	var end model.Message
	if err := db.Select("id", "created_at").Where("session_id = ? AND id = ?", sess.ID, current.ID).Take(&end).Error; err != nil {
		return nil, err
	}
	tx := db.Model(&model.Message{}).Where("session_id = ? AND (created_at < ? OR (created_at = ? AND id <= ?))", sess.ID, end.CreatedAt, end.CreatedAt, end.ID)
	if cp := sess.ContextCheckpoint; cp != nil {
		tx = tx.Where("(created_at > ? OR (created_at = ? AND id >= ?))", cp.FromTime, cp.FromTime, cp.FromID)
	}
	var rows []*model.Message
	for {
		var page []*model.Message
		if err := tx.Order("created_at ASC, id ASC").Limit(256).Find(&page).Error; err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		if err := r.hydrateToolCalls(ctx, page); err != nil {
			return nil, err
		}
		if err := r.hydrateToolResults(ctx, page); err != nil {
			return nil, err
		}
		if err := r.hydrateAttachments(ctx, page, true); err != nil {
			return nil, err
		}
		rows = append(rows, page...)
		if len(page) < 256 {
			break
		}
		last := page[len(page)-1]
		// Restart from the fixed upper bound to avoid accumulating SQL predicates.
		tx = db.Model(&model.Message{}).Where("session_id = ? AND (created_at > ? OR (created_at = ? AND id > ?)) AND (created_at < ? OR (created_at = ? AND id <= ?))", sess.ID, last.CreatedAt, last.CreatedAt, last.ID, end.CreatedAt, end.CreatedAt, end.ID)
	}
	remaining := int64(20 << 20)
	for _, a := range current.Attachments {
		remaining -= a.Size
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].ID == current.ID {
			continue
		}
		for j := range rows[i].Attachments {
			a := &rows[i].Attachments[j]
			if a.Size > remaining {
				continue
			}
			b, err := os.ReadFile(a.StoragePath)
			if err != nil {
				return nil, fmt.Errorf("read context image %s: %w", a.ID, err)
			}
			if int64(len(b)) != a.Size {
				return nil, fmt.Errorf("context image %s changed size", a.ID)
			}
			a.Data = b
			remaining -= a.Size
		}
	}
	return rows, nil
}

func (r *SessionRepo) BeginContextTurn(ctx context.Context, id, turn string) error {
	db := r.db.WithContext(ctx)
	latest := db.Model(&model.Message{}).Select("id").Where("session_id = ? AND role = ?", id, model.RoleUser).Order("created_at DESC, id DESC").Limit(1)
	return db.Model(&model.Session{}).Where("id = ? AND (?) = ?", id, latest, turn).UpdateColumn("context_turn", turn).Error
}

func (r *SessionRepo) SaveContextUsage(ctx context.Context, id, turn string, usage model.ContextUsage) error {
	raw, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.Session{}).Where("id = ? AND context_turn = ?", id, turn).UpdateColumn("context_usage", string(raw)).Error
}

func (r *SessionRepo) SaveContextCheckpoint(ctx context.Context, id, turn string, revision int64, cp model.ContextCheckpoint) (bool, error) {
	raw, err := json.Marshal(cp)
	if err != nil {
		return false, err
	}
	result := r.db.WithContext(ctx).Model(&model.Session{}).Where("id = ? AND context_turn = ? AND context_revision = ?", id, turn, revision).Updates(map[string]any{"context_revision": revision + 1, "context_checkpoint": string(raw)})
	return result.RowsAffected == 1, result.Error
}

// A late insert or repair in a closed turn invalidates its derived summary.
// The revision also fences a summarizer which read the previous evidence.
func invalidateEarlierContext(tx *gorm.DB, messageIDs *gorm.DB) error {
	changed := tx.Table("chat_messages AS changed").Select("changed.session_id").
		Joins("JOIN chat_messages AS anchor ON anchor.id = chat_sessions.context_turn AND anchor.session_id = changed.session_id").
		Where("changed.id IN (?)", messageIDs).
		Where("changed.created_at < anchor.created_at OR (changed.created_at = anchor.created_at AND changed.id < anchor.id)")
	owner := tx.Model(&model.Message{}).Select("session_id").Where("id IN (?)", messageIDs)
	return tx.Model(&model.Session{}).Where("id IN (?) AND context_turn <> '' AND id IN (?)", owner, changed).
		Updates(map[string]any{"context_checkpoint": nil, "context_revision": gorm.Expr("context_revision + 1")}).Error
}
