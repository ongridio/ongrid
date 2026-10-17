package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	aiopsgraph "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	aiopsgraphcb "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph/callbacks"
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

// loadBootstrapRegistries walks ./agents + configured skills + the marketplace
// skill root and returns populated registries. Called once at boot
// independently of runtime availability, so /v1/agents has data to render even
// when the chat runtime can't build (no LLM provider, build failure).
//
// Env knobs mirror the values buildAIOpsRuntime used to read inline:
//
//	ONGRID_BUILTIN_AGENTS_ROOT  default ./agents
//	ONGRID_BUILTIN_SKILLS_ROOT  optional extra skills directory (no default)
//	ONGRID_SKILLS_ROOT          default /var/lib/ongrid/skills (marketplace mutable root)
func loadBootstrapRegistries(log *slog.Logger) (*aiopschatruntime.SkillRegistry, *aiopschatruntime.AgentRegistry) {
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
func buildAIOpsRuntime(
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
	traceCallback *aiopsgraphcb.TraceHandler,
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

	// 3. Skill / Agent registries — pre-loaded by loadBootstrapRegistries
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
	reviewSpawner.SetRuntime(rt)
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

// buildRuntimeChatModel shares provider configuration, not version-local execution state.
func buildRuntimeChatModel(ctx context.Context, cfg *config.Config, llmClient llm.Client, resolver *managerbizsetting.LLMSettingsResolver, log *slog.Logger) (einomodel.ToolCallingChatModel, error) {
	// 1. RoutingChatModel — one inner per provider that exists. We
	//    layer managersvcaiops.ProviderClient around the existing
	//    llmRouter so each inner ChatModel routes its Chat() call
	//    to the correct sub-Client. Models stamp their default model
	//    name from cfg so a per-call model.WithModel still wins.
	innerModels := map[string]einomodel.ChatModel{}
	addInner := func(provider, defaultModel string) {
		ic := &managersvcaiops.ProviderClient{Inner: llmClient, Provider: provider}
		m, err := llm.NewClientChatModel(llm.ClientChatModelConfig{
			Client: ic,
			Model:  defaultModel,
		})
		if err != nil {
			log.Warn("chatruntime: build inner ChatModel",
				slog.String("provider", provider), slog.Any("err", err))
			return
		}
		innerModels[provider] = m
	}
	// Build inners from the RESOLVED provider set (env + Settings-UI/DB),
	// the same source the SPA model picker uses. Previously this gated on
	// boot-time env keys only — so a provider configured via the UI (e.g.
	// anthropic, with its key in the DB and an empty env var) showed in the
	// picker but had no inner ChatModel, and picking it failed with
	// "unknown provider". The per-call key is resolved by the
	// resolver-backed llmClient, so registering the inner is all that's
	// needed. defProv comes from the resolved default (DB default_provider).
	defProv := cfg.LLM.Default
	if resolver != nil {
		if provCfgs, resolvedDefault, rerr := resolver.ResolveProviders(ctx); rerr == nil {
			for _, pc := range provCfgs {
				addInner(pc.ID, pc.Model)
			}
			if id, _ := llm.PickProviderDefault(provCfgs, resolvedDefault); id != "" {
				defProv = id
			}
		} else {
			log.Warn("chatruntime: resolve providers for inner models", slog.Any("err", rerr))
		}
	}
	// Safety net: if the resolver gave nothing (error / no rows), fall back
	// to the boot-time env-keyed providers so the kernel still wires.
	if len(innerModels) == 0 {
		if cfg.OpenAI.APIKey != "" {
			addInner(llm.ProviderOpenAI, firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"))
		}
		if cfg.LLM.Anthropic.APIKey != "" {
			addInner(llm.ProviderAnthropic, firstNonEmpty(cfg.LLM.Anthropic.Model, "claude-sonnet-4-6"))
		}
		if cfg.LLM.Zhipu.APIKey != "" {
			addInner(llm.ProviderZhipu, firstNonEmpty(cfg.LLM.Zhipu.Model, "glm-4.7"))
		}
		if cfg.LLM.Gemini.APIKey != "" {
			addInner(llm.ProviderGemini, firstNonEmpty(cfg.LLM.Gemini.Model, "gemini-2.5-pro"))
		}
	}
	// Pre-register an inner for every known provider id (incl. the generic
	// "custom" endpoint) even if unconfigured at boot, so a provider whose key
	// is added via the UI AFTER boot routes immediately — no restart. Only the
	// inner's existence is boot-time; the per-call key/baseURL is resolved
	// dynamically by llmClient. Unconfigured providers never reach the picker
	// (the /v1/aiops/models catalog gates on ResolveProviders), so they're
	// never selected; a stray call to one fails cleanly at key resolution.
	for _, id := range knownLLMProviderIDs() {
		if _, ok := innerModels[id]; !ok {
			addInner(id, "") // model supplied per-call (picker / DefaultResolver)
		}
	}
	if len(innerModels) == 0 {
		return nil, fmt.Errorf("chatruntime: no LLM provider configured")
	}
	if defProv == "" {
		defProv = llm.ProviderOpenAI
	}
	if _, ok := innerModels[defProv]; !ok {
		// Default provider not configured — pick the first configured
		// provider alphabetically so the result is deterministic across
		// restarts (Go map iteration order is randomized).
		keys := make([]string, 0, len(innerModels))
		for k := range innerModels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		defProv = keys[0]
	}
	// DefaultResolver lets calls that omit a provider (the RCA investigator
	// worker, query_translate) track the LIVE configured default — the model
	// the home-page picker writes to default_provider / <provider>_default_model
	// — instead of the boot-time defProv. The chat picker pins a provider
	// per-message and is unaffected. Resolved per-call (cheap: a settings read,
	// and only on default-routed calls, which are low-frequency).
	var defaultResolver func(context.Context) (string, string)
	if resolver != nil {
		defaultResolver = resolver.DefaultModel
	}
	chatModel, err := llm.NewRoutingChatModel(llm.RoutingChatModelConfig{
		Inner:           innerModels,
		DefaultProvider: defProv,
		DefaultResolver: defaultResolver,
	})
	if err != nil {
		return nil, fmt.Errorf("chatruntime: NewRoutingChatModel: %w", err)
	}

	return chatModel, nil
}
