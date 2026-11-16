// Package store implements the cmdb/biz/relationtype persistence port on GORM.
package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	relationtypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/relationtype"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed implementation of the relation-type port.
type Repo struct {
	db *gorm.DB
}

var _ relationtypebiz.Repo = (*Repo)(nil)

// NewRepo builds the repo around an open *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// List returns every live relation type ordered by id.
//
// Ordering is explicit so the dictionary is stable between requests; cantor
// relied on the storage engine's incidental order.
func (r *Repo) List(ctx context.Context) ([]citypemodel.RelationType, error) {
	var out []citypemodel.RelationType
	if err := r.db.WithContext(ctx).Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb relation type: list: %w", err)
	}
	return out, nil
}

// Get returns one relation type, or nil when it does not exist.
func (r *Repo) Get(ctx context.Context, id int32) (*citypemodel.RelationType, error) {
	var rt citypemodel.RelationType
	err := r.db.WithContext(ctx).First(&rt, id).Error
	switch {
	case err == nil:
		return &rt, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb relation type: get %d: %w", id, err)
	}
}

// FindByName returns the live relation type with this exact name, or nil.
func (r *Repo) FindByName(ctx context.Context, name string) (*citypemodel.RelationType, error) {
	var rt citypemodel.RelationType
	err := r.db.WithContext(ctx).Where("name = ?", name).First(&rt).Error
	switch {
	case err == nil:
		return &rt, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb relation type: find by name %q: %w", name, err)
	}
}

// Create inserts a relation type and fills in its assigned id.
func (r *Repo) Create(ctx context.Context, rt *citypemodel.RelationType) error {
	if rt == nil {
		return fmt.Errorf("cmdb relation type: create: nil row")
	}
	if err := r.db.WithContext(ctx).Create(rt).Error; err != nil {
		return fmt.Errorf("cmdb relation type: create %q: %w", rt.Name, err)
	}
	return nil
}

// Update writes the mutable fields of an existing relation type.
//
// It selects the columns explicitly rather than saving a whole row, so an
// unrelated field loaded by the caller cannot be written back by accident.
func (r *Repo) Update(ctx context.Context, rt *citypemodel.RelationType) error {
	if rt == nil {
		return fmt.Errorf("cmdb relation type: update: nil row")
	}
	res := r.db.WithContext(ctx).Model(&citypemodel.RelationType{}).
		Where("id = ?", rt.ID).
		Updates(map[string]any{"name": rt.Name, "color": rt.Color})
	if res.Error != nil {
		return fmt.Errorf("cmdb relation type: update %d: %w", rt.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		// The row vanished between the usecase's read and this write.
		return errs.ErrNotFound
	}
	return nil
}

// SoftDelete marks a relation type deleted.
func (r *Repo) SoftDelete(ctx context.Context, id int32) error {
	res := r.db.WithContext(ctx).Delete(&citypemodel.RelationType{}, id)
	if res.Error != nil {
		return fmt.Errorf("cmdb relation type: delete %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
