package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	managerbizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	managerbizdevice "github.com/ongridio/ongrid/internal/manager/biz/device"
	managerbizmarketplace "github.com/ongridio/ongrid/internal/manager/biz/marketplace"
	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	managerapprovaldata "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	managermarketplacedata "github.com/ongridio/ongrid/internal/manager/data/marketplace/store"
	managermcpdata "github.com/ongridio/ongrid/internal/manager/data/mcp/store"
	pagesdata "github.com/ongridio/ongrid/internal/manager/data/pages"
	managermodelmcp "github.com/ongridio/ongrid/internal/manager/model/mcp"
	managerserverapproval "github.com/ongridio/ongrid/internal/manager/server/approval"
	managerserverk8s "github.com/ongridio/ongrid/internal/manager/server/k8s"
	managerservermarketplace "github.com/ongridio/ongrid/internal/manager/server/marketplace"
	managerservermcp "github.com/ongridio/ongrid/internal/manager/server/mcp"
	managerserveroperatorrun "github.com/ongridio/ongrid/internal/manager/server/operatorrun"
	managerserversecret "github.com/ongridio/ongrid/internal/manager/server/secret"
	managerserverskill "github.com/ongridio/ongrid/internal/manager/server/skill"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	managersvcflow "github.com/ongridio/ongrid/internal/manager/service/flow"
	managersvcimbridge "github.com/ongridio/ongrid/internal/manager/service/imbridge"
	managersvck8s "github.com/ongridio/ongrid/internal/manager/service/k8s"
	managersvcskill "github.com/ongridio/ongrid/internal/manager/service/skill"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/runner"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
	"github.com/ongridio/ongrid/internal/pkg/workspace"
	"github.com/prometheus/client_golang/prometheus"
)

type extensionsModule struct {
	skillHandler       *managerserverskill.Handler
	operatorRunHandler *managerserveroperatorrun.Handler
	marketplaceHandler *managerservermarketplace.Handler
	secretHandler      *managerserversecret.Handler
	mcpHandler         *managerservermcp.Handler
	approvalHandler    *managerserverapproval.Handler
	k8sHandler         *managerserverk8s.Handler
	pageStore          pagesdata.FileStore
}

