package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/apm"
	"github.com/ongridio/ongrid/internal/manager/biz/topology"
)

func runServiceTopologyReconcile(ctx context.Context, services *apm.Service, graph *topology.Usecase, log *slog.Logger) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		syncCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		deployments, err := services.ServiceDeployments(syncCtx, time.Now())
		if err == nil {
			err = graph.ReconcileServiceDeployments(syncCtx, deployments)
		}
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("service topology reconcile failed", slog.Any("err", err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
