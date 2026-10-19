package main

import (
	"context"
	"log/slog"
	"time"

	managerbizagenttrace "github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	managerbizapm "github.com/ongridio/ongrid/internal/manager/biz/apm"
	managerbizlogs "github.com/ongridio/ongrid/internal/manager/biz/logs"
	managerbizpromwrite "github.com/ongridio/ongrid/internal/manager/biz/promwrite"
	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	manageraiopsdata "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	managerdevicedata "github.com/ongridio/ongrid/internal/manager/data/device/store"
	managerlogsdata "github.com/ongridio/ongrid/internal/manager/data/logs/store"
	managerserveragenttrace "github.com/ongridio/ongrid/internal/manager/server/agenttrace"
	managerserverapm "github.com/ongridio/ongrid/internal/manager/server/apm"
	managerserverintegration "github.com/ongridio/ongrid/internal/manager/server/integration"
	managerserverlogs "github.com/ongridio/ongrid/internal/manager/server/logs"
	managerservermetric "github.com/ongridio/ongrid/internal/manager/server/metric"
	managerserverprofiles "github.com/ongridio/ongrid/internal/manager/server/profiles"
	managerservertraces "github.com/ongridio/ongrid/internal/manager/server/traces"
	managersvclogs "github.com/ongridio/ongrid/internal/manager/service/logs"
	managersvcsystemhealth "github.com/ongridio/ongrid/internal/manager/service/systemhealth"
	managersvctelemetry "github.com/ongridio/ongrid/internal/manager/service/telemetry"
	"github.com/ongridio/ongrid/internal/pkg/config"
	pkglogquery "github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
	pkgpromquery "github.com/ongridio/ongrid/internal/pkg/promquery"
	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
	pkgtracequery "github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type telemetryModule struct {
	integrationHandler  *managerserverintegration.Handler
	promQueryClient     *pkgpromquery.Client
	promwriteIngester   *managerbizpromwrite.Ingester
	promTester          managersvcsystemhealth.PromQuerier
	lokiProbe           *managerbizsetting.LokiURLProbe
	tempoReadinessProbe *managerbizsetting.TempoReadinessProbe
	metricHandler       *managerservermetric.PromHandler
	lokiLogClient       *pkglogquery.RuntimeClient
	logsBackendSvc      *managerbizlogs.Service
	logsHandler         *managerserverlogs.Handler
	tracesHandler       *managerservertraces.Handler
	traceQuery          *managerbizagenttrace.ScopedQuerier
	profilesHandler     *managerserverprofiles.Handler
	apmProm             managerbizapm.PromQuerier
	apmService          *managerbizapm.Service
	apmHandler          *managerserverapm.Handler
	agentTraceService   *managerbizagenttrace.Service
	agentTraceHandler   *managerserveragenttrace.Handler
}

