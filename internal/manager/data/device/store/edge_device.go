package store

import (
	"context"
	"errors"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	biz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// EdgeDeviceRepo is the GORM-backed biz/device.EdgeDeviceRepo.
type EdgeDeviceRepo struct {
	db *gorm.DB
}

// NewEdgeDeviceRepo constructs the junction repo around an opened *gorm.DB.
func NewEdgeDeviceRepo(db *gorm.DB) *EdgeDeviceRepo { return &EdgeDeviceRepo{db: db} }

var _ biz.EdgeDeviceRepo = (*EdgeDeviceRepo)(nil)

// Link upserts the (edge, device, type) row. Duplicate triple is
// silently a no-op (ON CONFLICT DO NOTHING) so callers can call this
// every register without first checking existence.
func (r *EdgeDeviceRepo) Link(ctx context.Context, edgeID, deviceID uint64, t model.EdgeDeviceRelationType) error {
	if edgeID == 0 || deviceID == 0 {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if t == model.EdgeDeviceRelationHost {
			if tx.Migrator().HasTable("edges") {
				var edge edgemodel.Edge
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&edge, edgeID).Error; err != nil {
					return err
				}
				if edge.DeviceID != nil && *edge.DeviceID != deviceID {
					return errs.ErrInvalid
				}
				if err := tx.Model(&edgemodel.Edge{}).Where("id = ?", edgeID).Update("device_id", deviceID).Error; err != nil {
					return err
				}
			}
			// Lock the Host before changing its current installation and reverse link.
			var host model.Device
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, deviceID).Error; err != nil {
				return err
			}
			var existing model.EdgeDevice
			err := tx.Where("edge_id = ? AND type = ?", edgeID, t).First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && existing.DeviceID != deviceID {
				return errs.ErrInvalid
			}
			if host.CurrentEdgeID != nil {
				if err := adoptHostPluginConfigs(tx, deviceID, *host.CurrentEdgeID); err != nil {
					return err
				}
			}
			if host.CurrentEdgeID == nil || *host.CurrentEdgeID <= edgeID {
				if err := adoptHostPluginConfigs(tx, deviceID, edgeID); err != nil {
					return err
				}
			}
			if host.CurrentEdgeID == nil || *host.CurrentEdgeID < edgeID {
				if err := tx.Model(&model.Device{}).Where("id = ?", deviceID).Update("current_edge_id", edgeID).Error; err != nil {
					return err
				}
			}
		}
		row := model.EdgeDevice{EdgeID: edgeID, DeviceID: deviceID, Type: t}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "edge_id"}, {Name: "device_id"}, {Name: "type"}, {Name: "delete_marker"}}, DoNothing: true}).Create(&row).Error
	})
}

// Unlink soft-deletes the (edge, device, type) row. Idempotent.
func (r *EdgeDeviceRepo) Unlink(ctx context.Context, edgeID, deviceID uint64, t model.EdgeDeviceRelationType) error {
	res := r.db.WithContext(ctx).
		Where("edge_id = ? AND device_id = ? AND type = ?", edgeID, deviceID, t).
		Delete(&model.EdgeDevice{})
	if res.Error != nil {
		return res.Error
	}
	return nil
}

// LookupHostDevice resolves edge_id → host device_id via the type=Host
// junction row.
func (r *EdgeDeviceRepo) LookupHostDevice(ctx context.Context, edgeID uint64) (uint64, error) {
	var row model.EdgeDevice
	if err := r.db.WithContext(ctx).
		Where("edge_id = ? AND type = ?", edgeID, model.EdgeDeviceRelationHost).
		Order("id DESC").
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errs.ErrNotFound
		}
		return 0, err
	}
	return row.DeviceID, nil
}

// LookupEdgeForDevice uses the authoritative current binding for Host execution.
// Historical reverse links never determine the current installation.
func (r *EdgeDeviceRepo) LookupEdgeForDevice(ctx context.Context, deviceID uint64, t model.EdgeDeviceRelationType) (uint64, error) {
	if t == model.EdgeDeviceRelationHost {
		var host model.Device
		if err := r.db.WithContext(ctx).First(&host, deviceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return 0, errs.ErrNotFound
			}
			return 0, err
		}
		if host.CurrentEdgeID == nil || *host.CurrentEdgeID == 0 {
			return 0, errs.ErrNotFound
		}
		var link model.EdgeDevice
		if err := r.db.WithContext(ctx).Where("edge_id = ? AND device_id = ? AND type = ?", *host.CurrentEdgeID, deviceID, t).First(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return 0, errs.ErrNotFound
			}
			return 0, err
		}
		return link.EdgeID, nil
	}

	var row model.EdgeDevice
	if err := r.db.WithContext(ctx).
		Where("device_id = ? AND type = ?", deviceID, t).
		Order("id DESC").
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errs.ErrNotFound
		}
		return 0, err
	}
	return row.EdgeID, nil
}

// ListDevicesForEdge returns every junction row for this edge.
func (r *EdgeDeviceRepo) ListDevicesForEdge(ctx context.Context, edgeID uint64) ([]*model.EdgeDevice, error) {
	var rows []*model.EdgeDevice
	if err := r.db.WithContext(ctx).
		Where("edge_id = ?", edgeID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListEdgesForDevice returns every junction row for this device.
func (r *EdgeDeviceRepo) ListEdgesForDevice(ctx context.Context, deviceID uint64) ([]*model.EdgeDevice, error) {
	var rows []*model.EdgeDevice
	if err := r.db.WithContext(ctx).
		Where("device_id = ?", deviceID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
