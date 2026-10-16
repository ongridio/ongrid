package k8s

import (
	"context"
	"errors"
	"log/slog"
	"time"

	managerbizk8s "github.com/ongridio/ongrid/internal/manager/biz/k8s"
)

func RunEventRetention(ctx context.Context, uc *managerbizk8s.Usecase, log *slog.Logger) error {
	if uc == nil {
		return nil
	}
	interval := uc.EventCleanupInterval()
	if interval <= 0 {
		interval = time.Hour
	}
	run := func() {
		stats, err := uc.CleanupEvents(ctx, time.Now().UTC())
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Warn("k8s event retention failed", slog.Any("err", err))
			return
		}
		deleted := stats.DeletedByTTL + stats.DeletedByClusterLimit
		if deleted > 0 {
			log.Info(
				"k8s event retention complete",
				slog.Int64("deleted", deleted),
				slog.Int64("deleted_by_ttl", stats.DeletedByTTL),
				slog.Int64("deleted_by_cluster_limit", stats.DeletedByClusterLimit),
			)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			run()
		}
	}
}

func RunTopologyReconcile(ctx context.Context, uc *managerbizk8s.Usecase, log *slog.Logger) error {
	if uc == nil {
		return nil
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := uc.ReconcileTopology(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("k8s topology reconcile failed", slog.Any("err", err))
			}
		}
	}
}