func bootstrapTelemetry(rootCtx context.Context, cfg *config.Config, log *slog.Logger, storage *storageModule, settings *settingsModule, models *modelsModule, devices *devicesModule, alerts *alertsModule) (*telemetryModule, error) {
	// Cloud-side Prometheus. When disabled, all three handles
	// stay nil; downstream wiring is nil-safe (push_prom_samples silently
	// drops, query_promql tool is not registered).
	var (
		promwriteClient   *pkgpromwrite.Client
		promQueryClient   *pkgpromquery.Client
		promwriteIngester *managerbizpromwrite.Ingester
	)
	if cfg.Prom.Enabled {
		// One resolver, three roles: implements promauth.Resolver (auth),
		// promwrite.EndpointResolver (write URL), promquery.BaseURLResolver
		// (query URL). All three read from system_settings.{prom} on every
		// call, with the env-derived URLs in cfg.Prom acting as fallback
		// when the DB rows are absent. UI saves take effect within ~5s
		// without a manager restart — the prom clients re-resolve on each
		// request and the round-tripper has its own 5s cache.
		promHTTPClient, herr := promauth.BuildClient(
			promauth.TLSConfig{
				Insecure: cfg.Prom.TLSInsecure,
				CAPath:   cfg.Prom.TLSCAPath,
			},
			settings.promResolver,
			30*time.Second,
		)
		if herr != nil {
			log.Error("prom http client build", slog.Any("err", herr))
			return nil, herr
		}
		promwriteClient = pkgpromwrite.NewWithResolverAndHTTPClient(settings.promResolver, promHTTPClient, log.With(slog.String("comp", "promwrite")))
		promQueryClient = pkgpromquery.NewWithResolverAndHTTPClient(settings.promResolver, promHTTPClient, log.With(slog.String("comp", "promquery")))
		promwriteIngester = managerbizpromwrite.NewIngester(
			promwriteClient,
			log.With(slog.String("comp", "promwrite-ingest")),
		)
		log.Info("prom enabled",
			slog.String("query_fallback", settings.queryFallback),
			slog.String("write_fallback", cfg.Prom.RemoteWriteURL),
			slog.Bool("tls_insecure", cfg.Prom.TLSInsecure),
			slog.String("note", "URLs, auth and TLS hot-reload from system_settings"),
		)
	} else {
		log.Warn("prom disabled — push_prom_samples will be silently dropped, query_promql tool not registered, /v1/edges/{id}/metrics returns 501")
	}

	// Build the integration handler now that we know whether Prometheus is
	// enabled. The probe uses the unsaved UI draft and never mutates settings.
	var promProbe managerserverintegration.PromConfigProbe
	var promTester managersvcsystemhealth.PromQuerier
	if promQueryClient != nil {
		promProbe = managerbizsetting.NewPromConfigurationProbe(log.With(slog.String("comp", "prom-config-probe")))
		promTester = healthPromQuerier{promQueryClient}
	}
	// Loki / Tempo URL probes — back the Integrations "测试连接" buttons.
	// Loki checks /ready; Tempo checks either a query URL or an explicit
	// edge-facing OTLP/HTTP endpoint. Tempo's manager-side query readiness is
	// wired separately below because standard deployments use another port.
	lokiProbe := managerbizsetting.NewLokiURLProbe(settings.lokiResolver)
	tempoIngestProbe := managerbizsetting.NewTempoURLProbe(settings.tempoResolver)
	tempoReadinessProbe := managerbizsetting.NewTempoReadinessProbe(cfg.Traces.URL)
	// Web search probe — same WebSearchResolver the skill uses, so a
	// passing probe means the skill itself will work.
	webSearchProbe := managerbizsetting.NewWebSearchProbe(managerbizsetting.NewWebSearchResolver(settings.settingSvc))
	integrationHandler := managerserverintegration.NewHandler(settings.grafanaSvc, promProbe, lokiProbe, tempoIngestProbe, webSearchProbe)
	integrationHandler.SetLLMRouter(models.llmRouter)
	integrationHandler.SetLLMProbe(managerbizsetting.NewLLMConfigurationService(models.llmEnvDefaults, settings.settingSvc))

	// Prom-backed metric read handler (PR-F replacement for the MySQL
	// fast path). When prom is disabled the handler still installs but
	// returns 501 so the UI can degrade gracefully.
	var metricPromQuerier managerservermetric.PromQuerier
	if promQueryClient != nil {
		metricPromQuerier = promQueryClient
	}
	metricHandler := managerservermetric.NewPromHandler(metricPromQuerier, hostDeviceResolverAdapter{devices.edgeDeviceRepo})

	// Log control/query plane. Loki remains the built-in default; selecting an
	// external Elasticsearch configuration routes every log query directly to
	// Elasticsearch. Log payloads never traverse this service; Edge collectors
	// still write directly to the selected data-plane endpoint.
	lokiLogClient := pkglogquery.NewRuntimeClient(
		managersvctelemetry.NewLokiQueryEndpointResolver(settings.lokiResolver),
		log.With(slog.String("comp", "logquery")),
	)
	logsBackendRepo := managerlogsdata.NewRepo(storage.db)
	logsBackendSvc := managerbizlogs.NewService(logsBackendRepo, storage.secretUC, lokiLogClient, log.With(slog.String("comp", "logs-backend")))
	logsBackendSvc.SetLogAlertMigrator(alerts.alertUC)
	logsBackendSvc.SetHostDeviceResolver(devices.edgeDeviceRepo)
	logsBackendSvc.SetLokiTargetResolver(managersvclogs.NewLokiTargetResolver(devices.pluginEndpointResolver))
	logsBackendSvc.SetConnectionEdgeInventory(managersvclogs.NewConnectionEdgeInventory(devices.edgeUC, devices.pluginConfigUC))
	logsBackendSvc.SetGrafanaSyncer(settings.grafanaSvc)
	settings.grafanaSvc.SetLogsDatasourceProvider(logsBackendSvc.SelectedElasticsearchDatasource)
	go managersvclogs.SyncGrafanaAtBoot(rootCtx, settings.grafanaSvc, log)
	devices.pluginConfigUC.SetRuntimeOverlayProvider(managersvclogs.NewRuntimeOverlayProvider(logsBackendSvc, devices.edgeDeviceRepo, devices.deviceUC, devices.topologyUC))
	logsHandler := managerserverlogs.NewHandlerWithServices(lokiLogClient, logsBackendSvc, logsBackendSvc)

	// Tempo query proxy. Mirrors the Loki block above — same role for the
	// trace signal. Enables the in-product Traces page to run TraceQL /
	// facet searches without exposing Tempo's /api/* read paths through
	// nginx. The data plane /v1/traces ingest route stays auth_request-
	// gated for OTLP push only —
	var tracesHandler *managerservertraces.Handler
	var traceQuery *managerbizagenttrace.ScopedQuerier
	if cfg.Traces.URL != "" {
		traceQuery = managerbizagenttrace.NewScopedQuerier(
			pkgtracequery.New(cfg.Traces.URL, log.With(slog.String("comp", "tracequery"))),
			manageraiopsdata.NewSessionRepo(storage.db),
		)
		tracesHandler = managerservertraces.NewHandler(traceQuery)
	} else {
		// Tempo disabled — handler installs but every route returns 503.
		tracesHandler = managerservertraces.NewHandler(nil)
	}
	profilesHandler := managerserverprofiles.NewHandler(cfg.Profiles.URL)
	var apmProm managerbizapm.PromQuerier
	if promQueryClient != nil {
		apmProm = promQueryClient
	}
	var apmTraces managerbizapm.TraceQuerier
	if traceQuery != nil {
		apmTraces = traceQuery
	}
	apmService := managerbizapm.New(apmProm, apmTraces, logsBackendSvc).WithClusterScopes(devices.topologyUC)
	apmHandler := managerserverapm.NewHandler(apmService, log)
	agentTraceService := managerbizagenttrace.New(apmTraces)
	agentTraceHandler := managerserveragenttrace.NewHandler(agentTraceService, log)

	return &telemetryModule{
		integrationHandler:  integrationHandler,
		promQueryClient:     promQueryClient,
		promwriteIngester:   promwriteIngester,
		promTester:          promTester,
		lokiProbe:           lokiProbe,
		tempoReadinessProbe: tempoReadinessProbe,
		metricHandler:       metricHandler,
		lokiLogClient:       lokiLogClient,
		logsBackendSvc:      logsBackendSvc,
		logsHandler:         logsHandler,
		tracesHandler:       tracesHandler,
		traceQuery:          traceQuery,
		profilesHandler:     profilesHandler,
		apmProm:             apmProm,
		apmService:          apmService,
		apmHandler:          apmHandler,
		agentTraceService:   agentTraceService,
		agentTraceHandler:   agentTraceHandler,
	}, nil
}

type hostDeviceResolverAdapter struct {
	repo *managerdevicedata.EdgeDeviceRepo
}

type healthPromQuerier struct{ client *pkgpromquery.Client }

func (q healthPromQuerier) Query(ctx context.Context, expr string, ts time.Time) (any, error) {
	return q.client.Query(ctx, expr, ts)
}

func (a hostDeviceResolverAdapter) ResolveHostDeviceID(ctx context.Context, edgeID uint64) (uint64, error) {
	return a.repo.LookupHostDevice(ctx, edgeID)
}
