package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/k8s/dataplane"
)

func runK8sDataPlaneMode(ctx context.Context, mode string, log *slog.Logger) (bool, error) {
	switch strings.TrimSpace(mode) {
	case "k8s-telemetry-gateway":
		return true, dataplane.RunTelemetryGateway(ctx, log)
	case "k8s-metrics-scraper":
		return true, dataplane.RunMetricsScraper(ctx, log)
	default:
		return false, nil
	}
}
