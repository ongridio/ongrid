// Command ongrid-edge is the edge-side binary. It opens a tunnel to cloud,
// pushes host metrics, and serves tool RPC handlers (get_host_load / ...).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/httpserver"
	"github.com/ongridio/ongrid/internal/pkg/logger"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"

	edgebash "github.com/ongridio/ongrid/internal/edgeagent/bash"
	edgebiz "github.com/ongridio/ongrid/internal/edgeagent/biz"
	edgecollector "github.com/ongridio/ongrid/internal/edgeagent/collector"
	edgehostfiles "github.com/ongridio/ongrid/internal/edgeagent/host_files"
	edgek8s "github.com/ongridio/ongrid/internal/edgeagent/k8s"
	edgeplugins "github.com/ongridio/ongrid/internal/edgeagent/plugins"
	edgeplugincustommetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/custommetrics"
	edgeplugindatabasemetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/databasemetrics"
	edgepluginhostmetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/hostmetrics"
	edgepluginlogs "github.com/ongridio/ongrid/internal/edgeagent/plugins/logs"
	edgepluginmetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/metrics"
	edgepluginprocmetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/procmetrics"
	edgeplugintraces "github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	edgerestartservice "github.com/ongridio/ongrid/internal/edgeagent/restart_service"
	edgesvc "github.com/ongridio/ongrid/internal/edgeagent/service"
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

