package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/marketplace"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed persistence for installed_skills. Concurrency-
// safe; gorm sessions are independent per call.
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repo around an opened *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Create inserts a new installed pack row. PackID + TenantID together
// form the uniqueness key — duplicate inserts return errs.ErrConflict.
func (r *Repo) Create(ctx context.Context, p *model.InstalledPack) error {
	if p == nil {
		return fmt.Errorf("%w: pack is nil", errs.ErrInvalid)
	}
	if p.PackID == "" {
		return fmt.Errorf("%w: pack_id required", errs.ErrInvalid)
	}
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		// gorm doesn't expose driver-neutral conflict detection; both
		// MySQL and SQLite return UNIQUE-constraint errors with the
		// substring "UNIQUE" / "Duplicate". Match either.
		msg := err.Error()
		if contains(msg, "UNIQUE") || contains(msg, "Duplicate") || contains(msg, "duplicate") {
			return fmt.Errorf("%w: pack already installed", errs.ErrConflict)
		}
		return err
	}
	return nil
}

// GetByPackID fetches a non-deleted row matching (tenantID, packID).
// Missing rows return errs.ErrNotFound so callers can distinguish
// "absent" from "DB error".
func (r *Repo) GetByPackID(ctx context.Context, tenantID uint64, packID string) (*model.InstalledPack, error) {
	if packID == "" {
		return nil, fmt.Errorf("%w: pack_id required", errs.ErrInvalid)
	}
	var p model.InstalledPack
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND pack_id = ? AND deleted_at IS NULL", tenantID, packID).
		First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// GetByManifestSHA returns the (tenantID, sha) row, or ErrNotFound.
// Used during install to reject "same content already installed
// under a different pack id" — protects against the user sneaking the
// same pack in twice via a renamed local copy.
func (r *Repo) GetByManifestSHA(ctx context.Context, tenantID uint64, sha string) (*model.InstalledPack, error) {
	if sha == "" {
		return nil, fmt.Errorf("%w: sha required", errs.ErrInvalid)
	}
	var p model.InstalledPack
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND manifest_sha256 = ? AND deleted_at IS NULL", tenantID, sha).
		First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// List returns every non-deleted pack for the tenant ordered by
// installed_at desc. tenantID == 0 returns every tenant's rows
// (admin-cross-tenant view; today single-tenant ⇒ also returns the
// single tenant's rows).
func (r *Repo) List(ctx context.Context, tenantID uint64) ([]*model.InstalledPack, error) {
	tx := r.db.WithContext(ctx).Model(&model.InstalledPack{}).
		Where("deleted_at IS NULL")
	if tenantID != 0 {
		tx = tx.Where("tenant_id = ?", tenantID)
	}
	var out []*model.InstalledPack
	if err := tx.Order("installed_at desc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSoft soft-deletes the matching row. Idempotent: deleting
// an already-deleted (or missing) row returns errs.ErrNotFound.
func (r *Repo) DeleteSoft(ctx context.Context, tenantID uint64, packID string) error {
	if packID == "" {
		return fmt.Errorf("%w: pack_id required", errs.ErrInvalid)
	}
	res := r.db.WithContext(ctx).
		Where("tenant_id = ? AND pack_id = ?", tenantID, packID).
		Delete(&model.InstalledPack{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// SetBindings stores the slot→credential JSON for an installed pack
// (HLD-017 credential binding). Empty/missing pack → ErrNotFound.
func (r *Repo) SetBindings(ctx context.Context, tenantID uint64, packID, bindingsJSON string) error {
	if packID == "" {
		return fmt.Errorf("%w: pack_id required", errs.ErrInvalid)
	}
	res := r.db.WithContext(ctx).Model(&model.InstalledPack{}).
		Where("tenant_id = ? AND pack_id = ? AND deleted_at IS NULL", tenantID, packID).
		Update("bindings_json", bindingsJSON)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// contains is a tiny strings.Contains shim — keeps the import surface
// small (we only need it for one error-message check above).
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
