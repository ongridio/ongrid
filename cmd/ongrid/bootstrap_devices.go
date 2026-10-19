package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	managerbizdevice "github.com/ongridio/ongrid/internal/manager/biz/device"
	managerbizedge "github.com/ongridio/ongrid/internal/manager/biz/edge"
	managerbizk8s "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	managerbiztopology "github.com/ongridio/ongrid/internal/manager/biz/topology"
	manageralertdata "github.com/ongridio/ongrid/internal/manager/data/alert/store"
	managerdevicedata "github.com/ongridio/ongrid/internal/manager/data/device/store"
	manageredgedata "github.com/ongridio/ongrid/internal/manager/data/edge/store"
	managerk8sdata "github.com/ongridio/ongrid/internal/manager/data/k8s/store"
	managertopologydata "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	managerserverdevice "github.com/ongridio/ongrid/internal/manager/server/device"
	managerserveredge "github.com/ongridio/ongrid/internal/manager/server/edge"
	managerserveredgeauth "github.com/ongridio/ongrid/internal/manager/server/edgeauth"
	managerservertopology "github.com/ongridio/ongrid/internal/manager/server/topology"
	managersvcedge "github.com/ongridio/ongrid/internal/manager/service/edge"
	managersvck8s "github.com/ongridio/ongrid/internal/manager/service/k8s"
	managersvctelemetry "github.com/ongridio/ongrid/internal/manager/service/telemetry"
	"github.com/ongridio/ongrid/internal/pkg/config"
)

type devicesModule struct {
	edgeRepo                 *manageredgedata.Repo
	deviceRepo               *managerdevicedata.Repo
	edgeDeviceRepo           *managerdevicedata.EdgeDeviceRepo
	deviceUC                 *managerbizdevice.Usecase
	networkDiscoveryUC       *managerbizdevice.NetworkDiscoveryUsecase
	edgeUC                   *managerbizedge.Usecase
	edgeOfflineThreshold     time.Duration
	edgeAuthn                *managerbizedge.AccessKeyAuthenticator
	edgeSvc                  *managersvcedge.Service
	k8sUC                    *managerbizk8s.Usecase
	k8sSvc                   *managersvck8s.Service
	pluginEndpointResolver   managersvctelemetry.PluginEndpointResolver
	pluginConfigUC           *managerbizedge.PluginConfigUC
	edgeHandler              *managerserveredge.Handler
	deviceHandler            *managerserverdevice.Handler
	topologyUC               *managerbiztopology.Usecase
	topologyHandler          *managerservertopology.Handler
	edgeUpgradeJobUC         *managerbizedge.UpgradeJobUsecase
	edgeEnrollmentHandler    *managerserveredge.EnrollmentHandler
	dataPlaneAuthHandler     *managerserveredgeauth.Handler
	edgeOnlyAuthHandler      *managerserveredgeauth.Handler
	telemetryOnlyAuthHandler *managerserveredgeauth.Handler
}

