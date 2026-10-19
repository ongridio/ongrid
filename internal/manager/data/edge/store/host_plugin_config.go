package store

import (
	"context"
	"errors"
	"fmt"

	device "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// hostForEdge 只接受显式且仍为当前安装的双向绑定；从不猜测同号身份。
func (r *PluginConfigRepo) hostForEdge(ctx context.Context, edgeID uint64) (uint64, error) {
	var edge model.Edge
	if err := r.db.WithContext(ctx).First(&edge, edgeID).Error; err != nil {
		return 0, err
	}
	if edge.DeviceID == nil {
		return 0, nil
	}
	var host device.Device
	if err := r.db.WithContext(ctx).First(&host, *edge.DeviceID).Error; err != nil {
		return 0, err
	}
	if host.CurrentEdgeID == nil || *host.CurrentEdgeID != edgeID {
		return 0, fmt.Errorf("%w: superseded edge installation", errs.ErrConflict)
	}
	var count int64
	if err := r.db.WithContext(ctx).Model(&device.EdgeDevice{}).Where("edge_id = ? AND device_id = ? AND type = ?", edgeID, host.ID, device.EdgeDeviceRelationHost).Count(&count).Error; err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, fmt.Errorf("%w: host/edge association mismatch", errs.ErrConflict)
	}
	return host.ID, nil
}

func (r *PluginConfigRepo) ListByHost(ctx context.Context, hostID uint64) ([]*model.PluginConfig, error) {
	var host device.Device
	if err := r.db.WithContext(ctx).First(&host, hostID).Error; err != nil {
		return nil, err
	}
	var rows []model.HostPluginConfig
	if err := r.db.WithContext(ctx).Where("device_id = ? AND removed = ?", hostID, false).Order("plugin_name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*model.PluginConfig, 0, len(rows))
	for _, row := range rows {
		out = append(out, &model.PluginConfig{PluginName: row.PluginName, Enabled: row.Enabled, SpecJSON: row.SpecJSON})
	}
	return out, nil
}
func (r *PluginConfigRepo) GetByHost(ctx context.Context, hostID uint64, plugin string) (*model.PluginConfig, error) {
	var row model.HostPluginConfig
	err := r.db.WithContext(ctx).Where("device_id = ? AND plugin_name = ? AND removed = ?", hostID, plugin, false).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &model.PluginConfig{PluginName: row.PluginName, Enabled: row.Enabled, SpecJSON: row.SpecJSON}, nil
}
func (r *PluginConfigRepo) UpsertHost(ctx context.Context, hostID uint64, in *model.PluginConfig) (*model.PluginConfig, error) {
	if in == nil || !model.DurableHostPlugin(in.PluginName) {
		return nil, errs.ErrInvalid
	}
	row := model.HostPluginConfig{DeviceID: hostID, PluginName: in.PluginName, Enabled: in.Enabled, SpecJSON: in.SpecJSON}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var host device.Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, hostID).Error; err != nil {
			return err
		}
		if in.EdgeID != 0 && (host.CurrentEdgeID == nil || *host.CurrentEdgeID != in.EdgeID) {
			return errs.ErrConflict
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "device_id"}, {Name: "plugin_name"}}, DoUpdates: clause.AssignmentColumns([]string{"enabled", "spec_json", "updated_at", "removed"})}).Create(&row).Error
	})
	return in, err
}
func (r *PluginConfigRepo) DeleteHost(ctx context.Context, hostID uint64, plugin string) error {
	// 保留删除意图，升级/重注册不得重新接纳旧安装的配置。
	row := model.HostPluginConfig{DeviceID: hostID, PluginName: plugin, SpecJSON: "{}", Removed: true}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "device_id"}, {Name: "plugin_name"}}, DoUpdates: clause.AssignmentColumns([]string{"removed", "updated_at"})}).Create(&row).Error
}
