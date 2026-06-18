package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// PluginConfigRepo persists edge_plugin_configs.
// Constructed by cmd/ongrid wiring; biz consumes the narrow interface in
// internal/manager/biz/edge/plugin_config.go.
type PluginConfigRepo struct {
	db *gorm.DB
}

// NewPluginConfigRepo wires the repo around an open *gorm.DB.
func NewPluginConfigRepo(db *gorm.DB) *PluginConfigRepo {
	return &PluginConfigRepo{db: db}
}

// ListByEdge returns every plugin config row for one edge, sorted by
// plugin_name for stable UI rendering.
func (r *PluginConfigRepo) ListByEdge(ctx context.Context, edgeID uint64) ([]*model.PluginConfig, error) {
	var out []*model.PluginConfig
	tx := r.db.WithContext(ctx).Where("edge_id = ?", edgeID).Order("plugin_name ASC")
	if err := tx.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns a single (edge_id, plugin_name) row or errs.ErrNotFound.
func (r *PluginConfigRepo) Get(ctx context.Context, edgeID uint64, plugin string) (*model.PluginConfig, error) {
	var row model.PluginConfig
	err := r.db.WithContext(ctx).Where("edge_id = ? AND plugin_name = ?", edgeID, plugin).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Upsert inserts a new row or updates an existing row matching
// (edge_id, plugin_name). Returns the persisted row (with ID + timestamps).
func (r *PluginConfigRepo) Upsert(ctx context.Context, in *model.PluginConfig) (*model.PluginConfig, error) {
	if in == nil {
		return nil, errs.ErrInvalid
	}
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "edge_id"}, {Name: "plugin_name"}, {Name: "delete_marker"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "spec_json", "updated_at"}),
	}).Create(in).Error
	if err != nil {
		return nil, err
	}
	return in, nil
}

// Delete removes one (edge_id, plugin_name) row. Idempotent (no error
// when the row didn't exist).
func (r *PluginConfigRepo) Delete(ctx context.Context, edgeID uint64, plugin string) error {
	return r.db.WithContext(ctx).
		Where("edge_id = ? AND plugin_name = ?", edgeID, plugin).
		Delete(&model.PluginConfig{}).Error
}

// CountByPlugin returns how many edges have each plugin enabled. Used by
// the Integrations UI cards to show "active on N/M edges".
func (r *PluginConfigRepo) CountByPlugin(ctx context.Context) (map[string]int64, error) {
	type row struct {
		PluginName string
		N          int64
	}
	var rows []row
	if err := r.db.WithContext(ctx).Model(&model.PluginConfig{}).
		Select("plugin_name, count(*) as n").
		Where("enabled = ?", true).
		Group("plugin_name").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.PluginName] = r.N
	}
	return out, nil
}