func bootstrapDevices(rootCtx context.Context, cfg *config.Config, log *slog.Logger, storage *storageModule, iam *iamModule, settings *settingsModule) *devicesModule {
	// manager/edge biz + service + server.
	edgeRepo := manageredgedata.NewRepo(storage.db)
	deviceRepo := managerdevicedata.NewRepo(storage.db)
	edgeDeviceRepo := managerdevicedata.NewEdgeDeviceRepo(storage.db)
	deviceUC := managerbizdevice.NewUsecase(deviceRepo, edgeDeviceRepo, log)
	networkDiscoveryRepo := managerdevicedata.NewNetworkDiscoveryRepo(storage.db)
	networkDiscoveryUC := managerbizdevice.NewNetworkDiscoveryUsecase(networkDiscoveryRepo)
	networkDiscoveryUC.SetEnabledProvider(settings.settingSvc.NetworkDiscoveryEnabled)
	networkDiscoveryUC.SetPromotionDependencies(networkDiscoveryRepo, deviceRepo, edgeDeviceRepo)
	deviceUC.SetNetworkDiscovery(networkDiscoveryUC)
	edgeUC := managerbizedge.NewUsecase(edgeRepo, deviceRepo, edgeDeviceRepo, log)

	edgeOfflineThreshold := cfg.Alert.EdgeOfflineThreshold
	if edgeOfflineThreshold <= 0 {
		edgeOfflineThreshold = 90 * time.Second
	}
	if _, err := edgeUC.ReconcileStaleOnline(rootCtx, time.Now().UTC().Add(-edgeOfflineThreshold)); err != nil {
		log.Warn("edge presence reconcile (boot) failed", slog.Any("err", err))
	}

	// Boot backfill: heal orphaned investigation reports. An RCA worker only
	// lives inside this process, so any report left in pending/running by a
	// previous process (crash or deploy mid-investigation) is orphaned —
	// nothing will ever finish it, and IncidentDetail spins on "Spawning
	// root-cause analysis worker…" forever. Fail them once at startup so the
	// SPA shows a re-analyzable error instead of a dead spinner.
	if n, err := manageralertdata.NewInvestigationRepo(storage.db).FailOrphaned(rootCtx, "interrupted by manager restart"); err != nil {
		log.Warn("alert: orphaned-investigation backfill failed", slog.Any("err", err))
	} else if n > 0 {
		log.Info("alert: failed orphaned investigations on boot", slog.Int64("rows", n))
	}
	edgeAuthn := managerbizedge.NewAccessKeyAuthenticator(edgeRepo, log)
	edgeSvc := managersvcedge.New(edgeUC, nil, log)
	k8sRepo := managerk8sdata.NewRepo(storage.db)
	k8sUC := managerbizk8s.NewUsecase(k8sRepo, managersvck8s.NewEdgeIdentityIssuer(edgeSvc), managerbizk8s.Config{
		PublicURL:            cfg.PublicURL,
		TunnelAddr:           cfg.TunnelAddr,
		ImageTag:             version,
		EventRetention:       cfg.K8sEventRetention,
		EventMaxPerCluster:   cfg.K8sEventMaxPerCluster,
		EventCleanupInterval: cfg.K8sEventCleanupInterval,
	})
	k8sUC.SetRemoteWriteResolver(managersvctelemetry.NewRemoteWriteResolver(settings.promResolver, cfg.Prom, cfg.PublicURL))
	k8sSvc := managersvck8s.New(k8sUC)
	telemetryAuthn := managerbizk8s.NewTelemetryAuthenticator(k8sRepo)
	edgeSvc.SetManagedEdgeGuard(k8sSvc)
	// Plugin runtime config storage. UC notifier
	// (cloud → edge reload push) is back-filled after frontierbound is
	// constructed below; until then SetEdge() etc. are no-ops on the wire
	// (edge's 60s safety-net poll covers).
	pluginConfigRepo := manageredgedata.NewPluginConfigRepo(storage.db)
	pluginEndpointResolver := managersvctelemetry.NewPluginEndpointResolver(cfg.PublicURL, settings.lokiResolver, settings.tempoResolver)
	k8sUC.SetTelemetryTargetResolver(pluginEndpointResolver)
	pluginConfigUC := managerbizedge.NewPluginConfigUC(pluginConfigRepo, nil, pluginEndpointResolver, log)
	edgeUC.SetPluginSeeder(pluginConfigUC)
	pluginConfigUC.SetHostConfigTarget(managersvcedge.HostConfigTargetProvider(edgeDeviceRepo, deviceUC))

	edgeHandler := managerserveredge.NewHandler(edgeSvc, deviceRepo, pluginConfigUC)
	edgeHandler.SetAuthz(iam.authzMW)
	// Edge upgrade bundles are installed on the host and mounted read-only
	// into the manager container. publicURL comes from runtime config so
	// edges across the internet can pull them. The resolver is optional in
	// degraded boots without the mount; package-upgrade handlers return 503.
	edgeBundleDir := os.Getenv("ONGRID_EDGE_BUNDLE_DIR")
	if edgeBundleDir == "" {
		edgeBundleDir = "/usr/share/ongrid/edge-bundles"
	}
	var edgeBundleResolver *managerserveredge.FileBundleResolver
	if _, err := os.Stat(edgeBundleDir); err == nil {
		edgeBundleResolver = managerserveredge.NewFileBundleResolver(edgeBundleDir, version, cfg.PublicURL)
		edgeHandler.SetPackageResolver(edgeBundleResolver)
	} else {
		log.Warn("edge bundle dir unavailable; package upgrade endpoint will 503",
			slog.String("dir", edgeBundleDir), slog.Any("err", err))
	}
	deviceHandler := managerserverdevice.NewHandler(deviceUC)

	// topology layer: nodes / relations / relation types. PR-1
	// stands up CRUD + 6 built-in relation type seeds; later PRs hook
	// AIOps tools onto the same UC. Wired here so /v1/topology/* routes
	// can be Register-ed alongside other admin-gated handlers below.
	topologyNodeRepo := managertopologydata.NewNodeRepo(storage.db)
	topologyRelationRepo := managertopologydata.NewRelationRepo(storage.db)
	topologyRelationTypeRepo := managertopologydata.NewRelationTypeRepo(storage.db)
	topologyNodeTypeRepo := managertopologydata.NewNodeTypeRepo(storage.db)
	topologyUC := managerbiztopology.NewUsecase(
		topologyNodeRepo, topologyRelationRepo, topologyRelationTypeRepo, topologyNodeTypeRepo, log,
	).WithClusterDevices(deviceRepo).WithServiceDevices(deviceRepo)
	topologyUC.AddClusterDeleteGuard(managerbizedge.NewUpgradeJobClusterDeleteGuard(edgeRepo))
	topologyHandler := managerservertopology.NewHandler(topologyUC)

	// Persistent package rollout coordinator. It owns job/item records and
	// continues dispatch + registration verification after the initiating
	// browser disconnects. When bundles are unavailable the legacy endpoints
	// keep their existing degraded behavior and job creation stays unwired.
	var edgeUpgradeJobUC *managerbizedge.UpgradeJobUsecase
	if edgeBundleResolver != nil {
		edgeUpgradeJobUC = managerbizedge.NewUpgradeJobUsecase(
			edgeRepo,
			edgeRepo,
			deviceRepo,
			topologyUC,
			edgeSvc,
			edgeBundleResolver,
			managerbizedge.UpgradeJobConfig{},
			log.With(slog.String("comp", "edge-upgrade-job")),
		)
		edgeHandler.SetUpgradeJobService(managersvcedge.NewUpgradeJobs(edgeUpgradeJobUC))
	}

	// Non-Kubernetes fleet enrollment: a short-lived reusable profile issues
	// one independent Edge identity per host. Cluster assignment is delegated
	// to the topology UC and finalized by the normal register_edge flow.
	edgeEnrollmentRepo := manageredgedata.NewEnrollmentRepo(storage.db)
	edgeEnrollmentUC := managerbizedge.NewEnrollmentUsecase(
		edgeEnrollmentRepo,
		topologyUC,
		edgeUC,
		managerbizedge.EnrollmentConfig{PublicURL: cfg.PublicURL, TunnelAddr: cfg.TunnelAddr},
		log,
	)
	edgeEnrollmentSvc := managersvcedge.NewEnrollmentService(edgeEnrollmentUC)
	edgeEnrollmentHandler := managerserveredge.NewEnrollmentHandler(edgeEnrollmentSvc)
	edgeEnrollmentHandler.SetAuthz(iam.authzMW)
	edgeUC.SetEnrollmentFinalizer(edgeEnrollmentUC)
	topologyUC.AddClusterDeleteGuard(edgeEnrollmentUC)

	// device→topology mirror. Plug the topology UC into edge UC
	// so the register flow drops a `nodes` row alongside each new
	// device row + writes device.node_id. Existing devices were already
	// backfilled by topology.Migrate above; this hook covers ongoing
	// registers + any device that landed between migration and now.
	edgeUC.SetNodeMirror(topologyUC)
	deviceUC.SetTopologyMirror(topologyUC)
	networkDiscoveryUC.SetTopologyMirror(topologyUC)
	if n, err := deviceUC.ReconcileDeletedTopology(rootCtx); err != nil {
		log.Warn("device: deleted topology reconcile on boot failed", slog.Any("err", err))
	} else if n > 0 {
		log.Info("device: deleted topology reconcile on boot completed", slog.Int("count", n))
	}
	if n, err := deviceUC.ReconcileOrphanDevices(rootCtx); err != nil {
		log.Warn("device: orphan reconcile on boot failed", slog.Any("err", err))
	} else if n > 0 {
		log.Info("device: orphan reconcile on boot completed", slog.Int("count", n))
	}
	k8sUC.SetTopologyMirror(topologyUC)
	pluginConfigUC.SetKubernetesAutoAPMProvider(k8sUC.AutoAPMForEdge, k8sUC.AutoAPMSpecs)
	pluginConfigUC.SetKubernetesLogPathsProvider(k8sUC.LogPathsForEdge)
	pluginConfigUC.SetKubernetesTelemetryProvider(k8sUC.TelemetryClusterForEdge)
	k8sUC.SetAutoAPMProviders(topologyUC.ClusterEnvironment, pluginConfigUC.NotifyAutoAPMChanged)
	deviceUC.SetEnvironmentProvider(topologyUC.DeviceClusterEnvironment, pluginConfigUC.NotifyAutoAPMChanged)
	pluginConfigUC.SetAutoAPMEnvironmentProvider(managersvcedge.AutoAPMEnvironmentProvider(edgeDeviceRepo, deviceUC))
	if err := k8sUC.ReconcileTopology(rootCtx); err != nil {
		log.Warn("k8s: topology reconcile on boot failed", slog.Any("err", err))
	}

	// Data plane auth verify — nginx auth_request calls the compatibility
	// endpoint for Loki/Tempo and exact scope endpoints for controller config
	// and Prometheus remote_write. Telemetry credentials never enter the
	// tunnel authenticator.
	dataPlaneAuthHandler := managerserveredgeauth.NewHandler(
		managerserveredgeauth.NewDataPlaneAuthenticator(edgeAuthn, telemetryAuthn),
		log,
	)
	edgeOnlyAuthHandler := managerserveredgeauth.NewHandler(managerserveredgeauth.NewEdgeAuthenticator(edgeAuthn), log)
	telemetryOnlyAuthHandler := managerserveredgeauth.NewHandler(managerserveredgeauth.NewTelemetryAuthenticator(telemetryAuthn), log)

	return &devicesModule{
		edgeRepo:                 edgeRepo,
		deviceRepo:               deviceRepo,
		edgeDeviceRepo:           edgeDeviceRepo,
		deviceUC:                 deviceUC,
		networkDiscoveryUC:       networkDiscoveryUC,
		edgeUC:                   edgeUC,
		edgeOfflineThreshold:     edgeOfflineThreshold,
		edgeAuthn:                edgeAuthn,
		edgeSvc:                  edgeSvc,
		k8sUC:                    k8sUC,
		k8sSvc:                   k8sSvc,
		pluginEndpointResolver:   pluginEndpointResolver,
		pluginConfigUC:           pluginConfigUC,
		edgeHandler:              edgeHandler,
		deviceHandler:            deviceHandler,
		topologyUC:               topologyUC,
		topologyHandler:          topologyHandler,
		edgeUpgradeJobUC:         edgeUpgradeJobUC,
		edgeEnrollmentHandler:    edgeEnrollmentHandler,
		dataPlaneAuthHandler:     dataPlaneAuthHandler,
		edgeOnlyAuthHandler:      edgeOnlyAuthHandler,
		telemetryOnlyAuthHandler: telemetryOnlyAuthHandler,
	}
}
