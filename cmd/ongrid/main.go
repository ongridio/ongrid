package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/logger"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() (runErr error) {
	fmt.Fprintf(os.Stderr, "ongrid %s starting\n", version)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		return err
	}

	log := logger.WithService(logger.New(slog.LevelInfo), "ongrid")
	log.Info("configuration loaded",
		slog.String("http_addr", cfg.HTTPAddr),
		slog.String("metrics_addr", cfg.MetricsAddr),
		slog.String("frontier_addr", cfg.FrontierClient.Addr),
		slog.String("frontier_service_name", cfg.FrontierClient.ServiceName),
		slog.String("db_dialect", cfg.DB.Dialect),
		slog.String("version", version),
	)

	// Parent context cancelled on SIGINT/SIGTERM.
	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	var cleanup startupCleanup
	defer func() {
		// Preserve fatal exits: shutdown hooks previously ran only on normal return.
		if runErr == nil {
			defer cancel()
			cleanup.run()
		}
	}()

	tracing := bootstrapTracing(rootCtx, log, &cleanup)
	storage, err := bootstrapStorage(rootCtx, cfg, log)
	if err != nil {
		return err
	}
	iam, err := bootstrapIAM(rootCtx, cfg, log, storage)
	if err != nil {
		return err
	}

	// Prometheus registry shared by all BCs.
	reg := prom.NewRegistry()
	// Self-observability collectors (alert evaluator latency, prom remote_write
	// outcome). Registered once here so package-globals in internal/pkg/prom
	// are non-nil before any evaluator tick or promwrite Push runs.
	prom.RegisterManagerMetrics(reg, log.With(slog.String("comp", "prom-manager-metrics")))
	notifyRouter := notify.NewFromConfig(cfg.Notification, log.With(slog.String("comp", "notify")))

	settings := bootstrapSettings(rootCtx, cfg, log, storage)
	models := bootstrapModels(rootCtx, cfg, log, reg, settings)
	devices := bootstrapDevices(rootCtx, cfg, log, storage, iam, settings)
	alerts := bootstrapAlerts(rootCtx, cfg, log, storage)
	telemetry, err := bootstrapTelemetry(rootCtx, cfg, log, storage, settings, models, devices, alerts)
	if err != nil {
		return err
	}
	tunnel, err := bootstrapTunnel(rootCtx, cfg, log, &cleanup, storage, iam, devices, alerts, telemetry)
	if err != nil {
		return err
	}
	aiops := bootstrapAIOps(cfg, log, notifyRouter, storage, settings, models, devices, alerts, telemetry, tunnel)
	knowledge := bootstrapKnowledge(rootCtx, cfg, log, storage, settings, telemetry, aiops)
	runtime := bootstrapRuntime(rootCtx, cfg, log, reg, tracing, models, devices, telemetry, tunnel, aiops, settings)
	imbridge := bootstrapIMBridge(rootCtx, log, storage, models, devices, alerts, telemetry, aiops, runtime)
	automation := bootstrapAutomation(rootCtx, cfg, log, reg, notifyRouter, &cleanup, storage, settings, models, alerts, telemetry, aiops, runtime)
	system := bootstrapSystem(cfg, storage, iam, settings, models, telemetry, aiops, knowledge, automation)
	extensions := bootstrapExtensions(rootCtx, cfg, log, reg, notifyRouter, storage, settings, devices, alerts, tunnel, aiops, runtime, automation)
	external, err := bootstrapExternalMCP(cfg, log, reg, storage, settings, devices, tunnel, aiops, extensions)
	if err != nil {
		return err
	}
	http, err := buildHTTPServers(cfg, log, reg, iam, settings, devices, telemetry, tunnel, aiops, runtime, imbridge, automation, system, extensions, external)
	if err != nil {
		return err
	}
	return runWorkers(rootCtx, cfg, log, notifyRouter, storage, settings, devices, alerts, telemetry, aiops, runtime, automation, http, external)
}

// startupCleanup preserves the original reverse shutdown order.
type startupCleanup []func()

func (c *startupCleanup) add(fn func()) { *c = append(*c, fn) }

func (c *startupCleanup) run() {
	// Defer every hook so a panicking hook does not skip earlier acquisitions.
	for _, fn := range *c {
		defer fn()
	}
}
