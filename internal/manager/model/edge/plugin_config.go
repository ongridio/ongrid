package edge

import (
	"time"

	"gorm.io/gorm"
)

// PluginConfig is one row in edge_plugin_configs — the per-edge
// per-plugin enable flag + plugin-specific spec.
//
// The (edge_id, plugin_name) pair is unique. Schema is intentionally
// narrow:
//
//   - enabled: gates whether supervisor starts the plugin at all.
//   - spec_json: plugin-specific settings (e.g. logs plugin's
//     journald_units / file_paths). Manager validates shape per plugin
//     name in the biz layer; storage is free-form JSON to keep schema
//     evolution cheap when plugin specs grow.
//
// What is NOT in this row:
//
//   - endpoint URL — derived from manager-wide config (ONGRID_PUBLIC_URL)
//     when serving the config to the edge, so all edges always point at
//     the canonical manager URL without per-row drift.
//   - auth_user / auth_pass — edge already has its own access_key /
//     secret_key from enrollment; it fills these in locally.
//   - edge_id (in the wire payload) — tunnel session knows which edge is
//     calling, manager injects on the way out.
type PluginConfig struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	EdgeID     uint64 `gorm:"not null;column:edge_id;uniqueIndex:uk_edge_plugin,priority:1;index:idx_edge_plugin_edge"`
	PluginName string `gorm:"size:32;not null;column:plugin_name;uniqueIndex:uk_edge_plugin,priority:2"`
	Enabled    bool   `gorm:"not null;default:false;column:enabled"`
	// SpecJSON is plugin-specific settings as JSON. No DEFAULT clause —
	// MySQL rejects DEFAULT on TEXT columns (Error 1101). Application
	// code always writes a non-empty value (at minimum "{}" via Set).
	SpecJSON  string         `gorm:"type:text;not null;column:spec_json"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at"`
}

// TableName pins the SQLite table.
func (PluginConfig) TableName() string { return "edge_plugin_configs" }

// Plugin name constants. Keep in lock-step with
// internal/edgeagent/plugins/<name> packages and
const (
	PluginNameMetrics  = "metrics"
	PluginNameLogs     = "logs"
	PluginNameTraces   = "traces"
	PluginNameProfiles = "profiles"
	// hostmetrics / procmetrics wrap Prometheus-ecosystem exporters
	// (node_exporter, ncabatoff/process-exporter) the edge ships as
	// bundled subprocess plugins. Manager toggles enable + spec.
	PluginNameHostMetrics     = "hostmetrics"
	PluginNameProcMetrics     = "procmetrics"
	PluginNameCustomMetrics   = "custommetrics"
	PluginNameDatabaseMetrics = "databasemetrics"
)

// IsKnownPluginName reports whether n is a plugin the manager knows
// about today. metrics/logs/traces/profiles are the original OTel
// signal names; hostmetrics/procmetrics are the Prom-ecosystem
// exporter wrappers.
func IsKnownPluginName(n string) bool {
	switch n {
	case PluginNameMetrics, PluginNameLogs, PluginNameTraces, PluginNameProfiles,
		PluginNameHostMetrics, PluginNameProcMetrics,
		PluginNameCustomMetrics, PluginNameDatabaseMetrics:
		return true
	}
	return false
}
