package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/chatruntime"
	aiopsgraph "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/graph"
	aiopsgraphcb "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/graph/callbacks"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	managerbizdevice "github.com/ongridio/ongrid/internal/manager/biz/device"
	managerbizedge "github.com/ongridio/ongrid/internal/manager/biz/edge"
	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	managersvcfb "github.com/ongridio/ongrid/internal/manager/service/frontierbound"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/prometheus/client_golang/prometheus"
)

// loadV2BootstrapRegistries walks ./agents + configured skills + the marketplace
// skill root and returns populated registries. Called once at boot
// independently of runtime availability, so /v1/agents has data to render even
// when the chat runtime can't build (no LLM provider, build failure).
//
// Env knobs mirror the values buildV2AIOpsRuntime used to read inline:
//
//	ONGRID_BUILTIN_AGENTS_ROOT  default ./agents
//	ONGRID_BUILTIN_SKILLS_ROOT  optional extra skills directory (no default)
//	ONGRID_SKILLS_ROOT          default /var/lib/ongrid/skills (marketplace mutable root)
func loadV2BootstrapRegistries(log *slog.Logger) (*aiopschatruntime.SkillRegistry, *aiopschatruntime.AgentRegistry) {
	builtinSkillsRoot := os.Getenv("ONGRID_BUILTIN_SKILLS_ROOT")
	builtinAgentsRoot := firstNonEmpty(os.Getenv("ONGRID_BUILTIN_AGENTS_ROOT"), "./agents")
	marketplaceSkillsRoot := firstNonEmpty(os.Getenv("ONGRID_SKILLS_ROOT"), "/var/lib/ongrid/skills")
	skillReg := aiopschatruntime.NewSkillRegistry()
	agentReg := aiopschatruntime.NewAgentRegistry()
	loadRes, loadErr := aiopschatruntime.LoadAll(aiopschatruntime.LoadAllConfig{
		SkillsRoot:       builtinSkillsRoot,
		AgentsRoot:       builtinAgentsRoot,
		ExtraSkillsRoots: []string{marketplaceSkillsRoot},
	})
	if loadErr != nil {
		log.Warn("chatruntime: load all", slog.Any("err", loadErr))
		return skillReg, agentReg
	}
	skillReg.AddAll(loadRes.Skills)
	agentReg.AddAll(loadRes.Agents)
	skillReg.AddWarnings(loadRes.Warnings)
	log.Info("chatruntime: loaded skills + agents",
		slog.Int("skills", len(loadRes.Skills)),
		slog.Int("agents", len(loadRes.Agents)),
		slog.Int("warnings", len(loadRes.Warnings)))
	for _, w := range skillReg.Warnings() {
		log.Warn("chatruntime: skill warning",
			slog.String("path", w.Path), slog.String("code", w.Code), slog.String("reason", w.Reason))
	}
	for _, w := range agentReg.Warnings() {
		log.Warn("chatruntime: agent warning",
			slog.String("path", w.Path), slog.String("code", w.Code), slog.String("reason", w.Reason))
	}
	return skillReg, agentReg
}

