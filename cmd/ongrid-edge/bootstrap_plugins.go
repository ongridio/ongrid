package main

import (
	"context"
	"log/slog"
	"path/filepath"

	edgebiz "github.com/ongridio/ongrid/internal/edgeagent/biz"
	edgeconfig "github.com/ongridio/ongrid/internal/edgeagent/config"
	edgek8s "github.com/ongridio/ongrid/internal/edgeagent/k8s"
	edgeplugins "github.com/ongridio/ongrid/internal/edgeagent/plugins"
	edgepluginautoapm "github.com/ongridio/ongrid/internal/edgeagent/plugins/autoapm"
	edgeplugincustommetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/custommetrics"
	edgeplugindatabasemetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/databasemetrics"
	edgepluginhostmetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/hostmetrics"
	edgepluginlogs "github.com/ongridio/ongrid/internal/edgeagent/plugins/logs"
	edgepluginmetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/metrics"
	edgepluginprocmetrics "github.com/ongridio/ongrid/internal/edgeagent/plugins/procmetrics"
	edgepluginprofiles "github.com/ongridio/ongrid/internal/edgeagent/plugins/profiles"
	edgeplugintraces "github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"golang.org/x/sync/errgroup"
)

func startPlugins(ctx context.Context, cfg *config.Config, log *slog.Logger, client tunnel.Client, agent *edgebiz.Agent, k8sInfo *tunnel.KubernetesInfo, eg *errgroup.Group) {
	telemetryGatewayEnabled := edgek8s.IsController(k8sInfo) && edgeconfig.EnvBool("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false)
	if edgek8s.IsController(k8sInfo) && !telemetryGatewayEnabled {
		log.Info("kubernetes controller: plugin runtime disabled",
			slog.String("role", k8sInfo.Role),
			slog.Uint64("cluster_id", k8sInfo.ClusterID),
		)
	} else {
		// Plugin runtime: logs / traces / future plugins. Each plugin is a
		// goroutine (in-process) or supervised subprocess. Kubernetes
		// controllers do not run this host-oriented plugin set; node-mode
		// DaemonSet agents keep it so they can reuse edge host capability.
		pluginBinDir := edgeconfig.EnvOr("ONGRID_EDGE_PLUGIN_BIN_DIR", defaultPluginBinDir())
		pluginWorkDir := edgeconfig.EnvOr("ONGRID_EDGE_PLUGIN_WORK_DIR", defaultPluginWorkDir())
		pluginLog := log.With(slog.String("comp", "plugins"))

		var registered []edgeplugins.Plugin
		var instanceReceiver edgeplugintraces.InstanceReceiver
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
			autoAPM := edgepluginautoapm.New(pluginBinDir, pluginWorkDir, client, agent.EdgeID, pluginLog)
			instanceReceiver = autoAPM
			registered = []edgeplugins.Plugin{
				edgepluginlogs.New(pluginBinDir, pluginWorkDir, pluginLog),
				// traces plugin: subprocess otelcol-contrib. Stays
				// disabled until manager pushes a PluginConfig with enabled=true
				// + Endpoint set to the manager public /v1/traces URL.
				edgeplugintraces.New(pluginBinDir, pluginWorkDir, pluginLog),
				edgepluginprofiles.New(pluginBinDir, pluginWorkDir, pluginLog),
				autoAPM,
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
		var pluginFetcher edgeplugins.ConfigFetcher = tunnelFetcher
		if k8sInfo == nil {
			for i, p := range registered {
				if p.Name() == edgeplugintraces.Name {
					bridges := edgeplugintraces.WithDockerBridges(pluginFetcher, p, pluginLog)
					pluginFetcher, registered[i] = bridges, bridges
					break
				}
			}
		}
		supervisor := edgeplugins.NewSupervisor(edgeplugins.SupervisorOpts{
			Fetcher: pluginFetcher,
			Log:     pluginLog,
		})
		for _, p := range registered {
			supervisor.Register(p)
		}
		edgeplugintraces.RegisterApplicationReceiver(client, supervisor, filepath.Join(pluginWorkDir, edgeplugintraces.Name, "otelcol.yaml"), instanceReceiver)
		agent.SetPluginHealthFn(supervisor.HealthWire)
		supervisor.RegisterReloadHandler(client)
		eg.Go(func() error { return supervisor.Run(ctx) })
	}
}
