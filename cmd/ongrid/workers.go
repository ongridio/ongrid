package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	managersvcedge "github.com/ongridio/ongrid/internal/manager/service/edge"
	managersvck8s "github.com/ongridio/ongrid/internal/manager/service/k8s"
	managersvctopology "github.com/ongridio/ongrid/internal/manager/service/topology"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"golang.org/x/sync/errgroup"
)

func runWorkers(rootCtx context.Context, cfg *config.Config, log *slog.Logger, notifyRouter *notify.Router, storage *storageModule, settings *settingsModule, devices *devicesModule, alerts *alertsModule, telemetry *telemetryModule, aiops *aiopsModule, runtime *runtimeModule, automation *automationModule, http *httpModule) error {
	eg, egCtx := errgroup.WithContext(rootCtx)
	// PR-F: legacy metricIngester.Start flush loop removed — no MySQL writes.
	// eg.Go(func() error { return metricIngester.Start(egCtx) })
	eg.Go(func() error { return http.apiServer.Start(egCtx) })
	eg.Go(func() error { return http.metricsServer.Start(egCtx) })
	if devices.edgeUpgradeJobUC != nil {
		eg.Go(func() error {
			devices.edgeUpgradeJobUC.Run(egCtx)
			return nil
		})
	}

	// ADR-026: DB pool sampler ticks every 10s. database/sql.DBStats is
	// the canonical source for OpenConnections / InUse / Idle / WaitCount
	// — same numbers the runtime would print under pprof. We pull through
	// gorm's underlying *sql.DB. WaitCount is a monotone counter inside
	// database/sql, so we expose the delta (DBStats already accumulates).
	eg.Go(func() error { return prom.RunDBPoolSampler(egCtx, storage.sqlDB, storage.errDB, log) })

	// HLD-010: audit retention sweep — drops audit_logs rows older than
	// auditRetentionDays once a day at 03:00. Disabled when retention=0.
	eg.Go(func() error { return settings.auditUC.RunRetention(egCtx, settings.auditRetentionDays) })
	eg.Go(func() error { return managersvck8s.RunEventRetention(egCtx, devices.k8sUC, log) })
	eg.Go(func() error { return managersvck8s.RunTopologyReconcile(egCtx, devices.k8sUC, log) })
	if telemetry.apmProm != nil {
		eg.Go(func() error {
			return managersvctopology.RunServiceReconcile(egCtx, telemetry.apmService, devices.topologyUC, log)
		})
	}

	// ADR-026: chatruntime worker session sampler — surfaces orphan
	// worker accumulation as a gauge. The 161-orphan incident (v0.7.44)
	// would have lit up here at running > 10 for hours. Interval is 15s
	// because workers are short-lived and an orphan is unusual; faster
	// polling would just thrash the mutex.
	if runtime.aiopsRuntime != nil {
		eg.Go(func() error { return managersvcaiops.RunWorkerMetrics(egCtx, runtime.aiopsRuntime) })
	}

	// Packet captures outlive the HTTP/SSE turn that created them. Register
	// their domain reconciler with the generic Operation coordinator; future
	// operation kinds share this scheduler instead of adding another loop.
	eg.Go(func() error {
		return managersvcaiops.RunPacketCaptureOperations(egCtx, aiops.operationUC, aiops.packetCaptureUC, aiops.aiopsRepo, log)
	})

	// RCA investigator inflight gauge — samples concurrency cap usage.
	// Same 15s cadence as the worker sampler; cheap channel-len read.
	if automation.rcaInvConcrete != nil {
		eg.Go(func() error { return managersvcalert.RunInvestigatorMetrics(egCtx, automation.rcaInvConcrete) })
	}

	// Edge/device presence reconciler: per-event MarkOnline/MarkOffline can't
	// flip a device offline when its edge no longer exists (hard delete) or
	// re-registered under a new fingerprint, and a manager restart while an
	// edge is offline leaves the denormalised flag stale. Frontier can also
	// miss a final disconnect event after an abrupt host deletion, so mark
	// stale edge rows offline before syncing device presence. Runs once at
	// boot, then every 60s. See #145.
	eg.Go(func() error {
		return managersvcedge.RunPresenceReconcile(egCtx, devices.edgeUC, devices.deviceUC, devices.edgeOfflineThreshold, log)
	})

	// Pipeline evaluator: runs metric_raw / metric_anomaly /
	// metric_forecast / metric_burn_rate rules on a ticker. Also refreshes
	// the edge_last_seen_seconds_ago gauge (replacement
	// for edge_absence). PromQuerier is nil-safe — deployments without
	// cloud Prom skip every metric_* rule and just keep the gauge fresh.
	var alertPromQuerier managerbizalert.PromQuerier
	if telemetry.promQueryClient != nil {
		alertPromQuerier = telemetry.promQueryClient
	}
	if cfg.Alert.Enabled {
		eg.Go(func() error { return alerts.alertRules.Loop(egCtx) })
		// Legacy log_match / log_volume kinds still need a Loki client.
		// New log_search rules use logsBackendSvc below and therefore follow
		// the same Loki/Elasticsearch history plan as the Logs UI.
		pipelineEval := managerbizalert.NewPipelineEvaluator(managerbizalert.PipelineEvaluatorOpts{
			Usecase:                alerts.alertUC,
			Rules:                  alerts.alertRules,
			Notifier:               notifyRouter,
			Resolver:               alerts.alertResolver,
			Inhibitor:              alerts.alertInhibitor,
			DefaultChannels:        cfg.Notification.DefaultChannels,
			Cooldown:               cfg.Alert.Cooldown,
			Interval:               cfg.Alert.EvaluatorInterval,
			EdgeLister:             devices.edgeUC,
			PromQuerier:            alertPromQuerier,
			LogQuerier:             telemetry.lokiLogClient,
			LogSearcher:            telemetry.logsBackendSvc,
			DeviceIdentityResolver: managersvcalert.DeviceIdentityResolver(devices.deviceUC),
			Log:                    log.With(slog.String("comp", "alert-pipeline")),
		})
		eg.Go(func() error { return pipelineEval.Loop(egCtx) })

		// Delivery retry worker drains failed notification_deliveries with
		// linear backoff (delivery_tracking).
		retryWorker := managerbizalert.NewRetryWorker(managerbizalert.RetryWorkerOpts{
			Repo:        alerts.alertRepo,
			Notifier:    notifyRouter,
			Resolver:    alerts.alertResolver,
			Usecase:     alerts.alertUC,
			MaxAttempts: 5,
			Tick:        cfg.Alert.EvaluatorInterval,
			Log:         log.With(slog.String("comp", "alert-retry")),
		})
		eg.Go(func() error { return retryWorker.Loop(egCtx) })
	}

	// Optional crons: wire when ready to enable (leave commented for now).
	// metricDownsampler := managerbizmetric.NewDownsampler(metricWriter, metricReader, log)
	// eg.Go(func() error { return metricDownsampler.Loop(egCtx) })
	// metricRetention := managerbizmetric.NewRetention(metricWriter, log)
	// eg.Go(func() error { return metricRetention.Loop(egCtx) })

	err := eg.Wait()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = shutCtx

	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("shutdown with error", slog.Any("err", err))
		return err
	}
	log.Info("ongrid shutdown complete")

	return nil
}
