package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) SaveBackend(ctx context.Context, backend *model.Backend) error {
	if backend == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Save(backend).Error
}

func (r *Repo) GetBackend(ctx context.Context, id uint64) (*model.Backend, error) {
	var out model.Backend
	if err := r.db.WithContext(ctx).First(&out, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (r *Repo) LatestBackend(ctx context.Context) (*model.Backend, error) {
	var out model.Backend
	if err := r.db.WithContext(ctx).Order("id DESC").First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (r *Repo) SelectedBackend(ctx context.Context) (*model.Backend, error) {
	var out model.Backend
	if err := r.db.WithContext(ctx).
		Where("status = ?", model.BackendStatusSelected).
		Order("id DESC").First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (r *Repo) SelectBackend(ctx context.Context, id uint64, version string, testedAt time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Backend{}).
			Where("status = ? AND id <> ?", model.BackendStatusSelected, id).
			Update("status", model.BackendStatusUnselected).Error; err != nil {
			return err
		}
		result := tx.Model(&model.Backend{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"status":           model.BackendStatusSelected,
				"detected_version": version,
				"last_test_at":     testedAt.UTC(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errs.ErrConflict
		}
		return tx.Where("backend_id = ?", id).Delete(&model.BackendAssignment{}).Error
	})
}

// SelectLoki makes Loki authoritative by unselecting every Elasticsearch
// configuration. Device verification is intentionally independent.
func (r *Repo) SelectLoki(ctx context.Context) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var selectedIDs []uint64
		if err := tx.Model(&model.Backend{}).
			Where("status = ?", model.BackendStatusSelected).
			Pluck("id", &selectedIDs).Error; err != nil {
			return err
		}
		if len(selectedIDs) == 0 {
			return nil
		}
		result := tx.Model(&model.Backend{}).
			Where("id IN ?", selectedIDs).
			Update("status", model.BackendStatusUnselected)
		if result.Error != nil {
			return result.Error
		}
		return tx.Where("backend_id IN ?", selectedIDs).Delete(&model.BackendAssignment{}).Error
	})
}

func (r *Repo) GetAssignment(ctx context.Context, backendID, edgeID uint64) (*model.BackendAssignment, error) {
	var out model.BackendAssignment
	if err := r.db.WithContext(ctx).
		Where("backend_id = ? AND edge_id = ?", backendID, edgeID).
		First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (r *Repo) UpsertAssignment(ctx context.Context, assignment *model.BackendAssignment) error {
	if assignment == nil || assignment.BackendID == 0 || assignment.EdgeID == 0 {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "backend_id"}, {Name: "edge_id"}, {Name: "delete_marker"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"desired_generation", "applied_generation", "status", "probe_id",
			"last_probe_at", "last_write_success_at", "last_error", "updated_at",
		}),
	}).Create(assignment).Error
}

func (r *Repo) ListAssignments(ctx context.Context, backendID uint64) ([]*model.BackendAssignment, error) {
	var out []*model.BackendAssignment
	if err := r.db.WithContext(ctx).Where("backend_id = ?", backendID).Order("edge_id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
