package logs

import (
	"context"
	"log/slog"
	"time"

	grafanabiz "github.com/ongridio/ongrid/internal/manager/biz/grafana"
)

// SyncGrafanaAtBoot waits for embedded Grafana before reconciling the selected backend.
func SyncGrafanaAtBoot(ctx context.Context, grafana *grafanabiz.Service, log *slog.Logger) {
	// Reconcile the selected backend after upgrades. Wait until the
	// embedded Grafana bootstrap has had a chance to persist its SA token.
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	syncCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := grafana.SyncLogsDatasource(syncCtx); err != nil {
		log.Warn("logs: initial grafana datasource sync failed (retry from integrations)", slog.Any("err", err))
	} else {
		log.Info("logs: selected grafana datasource synced at boot")
	}
}
