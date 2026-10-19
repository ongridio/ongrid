package store

import (
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// adoptHostPluginConfigs 仅在已有明确绑定的事务内接纳安装配置，Host 已有意图始终优先。
func adoptHostPluginConfigs(tx *gorm.DB, hostID, edgeID uint64) error {
	if !tx.Migrator().HasTable(&model.HostPluginConfig{}) || !tx.Migrator().HasTable(&model.PluginConfig{}) {
		return nil
	}
	var rows []model.PluginConfig
	if err := tx.Where("edge_id = ?", edgeID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if !model.DurableHostPlugin(row.PluginName) {
			continue
		}
		target := model.HostPluginConfig{DeviceID: hostID, PluginName: row.PluginName, Enabled: row.Enabled, SpecJSON: row.SpecJSON}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&target).Error; err != nil {
			return err
		}
	}
	return nil
}

func initializeHostPluginConfigs(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.HostPluginConfig{}) || !db.Migrator().HasTable("edges") {
		return nil
	}
	var bindings []struct{ HostID, EdgeID uint64 }
	if err := db.Table("devices d").Select("d.id AS host_id, e.id AS edge_id").Joins("JOIN edges e ON e.id=d.current_edge_id AND e.device_id=d.id AND e.delete_marker=0").Joins("JOIN edge_devices l ON l.device_id=d.id AND l.edge_id=e.id AND l.type=1 AND l.delete_marker=0").Where("d.delete_marker=0").Scan(&bindings).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, b := range bindings {
			if err := adoptHostPluginConfigs(tx, b.HostID, b.EdgeID); err != nil {
				return err
			}
		}
		return nil
	})
}
