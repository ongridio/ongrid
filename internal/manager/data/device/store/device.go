// Package sqlite is the GORM-backed implementation of biz/device.Repo
// and biz/device.EdgeDeviceRepo. Despite the package name it works
// against MySQL too — GORM hides the dialect at this level.
package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	biz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed biz/device.Repo.
type Repo struct {
	db *gorm.DB
}

// NewRepo constructs the repo around an opened *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// compile-time check.
var _ biz.Repo = (*Repo)(nil)

// FindOrCreateByFingerprint returns the existing row for seed.Fingerprint
// or creates a fresh row populated from seed. Implementation uses an
// ON CONFLICT DO NOTHING insert plus a follow-up select; this works on
// both MySQL and SQLite without requiring database-level locking.
func (r *Repo) FindOrCreateByFingerprint(ctx context.Context, seed *model.Device) (*model.Device, error) {
	if seed == nil || seed.Fingerprint == "" {
		return nil, errs.ErrInvalid
	}
	tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "fingerprint"}, {Name: "delete_marker"}},
		DoNothing: true,
	}).Create(seed)
	if tx.Error != nil {
		return nil, tx.Error
	}
	var out model.Device
	if err := r.db.WithContext(ctx).Where("fingerprint = ?", seed.Fingerprint).First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// RebindFingerprint moves a device from oldFP to newFP in place. See the
// biz.Repo interface doc.
func (r *Repo) RebindFingerprint(ctx context.Context, oldFP, newFP string) error {
	if oldFP == "" || newFP == "" || oldFP == newFP {
		return nil
	}
	// Skip if newFP already exists — the v2 device already won; nothing to
	// migrate (and the UPDATE would hit the unique index anyway).
	var cnt int64
	if err := r.db.WithContext(ctx).Model(&model.Device{}).
		Where("fingerprint = ?", newFP).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	// In-place rebind: same row, only the fingerprint column changes, so
	// device.ID / junction / history all carry over. Idempotent — if no row
	// carries oldFP, this updates nothing.
	return r.db.WithContext(ctx).Model(&model.Device{}).
		Where("fingerprint = ?", oldFP).
		Update("fingerprint", newFP).Error
}

// UpdateHostFacts overwrites the host fact columns for the device. We
// don't include online/last_seen_at here — those have a separate
// lifecycle (MarkOnline / MarkOffline).
func (r *Repo) UpdateHostFacts(ctx context.Context, id uint64, f biz.HostFacts) error {
	res := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Updates(map[string]any{
		"hostname":         f.Hostname,
		"os":               f.OS,
		"os_version":       f.OSVersion,
		"arch":             f.Arch,
		"kernel_version":   f.KernelVersion,
		"cpu_count":        f.CPUCount,
		"mem_total_bytes":  f.MemTotalBytes,
		"disk_total_bytes": f.DiskTotalBytes,
		"ip_address":       f.IPAddress,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateUsage refreshes the live usage gauges.
func (r *Repo) UpdateUsage(ctx context.Context, id uint64, u biz.Usage) error {
	res := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Updates(map[string]any{
		"cpu_usage_pct":  u.CPUPct,
		"mem_usage_pct":  u.MemPct,
		"disk_usage_pct": u.DiskPct,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateRoles writes the operator-assigned role bit mask.
func (r *Repo) UpdateRoles(ctx context.Context, id uint64, roles uint8) error {
	res := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Update("roles", roles)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// SetNodeID writes Device.NodeID — the link to the topology
// `nodes` table. Skip if the device already points at the same node
// (idempotent so the NodeMirror hook is safe to call on every register
// without redundant writes). Returns ErrNotFound when the device row
// is gone.
func (r *Repo) SetNodeID(ctx context.Context, id, nodeID uint64) error {
	res := r.db.WithContext(ctx).Model(&model.Device{}).
		Where("id = ? AND (node_id IS NULL OR node_id <> ?)", id, nodeID).
		Update("node_id", nodeID)
	if res.Error != nil {
		return res.Error
	}
	// RowsAffected==0 either means the row is gone OR we already had
	// this value; check the row to disambiguate.
	if res.RowsAffected == 0 {
		var exists int64
		if err := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return errs.ErrNotFound
		}
	}
	return nil
}

// UpdateNameDescription writes the operator-editable display fields.
func (r *Repo) UpdateNameDescription(ctx context.Context, id uint64, name, description string) error {
	res := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Updates(map[string]any{
		"name":        name,
		"description": description,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// MarkOnline flips online=true and bumps last_seen_at.
func (r *Repo) MarkOnline(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Updates(map[string]any{
		"online":       true,
		"last_seen_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// MarkOffline flips online=false (last_seen_at left untouched so callers
// can still see "last contacted" time).
func (r *Repo) MarkOffline(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", id).Update("online", false)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// Get returns the device by primary key.
func (r *Repo) Get(ctx context.Context, id uint64) (*model.Device, error) {
	var d model.Device
	if err := r.db.WithContext(ctx).First(&d, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// GetMany batch-loads devices by id. Missing ids are absent from the map.
func (r *Repo) GetMany(ctx context.Context, ids []uint64) (map[uint64]*model.Device, error) {
	if len(ids) == 0 {
		return map[uint64]*model.Device{}, nil
	}
	var rows []*model.Device
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint64]*model.Device, len(rows))
	for _, d := range rows {
		out[d.ID] = d
	}
	return out, nil
}

// List returns devices matching f.
func (r *Repo) List(ctx context.Context, f biz.ListFilter) ([]*model.Device, error) {
	tx := r.db.WithContext(ctx).Model(&model.Device{})
	switch {
	case f.RolesUnknownOnly:
		tx = tx.Where("roles = ?", 0)
	case f.RolesAny != 0:
		tx = tx.Where("roles IN ?", model.MatchingRoleValues(f.RolesAny))
	}
	if f.Online != nil {
		tx = tx.Where("online = ?", *f.Online)
	}
	if f.Hostname != "" {
		tx = tx.Where("hostname LIKE ?", "%"+f.Hostname+"%")
	}
	if f.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+f.Name+"%")
	}
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.Device
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Count returns the non-soft-deleted device count.
func (r *Repo) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.Device{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// Delete soft-deletes a device by id.
func (r *Repo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&model.Device{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
