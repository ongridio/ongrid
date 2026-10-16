package main

import (
	"context"
	managersvcimbridge "github.com/ongridio/ongrid/internal/manager/service/imbridge"
	"log/slog"

	managerbizaiopsmentions "github.com/ongridio/ongrid/internal/manager/biz/aiops/mentions"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	managerbizimbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	managerbizimbridgedingtalk "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/dingtalk"
	managerbizimbridgefeishu "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/feishu"
	managerbizimbridgeslack "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/slack"
	managerbizimbridgetelegram "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/telegram"
	manageraiopsdata "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	managerserverimbridge "github.com/ongridio/ongrid/internal/manager/server/imbridge"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
)

type imbridgeModule struct {
	imbridgeHandler *managerserverimbridge.Handler
}

func bootstrapIMBridge(rootCtx context.Context, log *slog.Logger, storage *storageModule, models *modelsModule, devices *devicesModule, alerts *alertsModule, telemetry *telemetryModule, aiops *aiopsModule, runtime *runtimeModule) *imbridgeModule {
	// IM bridge: multi-turn chat via Feishu (S1) / DingTalk
	// (S2 follow-up). Inbound webhooks land outside the bearer-auth
	// group; signature verification is enforced inside the handler.
	// Threads map to ongrid chat_sessions owned by a service-account
	// user — S3 will replace that with per-IM-user binding.
	// Service-account user_id: superuser admin (id=1 on every install
	// thanks to bootstrap). Future: take from cfg.
	const imBridgeServiceUserID uint64 = 1
	imbridgeAgentAdapter := managersvcimbridge.NewAiopsAdapter(runtime.aiopsSvc, imBridgeServiceUserID, models.llmSettingsResolver, log)
	imbridgeSvc := managerbizimbridge.NewBridge(aiops.imbridgeRepo, imbridgeAgentAdapter, imBridgeServiceUserID, log)
	imbridgeHandler := managerserverimbridge.NewHandler(imbridgeSvc, aiops.imbridgeRepo, aiops.imbridgeUC, log)

	// Stream supervisor: long-connection mode. Runs one
	// goroutine per (enabled, stream-mode) ImApp; reconciles every
	// 30s against the DB. Factories are registered separately so we
	// don't drag in the Feishu / DingTalk SDKs from this file —
	// they live under internal/manager/biz/imbridge/provider/{feishu,
	// dingtalk}/stream and self-register via stream_supervisor.go's
	// RegisterFactory hook. Without a factory the supervisor just
	// logs "no factory for provider — skipping" and the webhook path
	// is still available as fallback.
	imbridgeStreamSupervisor := managerbizimbridge.NewStreamSupervisor(aiops.imbridgeRepo, imbridgeSvc, log)
	// Register long-connection providers. All connections dial out, so
	// operators do not need to expose public webhook endpoints.
	imbridgeStreamSupervisor.RegisterFactory("feishu", managerbizimbridgefeishu.NewStreamFactory(log))
	imbridgeStreamSupervisor.RegisterFactory("dingtalk", managerbizimbridgedingtalk.NewStreamFactory(log))
	// Telegram is stream-only (getUpdates long-poll, outbound → proxy-
	// friendly behind GFW). Sender allowlist enforced in the provider
	// (ADR-031).
	imbridgeStreamSupervisor.RegisterFactory("telegram", managerbizimbridgetelegram.NewStreamFactory(log))
	// Slack is stream-only via Socket Mode (outbound WebSocket → same
	// proxy-friendly philosophy as Telegram getUpdates). Allowlist
	// enforced in the provider so a misconfigured open workspace can't
	// turn the agent into an LLM toy for random members.
	imbridgeStreamSupervisor.RegisterFactory("slack", managerbizimbridgeslack.NewStreamFactory(log))
	go imbridgeStreamSupervisor.Run(rootCtx)

	// @-mention search backend (HLD: ChatInput @-popover). The shared runtime
	// Loki client follows integration URL, auth and TLS edits without restart.
	// Nil device/alert dependencies still degrade their corresponding types.
	mentionSearcher := managerbizaiopsmentions.New(devices.deviceUC, alerts.alertUC, telemetry.lokiLogClient)
	runtime.aiopsHandler.SetMentionSearcher(mentionSearcher)
	// Provider catalog → /v1/aiops/models. The router has the canonical
	// list; the handler reads from it via a narrow interface so wiring
	// stays one-way.
	runtime.aiopsHandler.SetModelCatalog(models.llmRouter)
	// LLM client for /v1/aiops/query-translate (NL → LogQL/TraceQL/PromQL).
	// Optional helper — endpoint 503s when nil; SPA hides the ✨ button.
	runtime.aiopsHandler.SetLLMClient(models.llmClient)
	// Agent persona inventory → /v1/agents. We use the bootstrap
	// registry directly so the SPA's assistant list renders even when
	// the graph runtime didn't build (no LLM provider yet on fresh
	// install). Chat dispatch still 503s in that case; reading personas
	// doesn't need a chat runtime.
	if runtime.bootstrapAgentReg != nil {
		runtime.aiopsHandler.SetAgentLister(runtime.bootstrapAgentReg)
		// Phase-3 user-defined personas — CRUD + DB persistence + live
		// registry mutation. Hydrate registry from DB on boot so
		// persisted user agents survive restarts. Wires regardless of
		// kernel.
		userAgentRepo := manageraiopsdata.NewUserAgentRepo(storage.db)
		userAgentSvc := managersvcaiops.NewUserAgentService(userAgentRepo, runtime.bootstrapAgentReg,
			log.With(slog.String("comp", "user-agent")))
		if hErr := userAgentSvc.HydrateRegistry(rootCtx); hErr != nil {
			log.Warn("user-agent: hydrate registry failed", slog.Any("err", hErr))
		}
		runtime.aiopsHandler.SetUserAgentManager(userAgentSvc)
	}
	// Hand the agent the mention resolver so @-mentions get inlined
	// into the user message prelude on each Run. The agent uses its
	// own Mention type to keep the agent package dep-light; an adapter
	// shuttles between the two shapes here at the wiring site.
	// Mirror the same wiring on the new graph kernel runtime when it
	// was successfully constructed. Same searcher, two adapter shapes
	// — see the type definitions at the bottom of this file.
	if rt, ok := runtime.aiopsRuntime.(*aiopschatruntime.Runtime); ok && rt != nil {
		rt.SetMentionResolver(managersvcaiops.ChatMentionResolver{Inner: mentionSearcher})
	}

	return &imbridgeModule{
		imbridgeHandler: imbridgeHandler,
	}
}
