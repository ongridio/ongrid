// Package store is the persistence layer for HLD-010 audit_logs.
// Inserts are append-only; the only mutation is the retention sweep.
package store

import (
	"context"
	"time"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/audit"
)

// Repo wraps *gorm.DB with the audit_logs operations the biz layer
// consumes. Kept thin — audit log doesn't have business rules, just
// "write a row" + "list with filters" + "delete old".
type Repo struct {
	db *gorm.DB
}

// New builds a Repo. db must not be nil.
func New(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

// Insert persists one log row. OccurredAt + CreatedAt are stamped by the
// caller (biz.Usecase) and GORM autoCreate respectively. Failure must
// not propagate to business code — the caller wraps with a warn-log
// (see biz.Usecase.Emit) to honour HLD-010's "audit write failure
// must never block the request".
func (r *Repo) Insert(ctx context.Context, log *model.Log) error {
	return r.db.WithContext(ctx).Create(log).Error
}

// ListFilters captures all the GET /v1/admin/audit-logs querystring
// knobs in one struct so the http layer doesn't have to wire 7 args.
// Zero values mean "no filter on this column".
type ListFilters = model.ListFilters

// List returns matching rows newest-first plus the total count for the
// same filters (excluding limit/offset) so the UI can render
// "showing N of total".
func (r *Repo) List(ctx context.Context, f ListFilters) ([]model.Log, int64, error) {
	q := r.db.WithContext(ctx).Model(&model.Log{})
	if f.UserEmail != "" {
		q = q.Where("user_email = ?", f.UserEmail)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.ResourceType != "" {
		q = q.Where("resource_type = ?", f.ResourceType)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if !f.From.IsZero() {
		q = q.Where("occurred_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("occurred_at <= ?", f.To)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	var rows []model.Log
	if err := q.Order("occurred_at DESC, id DESC").
		Limit(limit).Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// DeleteOlderThan implements the retention sweep. Returns the number of
// rows removed. Called from the daily retention goroutine in biz layer.
func (r *Repo) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Where("occurred_at < ?", cutoff).Delete(&model.Log{})
	return res.RowsAffected, res.Error
}
