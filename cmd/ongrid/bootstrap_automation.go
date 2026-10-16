package main

import (
	"context"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	aiopsinvestigator "github.com/ongridio/ongrid/internal/manager/biz/aiops/investigator"
	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	managerbizflow "github.com/ongridio/ongrid/internal/manager/biz/flow"
	managerbizreport "github.com/ongridio/ongrid/internal/manager/biz/report"
	manageralertdata "github.com/ongridio/ongrid/internal/manager/data/alert/store"
	managerflowdata "github.com/ongridio/ongrid/internal/manager/data/flow/store"
	managerreportdelivery "github.com/ongridio/ongrid/internal/manager/data/report"
	managerreportdata "github.com/ongridio/ongrid/internal/manager/data/report/store"
	managerserverflow "github.com/ongridio/ongrid/internal/manager/server/flow"
	managerserverreport "github.com/ongridio/ongrid/internal/manager/server/report"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	managersvcflow "github.com/ongridio/ongrid/internal/manager/service/flow"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/prometheus/client_golang/prometheus"
)

type automationModule struct {
	rcaInvConcrete *investigator.Usecase
	reportHandler  *managerserverreport.Handler
	flowInvoker    *managersvcflow.ToolInvoker
	flowUC         *managerbizflow.Usecase
	flowHandler    *managerserverflow.Handler
}

