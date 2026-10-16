package main

import (
	"context"
	"log/slog"

	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	managerbizmetric "github.com/ongridio/ongrid/internal/manager/biz/metric"
	manageralertdata "github.com/ongridio/ongrid/internal/manager/data/alert/store"
	managermetricdata "github.com/ongridio/ongrid/internal/manager/data/metric/store"
	managersvcmetric "github.com/ongridio/ongrid/internal/manager/service/metric"
	"github.com/ongridio/ongrid/internal/pkg/config"
)

type alertsModule struct {
	alertRepo       *manageralertdata.Repo
	alertUC         *managerbizalert.Usecase
	alertRules      *managerbizalert.CachedRulesProvider
	alertResolver   *managerbizalert.DBChannelResolver
	alertInhibitor  *managerbizalert.BuiltinInhibitor
	metricIngestSvc *managerbizalert.NoopHostMetricIngester
}

func bootstrapAlerts(rootCtx context.Context, cfg *config.Config, log *slog.Logger, storage *storageModule) *alertsModule {
	// PR-F: MySQL fast path commented out — single source of truth is now
	// cloud Prometheus. Edges still emit push_host_metrics for backward
	// compat (NoopHostMetricIngester drops the batch); host-metric alerts
	// are evaluated by the Prom-backed PipelineEvaluator on its 30s ticker.
	// No MySQL writes happen and no /v1/edges/{id}/metrics MySQL handler
	// is registered. The Prom-backed handler below replaces it.
	//
	// metricWriter := managermetricdata.NewBizWriter(db)
	// metricReader := managermetricdata.NewBizReader(db)
	// metricIngester := managerbizmetric.NewIngester(metricWriter, reg, log)
	_ = managermetricdata.NewBizReader // keep imports alive while file is in tree
	_ = managerbizmetric.NewIngester

	// Alert subdomain — incident lifecycle, silence consumption, delivery
	// persistence. The host metric decorator below feeds the
	// firing path; the pipeline evaluator (started below) layers in
	// pipeline-health rules on the same usecase.
	alertRepo := manageralertdata.NewRepo(storage.db)
	alertUC := managerbizalert.NewUsecase(alertRepo, log.With(slog.String("comp", "alert")))
	if err := manageralertdata.SeedChannelsFromConfig(rootCtx, alertRepo, cfg.Notification); err != nil {
		log.Warn("seed notification channels", slog.Any("err", err))
	}
	if err := manageralertdata.SeedBuiltinRules(rootCtx, alertRepo, cfg.Alert); err != nil {
		log.Warn("seed builtin alert rules", slog.Any("err", err))
	}
	alertRules := managerbizalert.NewCachedRulesProvider(
		alertRepo,
		cfg.Alert.EvaluatorInterval,
		log.With(slog.String("comp", "alert-rules")),
	)
	if err := alertRules.Refresh(rootCtx); err != nil {
		log.Warn("alert rules initial refresh", slog.Any("err", err))
	}
	alertUC.SetRuleCacheRefresher(alertRules)
	alertResolver := managerbizalert.NewDBChannelResolver(alertRepo, cfg.Notification.DefaultChannels)
	// Honour rule-level notify_channel_ids overrides — resolver looks
	// the rule up by key and reads its NotifyChannelIDsJSON.
	alertResolver.SetRuleLookup(alertRepo.GetRuleByKey)
	alertInhibitor := managerbizalert.NewBuiltinInhibitor(alertRepo)
	// Lifecycle alerting path was removed in — every
	// "edge offline" alert is now a metric_raw rule on the
	// edge_last_seen_seconds_ago gauge that PipelineEvaluator refreshes
	// every tick. Detection delay = 1× evaluator interval (default 30s).

	//-final collapse: HostMetricDecorator is gone. Every
	// host-metric threshold alert is a metric_raw rule the
	// PipelineEvaluator runs against Prom on its 30s ticker. The
	// push_host_metrics tunnel handler is still wired (legacy edge
	// agents) but we no longer evaluate alerts inline; the no-op
	// ingester just accepts the batch so edges back off cleanly.
	// New edges write directly to Prom via push_prom_samples.
	metricIngestSvc := managerbizalert.NewNoopHostMetricIngester()
	// PR-F: legacy MySQL-backed metric service + handler removed from the
	// router. Replacement registered after promQueryClient is constructed.
	// metricQuery := managerbizmetric.NewQueryUsecase(metricReader, log)
	// metricSvc := managersvcmetric.New(metricIngester, metricQuery, log)
	// metricHandler := managerservermetric.NewHandler(metricSvc)
	_ = managersvcmetric.New

	return &alertsModule{
		alertRepo:       alertRepo,
		alertUC:         alertUC,
		alertRules:      alertRules,
		alertResolver:   alertResolver,
		alertInhibitor:  alertInhibitor,
		metricIngestSvc: metricIngestSvc,
	}
}
