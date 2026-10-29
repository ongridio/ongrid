// Package store is the GORM-backed persistence for the approval inbox.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed approvals store.
type Repo struct{ db *gorm.DB }

// NewRepo builds the repo.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Create inserts a new proposal.
func (r *Repo) Create(ctx context.Context, a *model.Approval) error {
	return dbx.WithContext(ctx, r.db).Create(a).Error
}

// Get returns one by id.
func (r *Repo) Get(ctx context.Context, id string) (*model.Approval, error) {
	var a model.Approval
	if err := dbx.WithContext(ctx, r.db).Where("id = ?", id).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

// List returns proposals, newest first, optionally filtered by status.
func (r *Repo) List(ctx context.Context, status string, limit int) ([]*model.Approval, error) {
	if limit <= 0 {
		limit = 100
	}
	q := dbx.WithContext(ctx, r.db).Order("created_at DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []*model.Approval
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListKind returns one approval category newest first. Offset support lets
// consumers build a bounded cross-source audit view without loading unrelated
// approval payloads into memory.
func (r *Repo) ListKind(ctx context.Context, kind string, limit, offset int) ([]*model.Approval, error) {
	if limit <= 0 {
		limit = 100
	}
	q := dbx.WithContext(ctx, r.db).Where("kind = ?", kind).Order("created_at DESC").Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	var out []*model.Approval
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// CountPending returns the number of pending proposals (nav badge).
func (r *Repo) CountPending(ctx context.Context) (int64, error) {
	var n int64
	err := dbx.WithContext(ctx, r.db).Model(&model.Approval{}).
		Where("status = ?", model.StatusPending).Count(&n).Error
	return n, err
}

// Update persists status / approver / reason / result / timestamps. Only
// transitions a row still in `pending` (optimistic guard against double
// decisions). Returns ErrNotFound when no pending row matched.
func (r *Repo) Decide(ctx context.Context, id string, fields map[string]any) error {
	res := dbx.WithContext(ctx, r.db).Model(&model.Approval{}).
		Where("id = ? AND status = ?", id, model.StatusPending).
		Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// SetResult records the execution outcome after an approved action runs.
func (r *Repo) SetResult(ctx context.Context, id, status, resultJSON string, executedAt time.Time) error {
	return dbx.WithContext(ctx, r.db).Model(&model.Approval{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "result_json": resultJSON, "executed_at": executedAt}).Error
}
