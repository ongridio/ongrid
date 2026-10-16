package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	managerbizaudit "github.com/ongridio/ongrid/internal/manager/biz/audit"
	managerbizgrafana "github.com/ongridio/ongrid/internal/manager/biz/grafana"
	managerbizmonitor "github.com/ongridio/ongrid/internal/manager/biz/monitor"
	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	manageraudtdata "github.com/ongridio/ongrid/internal/manager/data/audit/store"
	managermonitordata "github.com/ongridio/ongrid/internal/manager/data/monitor/store"
	managersettingdata "github.com/ongridio/ongrid/internal/manager/data/setting/store"
	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	managerservermonitor "github.com/ongridio/ongrid/internal/manager/server/monitor"
	managerserversetting "github.com/ongridio/ongrid/internal/manager/server/setting"
	managersvcmonitor "github.com/ongridio/ongrid/internal/manager/service/monitor"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
)

type settingsModule struct {
	settingSvc         *managerbizsetting.Service
	queryFallback      string
	promResolver       *managerbizsetting.PromResolver
	auditUC            *managerbizaudit.Usecase
	auditRetentionDays int
	lokiResolver       *managerbizsetting.LokiResolver
	tempoResolver      *managerbizsetting.TempoResolver
	settingHandler     *managerserversetting.Handler
	grafanaSvc         *managerbizgrafana.Service
	monitorHandler     *managerservermonitor.Handler
}