func bootstrapExtensions(rootCtx context.Context, cfg *config.Config, log *slog.Logger, reg *prometheus.Registry, notifyRouter *notify.Router, storage *storageModule, settings *settingsModule, devices *devicesModule, alerts *alertsModule, tunnel *tunnelModule, aiops *aiopsModule, runtime *runtimeModule, automation *automationModule) *extensionsModule {
	skillSvc := aiops.skillSvc
	// HLD-017: surface chatruntime SKILL.md skills (built-in + marketplace-
	// installed) in the /v1/skills catalog. They live in a separate registry
	// from skillcore, so without this an installed pack (e.g. terrashark) is
	// invisible in the catalog even though the agent already uses it.
	skillSvc.WithExtraSkills(managersvcskill.RuntimeCatalog(runtime.bootstrapSkillReg))
	skillHandler := managerserverskill.NewHandler(skillSvc)
	operatorRunHandler := managerserveroperatorrun.NewHandler(tunnel.operatorRunSvc)

	// marketplace wiring. Install / List / Uninstall on
	// /v1/marketplace/*. The usecase reloads the chatruntime registries
	// after every mutation so newly installed skills appear in the next
	// chat without a restart. Registries exist independently of runtime assembly.
	// Reload both version-local registries, even when LLM assembly is unavailable.
	var mpSkillReg managerbizmarketplace.SkillRegistry = registryReloaders{runtime.bootstrapSkillReg, runtime.bootstrapV2SkillReg}
	var mpAgentReg managerbizmarketplace.AgentRegistry = registryReloaders{runtime.bootstrapAgentReg, runtime.bootstrapV2AgentReg}
	var skillAssetLeaser managersvcaiops.SkillAssetLeaser = runtime.bootstrapSkillReg
	mpRepo := managermarketplacedata.NewRepo(storage.db)
	mpDevMode := strings.EqualFold(os.Getenv("ONGRID_MARKETPLACE_DEVMODE"), "true") ||
		os.Getenv("ONGRID_MARKETPLACE_DEVMODE") == ""
	mpRequireSigned := []string{"ongrid-official"}
	if v := strings.TrimSpace(os.Getenv("ONGRID_MARKETPLACE_REQUIRE_SIGNED_SOURCES")); v != "" {
		mpRequireSigned = mpRequireSigned[:0]
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				mpRequireSigned = append(mpRequireSigned, part)
			}
		}
	}
	mpPinnedKey := os.Getenv("ONGRID_MARKETPLACE_PINNED_PUBKEY")
	// Skill roots — see boot LoadAll block downstream for the full
	// rationale. Defined here too because marketplace UC is wired
	// before that block runs.
	builtinSkillsRoot := os.Getenv("ONGRID_BUILTIN_SKILLS_ROOT")
	builtinAgentsRoot := firstNonEmpty(os.Getenv("ONGRID_BUILTIN_AGENTS_ROOT"), "./agents")
	marketplaceSkillsRoot := firstNonEmpty(os.Getenv("ONGRID_SKILLS_ROOT"), "/var/lib/ongrid/skills")
	if err := os.MkdirAll(marketplaceSkillsRoot, 0o755); err != nil {
		log.Warn("create marketplace skills root", slog.String("path", marketplaceSkillsRoot), slog.Any("err", err))
	}
	mpUC := managerbizmarketplace.NewUsecase(mpRepo, mpSkillReg, mpAgentReg, managerbizmarketplace.Config{
		SystemSkillsRoot:     marketplaceSkillsRoot,
		BuiltinSkillsRoots:   []string{builtinSkillsRoot},
		BuiltinAgentsRoots:   []string{builtinAgentsRoot},
		StagingDir:           filepath.Join(os.TempDir(), "ongrid-marketplace-staging"),
		AllowedSources:       []string{"ongrid-official", "local"},
		RequireSignedSources: mpRequireSigned,
		SignaturePinnedKey:   mpPinnedKey,
		DevMode:              mpDevMode,
	}, log.With(slog.String("comp", "marketplace")))
	marketplaceHandler := managerservermarketplace.NewHandler(mpUC)
	// HLD-017 generic secret vault: the single semantics-agnostic credential
	// store installed skills, external MCP clients, and the log backend use.
	// The network device domain owns polling but resolves encrypted credentials
	// through this narrow vault facade. Only the credential name is persisted.
	devices.networkDiscoveryUC.SetCredentialResolver(storage.secretUC)
	managerbizdevice.NewNetworkPollScheduler(devices.networkDiscoveryUC, log).Start(rootCtx)
	secretHandler := managerserversecret.NewHandler(storage.secretUC)
	// HLD-018 MCP client: external MCP servers config + connect/list-tools.
	// Reuses the credential vault (secretUC) for server auth injection.
	mcpUC := managerbizmcp.NewUsecase(managermcpdata.NewRepo(storage.db), storage.secretUC, log.With(slog.String("comp", "mcp")))
	mcpHandler := managerservermcp.NewHandler(mcpUC)
	// HLD-018 + flow: MCP tools are schema-typed callables, so they're
	// first-class deterministic flow nodes (unlike SKILL.md skills). Wire a
	// LIVE source into the flow palette + dispatcher now that mcpUC exists —
	// the palette queries servers per editor load, and the tool node runs them
	// directly with NO approval (a published flow node is pre-authorized; the
	// inbox gate is only for agent-initiated calls). flowInvoker is a pointer,
	// so setting .mcp here is seen by the engine; re-set the catalog to the
	// MCP-aware one (WithToolCatalog just stores it).
	mcpFlowSrc := &managersvcflow.MCPSource{Usecase: mcpUC, Logger: log.With(slog.String("comp", "flow-mcp"))}
	automation.flowInvoker.MCP = mcpFlowSrc
	automation.flowUC.WithToolCatalog(managersvcflow.ToolCatalog{Registry: aiops.toolsReg, MCP: mcpFlowSrc})
	// HLD-017 propose-confirm inbox: human approval queue for dangerous
	// actions (agent cloud-shell, etc.). Additive — empty until a producer
	// proposes; producers register their execute-on-approve executor.
	approvalUC := managerbizapproval.NewUsecase(managerapprovaldata.NewRepo(storage.db), log.With(slog.String("comp", "approval")))
	runtime.humanApprovalBroker.SetUsecase(approvalUC)
	approvalUC.RegisterExecutor(managersvcaiops.GenericToolApprovalKind, runtime.humanApprovalBroker.Execute)
	managersvcaiops.RegisterRuntimeApprovalExecutor(approvalUC, managersvcaiops.GenericToolApprovalKind, contract.V3, runtime.humanApprovalBroker.Execute)
	managersvcaiops.RegisterRuntimeApprovalExecutor(approvalUC, managersvcaiops.GenericToolApprovalKind, contract.V2, runtime.humanApprovalBroker.Execute)
	approvalHandler := managerserverapproval.NewHandler(approvalUC)
	// HLD-017 cloud_bash producer: register the execute-on-approve executor
	// (resolve the bound credential → inject into the Runner sandbox → run)
	// and wire the cloud_bash tool's proposer seam to the approval inbox.
	cloudBashRunner := runner.NewShellRunner()
	// HLD-019 agent workspace: per-session persistent cwd for cloud_bash so a
	// skill (e.g. terraform-runner) can write .tf/state in one command and read
	// it back in the next, instead of running in a throwaway temp dir. Root is
	// a persistent volume; empty disables it (falls back to today's temp dir).
	workspaceRoot := os.Getenv("ONGRID_WORKSPACE_ROOT")
	if workspaceRoot == "" {
		workspaceRoot = "/var/lib/ongrid/workspace"
	}
	wsMgr := workspace.New(workspaceRoot)
	(managersvcaiops.ApprovalExecutors{
		Secrets:     storage.secretUC,
		Runner:      cloudBashRunner,
		Workspace:   wsMgr,
		SkillsRoot:  marketplaceSkillsRoot,
		SkillAssets: skillAssetLeaser,
		Frontier:    tunnel.fbClient,
		Edges:       devices.edgeUC,
		Devices:     devices.deviceUC,
		Marketplace: mpUC,
		MCP:         mcpUC,
		Log:         log,
	}).Register(approvalUC)
	aiops.toolsReg.SetCloudBashProposer(managersvcaiops.CloudBashProposer{Usecase: approvalUC})
	aiops.toolsReg.SetHostBashProposer(managersvcaiops.HostBashProposer{Usecase: approvalUC})
	// The Actions page is a read-only projection over two immutable audit
	// sources: graph ReviewGate decisions and legacy human approvals.
	k8sHandler := managerserverk8s.NewHandler(devices.k8sSvc, managersvck8s.NewActionAuditReader(aiops.mutatingProposalRepo, approvalUC))
	// send_notification: the assistant can proactively push to a configured
	// channel (飞书/钉钉/…), reusing the same BuildSenderFromChannel path the
	// alert notifier + flow notify node use.
	aiops.toolsReg.SetNotificationSender(managersvcaiops.NotificationSender{Channels: alerts.alertRepo, Router: notifyRouter})
	// send_im_message targets a real IM Bridge group by app + platform group ID.
	// It does not reuse notification channels.
	aiops.toolsReg.SetIMMessageSender(managersvcimbridge.IMMessageSender{Apps: aiops.imbridgeRepo})
	// serve_page: the assistant can host a generated HTML report at an
	// internal /pages/<token> URL. Pages live on the persistent volume; the
	// route is registered on the mux below.
	pagesDir := "/var/lib/ongrid/pages"
	if d := os.Getenv("ONGRID_PAGES_DIR"); d != "" {
		pagesDir = d
	}
	pageStore := pagesdata.FileStore{Dir: pagesDir, Log: log.With(slog.String("comp", "serve_page"))}
	if err := os.MkdirAll(pagesDir, 0o755); err != nil {
		log.Warn("serve_page: mkdir pages dir failed; serve_page disabled", slog.String("dir", pagesDir), slog.Any("err", err))
	} else {
		aiops.toolsReg.SetPageStore(pageStore)
	}
	// The chat runtime's tool bag was compiled during runtime setup
	// BEFORE the cloud_bash proposer existed, so that BuildBaseTools didn't
	// yield cloud_bash. SetCloudBashProposer fixes /v1/skills and any FRESH
	// bag, but the already-built coordinator/worker graph still lacks the
	// tool — and because the system prompt tells the LLM about cloud_bash,
	// it issues a call that eino can't route, failing the whole stream with
	// "tool cloud_bash not found in toolsNode indexes". Bolt it onto the
	// live bag here, exactly like the AgentTool trio above. The coordinator
	// (coordinatorToolNames) and specialist-ops (persona Tools list) filters
	// both whitelist cloud_bash, so this single append reaches both.
	var runtimes []extensionRuntime
	if runtime.chatRT != nil {
		runtime.chatRT.SetCredentialBinder(mpUC)
		runtimes = append(runtimes, runtime.chatRT)
	}
	if runtime.chatV2 != nil {
		runtime.chatV2.SetCredentialBinder(mpUC)
		runtimes = append(runtimes, runtime.chatV2)
	}
	var refreshers []*managersvcaiops.MCPToolRefresher
	for _, rt := range runtimes {
		cbDeps := aiopstoolsdec.Deps{
			// HLD-021: cloud_bash now BLOCKS in-tool until the human approves
			// (synchronous propose-confirm), so the per-call timeout must
			// outlast the approval wait budget (managersvcaiops.ApprovalWaitTimeout = 30m) —
			// a minute longer so the tool's own clean timeout blob wins over a
			// decorator-imposed ErrToolTimeout. install_skill (same deps)
			// still returns instantly, so the long bound is harmless there.
			Timeout:       managersvcaiops.ApprovalWaitTimeout + time.Minute,
			Limiter:       aiopstoolsdec.NewTokenBucketLimiter(0),
			Registerer:    reg,
			HumanApproval: runtime.humanApprovalBroker,
		}
		// serve_page + messaging tools are registered AFTER buildAIOpsRuntime
		// (SetPageStore / SetNotificationSender above), so like cloud_bash they're absent
		// from the startup chat bag and the LLM can't call them in chat — the
		// exact reason the agent "never triggered serve_page". They're instant
		// (no human-approval gate), so a short timeout, not cbDeps' 31m ceiling.
		// First registration on reg here (not in the startup bag) → no
		// double-register.
		quickDeps := aiopstoolsdec.Deps{
			Timeout:       60 * time.Second,
			Limiter:       aiopstoolsdec.NewTokenBucketLimiter(0),
			Registerer:    reg,
			HumanApproval: runtime.humanApprovalBroker,
		}
		if err := rt.AppendToolBag([]aiopstoolsbase.BaseTool{
			aiopstoolsdec.Wrap(aiopstools.NewBashToolWithProposer(tunnel.fbClient, devices.edgeUC, devices.deviceUC, managersvcaiops.HostBashProposer{Usecase: approvalUC}, log), cbDeps),
			aiopstoolsdec.Wrap(aiopstools.NewCloudBashTool(managersvcaiops.CloudBashProposer{Usecase: approvalUC}, log), cbDeps),
			aiopstoolsdec.Wrap(aiopstools.NewInstallSkillTool(managersvcaiops.InstallSkillProposer{Usecase: approvalUC}, log), cbDeps),
			aiopstoolsdec.Wrap(aiopstools.NewServePageTool(pageStore, log), quickDeps),
			aiopstoolsdec.Wrap(aiopstools.NewSendNotificationTool(managersvcaiops.NotificationSender{Channels: alerts.alertRepo, Router: notifyRouter}, log), quickDeps),
			aiopstoolsdec.Wrap(aiopstools.NewSendIMMessageTool(managersvcimbridge.IMMessageSender{Apps: aiops.imbridgeRepo}, log), quickDeps),
		}); err != nil {
			log.Error("append runtime extension tools failed", slog.Any("err", err))
		}
		log.Info("host_bash + cloud_bash + install_skill + serve_page + messaging tools bolted onto chat runtime bag", slog.Int("tool_count", rt.ToolCount()))
		// Admin write-action gate: consult the agent/write_enabled system
		// setting live on every chat request. When an admin turns it off the
		// agent goes read-only (all non-read tools stripped from the LLM's
		// toolbag). Missing settings fail closed.
		rt.SetAgentWriteEnabledProvider(func(ctx context.Context) bool {
			return settings.settingSvc.AgentWriteEnabled(ctx)
		})
		// HLD-018 P2: connect each enabled MCP server, pull its tools, and
		// bolt each onto the toolbag as mcp__<server>__<tool>. Trusted
		// servers' tools run synchronously; others queue to the approval
		// inbox. Best-effort per server — a slow/unreachable server is logged
		// and skipped, never blocks boot.
		mcpDeps := aiopstoolsdec.Deps{
			Timeout:       90 * time.Second,
			Limiter:       aiopstoolsdec.NewTokenBucketLimiter(0),
			Registerer:    reg,
			HumanApproval: runtime.humanApprovalBroker,
		}
		mcpCaller := managersvcaiops.MCPCaller{Usecase: mcpUC}
		mcpProposer := managersvcaiops.MCPProposer{Usecase: approvalUC, MCP: mcpUC}
		// Keep the graph-facing decorators and ToolSearch's unredacted catalog
		// in sync. Registration changes call this with one server's fresh test
		// result, so an admin save never reconnects every MCP endpoint.
		mcpTools := managersvcaiops.NewMCPToolRefresher(rt, mcpDeps, mcpCaller, mcpProposer, log)
		refreshers = append(refreshers, mcpTools)
	}
	mcpUC.SetToolChangeHook(func(ctx context.Context, name string, srv *managermodelmcp.Server, discovered []mcpclient.Tool) {
		for _, refresher := range refreshers {
			refresher.Refresh(ctx, name, srv, discovered)
		}
	})
	if len(refreshers) > 0 {
		// Discovery has one supervisor; every change fans out to both catalogs.
		refreshers[0].LoadEnabled(rootCtx, mcpUC)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Error("MCP refresh supervisor panic", slog.Any("panic", recovered))
				}
			}()
			refreshers[0].RunRefresh(rootCtx, mcpUC, time.Minute)
		}()
	}

	if secretbox.KeyIsWeak() {
		log.Warn("secret vault: no strong ONGRID_SECRET_KEY or ONGRID_JWT_SECRET; credentials use the local-development fallback key")
	}
	log.Info("marketplace wired",
		slog.Bool("dev_mode", mpDevMode),
		slog.Bool("skill_reload", mpSkillReg != nil),
		slog.Bool("agent_reload", mpAgentReg != nil),
		slog.Any("require_signed_sources", mpRequireSigned),
		slog.Bool("pinned_pubkey", mpPinnedKey != ""),
	)

	// Inventory bridge: register every BaseTool in the bag as a skill so
	// /skills surfaces the complete cloud-side capability inventory.
	// Runs even when the chat runtime is unavailable. Skipped tools (already exist as skills via
	// skill_bridge) are silently bypassed. Idempotent.
	{
		invBag := aiops.toolsReg.BuildBaseTools()
		aiops.toolsReg.RegisterBaseToolsAsSkills(invBag, log.With(slog.String("comp", "inventory-bridge")))
		// Re-merge so flow `tool` nodes can run tools registered after the
		// invoker was first built — cloud_bash (its proposer is wired above)
		// + host-files tools. Without this they report "unknown tool".
		automation.flowInvoker.MergeBag(invBag)
	}

	return &extensionsModule{
		skillHandler:       skillHandler,
		operatorRunHandler: operatorRunHandler,
		marketplaceHandler: marketplaceHandler,
		secretHandler:      secretHandler,
		mcpHandler:         mcpHandler,
		approvalHandler:    approvalHandler,
		k8sHandler:         k8sHandler,
		pageStore:          pageStore,
	}
}

// extensionRuntime exposes only version-independent startup hooks.
type extensionRuntime interface {
	AppendToolBag([]aiopstoolsbase.BaseTool) error
	ReplaceToolSource(string, []aiopstoolsbase.BaseTool, []aiopstoolsbase.BaseTool) error
	ToolCount() int
	SetAgentWriteEnabledProvider(func(context.Context) bool)
}
type registryReloader interface{ Reload(string, ...string) error }
type registryReloaders []registryReloader

func (rs registryReloaders) Reload(root string, extras ...string) error {
	var errs []error
	for _, r := range rs {
		if r != nil {
			if err := r.Reload(root, extras...); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
