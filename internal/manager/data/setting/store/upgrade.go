package store

import (
	"context"
	"encoding/json"
	"log/slog"

	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	"gorm.io/gorm"
)

// SeedInfrastructureMenuDefaults is a one-time upgrade migration. Fresh
// installs never call it; existing installations receive a conservative
// sidebar default, while subsequent changes remain entirely user-owned.
func SeedInfrastructureMenuDefaults(ctx context.Context, db *gorm.DB, settings *managerbizsetting.Service, log *slog.Logger) {
	if settings == nil {
		return
	}
	hidden := make([]string, 0, 4)
	counts := map[string]struct{ table, where string }{
		"clusters": {"device_clusters", ""}, "network-devices": {"devices", "os = 'network'"},
		"kubernetes": {"kubernetes_clusters", ""}, "topology": {"topology_nodes", ""},
	}
	for key, query := range counts {
		if !db.Migrator().HasTable(query.table) {
			hidden = append(hidden, key)
			continue
		}
		var count int64
		dbq := db.WithContext(ctx).Table(query.table)
		if query.where != "" {
			dbq = dbq.Where(query.where)
		}
		if err := dbq.Count(&count).Error; err != nil {
			log.Warn("count infrastructure menu data", slog.String("table", query.table), slog.Any("err", err))
			return
		}
		if count == 0 {
			hidden = append(hidden, key)
		}
	}
	value, err := json.Marshal(map[string][]string{"hidden": hidden})
	if err != nil {
		return
	}
	if err := settings.SetIfAbsent(ctx, "ui", "infrastructure_menu_upgrade_v0_10", string(value), false); err != nil {
		log.Warn("seed infrastructure menu defaults", slog.Any("err", err))
	}
}