func bootstrapAutomation(rootCtx context.Context, cfg *config.Config, log *slog.Logger, reg *prometheus.Registry, notifyRouter *notify.Router, cleanup *startupCleanup, storage *storageModule, settings *settingsModule, models *modelsModule, alerts *alertsModule, telemetry *telemetryModule, aiops *aiopsModule, runtime *runtimeModule) *automationModule {
	// Two-tier proactive investigation wiring:
	//
	//   1. The ai_initial_diagnosis emitter (~3 paragraphs on
	//      the alert timeline, lightweight, single LLM call via
	//      correlate_incident) — fast read for operators glancing at
	//      the incident timeline.
	//
	//   2. The new structured-report investigator (PR-2): spawns the
	//      incident-investigator chatruntime worker, persists the full
	//      transcript as kind='investigation' session, writes a row to
	//      investigation_reports for the IncidentDetail page. Gated by
	//      ONGRID_INVESTIGATOR_ENABLED=true (default off — heavy LLM
	//      cost; only flip when operators want auto-RCA).
	//
	// Both share the alert.Investigator interface and chain via
	// investigatorChain so each new-fire fans out to both. Either side
	// can be nil — the chain skips nil.
	var initialInv managerbizalert.Investigator
	if hasConfiguredLLMProvider(models.llmRouter) {
		initial := aiopsinvestigator.New(
			models.llmClient,
			managersvcaiops.NewBackgroundToolInvoker(aiops.toolsReg.BuildBaseTools().AllTools()),
			alerts.alertRepo,
			aiopsinvestigator.Config{},
			log,
		)
		cleanup.add(initial.Close)
		initialInv = initial
		log.Info("alert: AI initial-diagnosis investigator wired",
			slog.String("model_source", "llm_default"))
	} else {
		log.Info("alert: AI investigator disabled (no LLM provider)")
	}

	var (
		rcaInv managerbizalert.Investigator
		// rcaInvConcrete keeps the *investigator.Usecase handle so the
		// manual-trigger HTTP endpoint (POST /v1/alerts/incidents/{id}/investigation)
		// can call Enqueue directly. The alert.Investigator interface only
		// exposes InvestigateAsync which discards ctx; the trigger needs
		// the richer Enqueue signature.
		rcaInvConcrete *investigator.Usecase
	)
	if os.Getenv("ONGRID_INVESTIGATOR_ENABLED") == "true" {
		concreteRt := runtime.aiopsRuntime
		if concreteRt == nil {
			log.Warn("structured RCA investigator skipped: chatruntime runtime not available")
		} else {
			invRepo := manageralertdata.NewInvestigationRepo(storage.db)
			maxCC := 5
			if v := os.Getenv("ONGRID_INVESTIGATOR_MAX_CONCURRENT"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					maxCC = n
				}
			}
			// Empty provider/model means the extractor follows the live
			// llm.MultiClient default — the same default_provider +
			// <provider>_default_model the home-page picker writes and
			// invalidateLLMRouter refreshes. Env vars remain an explicit
			// operator override for rare cases.
			sumProvider := os.Getenv("ONGRID_INVESTIGATOR_SUMMARIZER_PROVIDER")
			sumModel := os.Getenv("ONGRID_INVESTIGATOR_SUMMARIZER_MODEL")
			rcaInvConcrete = investigator.NewUsecase(invRepo, concreteRt, models.llmClient, investigator.Config{
				Enabled:            true,
				MinSeverity:        firstNonEmpty(os.Getenv("ONGRID_INVESTIGATOR_MIN_SEVERITY"), "warning"),
				DedupWindow:        5 * time.Minute,
				WorkerTimeout:      5 * time.Minute,
				AgentName:          "incident-investigator",
				SummarizerModel:    sumModel,
				SummarizerProvider: sumProvider,
				SummarizerTimeout:  30 * time.Second,
				MaxConcurrent:      maxCC,
				// Fall-back language for auto-fire + backfill (no request
				// context, no Accept-Language). Manual triggers override per
				// request. Default "en" so a fresh deployment matches the
				// English SPA by default; ops sets ONGRID_DEFAULT_LOCALE=zh
				// for an explicitly Chinese-default install.
				// See [[feedback_ai_output_locale]].
				DefaultLocale: firstNonEmpty(os.Getenv("ONGRID_DEFAULT_LOCALE"), "en"),
			}, log)
			// Same InvestigationRepo also implements the
			// related-alerts query (same DB handle, different method).
			rcaInvConcrete = rcaInvConcrete.
				WithRelatedQuerier(invRepo).
				// Salvage seam: when the worker hits the eino
				// MaxStep cap, read its partial trail back from
				// chat_messages and synthesise a low-confidence
				// report instead of an empty failure.
				WithMessageReader(aiops.aiopsRepo).
				WithLocaleResolver(settings.settingSvc)
			rcaInv = rcaInvConcrete
			log.Info("alert: structured RCA investigator wired",
				slog.String("summarizer_provider", firstNonEmpty(sumProvider, "llm_default")),
				slog.String("summarizer_model", firstNonEmpty(sumModel, "llm_default")))
		}
	}

	if chained := managerbizalert.ChainInvestigators(initialInv, rcaInv); chained != nil {
		alerts.alertUC.SetInvestigator(chained)
	}

	// Report scheduler + API (HLD-014): scheduled operational reports.
	// Routes are mounted even when the LLM runtime is unavailable, so the
	// UI can list existing reports and surface a clear not-configured
	// error on generate instead of a route-level 404.
	reportRepo := managerreportdata.NewRepo(storage.db)
	var reportGen managerbizreport.Generator
	reportSchedulerReady := false
	if reportRT := runtime.aiopsRuntime; reportRT != nil {
		// Pass the prom query client for fleet resource trends; a typed-nil
		// guard mirrors the tools wiring so a missing client stays a clean
		// untyped nil (collector degrades Resource.Available=false).
		var reportProm managerreportdata.PromQuerier
		if telemetry.promQueryClient != nil {
			reportProm = telemetry.promQueryClient
		}
		reportGen = managerbizreport.NewWorkerGenerator(
			reportRepo,
			managerreportdata.NewFactsCollector(storage.db, reportProm),
			reportRT,
			managerbizreport.GeneratorConfig{
				DefaultLocale:   firstNonEmpty(os.Getenv("ONGRID_DEFAULT_LOCALE"), "en"),
				PublicURL:       cfg.PublicURL,
				TimeoutProvider: settings.settingSvc.AgentLLMTimeout,
			},
			log,
		).
			WithDeliverer(managerreportdelivery.NewNotificationDeliverer(alerts.alertRepo, notifyRouter)).
			WithReadyCheck(models.llmSettingsResolver.CheckReady)
		reportSchedulerReady = true
		log.Info("report: generator wired")
	} else {
		reportGen = managerbizreport.NewUnavailableGenerator("LLM provider not configured")
		log.Info("report: API wired without generator", slog.String("reason", "LLM provider not configured"))
	}
	reportUC := managerbizreport.NewUsecase(reportRepo, reportGen, uuid.NewString).
		WithReadRepo(reportRepo).
		WithDefaultLocale(firstNonEmpty(os.Getenv("ONGRID_DEFAULT_LOCALE"), "en"))
	if reportSchedulerReady {
		managerbizreport.NewScheduler(reportUC, log).Start(rootCtx)
		log.Info("report: scheduler wired")
	}
	reportHandler := managerserverreport.NewHandler(reportUC)

	// Flow orchestration (HLD-016): user-authored workflow DAGs executed
	// over the existing agent / tool / notify subsystems. Routes mount
	// even when the LLM runtime is down — tool/notify/condition nodes
	// still work; only agent nodes degrade with a clear error.
	flowRepo := managerflowdata.NewRepo(storage.db)
	flowRunRepo := managerflowdata.NewRunRepo(storage.db)
	// Captured so MCP tools (discovered later, after mcpUC exists) can be
	// appended to the same dispatch map the flow engine uses.
	flowInvoker := managersvcflow.NewToolInvoker(aiops.toolsReg, reg)
	flowExec := managerbizflow.Executors{
		Tools:  flowInvoker,
		Notify: managersvcflow.Notifier{Channels: alerts.alertRepo, Router: notifyRouter},
		LLM:    managersvcflow.LLMRunner{Client: models.llmClient},
	}
	if flowRT := runtime.aiopsRuntime; flowRT != nil {
		flowExec.Agent = managersvcflow.AgentRunner{Runtime: flowRT}
	}
	flowUC := managerbizflow.NewUsecase(flowRepo, flowRunRepo,
		managerbizflow.NewEngine(flowExec, flowRunRepo, log), log).
		WithToolCatalog(managersvcflow.ToolCatalog{Registry: aiops.toolsReg}).
		WithLLM(managersvcflow.LLMRunner{Client: models.llmClient})
	flowUC.HealStaleRuns(rootCtx)
	// HLD-016 triggers: alert dispatcher (auto-start matching flows when an
	// alert fires) + cron scheduler (time-based flows). Both nil-safe and
	// independent of the LLM runtime — tool/notify/condition flows run
	// regardless of whether agent nodes are usable.
	alerts.alertUC.SetWorkflowDispatcher(managerbizflow.NewDispatcher(flowUC, log))
	managerbizflow.NewScheduler(flowUC, log).Start(rootCtx)
	flowHandler := managerserverflow.NewHandler(flowUC)
	log.Info("flow: orchestration wired (alert + cron triggers active)")

	// Boot compensation pass for the structured RCA path: incidents that
	// fired while no LLM provider was configured had their auto-investigation
	// silently skipped (RecordFiring nil-checks the investigator), so the
	// IncidentDetail page would show status=not_started forever. Now that
	// the investigator is wired, re-enqueue any unstarted incidents in the
	// last 24h through the normal severity / inflight / concurrency-cap
	// gates. Bounded by limit=100 to cap the LLM burst (the global
	// concurrency cap further damps it). Goroutine + brief detached ctx so
	// boot doesn't block on the DB scan.
	if rcaInvConcrete != nil {
		go managersvcalert.BackfillInvestigations(rcaInvConcrete, alerts.alertUC, log)
	}

	return &automationModule{
		rcaInvConcrete: rcaInvConcrete,
		reportHandler:  reportHandler,
		flowInvoker:    flowInvoker,
		flowUC:         flowUC,
		flowHandler:    flowHandler,
	}
}
