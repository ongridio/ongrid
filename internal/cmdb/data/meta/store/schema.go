package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	metabiz "github.com/ongridio/ongrid/internal/cmdb/biz/meta"
	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
)

// Repo is the GORM-backed implementation of the cmdb/biz/meta port.
type Repo struct {
	db *gorm.DB
}

// Compile-time check that Repo satisfies the consumer-side interface.
var _ metabiz.Repo = (*Repo)(nil)

// NewRepo builds the repo around an open *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Latest returns the newest recorded generation by AppliedAt, tie-broken by
// descending id so two rows written in the same instant still order
// deterministically.
func (r *Repo) Latest(ctx context.Context) (meta.SchemaMeta, bool, error) {
	var row meta.SchemaMeta
	err := r.db.WithContext(ctx).
		Order("applied_at DESC, id DESC").
		First(&row).Error
	switch {
	case err == nil:
		return row, true, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return meta.SchemaMeta{}, false, nil
	default:
		return meta.SchemaMeta{}, false, fmt.Errorf("cmdb meta: latest generation: %w", err)
	}
}

// Record appends a generation row.
func (r *Repo) Record(ctx context.Context, row meta.SchemaMeta) error {
	if row.Generation == "" {
		return fmt.Errorf("cmdb meta: record: empty generation")
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("cmdb meta: record generation %q: %w", row.Generation, err)
	}
	return nil
}
