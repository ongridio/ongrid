// Package store is the GORM-backed implementation of biz/flow.Repo and
// biz/flow.RunRepo. Works against MySQL and SQLite alike — GORM hides
// the dialect at this level.
package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/flow"
	model "github.com/ongridio/ongrid/internal/manager/model/flow"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo implements biz/flow.Repo.
type Repo struct{ db *gorm.DB }

// NewRepo constructs the definition repo.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

var _ biz.Repo = (*Repo)(nil)

func (r *Repo) Create(ctx context.Context, f *model.Flow) error {
	return r.db.WithContext(ctx).Create(f).Error
}

func (r *Repo) Update(ctx context.Context, f *model.Flow) error {
	return r.db.WithContext(ctx).Save(f).Error
}

func (r *Repo) Get(ctx context.Context, id uint64) (*model.Flow, error) {
	var f model.Flow
	if err := r.db.WithContext(ctx).First(&f, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

func (r *Repo) List(ctx context.Context, limit, offset int) ([]*model.Flow, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&model.Flow{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []*model.Flow
	if err := r.db.WithContext(ctx).Order("id DESC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *Repo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&model.Flow{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// RunRepo implements biz/flow.RunRepo.
type RunRepo struct{ db *gorm.DB }

// NewRunRepo constructs the run repo.
func NewRunRepo(db *gorm.DB) *RunRepo { return &RunRepo{db: db} }

var _ biz.RunRepo = (*RunRepo)(nil)

func (r *RunRepo) CreateRun(ctx context.Context, run *model.FlowRun) error {
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *RunRepo) UpdateRun(ctx context.Context, run *model.FlowRun) error {
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *RunRepo) GetRun(ctx context.Context, id string) (*model.FlowRun, error) {
	var run model.FlowRun
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &run, nil
}

func (r *RunRepo) ListRuns(ctx context.Context, flowID uint64, limit int) ([]*model.FlowRun, error) {
	var out []*model.FlowRun
	q := r.db.WithContext(ctx).Order("created_at DESC").Limit(limit)
	if flowID > 0 {
		q = q.Where("flow_id = ?", flowID)
	}
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *RunRepo) CreateNode(ctx context.Context, n *model.FlowRunNode) error {
	return r.db.WithContext(ctx).Create(n).Error
}

func (r *RunRepo) UpdateNode(ctx context.Context, n *model.FlowRunNode) error {
	return r.db.WithContext(ctx).Save(n).Error
}

func (r *RunRepo) ListNodes(ctx context.Context, runID string) ([]*model.FlowRunNode, error) {
	var out []*model.FlowRunNode
	if err := r.db.WithContext(ctx).Where("run_id = ?", runID).Order("id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *RunRepo) SweepStaleRunning(ctx context.Context, reason string) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.FlowRun{}).
		Where("status IN ?", []string{model.RunStatusPending, model.RunStatusRunning}).
		Updates(map[string]any{"status": model.RunStatusFailed, "error": reason})
	return res.RowsAffected, res.Error
}
