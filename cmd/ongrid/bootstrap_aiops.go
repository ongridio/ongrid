package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	aiopsagent "github.com/ongridio/ongrid/internal/manager/biz/aiops/agent"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	managerbizimbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	managerbizpacketcapture "github.com/ongridio/ongrid/internal/manager/biz/packetcapture"
	manageraiopsdata "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	managerimbridgedata "github.com/ongridio/ongrid/internal/manager/data/imbridge/store"
	managerpacketcapturedata "github.com/ongridio/ongrid/internal/manager/data/packetcapture/store"
	managerserverpacketcapture "github.com/ongridio/ongrid/internal/manager/server/packetcapture"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	manageraiopsconfig "github.com/ongridio/ongrid/internal/manager/service/aiopsconfig"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	pkgtracequery "github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type aiopsModule struct {
	alertSvc             *managersvcalert.Service
	imbridgeRepo         *managerimbridgedata.Repo
	imbridgeUC           *managerbizimbridge.UC
	aiopsRepo            *manageraiopsdata.SessionRepo
	operationUC          *managerbizaiops.OperationUsecase
	mutatingProposalRepo *manageraiopsdata.MutatingProposalRepo
	toolsReg             *aiopstools.Registry
	packetCaptureUC      *managerbizpacketcapture.Usecase
	packetCaptureHandler *managerserverpacketcapture.Handler
	aiopsAgent           *aiopsagent.Agent
	aiopsUsage           *managerbizaiops.UsageUsecase
	kernel               managersvcaiops.Kernel
}

