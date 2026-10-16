package main

import (
	"context"
	"log/slog"
	"time"

	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
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
	aiopsRuntime        managersvcaiops.RuntimeHandler
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
		aiopsRuntime managersvcaiops.RuntimeHandler
		// chatRT keeps the concrete runtime handle so the ADR-026
		// self-obs sampler ticker (eg.Go in the goroutine wiring below)
		// can call CountWorkersByStatus. The interface-typed
		// aiopsRuntime is what the chat service consumes.
		chatRT *aiopschatruntime.Runtime
	)
	humanApprovalBroker := managersvcaiops.NewHumanApprovalBroker()
	if aiops.kernel == managersvcaiops.KernelGraph {
		rt, rterr := buildAIOpsRuntime(rootCtx, cfg, models.llmClient, models.llmRouter, aiops.toolsReg, aiops.aiopsRepo, aiops.mutatingProposalRepo, tunnel.fbClient, devices.edgeUC, devices.deviceUC, reg, log, bootstrapSkillReg, bootstrapAgentReg, models.llmSettingsResolver, humanApprovalBroker, tracing.agentTraceCallback)
		if rterr != nil {
			log.Warn("aiops runtime build failed — falling back to legacy kernel", slog.Any("err", rterr))
			aiops.kernel = managersvcaiops.KernelLegacy
		} else {
			aiopsRuntime = rt
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
				managersvcaiops.WorkerSpawner{Runtime: rt},
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
				aiopstoolsdec.Wrap(aiopstools.NewAgentTool(managersvcaiops.WorkerSpawner{Runtime: rt}, managersvcaiops.AgentRegistry{Inner: rt.AgentRegistry()}, log), coordDepsDispatch),
				aiopstoolsdec.Wrap(aiopstools.NewSendMessageTool(managersvcaiops.WorkerSpawner{Runtime: rt}, log), coordDepsFast),
				aiopstoolsdec.Wrap(aiopstools.NewTaskStopTool(managersvcaiops.WorkerSpawner{Runtime: rt}, log), coordDepsFast),
			}
			rt.AppendToolBag(wrappedCoord)
			log.Info("aiops runtime wired",
				slog.Int("tool_count", rt.ToolCount()),
				slog.Any("tool_names", rt.ToolNames(rootCtx)),
			)
		}
	}

	aiopsSvc := managersvcaiops.NewWithKernel(aiops.aiopsAgent, aiopsRuntime, aiops.kernel, aiops.aiopsRepo, aiops.aiopsUsage, log)
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
