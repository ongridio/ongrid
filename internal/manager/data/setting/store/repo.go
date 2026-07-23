package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed persistence for system_settings. It is
// concurrency-safe (gorm sessions are independent per call).
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repo around an opened *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Get returns the row matching (category, key). Missing rows return
// (nil, errs.ErrNotFound) so callers can distinguish "absent" from "empty".
func (r *Repo) Get(ctx context.Context, category, key string) (*model.Setting, error) {
	if category == "" || key == "" {
		return nil, fmt.Errorf("%w: category/key required", errs.ErrInvalid)
	}
	var s model.Setting
	err := r.db.WithContext(ctx).
		Where("category = ? AND `key` = ?", category, key).
		First(&s).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

// Set upserts the row. Insert when missing, update value+sensitive when
// present. The unique index on (category, key) keeps it idempotent.
func (r *Repo) Set(ctx context.Context, category, key, value string, sensitive bool) (*model.Setting, error) {
	if category == "" || key == "" {
		return nil, fmt.Errorf("%w: category/key required", errs.ErrInvalid)
	}
	row := model.Setting{
		Category:  category,
		Key:       key,
		Value:     value,
		Sensitive: sensitive,
	}
	// ON CONFLICT (category, key) DO UPDATE — works on both MySQL (via
	// `INSERT ... ON DUPLICATE KEY UPDATE`) and SQLite via the gorm clause.
	err := upsertSetting(r.db.WithContext(ctx), &row)
	if err != nil {
		return nil, err
	}
	// row.ID may be zero on the update path for some drivers; reload to
	// guarantee the caller gets the persisted timestamps.
	return r.Get(ctx, category, key)
}

// SetBatch upserts every row in one database transaction. Either the complete
// configuration tuple commits or none of it does.
func (r *Repo) SetBatch(ctx context.Context, settings []model.Setting) error {
	if len(settings) == 0 {
		return fmt.Errorf("%w: at least one setting required", errs.ErrInvalid)
	}
	for i := range settings {
		if settings[i].Category == "" || settings[i].Key == "" {
			return fmt.Errorf("%w: category/key required", errs.ErrInvalid)
		}
	}
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range settings {
			row := settings[i]
			if err := upsertSetting(tx, &row); err != nil {
				return fmt.Errorf("upsert %s.%s: %w", row.Category, row.Key, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("set settings transaction: %w", err)
	}
	return nil
}

func upsertSetting(db *gorm.DB, row *model.Setting) error {
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "category"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "sensitive", "updated_at"}),
	}).Create(row).Error
}

// List returns all rows in a category ordered by key asc. Empty category
// returns every row.
func (r *Repo) List(ctx context.Context, category string) ([]*model.Setting, error) {
	tx := r.db.WithContext(ctx).Model(&model.Setting{})
	if category != "" {
		tx = tx.Where("category = ?", category)
	}
	var out []*model.Setting
	if err := tx.Order("category asc").Order("`key` asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes the row matching (category, key). Missing row maps to
// errs.ErrNotFound.
func (r *Repo) Delete(ctx context.Context, category, key string) error {
	if category == "" || key == "" {
		return fmt.Errorf("%w: category/key required", errs.ErrInvalid)
	}
	res := r.db.WithContext(ctx).
		Where("category = ? AND `key` = ?", category, key).
		Delete(&model.Setting{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
