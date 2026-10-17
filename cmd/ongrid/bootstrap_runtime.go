package main

import (
	"context"
	"log/slog"
	"time"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	v2 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/chatruntime"
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
	chatV2              *v2.Runtime
	bootstrapV2SkillReg *v2.SkillRegistry
	bootstrapV2AgentReg *v2.AgentRegistry
	humanApprovalBroker *managersvcaiops.HumanApprovalBroker
	aiopsSvc            *managersvcaiops.Service
	aiopsHandler        *managerserveraiops.Handler
}

func bootstrapRuntime(rootCtx context.Context, cfg *config.Config, log *slog.Logger, reg *prometheus.Registry, tracing *tracingModule, models *modelsModule, devices *devicesModule, telemetry *telemetryModule, tunnel *tunnelModule, aiops *aiopsModule, settings *settingsModule) *runtimeModule {
	// 在创建任何新 Worker 前收尾上一进程的内部会话，避免重启后永久显示运行中。
	if n, err := aiops.aiopsRepo.ReconcileInterruptedWorkers(rootCtx, time.Now().UTC()); err != nil {
		log.Error("interrupted worker recovery failed", slog.Any("err", err))
	} else if n > 0 {
		log.Info("interrupted workers closed on startup", slog.Int64("sessions", n))
	}

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
	v2Skills, v2Agents := loadV2BootstrapRegistries(log)
	var chatV2 *v2.Runtime

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
			var v2Trace einocallbacks.Handler
			if tracing.agentTraceCallback != nil {
				v2Trace = tracing.agentTraceCallback
			}
			v2rt, err := buildV2AIOpsRuntime(rootCtx, cfg, models.llmClient, models.llmRouter, aiops.toolsReg, aiops.aiopsRepo, aiops.mutatingProposalRepo, tunnel.fbClient, devices.edgeUC, devices.deviceUC, reg, log, v2Skills, v2Agents, models.llmSettingsResolver, humanApprovalBroker, v2Trace)
			engines := map[contract.Version]managersvcaiops.RuntimeEngine{contract.V3: rt}
			if err != nil {
				log.Error("v2 runtime unavailable", slog.Any("err", err))
			} else {
				chatV2 = v2rt
				engines[contract.V2] = managersvcaiops.V2Runtime{Runtime: v2rt}
			}
			router, err := managersvcaiops.NewRuntimeRouter(aiops.aiopsRepo, contract.V3, engines,
				managersvcaiops.WithRuntimeDefaultProvider(func(ctx context.Context) (contract.Version, error) {
					version, err := settings.settingSvc.AgentRuntimeVersion(ctx)
					return contract.Version(version), err
				}))
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
			// SendMessage / TaskStop follow caller cancellation without an added
			// blanket deadline. Synchronous AgentTool dispatch waits for the full
			// worker loop, so it retains its explicit 180s operation budget.
			coordDepsFast := aiopstoolsdec.Deps{
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
			if chatV2 != nil {
				v2Coord := []aiopstoolsbase.BaseTool{
					aiopstoolsdec.Wrap(aiopstools.NewAgentTool(managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime}, managersvcaiops.V2AgentRegistry{Inner: chatV2.AgentRegistry()}, log), coordDepsDispatch),
					aiopstoolsdec.Wrap(aiopstools.NewSendMessageTool(managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime}, log), coordDepsFast),
					aiopstoolsdec.Wrap(aiopstools.NewTaskStopTool(managersvcaiops.WorkerSpawner{Runtime: aiopsRuntime}, log), coordDepsFast),
				}
				if err := chatV2.AppendToolBag(v2Coord); err != nil {
					log.Error("append v2 coordinator tools failed", slog.Any("err", err))
				}
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
		chatV2:              chatV2,
		bootstrapV2SkillReg: v2Skills,
		bootstrapV2AgentReg: v2Agents,
		humanApprovalBroker: humanApprovalBroker,
		aiopsSvc:            aiopsSvc,
		aiopsHandler:        aiopsHandler,
	}
}