func bootstrapAIOps(cfg *config.Config, log *slog.Logger, notifyRouter *notify.Router, storage *storageModule, settings *settingsModule, models *modelsModule, devices *devicesModule, alerts *alertsModule, telemetry *telemetryModule, tunnel *tunnelModule) *aiopsModule {
	alertSvc := managersvcalert.New(alerts.alertUC, alerts.alertRepo, notifyRouter, log.With(slog.String("comp", "alert-svc")))
	// Wire the read-only preview clients (Prom range + Loki range). Each
	// is optional — when nil, the corresponding kind returns skipped_reason
	// instead of a hard error. Built before the AIOps runtime so
	// conversational draft tools can reuse the same service path.
	{
		previewDeps := managerbizalert.PreviewDeps{Search: telemetry.logsBackendSvc}
		if telemetry.promQueryClient != nil {
			previewDeps.Prom = telemetry.promQueryClient
		}
		previewDeps.Log = telemetry.lokiLogClient
		alertSvc.SetPreviewDeps(previewDeps)
	}

	// IM bridge admin repo/UC is needed by Settings -> Channels HTTP handlers.
	// The runtime-facing Bridge service still wires later, after aiopsSvc
	// exists.
	if err := managerimbridgedata.Migrate(storage.db); err != nil {
		log.Error("imbridge: migrate failed", slog.Any("err", err))
	}
	imbridgeRepo := managerimbridgedata.New(storage.db)
	imbridgeUC := managerbizimbridge.NewUC(imbridgeRepo)

	// manager/aiops biz + service + server.
	//
	// BudgetChecker wiring: cfg.LLM.DailyTokenLimit (ONGRID_LLM_DAILY_TOKEN_LIMIT,
	// default 0=unlimited) drives an llm.InMemoryBudget that the graph-layer
	// callback chain checks before each ChatModel turn — see Phase 4 cbDeps
	// build below where the checker is set when the limit > 0.
	aiopsRepo := manageraiopsdata.NewSessionRepo(storage.db)
	operationUC := managerbizaiops.NewOperationUsecase(aiopsRepo)
	mutatingProposalRepo := manageraiopsdata.NewMutatingProposalRepo(storage.db)
	// PromQuerier is the interface tools/registry takes; passing a typed-nil
	// *Client would yield a non-nil interface and bypass the conditional
	// tool registration. Explicitly hand it nil when Prom is disabled.
	var promQuerier aiopstools.PromQuerier
	if telemetry.promQueryClient != nil {
		promQuerier = telemetry.promQueryClient
	}
	// query_logql is wired to the selected-backend service so its stable name
	// and arguments work for both backends while each keeps its own response
	// shape. TraceQL remains conditionally available by URL.
	var logQuerier aiopstools.LogQuerier = telemetry.logsBackendSvc
	var traceQuerier aiopstools.TraceQuerier
	if cfg.Traces.URL != "" {
		traceQuerier = pkgtracequery.New(cfg.Traces.URL, log.With(slog.String("comp", "aiops-tracequery")))
	}
	toolsReg := aiopstools.NewRegistry(tunnel.fbClient, devices.edgeUC, devices.deviceUC, promQuerier, logQuerier, traceQuerier, alerts.alertUC, log)
	packetCaptureUC := managerbizpacketcapture.New(
		managerpacketcapturedata.New(storage.db),
		tunnel.fbClient,
		aiopstools.NewDeviceResolver(devices.deviceUC, devices.edgeUC),
		log.With(slog.String("comp", "packet-capture")),
	)
	packetCaptureRawStore, err := managerbizpacketcapture.NewLocalRawStore(cfg.PacketCapture.RawDir)
	if err != nil {
		log.Warn("packet capture raw store disabled", slog.Any("err", err))
	} else {
		packetCaptureUC.SetRawStore(packetCaptureRawStore)
	}
	packetParserArtifactBaseURL := cfg.PacketCapture.ParserArtifactBaseURL
	if strings.TrimSpace(packetParserArtifactBaseURL) == "" {
		packetParserArtifactBaseURL = cfg.PublicURL
	}
	if strings.TrimSpace(cfg.PacketCapture.ParserURL) != "" {
		packetParser, parserErr := managerbizpacketcapture.NewParserClient(managerbizpacketcapture.ParserClientConfig{
			URL:             cfg.PacketCapture.ParserURL,
			ArtifactBaseURL: packetParserArtifactBaseURL,
			TokenSecret:     cfg.PacketCapture.ParserTokenSecret,
			PrivateKeyFile:  cfg.PacketCapture.ParserManagerPrivateKeyFile,
			CAFile:          cfg.PacketCapture.ParserCAFile,
			Timeout:         cfg.PacketCapture.ParserTimeout,
			MaxPackets:      cfg.PacketCapture.ParserMaxPackets,
			MaxBytes:        cfg.PacketCapture.ParserMaxBytes,
			IncludeHex:      cfg.PacketCapture.ParserIncludeHex,
		})
		if parserErr != nil {
			log.Warn("packet parser disabled", slog.Any("err", parserErr))
		} else {
			packetCaptureUC.SetParser(packetParser)
		}
	}
	packetCaptureHandler := managerserverpacketcapture.NewHandler(packetCaptureUC)
	toolsReg.SetPacketCaptureCreator(packetCaptureUC)
	toolsReg.SetPacketCaptureOperationCreator(managersvcaiops.PacketCaptureOperationCreator(operationUC))
	toolsReg.SetPluginConfigLister(devices.pluginConfigUC)
	toolsReg.SetConfigManager(manageraiopsconfig.NewAlertRuleManager(alertSvc))
	toolsReg.SetK8sSnapshotReader(devices.k8sSvc)
	// query_change_events (HLD-013 Phase 2) — RCA "what changed near T".
	// *audit.Usecase satisfies aiopstools.AuditLister via ListChanges.
	toolsReg.SetAuditLister(settings.auditUC)
	// Populate deployment-level facts for the get_topology tool. Channel
	// counter pulls from the alert repo's enabled-channel listing so the
	// number reflects what notify_router actually fans out to.
	toolsReg.SetTopologyInfo(aiopstools.TopologyInfo{
		ManagerVersion:     version,
		ConfiguredPromURL:  cfg.Prom.QueryURL,
		ConfiguredLokiURL:  cfg.Logs.URL,
		ConfiguredTempoURL: cfg.Traces.URL,
		ChannelCounter: func(ctx context.Context) (int, error) {
			rows, err := alerts.alertRepo.ListEnabledChannels(ctx)
			if err != nil {
				return 0, err
			}
			return len(rows), nil
		},
	})
	// Wire the topology graph usecase so expand_topology /
	// find_topology_node show up in the BaseTool roster. nil-safe — the
	// two BaseTools are gated on this exact field.
	toolsReg.SetTopologyGraph(devices.topologyUC)
	aiopsAgent := aiopsagent.New(
		models.llmClient,
		toolsReg,
		aiopsRepo,
		aiopsagent.Config{Model: cfg.OpenAI.Model, Temperature: 0.1, MaxIterations: 30},
		log,
	)
	aiopsAgent.SetAgentWriteEnabledProvider(func(ctx context.Context) bool {
		return settings.settingSvc.AgentWriteEnabled(ctx)
	})
	aiopsUsage := managerbizaiops.NewUsageUsecase(aiopsRepo, log)

	// The graph-based AgentLoop is the default execution path. Operators can
	// set ONGRID_AGENT_KERNEL=legacy for an explicit rollback while migration
	// issues are investigated.
	// When the env is set we build:
	//   - RoutingChatModel (PR-1) wrapping the existing llmRouter, one
	//     per provider id.
	//   - Decorated BaseTool slice via Registry.BuildBaseTools +
	//     AppendHostFilesTools, then Wrap'd with the standard chain.
	//   - SkillRegistry / AgentRegistry from configured skill roots + ./agents
	//     (silent on missing dirs — fresh installs boot fine).
	//   - chatruntime.Runtime, the cutover entry the service routes to.
	// Mismatch / build errors fall back to legacy with a logged warning.
	kernel := managersvcaiops.ParseKernel(os.Getenv("ONGRID_AGENT_KERNEL"))
	log.Info("aiops agent kernel selected", slog.String("kernel", string(kernel)))

	return &aiopsModule{
		alertSvc:             alertSvc,
		imbridgeRepo:         imbridgeRepo,
		imbridgeUC:           imbridgeUC,
		aiopsRepo:            aiopsRepo,
		operationUC:          operationUC,
		mutatingProposalRepo: mutatingProposalRepo,
		toolsReg:             toolsReg,
		packetCaptureUC:      packetCaptureUC,
		packetCaptureHandler: packetCaptureHandler,
		aiopsAgent:           aiopsAgent,
		aiopsUsage:           aiopsUsage,
		kernel:               kernel,
	}
}