// Heavy on parameters because every dep flows through this site
// exactly once — the alternative (build the runtime inline in main)
// would balloon main() further and make wiring harder to read.
func buildV2AIOpsRuntime(
	ctx context.Context,
	cfg *config.Config,
	llmClient llm.Client,
	llmRouter *llm.MultiClient,
	toolsReg *aiopstools.Registry,
	sessions managerbizaiops.SessionRepo,
	mutatingProposals managersvcaiops.MutatingProposalRepo,
	fbClient *managersvcfb.Client,
	edgeUC *managerbizedge.Usecase,
	deviceUC *managerbizdevice.Usecase,
	reg prometheus.Registerer,
	log *slog.Logger,
	skillReg *aiopschatruntime.SkillRegistry,
	agentReg *aiopschatruntime.AgentRegistry,
	resolver *managerbizsetting.LLMSettingsResolver,
	humanApproval aiopstoolsdec.HumanApprovalProposer,
	traceCallback einocallbacks.Handler,
) (*aiopschatruntime.Runtime, error) {
	chatModel, err := buildRuntimeChatModel(ctx, cfg, llmClient, resolver, log)
	if err != nil {
		return nil, err
	}

	// 2. Tool bag — Registry.BuildBaseTools,
	//    then wrap the whole thing through the standard decorator
	//    chain so audit / timeout / rate-limit / metric apply
	//    uniformly. chat_tool_calls writes still happen via the
	//    graph persistence callback, while mutating proposal decisions
	//    are written by ReviewGate through chat_mutating_proposals.
	//
	//    BuildBaseTools now returns a *tools.ToolBag
	//    (deferred-loading wrapper). When the bag size is below the
	//    deferral threshold (default 30, override via
	//    ONGRID_TOOLBAG_DEFERRAL_THRESHOLD) the LLM still sees full
	//    schemas for everything; once the marketplace pushes us past
	//    threshold the specialty tier auto-redacts and the LLM
	//    fetches schemas via the always-loaded ToolSearch tool.
	bag := toolsReg.BuildBaseTools()
	baseTools := bag.SchemasForLLM()
	reviewSpawner := &managersvcaiops.ReviewSpawner{}
	reviewSink := managersvcaiops.NewMutatingProposalSink(mutatingProposals)
	deps := aiopstoolsdec.Deps{
		Limiter:       aiopstoolsdec.NewTokenBucketLimiter(0),
		Registerer:    reg,
		ReviewSpawner: reviewSpawner,
		ReviewSink:    reviewSink,
		HumanApproval: humanApproval,
	}
	wrapped := make([]aiopstoolsbase.BaseTool, 0, len(baseTools))
	for _, t := range baseTools {
		toolDeps := deps
		if info, err := t.Info(context.Background()); err == nil && info != nil && info.Name == aiopstools.ToolNameCapturePCAP {
			// A short capture includes capture duration plus parser work. Keep
			// this operation-specific ceiling; other tools own their execution
			// budgets without a blanket runtime deadline.
			toolDeps.Timeout = 45 * time.Second
		}
		wrapped = append(wrapped, aiopstoolsdec.Wrap(t, toolDeps))
	}

	// 3. Skill / Agent registries — pre-loaded by loadV2BootstrapRegistries
	//    in main() so /v1/agents populates even when the chat runtime
	//    can't build (no LLM provider configured yet, etc). We just take
	//    the references handed in here and continue with the chat-wiring
	//    work below.

	// Register the virtual "default" persona — the user-facing root agent.
	// An empty Tools whitelist inherits the complete tool bag; permission
	// decorators and ToolSearch govern execution and progressive disclosure.
	// Specialists improve depth but are never a prerequisite for closure.
	agentReg.AddBuiltin(&aiopschatruntime.Agent{
		Name:        "default",
		Description: "默认助理",
		WhenToUse:   "首页发起的会话默认绑定它；适合任何运维 / 排查 / 知识库查询场景。",
		Capabilities: []aiopschatruntime.AgentCapability{
			{
				ID:           "operate_and_investigate",
				Description:  "Own the user conversation, resolve facts, execute permitted tools, and integrate expert evidence.",
				MaxToolCalls: managersvcaiops.DefaultCoordinatorMaxTurns,
			},
		},
		Tools: nil,
		// Keep the coordinator ceiling aligned with the global ReAct
		// budget. Loop prevention belongs in graph-level guards
		// (identical-call memo, per-tool execution caps, AgentTool
		// dedupe), not in a lower persona turn limit that can clip
		// legitimate long investigations.
		MaxTurns: managersvcaiops.DefaultCoordinatorMaxTurns,
		Source:   "builtin",
	})

	// 4. Callback deps. Persistence/Audit/Metrics use the same
	//    SessionRepo + Registerer threaded everywhere. Budget gate is
	//    wired when LLM.DailyTokenLimit > 0 — single global UTC-day cap
	//    enforced against llm.InMemoryBudget (sufficient for the
	//    private-MVP single-tenant scope).
	cbDeps := aiopsgraphcb.Deps{
		Persistence: aiopsgraphcb.PersistenceDeps{
			Repo:       sessions,
			Logger:     log.With(slog.String("comp", "chatruntime-persist")),
			Registerer: reg,
		},
		Audit: aiopsgraphcb.AuditDeps{
			Logger: log.With(slog.String("comp", "chatruntime-audit")),
		},
		Metrics: aiopsgraphcb.MetricsDeps{
			Registerer: reg,
		},
	}
	if traceCallback != nil {
		cbDeps.Tracing = traceCallback
	}
	if cfg.LLM.DailyTokenLimit > 0 {
		cbDeps.BudgetChecker = llm.NewInMemoryBudget(cfg.LLM.DailyTokenLimit)
		log.Info("aiops: daily token budget enabled",
			slog.Int("daily_limit", cfg.LLM.DailyTokenLimit),
		)
	}

	// 5. Stitch the runtime.
	// ctx + llmRouter are reserved for future runtime hooks (e.g.
	// per-call provider catalog refresh). Reference them so unused-
	// param lints stay quiet across edits.
	_ = ctx
	_ = llmRouter
	rt, err := aiopschatruntime.NewRuntime(aiopschatruntime.Config{
		SkillRegistry:   skillReg,
		AgentRegistry:   agentReg,
		Sessions:        sessions,
		ChatModel:       chatModel,
		ToolBag:         wrapped,
		MentionResolver: nil, // wired below if we have a searcher
		BasePrompt:      managersvcaiops.BasePrompt(),
		HistoryLimit:    50,
		GraphCfg: aiopsgraph.Config{
			Model:         cfg.OpenAI.Model,
			Temperature:   0.1,
			MaxIterations: 30,
		},
		CallbackDeps: cbDeps,
		Logger:       log.With(slog.String("comp", "chatruntime")),
	})
	if err != nil {
		return nil, err
	}
	reviewSpawner.SetRuntime(managersvcaiops.V2Runtime{Runtime: rt})
	// — hand the unredacted *ToolBag to the runtime so
	// future introspection paths can query the full tool universe even
	// when the LLM-facing slice is the deferred / redacted view.
	// ToolSearch already holds its own bag handle (registered inside
	// BuildBaseTools); SetToolBag here is for runtime-level callers.
	rt.SetToolBag(bag)
	log.Info("aiops toolbag",
		slog.Bool("deferring", bag.IsDeferring()),
		slog.Int("threshold", bag.Threshold()),
		slog.Int("total_tools", len(bag.AllTools())),
		slog.Int("deferred_tools", len(bag.DeferredTools())),
	)
	return rt, nil
}
