package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/monitor"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed persistence for monitor_panels. Each call uses
// a fresh session via WithContext so concurrent callers don't share
// transaction state.
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repo around an opened *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// List returns every panel ordered by (ordinal asc, id asc). Stable
// ordering ensures the SPA renders panels deterministically across
// refreshes.
func (r *Repo) List(ctx context.Context) ([]*model.Panel, error) {
	var out []*model.Panel
	if err := dbx.WithContext(ctx, r.db).
		Order("ordinal asc").
		Order("id asc").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one panel by id. Missing rows surface as errs.ErrNotFound.
func (r *Repo) Get(ctx context.Context, id uint64) (*model.Panel, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: id required", errs.ErrInvalid)
	}
	var p model.Panel
	if err := dbx.WithContext(ctx, r.db).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// MaxOrdinal returns the largest ordinal currently in the table, or 0
// when the table is empty. Used by Create to place new panels at the
// end without forcing the caller to read the list first.
func (r *Repo) MaxOrdinal(ctx context.Context) (int, error) {
	var n int
	row := dbx.WithContext(ctx, r.db).
		Model(&model.Panel{}).
		Select("COALESCE(MAX(ordinal), 0)").
		Row()
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Create inserts a new panel and returns the persisted row (with id +
// timestamps populated).
func (r *Repo) Create(ctx context.Context, p *model.Panel) (*model.Panel, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: panel required", errs.ErrInvalid)
	}
	if err := dbx.WithContext(ctx, r.db).Create(p).Error; err != nil {
		return nil, err
	}
	return p, nil
}

// Update writes the named columns onto the row with the given id and
// returns the post-update row. Missing id surfaces as errs.ErrNotFound.
//
// `fields` is the column-name → new-value map; gorm only emits an UPDATE
// for the columns named, leaving everything else untouched.
func (r *Repo) Update(ctx context.Context, id uint64, fields map[string]any) (*model.Panel, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: id required", errs.ErrInvalid)
	}
	if len(fields) == 0 {
		return r.Get(ctx, id)
	}
	res := dbx.WithContext(ctx, r.db).
		Model(&model.Panel{}).
		Where("id = ?", id).
		Updates(fields)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Distinguish "row missing" from "no-op update". gorm's Updates
		// reports 0 rows for both, so we follow up with a Get to confirm.
		return r.Get(ctx, id)
	}
	return r.Get(ctx, id)
}

// SetSyncResult records the outcome of an asynchronous Grafana mirror
// attempt. errMsg = "" means the sync succeeded; lastSyncAt is updated
// regardless. Failures are non-fatal — the caller still returns 200 to
// the operator and the row stays usable.
func (r *Repo) SetSyncResult(ctx context.Context, id uint64, errMsg string) error {
	if id == 0 {
		return fmt.Errorf("%w: id required", errs.ErrInvalid)
	}
	now := time.Now().UTC()
	return dbx.WithContext(ctx, r.db).
		Model(&model.Panel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_sync_error": errMsg,
			"last_sync_at":    &now,
		}).Error
}

// Delete removes the row with the given id. Missing row surfaces as
// errs.ErrNotFound.
func (r *Repo) Delete(ctx context.Context, id uint64) error {
	if id == 0 {
		return fmt.Errorf("%w: id required", errs.ErrInvalid)
	}
	res := dbx.WithContext(ctx, r.db).Delete(&model.Panel{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
