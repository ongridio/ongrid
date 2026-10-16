package edge

import (
	"context"
	"log/slog"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
)

// RunPresenceReconcile repairs stale Edge and device presence at boot and every minute.
func RunPresenceReconcile(ctx context.Context, edges *edgebiz.Usecase, devices *devicebiz.Usecase, offlineThreshold time.Duration, log *slog.Logger) error {
	reconcilePresence := func() {
		if _, err := edges.ReconcileStaleOnline(ctx, time.Now().UTC().Add(-offlineThreshold)); err != nil {
			log.Warn("edge presence reconcile failed", slog.Any("err", err))
		}
		if _, err := devices.ReconcilePresence(ctx); err != nil {
			log.Warn("device presence reconcile failed", slog.Any("err", err))
		}
	}
	reconcilePresence()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			reconcilePresence()
		}
	}
}
