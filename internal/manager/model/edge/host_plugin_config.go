package edge

import "time"

// HostPluginConfig 保存设备的长期采集意图；连接健康和有时限的 Profile 会话仍归 Edge。
type HostPluginConfig struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	DeviceID   uint64 `gorm:"not null;uniqueIndex:uk_host_plugin,priority:1"`
	PluginName string `gorm:"size:32;not null;uniqueIndex:uk_host_plugin,priority:2"`
	Removed    bool   `gorm:"not null;default:false"`
	Enabled    bool   `gorm:"not null"`
	SpecJSON   string `gorm:"type:text;not null"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (HostPluginConfig) TableName() string { return "host_plugin_configs" }

// DurableHostPlugin 排除绑定进程实例和截止时间的临时采样会话。
func DurableHostPlugin(name string) bool {
	return IsKnownPluginName(name) && name != PluginNameProfiles
}
