package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	managerserveraiops "github.com/ongridio/ongrid/internal/manager/server/aiops"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/prometheus/client_golang/prometheus"
)

type runtimeModule struct {
	bootstrapSkillReg   *aiopschatruntime.SkillRegistry
	bootstrapAgentReg   *aiopschatruntime.AgentRegistry
	aiopsRuntime        *managersvcaiops.RuntimeRouter
	chatRT              *aiopschatruntime.Runtime
	humanApprovalBroker *managersvcaiops.HumanApprovalBroker
	aiopsSvc            *managersvcaiops.Service
	aiopsHandler        *managerserveraiops.Handler
}

func bootstrapRuntime(rootCtx context.Context, cfg *config.Config, log *slog.Logger, reg *prometheus.Registry, tracing *tracingModule, models *modelsModule, devices *devicesModule, telemetry *telemetryModule, tunnel *tunnelModule, aiops *aiopsModule) *runtimeModule {
	// AgentRegistry + SkillRegistry are loaded UNCONDITIONALLY from
	// ./agents + configured skill roots + the marketplace root. These are
	// YAML+markdown metadata, so the /v1/agents
	// endpoint should populate even when the chat runtime can't build
	// (no LLM provider configured yet, etc). This also lets the SPA
	// render the assistant list on a fresh install so the operator can
	// browse personas before wiring up a provider.
	//
	// buildAIOpsRuntime now consumes these registries instead of
	// building its own; the chat coordinator + worker dispatch references
	// the same in-memory instances we hand to aiopsHandler below.
	bootstrapSkillReg, bootstrapAgentReg := loadBootstrapRegistries(log)

	var (
		aiopsRuntime *managersvcaiops.RuntimeRouter
		// chatRT owns version-local catalog, persona and credential assembly.
		// All external execution and worker metrics use aiopsRuntime.
		chatRT *aiopschatruntime.Runtime
	)
	humanApprovalBroker := managersvcaiops.NewHumanApprovalBroker()
	{
		rt, rterr := buildAIOpsRuntime(rootCtx, cfg, models.llmClient, models.llmRouter, aiops.toolsReg, aiops.aiopsRepo, aiops.mutatingProposalRepo, tunnel.fbClient, devices.edgeUC, devices.deviceUC, reg, log, bootstrapSkillReg, bootstrapAgentReg, models.llmSettingsResolver, humanApprovalBroker, tracing.agentTraceCallback)
		if rterr != nil {
			log.Error("aiops runtime unavailable", slog.Any("err", rterr))
		} else {
			router, err := managersvcaiops.NewRuntimeRouter(aiops.aiopsRepo, contract.V3, map[contract.Version]managersvcaiops.RuntimeEngine{contract.V3: rt})
			if err != nil {
				log.Error("aiops runtime routing unavailable", slog.Any("err", err))
			} else {
				aiopsRuntime = router
			}
			chatRT = rt
			// — wire the coordinator-only AgentTool /
			// SendMessage / TaskStop trio. Two-step because of the
			// chicken-and-egg: those tools take the Runtime as their
			// spawner, but the Runtime was already built above with the
			// regular tool bag. We now (a) plug the spawner into the
			// Registry so BuildBaseTools yields the 3 new tools, then
			// (b) wrap them through the standard decorator chain, then
			// (c) bolt them onto the Runtime's tool bag so the
			// coordinator graph sees them. Workers don't observe these
			// — chatruntime.filterToolsForAgent strips them
			// unconditionally via coordinatorOnlyTools (see
			// chatruntime/worker.go).
			aiops.toolsReg.SetWorkerSpawner(
				managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime},
				managersvcaiops.AgentRegistry{Inner: rt.AgentRegistry()},
			)
			// SendMessage / TaskStop are control-plane micro-ops; 15s
			// is plenty. AgentTool is the odd one out: synchronous
			// dispatch blocks until the worker LLM finishes its full
			// ReAct loop, which can legitimately take 60-120s on
			// non-trivial diagnoses. Use a separate deps with a much
			// larger timeout so AgentTool isn't killed mid-worker —
			// without this, every dispatch returns "tool timed out
			// after 15s" and the coordinator loops trying to
			// re-dispatch.
			coordDepsFast := aiopstoolsdec.Deps{
				Timeout:       15 * time.Second,
				Limiter:       aiopstoolsdec.NewTokenBucketLimiter(0),
				Registerer:    reg,
				HumanApproval: humanApprovalBroker,
			}
			coordDepsDispatch := aiopstoolsdec.Deps{
				Timeout:       180 * time.Second,
				Limiter:       aiopstoolsdec.NewTokenBucketLimiter(0),
				Registerer:    reg,
				HumanApproval: humanApprovalBroker,
			}
			wrappedCoord := []aiopstoolsbase.BaseTool{
				aiopstoolsdec.Wrap(aiopstools.NewAgentTool(managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime}, managersvcaiops.AgentRegistry{Inner: rt.AgentRegistry()}, log), coordDepsDispatch),
				aiopstoolsdec.Wrap(aiopstools.NewSendMessageTool(managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime}, log), coordDepsFast),
				aiopstoolsdec.Wrap(aiopstools.NewTaskStopTool(managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime}, log), coordDepsFast),
			}
			if err := rt.AppendToolBag(wrappedCoord); err != nil {
				log.Error("append coordinator tools failed", slog.Any("err", err))
			}
			log.Info("aiops runtime wired",
				slog.Int("tool_count", rt.ToolCount()),
				slog.Any("tool_names", rt.ToolNames(rootCtx)),
			)
		}
	}

	aiopsSvc := managersvcaiops.New(aiopsRuntime, aiops.aiopsRepo, aiops.aiopsUsage, log)
	aiopsSvc.SetMutatingProposalRepo(aiops.mutatingProposalRepo)
	aiopsSvc.WithAPMSourceResolver(telemetry.apmService)
	aiopsSvc.SetAttachmentRepo(aiops.aiopsRepo)
	aiopsSvc.SetModelCatalog(models.llmRouter)
	aiopsHandler := managerserveraiops.NewHandler(aiopsSvc)
	aiopsHandler.SetOperationActions(aiops.operationUC, managersvcaiops.PacketCaptureOperationAction(aiops.operationUC, aiops.packetCaptureUC))

	return &runtimeModule{
		bootstrapSkillReg:   bootstrapSkillReg,
		bootstrapAgentReg:   bootstrapAgentReg,
		aiopsRuntime:        aiopsRuntime,
		chatRT:              chatRT,
		humanApprovalBroker: humanApprovalBroker,
		aiopsSvc:            aiopsSvc,
		aiopsHandler:        aiopsHandler,
	}
}
