// Command ongrid-edge is the edge-side binary. It opens a tunnel to cloud,
// pushes host metrics, and serves tool RPC handlers (get_host_load / ...).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"

	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/httpserver"
	"github.com/ongridio/ongrid/internal/pkg/logger"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"

	edgebiz "github.com/ongridio/ongrid/internal/edgeagent/biz"
	edgeconfig "github.com/ongridio/ongrid/internal/edgeagent/config"
	edgek8s "github.com/ongridio/ongrid/internal/edgeagent/k8s"
	edgeoperator "github.com/ongridio/ongrid/internal/edgeagent/operator"
	edgesvc "github.com/ongridio/ongrid/internal/edgeagent/service"
	edgestreamrouter "github.com/ongridio/ongrid/internal/edgeagent/streamrouter"
	"github.com/ongridio/ongrid/internal/edgeagent/supervisorhealth"
	edgewebshell "github.com/ongridio/ongrid/internal/edgeagent/webshell"

	// Builtin skill init() blocks register Executors with the shared
	// internal/skill registry. The edge-side dispatcher
	// (internal/edgeagent/skill) routes execute_skill RPCs by key —
	// without this import the registry is empty and every skill call
	// returns "unknown skill".
	_ "github.com/ongridio/ongrid/internal/skill/builtin"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

// edgeMetricsAddr is the local debug /metrics port for edge. Kept separate
// from cloud metrics (:9100) so both can run on the same dev host.
const edgeMetricsAddr = ":9101"

