package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	managerbizoperatorrun "github.com/ongridio/ongrid/internal/manager/biz/operatorrun"
	managerwebshellbiz "github.com/ongridio/ongrid/internal/manager/biz/webshell"
	manageragenttracedata "github.com/ongridio/ongrid/internal/manager/data/agenttrace"
	managerapmdata "github.com/ongridio/ongrid/internal/manager/data/apm"
	managerwebshelldata "github.com/ongridio/ongrid/internal/manager/data/webshell/store"
	managerwebshellserver "github.com/ongridio/ongrid/internal/manager/server/webshell"
	managersvcfb "github.com/ongridio/ongrid/internal/manager/service/frontierbound"
	managersvclogs "github.com/ongridio/ongrid/internal/manager/service/logs"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type tunnelModule struct {
	fbClient        *managersvcfb.Client
	operatorRunSvc  *managerbizoperatorrun.Service
	webshellHandler *managerwebshellserver.Handler
}

func bootstrapTunnel(rootCtx context.Context, cfg *config.Config, log *slog.Logger, cleanup *startupCleanup, storage *storageModule, iam *iamModule, devices *devicesModule, alerts *alertsModule, telemetry *telemetryModule) (*tunnelModule, error) {
	// Frontierbound service-end SDK: opens a long-lived service connection
	// to the upstream frontier broker (a separate docker container) and
	// installs lifecycle callbacks + reverse-call handlers. aiops tools
	// reuse fbClient.Call to dispatch back to specific edges.
	//
	// ONGRID_FRONTIER_DISABLED=true bypasses the dial entirely — the
	// resulting Client errors all Call/OpenStream/NotifyX with
	// frontierbound.ErrDisabled and is a no-op for Register. Used by the
	// e2e harness so manager can come up without a real broker. The HTTP
	// surface and DB stack are unaffected; edge-tunnel-only features
	// (webssh, edge reverse calls) surface ErrDisabled at the call site.
	var fbClient *managersvcfb.Client
	if cfg.FrontierClient.Disabled {
		log.Warn("frontierbound: disabled (ONGRID_FRONTIER_DISABLED=true) — edge-tunnel features will error at call site")
		fbClient = managersvcfb.NewDisabled(log.With(slog.String("comp", "frontierbound")))
	} else {
		c, err := managersvcfb.New(managersvcfb.Config{
			Addr:        cfg.FrontierClient.Addr,
			ServiceName: cfg.FrontierClient.ServiceName,
		}, log.With(slog.String("comp", "frontierbound")))
		if err != nil {
			log.Error("frontierbound: new client", slog.Any("err", err))
			return nil, err
		}
		fbClient = c
	}
	cleanup.add(func() {
		if err := fbClient.Close(); err != nil {
			log.Warn("frontierbound: close", slog.Any("err", err))
		}
	})
	// Back-fill the edge service's tunnel dispatcher now that fbClient
	// exists. Until this point UpgradeAgent surfaced a "not wired" error
	// — by design, because we don't accept HTTP traffic until later.
	devices.edgeSvc.SetEdgeCaller(fbClient)
	telemetry.apmService.WithReceivers(managerapmdata.NewReceiverResolver(devices.k8sUC, devices.edgeDeviceRepo, fbClient), log.With(slog.String("comp", "apm")))
	telemetry.agentTraceService.WithReceivers(manageragenttracedata.NewReceiverResolver(managerapmdata.NewReceiverResolver(devices.k8sUC, devices.edgeDeviceRepo, fbClient)))
	devices.networkDiscoveryUC.SetEdgeCaller(fbClient)

	// promIngester for the Wiring is typed as the interface; passing a
	// typed-nil *Ingester would be a non-nil interface, so explicitly hand
	// the handler a true nil when Prom is disabled.
	var promWiring managersvcfb.PromwriteIngester
	if telemetry.promwriteIngester != nil {
		promWiring = telemetry.promwriteIngester
	}

	// WebSSH plumbing — built before frontierbound.Install so the
	// shell_output / shell_exit edge-to-manager handlers can route
	// pushes through the live router.
	webshellRouter := managerwebshellbiz.NewRouter()
	webshellAuditRepo := managerwebshelldata.NewRepo(storage.db)
	webshellAccess := managerwebshellbiz.NewAccess(webshellAuditRepo)
	operatorRunSvc := managerbizoperatorrun.New(fbClient, log.With(slog.String("comp", "operator-run")))

	if err := managersvcfb.Install(rootCtx, fbClient, managersvcfb.Wiring{
		EdgeAuthn:      devices.edgeAuthn,
		EdgeUC:         devices.edgeUC,
		MetricIngester: alerts.metricIngestSvc,
		PromIngester:   promWiring,
		PluginConfigUC: devices.pluginConfigUC,
		PluginSecrets:  telemetry.logsBackendSvc,
		WebshellRouter: webshellRouter,
		// DeviceResolver wires the post-split edge_id → device_id
		// resolution path (push pipeline). The biz junction repo is the
		// source of truth.
		DeviceResolver:   devices.edgeDeviceRepo,
		K8sRegistry:      devices.k8sSvc,
		K8sInventory:     devices.k8sSvc,
		NetworkDiscovery: devices.networkDiscoveryUC,
		Log:              log.With(slog.String("comp", "frontierbound")),
	}); err != nil {
		log.Error("frontierbound: install handlers", slog.Any("err", err))
		return nil, err
	}
	if err := fbClient.Register(rootCtx, tunnel.MethodOperatorPushEvent, func(rpcCtx context.Context, transportID uint64, body []byte) ([]byte, error) {
		edgeID := fbClient.CanonicalizeEdgeID(transportID)
		if edgeID == 0 {
			return nil, fmt.Errorf("operator push_event: edge binding not ready")
		}
		return operatorRunSvc.HandlePushEvent(rpcCtx, edgeID, body)
	}); err != nil {
		log.Error("frontierbound: install operator push handler", slog.Any("err", err))
		return nil, err
	}
	// Back-fill the reload notifier now that fbClient is alive — earlier
	// PluginConfigUC was constructed with notifier=nil because frontierbound
	// hadn't been built yet. From here on, mutating plugin config kicks a
	// real-time push to the affected edge.
	devices.pluginConfigUC.SetNotifier(fbClient)
	devices.pluginConfigUC.SetDatabaseMetricsSecretWriter(fbClient)
	telemetry.logsBackendSvc.SetBackendChangeNotifier(managersvclogs.NewPluginReloadBroadcaster(devices.edgeUC, fbClient, log.With(slog.String("comp", "logs-backend-change-notifier"))))

	// WebSSH HTTP handler — uses fbClient.OpenStream to layer ssh +
	// pty over a raw byte stream into edge:127.0.0.1:22. SSH client
	// runs in the manager; edge is a dumb byte forwarder.
	webshellHandler := managerwebshellserver.NewHandler(
		webshellStreamerAdapter{c: fbClient},
		webshellRouter,
		managerwebshelldata.NewRecorder(webshellAuditRepo),
		webshellAccess,
		devices.deviceRepo,
		devices.edgeRepo,
		log.With(slog.String("comp", "webshell")),
	)
	webshellHandler.SetAuthz(iam.authzMW)

	return &tunnelModule{
		fbClient:        fbClient,
		operatorRunSvc:  operatorRunSvc,
		webshellHandler: webshellHandler,
	}, nil
}

type webshellStreamerAdapter struct {
	c *managersvcfb.Client
}

func (a webshellStreamerAdapter) OpenStream(ctx context.Context, edgeID uint64) (io.ReadWriteCloser, error) {
	return a.c.OpenStream(ctx, edgeID)
}
