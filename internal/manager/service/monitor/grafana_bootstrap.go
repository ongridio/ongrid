package monitor

import (
	"context"
	"log/slog"
	"time"

	managerbizgrafana "github.com/ongridio/ongrid/internal/manager/biz/grafana"
	managerbizmonitor "github.com/ongridio/ongrid/internal/manager/biz/monitor"
)

// BootstrapGrafana creates the embedded service account and seeds its dashboard.
func BootstrapGrafana(ctx context.Context, grafanaSvc *managerbizgrafana.Service, monitorSvc *managerbizmonitor.Service, user, password string, log *slog.Logger) {
	// Give Grafana ~10s to come up before the first attempt; a
	// fresh container needs ~5s for the embedded sqlite migrations.
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-t.C:
	}
	grafanaSvc.BootstrapEmbedded(ctx, user, password)
	// Now that the SA token exists, push the ongrid-monitor
	// dashboard so it has the core fleet panels even on a fresh
	// install with no user-added panels — otherwise "open in
	// Grafana" from the Monitor page hit an empty/absent dashboard.
	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	if err := monitorSvc.SyncNow(syncCtx); err != nil {
		log.Warn("monitor: initial grafana mirror sync failed (retries on next panel edit)", slog.Any("err", err))
	} else {
		log.Info("monitor: ongrid-monitor dashboard synced at boot")
	}
}
