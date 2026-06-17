package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/database"
	model "github.com/ongridio/ongrid/internal/manager/model/database"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed biz/database.Repo.
type Repo struct {
	db *gorm.DB
}

// NewRepo constructs the repo around an opened *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// compile-time interface check.
var _ biz.Repo = (*Repo)(nil)

// Create inserts a database instance. Returns errs.ErrInvalid on nil input.
func (r *Repo) Create(ctx context.Context, inst *model.DatabaseInstance) error {
	if inst == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(inst).Error
}

// GetByID returns the instance by primary key. Soft-deleted rows excluded.
func (r *Repo) GetByID(ctx context.Context, id uint64) (*model.DatabaseInstance, error) {
	var inst model.DatabaseInstance
	if err := r.db.WithContext(ctx).First(&inst, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &inst, nil
}

// List returns instances matching f. Sorted by id DESC. Soft-deleted excluded.
func (r *Repo) List(ctx context.Context, f biz.ListFilter) ([]*model.DatabaseInstance, error) {
	tx := r.db.WithContext(ctx).Model(&model.DatabaseInstance{})
	if f.DBType != "" {
		tx = tx.Where("db_type = ?", f.DBType)
	}
	if f.Status != "" {
		tx = tx.Where("status = ?", f.Status)
	}
	if f.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+f.Name+"%")
	}
	if f.EdgeID != nil {
		tx = tx.Where("edge_id = ?", *f.EdgeID)
	}
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.DatabaseInstance
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Update replaces all writable fields on an existing instance identified by
// ID. Fields updated: name, host, port, version, status, config_json, labels,
// description. The edge_id and db_type are immutable after creation.
func (r *Repo) Update(ctx context.Context, inst *model.DatabaseInstance) error {
	if inst == nil {
		return errs.ErrInvalid
	}
	res := r.db.WithContext(ctx).Model(&model.DatabaseInstance{}).Where("id = ?", inst.ID).Updates(map[string]any{
		"name":        inst.Name,
		"host":        inst.Host,
		"port":        inst.Port,
		"version":     inst.Version,
		"status":      inst.Status,
		"config_json": inst.ConfigJSON,
		"labels":      inst.Labels,
		"description": inst.Description,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateStatus sets the connectivity state.
func (r *Repo) UpdateStatus(ctx context.Context, id uint64, status string) error {
	res := r.db.WithContext(ctx).Model(&model.DatabaseInstance{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateVersion sets the auto-detected database version string.
func (r *Repo) UpdateVersion(ctx context.Context, id uint64, version string) error {
	res := r.db.WithContext(ctx).Model(&model.DatabaseInstance{}).Where("id = ?", id).Update("version", version)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// Delete soft-deletes an instance.
func (r *Repo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&model.DatabaseInstance{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpsertFromDiscovery inserts or updates a database instance reported by
// the edge discovery component. Matches on (edge_id, name) — the unique
// constraint from the model. When a row matches, writable fields (host,
// port, version, status, config_json) are updated; otherwise a new row
// is created with StatusUnknown.
//
// Fields: dbType, name (required), host, port, version, status, configJSON.
func (r *Repo) UpsertFromDiscovery(ctx context.Context, edgeID uint64, dbType, name, host string, port int, version, status, configJSON string) error {
	if name == "" || dbType == "" {
		return errs.ErrInvalid
	}
	// Try to find existing row by edge_id + name.
	var existing model.DatabaseInstance
	err := r.db.WithContext(ctx).
		Where("edge_id = ? AND name = ?", edgeID, name).
		First(&existing).Error

	if err == nil {
		// Update existing.
		updates := map[string]any{}
		if host != "" {
			updates["host"] = host
		}
		if port > 0 {
			updates["port"] = port
		}
		if version != "" {
			updates["version"] = version
		}
		if status != "" {
			updates["status"] = status
		}
		if configJSON != "" {
			updates["config_json"] = configJSON
		}
		if len(updates) > 0 {
			return r.db.WithContext(ctx).Model(&model.DatabaseInstance{}).
				Where("id = ?", existing.ID).Updates(updates).Error
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	// Not found — create.
	if host == "" || port == 0 {
		return nil // skip incomplete discovery reports
	}
	if status == "" {
		status = model.StatusUnknown
	}
	return r.db.WithContext(ctx).Create(&model.DatabaseInstance{
		EdgeID:     edgeID,
		Name:       name,
		DBType:     dbType,
		Host:       host,
		Port:       port,
		Version:    version,
		Status:     status,
		ConfigJSON: configJSON,
	}).Error
}

// Count returns the number of non-soft-deleted instances.
func (r *Repo) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.DatabaseInstance{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}