func main() {
	if handled, err := runK8sHostCommand(context.Background(), os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "kubernetes host runtime: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Print-and-exit flags before anything that can fail (config load,
	// env access). install.sh and operators rely on `ongrid-edge --version`
	// printing the build tag without starting the agent.
	for _, a := range os.Args[1:] {
		switch a {
		case "--version", "-v":
			fmt.Fprintf(os.Stdout, "ongrid-edge %s\n", version)
			return
		case "--help", "-h":
			fmt.Fprintf(os.Stdout, "ongrid-edge %s\n", version)
			fmt.Fprintln(os.Stdout, "Run as a systemd service. See /etc/ongrid-edge/ongrid-edge.env for config.")
			return
		}
	}

	fmt.Fprintf(os.Stderr, "ongrid-edge %s starting\n", version)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}

	log := logger.WithService(logger.New(slog.LevelInfo), "ongrid-edge")
	log.Info("configuration loaded",
		slog.String("cloud_addr", cfg.Edge.CloudAddr),
		slog.String("collector_mode", cfg.Edge.CollectorMode),
		slog.String("version", version),
	)

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if handled, err := runK8sDataPlaneMode(rootCtx, strings.TrimSpace(os.Getenv("ONGRID_EDGE_MODE")), log); handled {
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("kubernetes data plane stopped with error", slog.Any("err", err))
			os.Exit(1)
		}
		log.Info("kubernetes data plane shutdown complete")
		return
	}

	k8sInfo, err := ensureK8sEnrollment(rootCtx, cfg, log)
	if err != nil {
		log.Error("kubernetes enrollment failed", slog.Any("err", err))
		os.Exit(1)
	}

	reg := prom.NewRegistry()

	// Tunnel client.
	client := tunnel.NewClient(tunnel.ClientConfig{
		CloudAddr: cfg.Edge.CloudAddr,
		AccessKey: cfg.Edge.AccessKey,
		SecretKey: cfg.Edge.SecretKey,
		Log:       log,
	})

	eg, egCtx := errgroup.WithContext(rootCtx)
	if isK8sController(k8sInfo) && strings.TrimSpace(os.Getenv("ONGRID_K8S_TELEMETRY_SECRET")) != "" {
		eg.Go(func() error { return runK8sTelemetryConfigSync(egCtx, cfg, k8sInfo, log) })
	}

	// Build the collector based on configured mode.
	collector, scraperRunner, err := buildCollector(egCtx, cfg, log, eg)
	if err != nil {
		log.Error("collector init failed", slog.Any("err", err))
		os.Exit(1)
	}
	_ = scraperRunner // scraper goroutine already wired into eg below

	edgesvc.RegisterWithCollector(client, collector, log)

	// host_files plugin (PR-8 + PR-N of): register the three
	// filesystem inspection handlers (find_large_files / du_summary /
	// stat_file). Real shell-out gated by SandboxConfig — failure to
	// validate the sandbox (no allowed paths, missing find/du in PATH)
	// is non-fatal: the edge boots without the host_files capability
	// and operators see the warning in the journal.
	if k8sInfo == nil || isK8sNode(k8sInfo) {
		if err := edgehostfiles.Register(client, log); err != nil {
			log.Warn("host_files register failed; capability disabled", slog.Any("err", err))
		}

		// restart_service plugin (/ first MUTATING skill).
		// Mocked posture in PR-7: handler returns Mocked=true without
		// shelling out. SandboxConfig.Validate enforces a non-empty
		// allow-list; on failure we boot without the capability so the
		// edge can still scrape metrics / read files.
		if err := edgerestartservice.Register(client, log); err != nil {
			log.Warn("restart_service register failed; capability disabled", slog.Any("err", err))
		}

		// bash skill: generic read-only shell-execution gated by
		// internal/edgeagent/cmdpolicy. The cmdpolicy package owns the
		// rules (binary classes / arg matchers / path + network
		// allowlists); this Register call wires the cmdpolicy.Sandbox to
		// the host_files path validator and installs the handler. Boot
		// continues on any soft failure (operator yaml override parse
		// error, missing binaries) — cmdpolicy.Sandbox.Decide just
		// rejects calls cleanly with a Reason the LLM can read.
		if err := edgebash.Register(client, log); err != nil {
			log.Warn("bash register failed; capability disabled", slog.Any("err", err))
		}

		// WebSSH: edge is a stream port-forwarder. Manager opens a
		// frontier stream with Meta describing the target (sshd at
		// 127.0.0.1:22), edge io.Copy's bytes both ways. SSH client
		// lives entirely on the manager — see internal/manager/server/
		// webshell. The edge has no SSH lib, no PTY, no session map.
		edgewebshell.Register(client, log.With(slog.String("comp", "webshell")))
	} else {
		log.Info("kubernetes controller: host handlers disabled",
			slog.String("role", k8sInfo.Role),
			slog.Uint64("cluster_id", k8sInfo.ClusterID),
		)
	}

	// UpgradeStageDir defaults to the systemd-install layout
	// /var/lib/ongrid-edge/.upgrade. Empty disables agent_upgrade entirely
	// (dev / non-systemd); set OVERRIDE via env to relocate for tests.
	stageDir := os.Getenv("ONGRID_EDGE_UPGRADE_STAGE_DIR")
	if stageDir == "" {
		stageDir = "/var/lib/ongrid-edge/.upgrade"
	}
	agent := edgebiz.NewAgent(client, collector, edgebiz.Config{
		MetricsInterval: cfg.Edge.CollectorInterval,
		AgentVersion:    version,
		Kubernetes:      k8sInfo,
		UpgradeStageDir: stageDir,
	}, log)
	if isK8sController(k8sInfo) {
		inventoryInterval := parseDurationEnv("ONGRID_K8S_INVENTORY_INTERVAL", 10*time.Minute)
		inventoryWatch := parseBoolEnv("ONGRID_K8S_INVENTORY_WATCH", true)
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
		telemetryGatewayMetricsEnabled := parseBoolEnv("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false)
		if gatewayMetricsEndpoint == "" && telemetryGatewayMetricsEnabled {
			gatewayMetricsEndpoint = "http://127.0.0.1:9464/metrics"
		}
		appMetricsDiscovery := parseBoolEnv("ONGRID_K8S_APP_METRICS_DISCOVERY", false)
		if metricsEndpoint != "" || gatewayMetricsEndpoint != "" || appMetricsDiscovery {
			metricsPusher, err := edgek8s.NewMetricsPusher(
				client,
				*k8sInfo,
				agent.EdgeID,
				edgek8s.MetricsConfig{
					Endpoint:         metricsEndpoint,
					GatewayEndpoint:  gatewayMetricsEndpoint,
					Interval:         parseDurationEnv("ONGRID_K8S_METRICS_INTERVAL", 30*time.Second),
					Timeout:          parseDurationEnv("ONGRID_K8S_METRICS_TIMEOUT", 15*time.Second),
					PushTimeout:      parseDurationEnv("ONGRID_K8S_METRICS_PUSH_TIMEOUT", 30*time.Second),
					SampleLimit:      parseIntEnv("ONGRID_K8S_METRICS_SAMPLE_LIMIT", 250000),
					BatchSampleLimit: parseIntEnv("ONGRID_K8S_METRICS_BATCH_SAMPLE_LIMIT", 10000),
					BatchByteLimit:   parseIntEnv("ONGRID_K8S_METRICS_BATCH_BYTE_LIMIT", 4<<20),
					DiscoverApps:     appMetricsDiscovery,
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
	metricsServer := httpserver.New(edgeMetricsAddr, metricsMux, log.With(slog.String("listener", "metrics")))

	eg.Go(func() error { return metricsServer.Start(egCtx) })
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

	telemetryGatewayEnabled := isK8sController(k8sInfo) && parseBoolEnv("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false)
	if isK8sController(k8sInfo) && !telemetryGatewayEnabled {
		log.Info("kubernetes controller: plugin runtime disabled",
			slog.String("role", k8sInfo.Role),
			slog.Uint64("cluster_id", k8sInfo.ClusterID),
		)
	} else {
		// Plugin runtime: logs / traces / future plugins. Each plugin is a
		// goroutine (in-process) or supervised subprocess. Kubernetes
		// controllers do not run this host-oriented plugin set; node-mode
		// DaemonSet agents keep it so they can reuse edge host capability.
		pluginBinDir := envOr("ONGRID_EDGE_PLUGIN_BIN_DIR", "/usr/local/lib/ongrid-edge")
		pluginWorkDir := envOr("ONGRID_EDGE_PLUGIN_WORK_DIR", "/var/lib/ongrid-edge/plugins")
		pluginLog := log.With(slog.String("comp", "plugins"))

		var registered []edgeplugins.Plugin
		if telemetryGatewayEnabled {
			log.Info("kubernetes controller: telemetry gateway plugin runtime enabled",
				slog.String("role", k8sInfo.Role),
				slog.Uint64("cluster_id", k8sInfo.ClusterID),
			)
			registered = []edgeplugins.Plugin{
				edgeplugintraces.New(pluginBinDir, pluginWorkDir, pluginLog),
			}
		} else {
			edgeplugindatabasemetrics.RegisterSecretHandler(client, pluginLog)
			registered = []edgeplugins.Plugin{
				edgepluginlogs.New(pluginBinDir, pluginWorkDir, pluginLog),
				// traces plugin: subprocess otelcol-contrib. Stays
				// disabled until manager pushes a PluginConfig with enabled=true
				// + Endpoint set to the manager public /v1/traces URL.
				edgeplugintraces.New(pluginBinDir, pluginWorkDir, pluginLog),
				// metrics plugin: in-process scraper that polls a
				// local /metrics endpoint (default node_exporter on
				// 127.0.0.1:9100) and pushes via the existing push_prom_samples
				// tunnel RPC. No subprocess, no remote_write — the pre-existing
				// manager-side ingester injects the canonical device_id label.
				edgepluginmetrics.New(client, agent.EdgeID, pluginLog),
				// custommetrics: in-process scraper for arbitrary operator-provided
				// Prometheus /metrics endpoints.
				edgeplugincustommetrics.New(client, agent.EdgeID, pluginLog),
				// databasemetrics: edge-side managed database exporters. The manager
				// sends source specs; the edge reads local secret files and starts the
				// exporter subprocesses.
				edgeplugindatabasemetrics.New(pluginBinDir, pluginWorkDir, client, agent.EdgeID, pluginLog),
				// hostmetrics plugin: subprocess node_exporter. Exposes node_*
				// at :9102 (configurable via spec.listen_address).
				edgepluginhostmetrics.New(pluginBinDir, pluginWorkDir, pluginLog),
				// procmetrics plugin: subprocess process-exporter. Exposes
				// namedprocess_namegroup_* at :9256.
				edgepluginprocmetrics.New(pluginBinDir, pluginWorkDir, pluginLog),
			}
		}
		pluginNames := make([]string, 0, len(registered))
		for _, p := range registered {
			pluginNames = append(pluginNames, p.Name())
		}
		tunnelFetcher := edgeplugins.NewTunnelConfigFetcherWithCredentials(
			client,
			pluginNames,
			cfg.Edge.AccessKey,
			cfg.Edge.SecretKey,
		)
		supervisor := edgeplugins.NewSupervisor(edgeplugins.SupervisorOpts{
			Fetcher: tunnelFetcher,
			Log:     pluginLog,
		})
		for _, p := range registered {
			supervisor.Register(p)
		}
		agent.SetPluginHealthFn(func() []tunnel.PluginHealthWire {
			snaps := supervisor.HealthSnapshots()
			out := make([]tunnel.PluginHealthWire, 0, len(snaps))
			for _, s := range snaps {
				targets := make([]tunnel.PluginTargetHealthWire, 0, len(s.Targets))
				for _, th := range s.Targets {
					wth := tunnel.PluginTargetHealthWire{
						ID:        th.ID,
						Name:      th.Name,
						Kind:      th.Kind,
						State:     th.State,
						LastError: th.LastError,
						Samples:   th.Samples,
					}
					if !th.LastSuccessAt.IsZero() {
						wth.LastSuccessAt = th.LastSuccessAt.Unix()
					}
					if !th.UpdatedAt.IsZero() {
						wth.UpdatedAt = th.UpdatedAt.Unix()
					}
					targets = append(targets, wth)
				}
				w := tunnel.PluginHealthWire{
					Name:         s.Name,
					State:        string(s.State),
					LastError:    s.LastError,
					RestartCount: s.RestartCount,
					PID:          s.PID,
					Targets:      targets,
				}
				if !s.StartedAt.IsZero() {
					w.StartedAt = s.StartedAt.Unix()
				}
				if !s.UpdatedAt.IsZero() {
					w.UpdatedAt = s.UpdatedAt.Unix()
				}
				out = append(out, w)
			}
			return out
		})
		client.RegisterHandler(tunnel.MethodPluginConfigsChanged, func(_ context.Context, _ tunnel.Session, _ string, _ []byte) ([]byte, error) {
			supervisor.TriggerReload()
			return []byte(`{}`), nil
		})
		eg.Go(func() error { return supervisor.Run(egCtx) })
	}

	err = eg.Wait()

	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("shutdown with error", slog.Any("err", err))
		os.Exit(1)
	}
	log.Info("ongrid-edge shutdown complete")
}

// buildCollector constructs the collector matching cfg.Edge.CollectorMode.
// For scrape mode the per-target scrape goroutines are added to eg so
// they share the agent's lifecycle.
//
// Modes:
//
//	off / "" — preferred for installs running the hostmetrics +
//	  procmetrics plugins. CollectAll is a no-op so no node_* samples
//	  are pushed via the tunnel; manager-side Prom scrapes the
//	  node_exporter / process-exporter subprocesses directly through
//	  the docker bridge. On-demand RPCs (host_info / get_host_load /
//	  get_host_processes) still work via the embedded gopsutil
//	  snapshot path.
//	auto — legacy: embedded (gopsutil push) + scraper.
//	embedded — embedded push only.
//	scrape — scraper only.
func buildCollector(ctx context.Context, cfg *config.Config, log *slog.Logger, eg *errgroup.Group) (edgebiz.Collector, *edgecollector.Scraper, error) {
	switch cfg.Edge.CollectorMode {
	case "off", "none", "":
		// Default for fresh installs: don't push anything periodically.
		// On-demand RPCs still hit gopsutil via the wrapped embedded
		// collector so AIOps tools and EdgeDetail cards keep working.
		em, err := edgecollector.NewEmbedded(log)
		if err != nil {
			return nil, nil, fmt.Errorf("embedded collector: %w", err)
		}
		return collectorAdapter{c: edgecollector.NewNoopPush(em)}, nil, nil

	case "auto":
		em, err := edgecollector.NewEmbedded(log)
		if err != nil {
			return nil, nil, fmt.Errorf("embedded collector: %w", err)
		}
		sc, err := edgecollector.LoadScrapeConfig(cfg.Edge.ScrapeConfigFile)
		if err != nil {
			log.Warn("scrape config unavailable; using embedded baseline only", slog.Any("err", err))
			return collectorAdapter{c: em}, nil, nil
		}
		scraper := edgecollector.NewScraper(sc, log)
		eg.Go(func() error { return scraper.Run(ctx) })
		return collectorAdapter{c: edgecollector.NewComposite(em, scraper, log)}, scraper, nil

	case "scrape":
		sc, err := edgecollector.LoadScrapeConfig(cfg.Edge.ScrapeConfigFile)
		if err != nil {
			return nil, nil, fmt.Errorf("scrape config: %w", err)
		}
		scraper := edgecollector.NewScraper(sc, log)
		eg.Go(func() error { return scraper.Run(ctx) })
		return collectorAdapter{c: scraper}, scraper, nil

	case "embedded":
		em, err := edgecollector.NewEmbedded(log)
		if err != nil {
			return nil, nil, fmt.Errorf("embedded collector: %w", err)
		}
		return collectorAdapter{c: em}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown collector mode %q", cfg.Edge.CollectorMode)
	}
}

type k8sEnrollRequest struct {
	ClusterID    uint64   `json:"cluster_id"`
	ClusterUID   string   `json:"cluster_uid"`
	Role         string   `json:"role"`
	NodeName     string   `json:"node_name,omitempty"`
	NodeUID      string   `json:"node_uid,omitempty"`
	ProviderID   string   `json:"provider_id,omitempty"`
	Namespace    string   `json:"namespace,omitempty"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type k8sEnrollResponse struct {
	ClusterID        uint64              `json:"cluster_id"`
	Role             string              `json:"role"`
	Mode             string              `json:"mode"`
	EdgeID           uint64              `json:"edge_id"`
	AccessKey        string              `json:"access_key"`
	SecretKey        string              `json:"secret_key"`
	CloudAddr        string              `json:"cloud_addr,omitempty"`
	ManagerPublicURL string              `json:"manager_public_url,omitempty"`
	Telemetry        *k8sTelemetryConfig `json:"telemetry,omitempty"`
}

type k8sTelemetryConfig struct {
	ClusterID              uint64 `json:"cluster_id"`
	AccessKey              string `json:"access_key"`
	SecretKey              string `json:"secret_key"`
	TracesEndpoint         string `json:"traces_endpoint,omitempty"`
	LogsEndpoint           string `json:"logs_endpoint,omitempty"`
	RemoteWriteEndpoint    string `json:"remote_write_endpoint,omitempty"`
	RemoteWriteBearer      string `json:"remote_write_bearer,omitempty"`
	RemoteWriteBasicUser   string `json:"remote_write_basic_user,omitempty"`
	RemoteWriteBasicPass   string `json:"remote_write_basic_pass,omitempty"`
	RemoteWriteTLSInsecure bool   `json:"remote_write_tls_insecure,omitempty"`
	RemoteWriteTLSCAPEM    string `json:"remote_write_tls_ca_pem,omitempty"`
}

func ensureK8sEnrollment(ctx context.Context, cfg *config.Config, log *slog.Logger) (*tunnel.KubernetesInfo, error) {
	edgeMode := strings.TrimSpace(os.Getenv("ONGRID_EDGE_MODE"))
	clusterIDRaw := strings.TrimSpace(os.Getenv("ONGRID_K8S_CLUSTER_ID"))
	bootstrapToken := strings.TrimSpace(os.Getenv("ONGRID_K8S_BOOTSTRAP_TOKEN"))
	if clusterIDRaw == "" && bootstrapToken == "" && !strings.HasPrefix(edgeMode, "k8s-") {
		return nil, nil
	}
	if clusterIDRaw == "" {
		return nil, fmt.Errorf("ONGRID_K8S_CLUSTER_ID is required in Kubernetes mode")
	}
	clusterID, err := strconv.ParseUint(clusterIDRaw, 10, 64)
	if err != nil || clusterID == 0 {
		if err == nil {
			err = errors.New("cluster id must be non-zero")
		}
		return nil, fmt.Errorf("parse ONGRID_K8S_CLUSTER_ID: %w", err)
	}
	role := strings.TrimSpace(os.Getenv("ONGRID_K8S_ROLE"))
	if role == "" {
		role = defaultK8sRole(edgeMode)
	}
	info := &tunnel.KubernetesInfo{
		Mode:      strings.TrimSpace(os.Getenv("ONGRID_K8S_MODE")),
		ClusterID: clusterID,
		Role:      role,
		NodeName:  strings.TrimSpace(os.Getenv("ONGRID_K8S_NODE_NAME")),
		NodeUID:   strings.TrimSpace(os.Getenv("ONGRID_K8S_NODE_UID")),
		Namespace: strings.TrimSpace(os.Getenv("ONGRID_K8S_POD_NAMESPACE")),
		PodName:   strings.TrimSpace(os.Getenv("ONGRID_K8S_POD_NAME")),
	}
	identityCtx, cancelIdentity := context.WithTimeout(ctx, 10*time.Second)
	clusterUID, err := edgek8s.DiscoverClusterUID(identityCtx)
	cancelIdentity()
	if err != nil {
		return nil, fmt.Errorf("discover kubernetes cluster UID: %w", err)
	}
	info.ClusterUID = clusterUID
	loaded, err := loadStoredK8sCredential(ctx, cfg, info, log)
	if err != nil {
		if bootstrapToken == "" {
			return nil, err
		}
		log.Warn("load kubernetes edge credentials failed; falling back to bootstrap enrollment", slog.Any("err", err))
	}
	if loaded {
		if isK8sController(info) && strings.TrimSpace(os.Getenv("ONGRID_K8S_TELEMETRY_SECRET")) != "" {
			refreshErr := refreshAndStoreK8sTelemetryConfig(ctx, cfg, info)
			if refreshErr != nil {
				if parseBoolEnv("ONGRID_K8S_TELEMETRY_REQUIRED", false) {
					return nil, fmt.Errorf("refresh required kubernetes telemetry config: %w", refreshErr)
				}
				log.Warn("refresh kubernetes telemetry config failed; control plane will continue", slog.Any("err", refreshErr))
			}
		}
		return info, nil
	}
	if bootstrapToken == "" {
		return info, nil
	}
	managerURL := strings.TrimRight(envOr("ONGRID_MANAGER_PUBLIC_URL", cfg.PublicURL), "/")
	if managerURL == "" {
		return nil, fmt.Errorf("ONGRID_MANAGER_PUBLIC_URL is required for Kubernetes enrollment")
	}
	endpoint, err := url.JoinPath(managerURL, "/internal/k8s/enroll")
	if err != nil {
		return nil, fmt.Errorf("build k8s enroll URL: %w", err)
	}
	reqBody := k8sEnrollRequest{
		ClusterID:    clusterID,
		ClusterUID:   info.ClusterUID,
		Role:         role,
		NodeName:     info.NodeName,
		NodeUID:      info.NodeUID,
		ProviderID:   strings.TrimSpace(os.Getenv("ONGRID_K8S_PROVIDER_ID")),
		Namespace:    info.Namespace,
		AgentVersion: version,
		Capabilities: k8sCapabilities(role),
	}
	if parseBoolEnv("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false) {
		reqBody.Capabilities = append(reqBody.Capabilities, "otel_gateway")
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal k8s enroll request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new k8s enroll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	hc := k8sManagerHTTPClient()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("k8s enroll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("k8s enroll failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out k8sEnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode k8s enroll response: %w", err)
	}
	if out.AccessKey == "" || out.SecretKey == "" {
		return nil, fmt.Errorf("k8s enroll response missing edge credentials")
	}
	cfg.Edge.AccessKey = out.AccessKey
	cfg.Edge.SecretKey = out.SecretKey
	if cfg.Edge.CloudAddr == "" && out.CloudAddr != "" {
		cfg.Edge.CloudAddr = out.CloudAddr
	}
	if out.Role != "" {
		info.Role = out.Role
	}
	if out.Mode != "" {
		info.Mode = out.Mode
	}
	if err := storeK8sCredential(ctx, info, out, cfg); err != nil {
		return nil, fmt.Errorf("store kubernetes edge credentials: %w", err)
	}
	log.Info("kubernetes enrollment completed",
		slog.Uint64("cluster_id", clusterID),
		slog.Uint64("edge_id", out.EdgeID),
		slog.String("role", info.Role),
	)
	return info, nil
}

func refreshK8sTelemetryConfig(ctx context.Context, cfg *config.Config, managerURL, currentAccessKey, currentSecretKey string) (*k8sTelemetryConfig, error) {
	if managerURL == "" {
		return nil, fmt.Errorf("manager public URL is required")
	}
	endpoint, err := url.JoinPath(managerURL, "/internal/k8s/telemetry-config")
	if err != nil {
		return nil, fmt.Errorf("build k8s telemetry config URL: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"access_key": currentAccessKey,
		"secret_key": currentSecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal k8s telemetry config request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new k8s telemetry config request: %w", err)
	}
	req.SetBasicAuth(cfg.Edge.AccessKey, cfg.Edge.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k8sManagerHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("k8s telemetry config request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return nil, errors.Join(
				fmt.Errorf("k8s telemetry config failed: status=%d", resp.StatusCode),
				fmt.Errorf("read k8s telemetry config error response: %w", readErr),
			)
		}
		return nil, fmt.Errorf("k8s telemetry config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out k8sTelemetryConfig
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode k8s telemetry config: %w", err)
	}
	if out.ClusterID == 0 || out.AccessKey == "" || out.SecretKey == "" {
		return nil, fmt.Errorf("k8s telemetry config response is incomplete")
	}
	applyManagerTelemetryTLS(&out, managerURL)
	return &out, nil
}

func refreshAndStoreK8sTelemetryConfig(ctx context.Context, cfg *config.Config, info *tunnel.KubernetesInfo) error {
	accessKey, secretKey, _, err := loadK8sTelemetryCredential(ctx, info)
	if err != nil {
		return err
	}
	managerURL := strings.TrimRight(envOr("ONGRID_MANAGER_PUBLIC_URL", cfg.PublicURL), "/")
	telemetry, err := refreshK8sTelemetryConfig(ctx, cfg, managerURL, accessKey, secretKey)
	if err != nil {
		return err
	}
	if err := storeK8sTelemetryConfig(ctx, info, *telemetry); err != nil {
		return fmt.Errorf("store kubernetes telemetry config: %w", err)
	}
	return nil
}

func runK8sTelemetryConfigSync(ctx context.Context, cfg *config.Config, info *tunnel.KubernetesInfo, log *slog.Logger) error {
	interval := parseDurationEnv("ONGRID_K8S_TELEMETRY_CONFIG_REFRESH_INTERVAL", time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := refreshAndStoreK8sTelemetryConfig(ctx, cfg, info); err != nil {
				log.Warn("refresh kubernetes telemetry config failed; keeping previous Secret data", slog.Any("err", err))
				continue
			}
			log.Debug("kubernetes telemetry config refreshed")
		}
	}
}

func k8sManagerHTTPClient() *http.Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	if strings.EqualFold(os.Getenv("ONGRID_K8S_ENROLL_TLS_INSECURE"), "true") {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
		hc.Transport = transport
	}
	return hc
}

func defaultK8sRole(edgeMode string) string {
	switch edgeMode {
	case "k8s-controller":
		return "controller"
	case "k8s-node":
		return "node"
	default:
		return "node"
	}
}

func k8sCapabilities(role string) []string {
	switch role {
	case "controller":
		return []string{"k8s_inventory"}
	default:
		return []string{"node_edge"}
	}
}

func isK8sController(info *tunnel.KubernetesInfo) bool {
	if info == nil {
		return false
	}
	switch strings.TrimSpace(info.Role) {
	case "controller":
		return true
	default:
		return false
	}
}

func isK8sNode(info *tunnel.KubernetesInfo) bool {
	if info == nil {
		return false
	}
	return strings.TrimSpace(info.Role) == "node"
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func parseIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func parseBoolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "t", "yes", "y", "on", "enabled":
		return true
	case "0", "false", "f", "no", "n", "off", "disabled":
		return false
	default:
		return fallback
	}
}

// envOr reads an env var, returning def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// collectorAdapter bridges the collector package's Collector interface to
// the biz package's identical-shaped interface. Two interfaces, one
// implementation — the seam exists so biz/agent.go does not import
// internal/edgeagent/collector (avoids cycles when the collector package
// in turn depends on tunnel types).
type collectorAdapter struct {
	c edgecollector.Collector
}

func (a collectorAdapter) CollectAll(ctx context.Context) ([]edgebiz.CollectorOutput, error) {
	outs, err := a.c.CollectAll(ctx)
	if err != nil {
		return nil, err
	}
	bizOuts := make([]edgebiz.CollectorOutput, 0, len(outs))
	for _, o := range outs {
		bizOuts = append(bizOuts, edgebiz.CollectorOutput{
			Source:         o.Source,
			HostPoint:      o.HostPoint,
			HostPointValid: o.HostPointValid,
			Samples:        o.Samples,
		})
	}
	return bizOuts, nil
}

func (a collectorAdapter) HostInfo(ctx context.Context) (tunnel.HostInfo, error) {
	return a.c.HostInfo(ctx)
}

func (a collectorAdapter) GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error) {
	return a.c.GetHostLoad(ctx)
}

func (a collectorAdapter) GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	return a.c.GetProcessList(ctx, topN, sortBy)
}