func bootstrapSettings(rootCtx context.Context, cfg *config.Config, log *slog.Logger, storage *storageModule) *settingsModule {
	// system_settings BC: admin-editable runtime config (LLM creds today,
	// more later). The service is consulted by the LLM client on every
	// Chat() call via a Resolver, with an internal TTL cache so the DB
	// round-trip is cheap. Env-derived values seed the DB only if no row
	// exists yet, so previous admin edits survive restarts.
	settingRepo := managersettingdata.NewRepo(storage.db)
	settingSvc := managerbizsetting.New(settingRepo, log.With(slog.String("comp", "setting")))
	if storage.upgradingExistingInstall {
		managersettingdata.SeedInfrastructureMenuDefaults(rootCtx, storage.db, settingSvc, log)
	}
	queryFallback := cfg.Prom.URL
	if cfg.Prom.QueryURL != "" {
		queryFallback = cfg.Prom.QueryURL
	}
	promResolver := managerbizsetting.NewPromResolver(settingSvc, queryFallback, cfg.Prom.RemoteWriteURL, promauth.TLSConfig{
		Insecure: cfg.Prom.TLSInsecure,
		CAPath:   cfg.Prom.TLSCAPath,
	})

	// HLD-010 audit log — append-only "who did what" trail. Built early
	// so the auth middleware factory below can capture login attempts.
	// Retention is 180 days by default; ONGRID_AUDIT_RETENTION_DAYS=0
	// disables the sweep entirely (operator manages archival externally).
	auditRepo := manageraudtdata.New(storage.db)
	auditUC := managerbizaudit.New(auditRepo, log.With(slog.String("comp", "audit")))
	auditRetentionDays := 180
	if v := os.Getenv("ONGRID_AUDIT_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			auditRetentionDays = n
		}
	}
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryLLM, settingmodel.KeyOpenAIAPIKey, cfg.OpenAI.APIKey, true); err != nil {
		log.Warn("seed llm api key", slog.Any("err", err))
	}
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryLLM, settingmodel.KeyOpenAIModel, cfg.OpenAI.Model, false); err != nil {
		log.Warn("seed llm model", slog.Any("err", err))
	}
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryLLM, settingmodel.KeyOpenAIBaseURL, cfg.OpenAI.BaseURL, false); err != nil {
		log.Warn("seed llm base url", slog.Any("err", err))
	}
	// Seed the platform default once so discovery ingestion reads the cached
	// setting instead of querying for an absent row on every Edge report.
	// SetIfAbsent preserves an administrator's explicit global opt-out.
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryPlatform, settingmodel.KeyNetworkDiscoveryEnabled, "true", false); err != nil {
		log.Warn("seed network discovery setting", slog.Any("err", err))
	}
	// Prom seeds. URLs are first-boot only — admin edits in UI persist;
	// auth fields are blank by default (env can override at boot).
	for _, seed := range []struct {
		key       string
		val       string
		sensitive bool
	}{
		{settingmodel.KeyPromQueryURL, cfg.Prom.QueryURL, false},
		{settingmodel.KeyPromRemoteWriteURL, cfg.Prom.RemoteWriteURL, false},
		{settingmodel.KeyPromTLSInsecure, strconv.FormatBool(cfg.Prom.TLSInsecure), false},
	} {
		if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryProm, seed.key, seed.val, seed.sensitive); err != nil {
			log.Warn("seed prom setting", slog.String("key", seed.key), slog.Any("err", err))
		}
	}
	// Grafana seed. Out of the box the manager points at the embedded
	// Grafana on the docker network. When bootstrap admin creds are
	// provided, startup creates an SA token automatically; otherwise the
	// admin can paste one in the UI. SetIfAbsent honors prior admin edits
	// across restarts.
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaRootURL, cfg.Grafana.InternalRootURL, false); err != nil {
		log.Warn("seed grafana root_url", slog.Any("err", err))
	}
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaTLSInsecure, strconv.FormatBool(cfg.Grafana.TLSInsecure), false); err != nil {
		log.Warn("seed grafana tls_insecure", slog.Any("err", err))
	}
	// Loki / Tempo seeds. Mirrors the Prom seed pattern — first-boot
	// only, admin edits in UI persist across restarts. The URL is the
	// only field we seed; auth and TLS stay blank by default since the
	// embedded loki/tempo containers don't authenticate.
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryLoki, settingmodel.KeyLokiURL, cfg.Logs.URL, false); err != nil {
		log.Warn("seed loki url", slog.Any("err", err))
	}
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryTempo, settingmodel.KeyTempoURL, cfg.Traces.URL, false); err != nil {
		log.Warn("seed tempo url", slog.Any("err", err))
	}
	// WebSearch seeds. Default provider = SearXNG (zero-config baseline),
	// pointing at the docker-internal http://searxng:8080. SetIfAbsent
	// preserves any prior admin choice across restarts.
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryWebSearch, settingmodel.KeyWebSearchProvider, settingmodel.ProviderSearxng, false); err != nil {
		log.Warn("seed websearch provider", slog.Any("err", err))
	}
	if err := settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryWebSearch, settingmodel.KeySearxngURL, settingmodel.DefaultSearxngURL, false); err != nil {
		log.Warn("seed searxng url", slog.Any("err", err))
	}
	// Resolvers used by PluginConfigUC and integration test endpoints.
	// They route through settingSvc.Get (60s cache), so admin UI saves
	// take effect on the edge's next reload (push or 60s safety-net poll).
	lokiResolver := managerbizsetting.NewLokiResolver(settingSvc, cfg.Logs.URL)
	tempoResolver := managerbizsetting.NewTempoResolver(settingSvc, cfg.Traces.URL)
	settingHandler := managerserversetting.NewHandler(settingSvc, managerbizsetting.NewObservabilityApplierFromEnv(settingSvc, log))

	// Grafana integration biz layer (PR-2). Wraps the pkg/grafana HTTP
	// client and reads creds from system_settings on every Test/Sync call.
	grafanaSvc := managerbizgrafana.New(settingSvc, cfg.Grafana.TLSInsecure, log.With(slog.String("comp", "grafana")))
	// Monitor-page mirror dashboard uid; ongrid → Grafana one-way sync of
	// user-managed PromQL panels. Override via env when the operator wants
	// to keep our managed dashboard out of an existing uid namespace.
	if v := os.Getenv("ONGRID_GRAFANA_PANEL_DASHBOARD_UID"); v != "" {
		grafanaSvc.SetPanelDashboardUID(v)
	}

	// Monitor BC: user-managed Monitor-page panels. Persists to MySQL
	// (monitor_panels) and asynchronously mirrors every change into the
	// ongrid-monitor Grafana dashboard via grafanaSvc.SyncMonitorPanels.
	// Sync failures don't block API 200 — see biz/monitor/service.go.
	monitorRepo := managermonitordata.NewRepo(storage.db)
	monitorSvc := managerbizmonitor.New(monitorRepo, grafanaSvc, log.With(slog.String("comp", "monitor")))
	monitorHandler := managerservermonitor.NewHandler(monitorSvc)

	// Embedded-Grafana SA bootstrap. Runs in a goroutine because:
	//   1. Grafana container often isn't healthy yet when manager boots
	//      (compose only enforces ordering, not readiness)
	//   2. We don't want to block the API listener — bootstrap failure is
	//      non-fatal; admin can still configure manually via UI
	// The service short-circuits if the token is already set, so retries
	// across restarts are safe.
	if cfg.Grafana.BootstrapPassword != "" {
		go managersvcmonitor.BootstrapGrafana(rootCtx, grafanaSvc, monitorSvc, cfg.Grafana.BootstrapUser, cfg.Grafana.BootstrapPassword, log)
	}

	return &settingsModule{
		settingSvc:         settingSvc,
		queryFallback:      queryFallback,
		promResolver:       promResolver,
		auditUC:            auditUC,
		auditRetentionDays: auditRetentionDays,
		lokiResolver:       lokiResolver,
		tempoResolver:      tempoResolver,
		settingHandler:     settingHandler,
		grafanaSvc:         grafanaSvc,
		monitorHandler:     monitorHandler,
	}
}
