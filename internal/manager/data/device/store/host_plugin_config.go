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
	if len(rows) == 0 {
		return nil
	}
	// 先读 Host 已有行，只为缺失的插件发 INSERT。
	// INSERT ... ON DUPLICATE KEY 会先向自增主键尾部插入再发现 uk_host_plugin
	// 冲突，39 台 Edge 同时重连时这些注定重复的 INSERT 在同一个尾部间隙上
	// 互相持有 gap 锁并申请 insert intention，触发 InnoDB 1213 死锁
	// （2026-10-30 线上 SHOW ENGINE INNODB STATUS 已确认）。
	// 稳态下所有行都已存在，跳过即可完全消除这条写入路径。
	var present []model.HostPluginConfig
	if err := tx.Where("device_id = ?", hostID).Find(&present).Error; err != nil {
		return err
	}
	adopted := make(map[string]struct{}, len(present))
	for _, p := range present {
		adopted[p.PluginName] = struct{}{}
	}
	for _, row := range rows {
		if !model.DurableHostPlugin(row.PluginName) {
			continue
		}
		if _, ok := adopted[row.PluginName]; ok {
			// 已接纳过，保留 Host 侧既有意图（含 removed 状态）。
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
