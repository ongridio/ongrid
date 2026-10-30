package store

import (
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"gorm.io/gorm"
)

// 稳态重连时 adoptHostPluginConfigs 不能重复 INSERT：重复的
// INSERT ... ON DUPLICATE KEY 会先向自增主键尾部写入再发现唯一键冲突，
// 39 台 Edge 并发重连时在同一个尾部间隙上互持 gap 锁，触发 InnoDB 1213
// 死锁（2026-10-30 线上 SHOW ENGINE INNODB STATUS 确认）。
// 这里用 GORM create 回调计数，确保第二次调用一条 INSERT 都不发。
func TestAdoptHostPluginConfigs_SkipsExistingRows(t *testing.T) {
	db := newDeviceTestDB(t)
	if err := db.AutoMigrate(&model.PluginConfig{}, &model.HostPluginConfig{}); err != nil {
		t.Fatalf("AutoMigrate plugin config tables: %v", err)
	}
	if !db.Migrator().HasTable(&model.HostPluginConfig{}) {
		t.Fatal("host_plugin_configs 表未创建")
	}

	const hostID, edgeID = uint64(7), uint64(3)
	seed := []model.PluginConfig{
		{EdgeID: edgeID, PluginName: model.PluginNameMetrics, Enabled: true, SpecJSON: "{}"},
		{EdgeID: edgeID, PluginName: model.PluginNameHostMetrics, Enabled: true, SpecJSON: "{}"},
		// profiles 不是 durable 插件，任何情况下都不应被接纳。
		{EdgeID: edgeID, PluginName: model.PluginNameProfiles, Enabled: true, SpecJSON: "{}"},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed plugin config: %v", err)
		}
	}

	inserts := 0
	if err := db.Callback().Create().After("gorm:create").Register("test:count_host_plugin_insert", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "host_plugin_configs" {
			inserts++
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	// 第一次接纳：只为两个 durable 插件各插一行。
	if err := db.Transaction(func(tx *gorm.DB) error {
		return adoptHostPluginConfigs(tx, hostID, edgeID)
	}); err != nil {
		t.Fatalf("first adopt: %v", err)
	}
	if inserts != 2 {
		t.Fatalf("first adopt issued %d inserts, want 2", inserts)
	}

	// 第二次接纳（等价于下一次 Edge 重连）：必须一条也不插。
	before := inserts
	if err := db.Transaction(func(tx *gorm.DB) error {
		return adoptHostPluginConfigs(tx, hostID, edgeID)
	}); err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if got := inserts - before; got != 0 {
		t.Fatalf("second adopt issued %d inserts, want 0", got)
	}

	var rows []model.HostPluginConfig
	if err := db.Where("device_id = ?", hostID).Find(&rows).Error; err != nil {
		t.Fatalf("list host plugin configs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("host_plugin_configs rows = %d, want 2", len(rows))
	}

	// 新增插件后，下一次接纳仍需补插缺失的那一行。
	if err := db.Create(&model.PluginConfig{EdgeID: edgeID, PluginName: model.PluginNameLogs, Enabled: true, SpecJSON: "{}"}).Error; err != nil {
		t.Fatalf("seed logs plugin config: %v", err)
	}
	before = inserts
	if err := db.Transaction(func(tx *gorm.DB) error {
		return adoptHostPluginConfigs(tx, hostID, edgeID)
	}); err != nil {
		t.Fatalf("third adopt: %v", err)
	}
	if got := inserts - before; got != 1 {
		t.Fatalf("third adopt issued %d inserts, want 1", got)
	}
	if err := db.Where("device_id = ?", hostID).Find(&rows).Error; err != nil {
		t.Fatalf("list host plugin configs: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("host_plugin_configs rows = %d, want 3", len(rows))
	}
}