// Keep the Edge's own runtime profiles local and off the conventional :6060
// application port so the default target cannot silently profile another app.
const edgePprofAddr = "127.0.0.1:16060"

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	if handled, err := runK8sHostCommand(context.Background(), os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "kubernetes host runtime: %v\n", err)
			return err
		}
		return nil
	}
	if handled, err := runK8sUpgradeCommand(context.Background(), os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "kubernetes upgrade preparation: %v\n", err)
			return err
		}
		return nil
	}
	if handled, err := runEnrollmentCommand(context.Background(), os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "edge enrollment: %v\n", err)
			return err
		}
		return nil
	}

	// Print-and-exit flags before anything that can fail (config load,
	// env access). install.sh and operators rely on `ongrid-edge --version`
	// printing the build tag without starting the agent.
	for _, a := range os.Args[1:] {
		switch a {
		case "--version", "-v":
			fmt.Fprintf(os.Stdout, "ongrid-edge %s\n", version)
			return nil
		case "--help", "-h":
			fmt.Fprintf(os.Stdout, "ongrid-edge %s\n", version)
			fmt.Fprintln(os.Stdout, "Run as a systemd service. See /etc/ongrid-edge/ongrid-edge.env for config.")
			return nil
		}
	}

	fmt.Fprintf(os.Stderr, "ongrid-edge %s starting\n", version)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		return err
	}

	log := logger.WithService(logger.New(slog.LevelInfo), "ongrid-edge")
	log.Info("configuration loaded",
		slog.String("cloud_addr", cfg.Edge.CloudAddr),
		slog.String("collector_mode", cfg.Edge.CollectorMode),
		slog.String("version", version),
	)

	// Load the DPAPI-encrypted broker token. Prefer secrets.enc; fall
	// back to the env var when the file is missing or fails to decrypt.
	secretsPath := cfg.Edge.SecretsFile
	if secretsPath == "" {
		secretsPath = defaultSecretsFile()
	}
	if secretsPath != "" {
		if token, err := edgeconfig.LoadSecrets(secretsPath); err == nil {
			cfg.Edge.SecretKey = token
			log.Info("loaded broker token from secrets.enc",
				slog.String("path", secretsPath))
		} else {
			// A missing file is the normal pre-install state (the env
			// var is used); a decrypt failure is not.
			log.Warn("secrets.enc load failed, using env var fallback",
				slog.String("path", secretsPath),
				slog.String("err", err.Error()))
		}
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if handled, err := runK8sDataPlaneMode(rootCtx, strings.TrimSpace(os.Getenv("ONGRID_EDGE_MODE")), log); handled {
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("kubernetes data plane stopped with error", slog.Any("err", err))
			return err
		}
		log.Info("kubernetes data plane shutdown complete")
		return nil
	}

	k8sInfo, enrollmentPending, err := edgek8s.EnsureEnrollment(rootCtx, cfg, version, log)
	if err != nil {
		log.Error("kubernetes enrollment failed", slog.Any("err", err))
		return err
	}

	reg := prom.NewRegistry()

	// Tunnel client.
	client := tunnel.NewClient(tunnel.ClientConfig{
		CloudAddr:     cfg.Edge.CloudAddr,
		AccessKey:     cfg.Edge.AccessKey,
		SecretKey:     cfg.Edge.SecretKey,
		TLSCAFile:     cfg.Edge.TLSCAFile,
		TLSServerName: cfg.Edge.TLSServerName,
		TLSRequired:   cfg.Edge.TLSRequired,
		Log:           log,
	})

	eg, egCtx := errgroup.WithContext(rootCtx)
	if enrollmentPending {
		recoveryCfg := *cfg
		eg.Go(func() error { return edgek8s.RetryNodeEnrollment(egCtx, recoveryCfg, version, log) })
	}
	if edgek8s.IsController(k8sInfo) && strings.TrimSpace(os.Getenv("ONGRID_K8S_TELEMETRY_SECRET")) != "" {
		eg.Go(func() error { return edgek8s.RunTelemetryConfigSync(egCtx, cfg, k8sInfo, log) })
	}

	// Heartbeat writer (Windows only, no-op stub on Linux) so
	// supervisor.exe can monitor this worker process via the
	// health.json IPC.
	eg.Go(func() error { return supervisorhealth.RunWriter(egCtx, log) })

	// Build the collector based on configured mode.
	collector, scraperRunner, err := buildCollector(egCtx, cfg, log, eg)
	if err != nil {
		log.Error("collector init failed", slog.Any("err", err))
		return err
	}
	_ = scraperRunner // scraper goroutine already wired into eg below

	edgesvc.RegisterWithCollector(client, collector, log)

	// Platform-specific capabilities (host_files / restart_service /
	// bash on Linux; host_files only on Windows — see
	// capabilities_{linux,windows}.go). All Register calls are
	// soft-fail: the edge boots and the operator sees a warning in the
	// log when a capability is disabled. Kubernetes controllers skip
	// host handlers; node-mode agents and standalone hosts keep them.
	if k8sInfo == nil || edgek8s.IsNode(k8sInfo) {
		registerEdgeCapabilities(client, log)
		operatorLog := log.With(slog.String("comp", "operator"))
		edgeoperator.Register(client, operatorLog)

		// WebSSH: edge is a stream port-forwarder. Manager carries the
		// allowed loopback port in the SSH identification line, then edge
		// io.Copy's bytes both ways. SSH client
		// lives entirely on the manager — see internal/manager/server/
		// webshell. The edge has no SSH lib, no PTY, no session map.
		webshellLog := log.With(slog.String("comp", "webshell"))
		edgestreamrouter.Register(client, map[string]edgestreamrouter.Handler{
			tunnel.StreamKindOperatorExec: func(stream tunnel.StreamConn) {
				edgeoperator.HandleStream(stream, operatorLog)
			},
		}, func(stream tunnel.StreamConn) {
			edgewebshell.HandleStream(stream, webshellLog)
		}, log.With(slog.String("comp", "streamrouter")))
	} else {
		log.Info("kubernetes controller: host handlers disabled",
			slog.String("role", k8sInfo.Role),
			slog.Uint64("cluster_id", k8sInfo.ClusterID),
		)
	}

	// UpgradeStageDir defaults to the platform-specific staging
	// directory (see paths_{linux,windows}.go). Empty disables
	// agent_upgrade entirely (dev / non-systemd); set OVERRIDE via env
	// to relocate for tests.
	stageDir := os.Getenv("ONGRID_EDGE_UPGRADE_STAGE_DIR")
	if stageDir == "" {
		stageDir = defaultStageDir()
	}
	agent := edgebiz.NewAgent(client, collector, edgebiz.Config{
		MetricsInterval:          cfg.Edge.CollectorInterval,
		NetworkDiscoveryEnabled:  edgeconfig.EnvBool("ONGRID_NETWORK_DISCOVERY_ENABLED", true),
		NetworkDiscoveryInterval: edgeconfig.EnvDuration("ONGRID_NETWORK_DISCOVERY_INTERVAL", time.Minute),
		AgentVersion:             version,
		Kubernetes:               k8sInfo,
		UpgradeStageDir:          stageDir,
		PacketCaptureDir:         edgeconfig.EnvOr("ONGRID_PACKET_CAPTURE_DIR", "/var/lib/ongrid-edge/pcap"),
	}, log)

	if edgek8s.IsController(k8sInfo) {
		inventoryInterval := edgeconfig.EnvDuration("ONGRID_K8S_INVENTORY_INTERVAL", 10*time.Minute)
		inventoryWatch := edgeconfig.EnvBool("ONGRID_K8S_INVENTORY_WATCH", true)
		pusher, err := edgek8s.NewInventoryPusher(
			client,
			*k8sInfo,
			agent.EdgeID,
			inventoryInterval,
			inventoryWatch,
			log.With(slog.String("comp", "k8s-inventory")),
		)
		if err != nil {
			log.Warn("k8s inventory disabled", slog.Any("err", err))
		} else {
			pusher.RegisterHandlers()
			eg.Go(func() error { return pusher.Run(egCtx) })
		}
		metricsEndpoint := strings.TrimSpace(os.Getenv("ONGRID_K8S_METRICS_ENDPOINT"))
		gatewayMetricsEndpoint := strings.TrimSpace(os.Getenv("ONGRID_K8S_GATEWAY_METRICS_ENDPOINT"))
		telemetryGatewayMetricsEnabled := edgeconfig.EnvBool("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false)
		if gatewayMetricsEndpoint == "" && telemetryGatewayMetricsEnabled {
			gatewayMetricsEndpoint = "http://127.0.0.1:9464/metrics"
		}
		appMetricsDiscovery := edgeconfig.EnvBool("ONGRID_K8S_APP_METRICS_DISCOVERY", false)
		if metricsEndpoint != "" || gatewayMetricsEndpoint != "" || appMetricsDiscovery {
			metricsPusher, err := edgek8s.NewMetricsPusher(
				client,
				*k8sInfo,
				agent.EdgeID,
				edgek8s.MetricsConfig{
					Endpoint:         metricsEndpoint,
					GatewayEndpoint:  gatewayMetricsEndpoint,
					Interval:         edgeconfig.EnvDuration("ONGRID_K8S_METRICS_INTERVAL", 30*time.Second),
					Timeout:          edgeconfig.EnvDuration("ONGRID_K8S_METRICS_TIMEOUT", 15*time.Second),
					PushTimeout:      edgeconfig.EnvDuration("ONGRID_K8S_METRICS_PUSH_TIMEOUT", 30*time.Second),
					SampleLimit:      edgeconfig.EnvInt("ONGRID_K8S_METRICS_SAMPLE_LIMIT", 250000),
					BatchSampleLimit: edgeconfig.EnvInt("ONGRID_K8S_METRICS_BATCH_SAMPLE_LIMIT", 10000),
					BatchByteLimit:   edgeconfig.EnvInt("ONGRID_K8S_METRICS_BATCH_BYTE_LIMIT", 4<<20),
					DiscoverApps:     appMetricsDiscovery,
					AppMetricsScope: func(ctx context.Context) (autoapm.MetricsScope, error) {
						return edgek8s.LoadAppMetricsScope(ctx, k8sInfo)
					},
				},
				log.With(slog.String("comp", "k8s-metrics")),
				edgek8s.WithMetricsRegisterer(reg),
			)
			if err != nil {
				log.Warn("k8s metrics disabled", slog.Any("err", err))
			} else {
				eg.Go(func() error { return metricsPusher.Run(egCtx) })
			}
		}
	}

	// Local /metrics listener for debugging.
	metricsMux := chi.NewRouter()
	metricsMux.Handle("/metrics", prom.Handler(reg))
	metricsMux.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	eg.Go(func() error {
		metricsLog := log.With(slog.String("listener", "metrics"))
		if edgek8s.IsController(k8sInfo) {
			// Legacy controllers retain their fixed diagnostics listener.
			return httpserver.New(edgeMetricsAddr, metricsMux, metricsLog).Start(egCtx)
		}
		return runEdgeMetrics(egCtx, metricsMux, metricsLog)
	})
	pprofServer := httpserver.New(edgePprofAddr, http.DefaultServeMux, log.With(slog.String("listener", "pprof")))
	eg.Go(func() error {
		if err := pprofServer.Start(egCtx); err != nil {
			log.Warn("pprof server disabled", slog.Any("err", err))
		}
		return nil
	})
	// When agent.Run returns (clean ctx cancel OR upgrade
	// swap), cancel rootCtx so every other goroutine — plugin
	// supervisor, scraper, metrics http server — unwinds and the
	// process exits. Without this, returning nil from agent.Run
	// leaves siblings on tickers and eg.Wait() blocks forever;
	// systemd never gets the EXIT it needs to swap the staged bundle.
	eg.Go(func() error {
		defer cancel()
		return agent.Run(egCtx)
	})

	startPlugins(egCtx, cfg, log, client, agent, k8sInfo, eg)

	err = eg.Wait()

	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("shutdown with error", slog.Any("err", err))
		return err
	}
	log.Info("ongrid-edge shutdown complete")
	return nil
}
