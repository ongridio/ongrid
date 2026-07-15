package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	defaultK8sMetricsInterval = 30 * time.Second
	defaultK8sMetricsTimeout  = 5 * time.Second
	defaultK8sMetricsLimit    = 20000
	k8sMetricsSource          = "k8s:kube-state-metrics"
	k8sAppMetricsSource       = "k8s:app-metrics"
	k8sGatewayMetricsSource   = "k8s:otlp-gateway-metrics"
)

type MetricsConfig struct {
	Endpoint        string
	GatewayEndpoint string
	Interval        time.Duration
	Timeout         time.Duration
	SampleLimit     int
	DiscoverApps    bool
}

type MetricsPusher struct {
	client tunnel.Client
	info   tunnel.KubernetesInfo
	edgeID func() uint64
	cfg    MetricsConfig
	log    *slog.Logger
	api    *apiClient
}

func NewMetricsPusher(client tunnel.Client, info tunnel.KubernetesInfo, edgeID func() uint64, cfg MetricsConfig, log *slog.Logger) (*MetricsPusher, error) {
	if client == nil {
		return nil, errors.New("k8s metrics: tunnel client is required")
	}
	if info.ClusterID == 0 {
		return nil, errors.New("k8s metrics: cluster_id is required")
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.GatewayEndpoint = strings.TrimSpace(cfg.GatewayEndpoint)
	if cfg.Endpoint == "" && cfg.GatewayEndpoint == "" && !cfg.DiscoverApps {
		return nil, errors.New("k8s metrics: endpoint, gateway endpoint, or app discovery is required")
	}
	if cfg.Endpoint != "" {
		if err := metricscommon.ValidateURL(cfg.Endpoint); err != nil {
			return nil, fmt.Errorf("k8s metrics endpoint: %w", err)
		}
	}
	if cfg.GatewayEndpoint != "" {
		if err := metricscommon.ValidateURL(cfg.GatewayEndpoint); err != nil {
			return nil, fmt.Errorf("k8s gateway metrics endpoint: %w", err)
		}
	}
	var api *apiClient
	if cfg.DiscoverApps {
		var err error
		api, err = newInClusterAPIClient()
		if err != nil {
			return nil, fmt.Errorf("k8s app metrics discovery: %w", err)
		}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultK8sMetricsInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultK8sMetricsTimeout
	}
	if cfg.Timeout > cfg.Interval {
		cfg.Timeout = cfg.Interval
	}
	if cfg.SampleLimit <= 0 {
		cfg.SampleLimit = defaultK8sMetricsLimit
	}
	if edgeID == nil {
		edgeID = func() uint64 { return 0 }
	}
	if log == nil {
		log = slog.Default()
	}
	return &MetricsPusher{
		client: client,
		info:   info,
		edgeID: edgeID,
		cfg:    cfg,
		log:    log,
		api:    api,
	}, nil
}

func (p *MetricsPusher) Run(ctx context.Context) error {
	if p == nil || !isControllerRole(p.info.Role) {
		return nil
	}
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()

	for {
		if id := p.edgeID(); id != 0 {
			p.scrapeAndPush(ctx, id)
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			id := p.edgeID()
			if id == 0 {
				continue
			}
			p.scrapeAndPush(ctx, id)
		}
	}
}

func (p *MetricsPusher) scrapeAndPush(ctx context.Context, edgeID uint64) {
	if p.cfg.Endpoint != "" {
		p.scrapeTargetAndPush(ctx, edgeID, p.kubeStateMetricsTarget(), "k8s", k8sMetricsSource)
	}
	if p.cfg.GatewayEndpoint != "" {
		p.scrapeTargetAndPush(ctx, edgeID, p.gatewayMetricsTarget(), "k8s_otlp_gateway", k8sGatewayMetricsSource)
	}
	if p.cfg.DiscoverApps {
		p.discoverAndPushAppMetrics(ctx, edgeID)
	}
}

func (p *MetricsPusher) scrapeTargetAndPush(ctx context.Context, edgeID uint64, target metricscommon.Target, plugin, source string) {
	rctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	samples, err := metricscommon.Scrape(rctx, target)
	now := time.Now()
	if err != nil {
		up := metricscommon.ScrapeUpSample(now, plugin, target, false)
		if pushErr := p.push(ctx, edgeID, source, []tunnel.PromSample{up}); pushErr != nil {
			p.log.Warn("k8s metrics scrape up push failed", slog.Any("err", pushErr))
		}
		p.log.Warn("k8s metrics scrape failed", slog.String("endpoint", target.URL), slog.Any("err", err))
		return
	}
	samples = append(samples, metricscommon.ScrapeUpSample(now, plugin, target, true))
	if err := p.push(ctx, edgeID, source, samples); err != nil {
		p.log.Warn("k8s metrics push failed",
			slog.String("endpoint", target.URL),
			slog.Int("samples", len(samples)),
			slog.Any("err", err),
		)
		return
	}
	p.log.Debug("k8s metrics pushed",
		slog.String("endpoint", target.URL),
		slog.Int("samples", len(samples)),
	)
}

func (p *MetricsPusher) discoverAndPushAppMetrics(ctx context.Context, edgeID uint64) {
	if p.api == nil {
		return
	}
	pods, err := p.api.listMetricPods(ctx, "")
	if err != nil {
		p.log.Warn("k8s app metrics discovery failed", slog.Any("err", err))
		return
	}
	discovered := 0
	for _, pod := range pods {
		target, ok := appMetricsTarget(pod, p.cfg)
		if !ok {
			continue
		}
		discovered++
		p.scrapeTargetAndPush(ctx, edgeID, target, "k8s_app", k8sAppMetricsSource)
	}
	p.log.Debug("k8s app metrics discovery completed", slog.Int("targets", discovered))
}

func (p *MetricsPusher) push(ctx context.Context, edgeID uint64, source string, samples []tunnel.PromSample) error {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var resp tunnel.PushPromSamplesResponse
	if err := p.client.Call(pctx, tunnel.MethodPushPromSamples, tunnel.PushPromSamplesRequest{
		EdgeID:  edgeID,
		Source:  source,
		Samples: samples,
	}, &resp); err != nil {
		return fmt.Errorf("push_prom_samples: %w", err)
	}
	return nil
}

func (p *MetricsPusher) kubeStateMetricsTarget() metricscommon.Target {
	return metricscommon.Target{
		ID:          "kube-state-metrics",
		Name:        "kube-state-metrics",
		URL:         p.cfg.Endpoint,
		Enabled:     true,
		Interval:    p.cfg.Interval,
		Timeout:     p.cfg.Timeout,
		SourceLabel: k8sMetricsSource,
		SampleLimit: p.cfg.SampleLimit,
		Kind:        "kubernetes",
		LabelDrop: []string{
			"uid",
			"pod_uid",
			"container_id",
			"image_id",
			"id",
			"owner_uid",
			"controller_revision_hash",
		},
	}
}

func (p *MetricsPusher) gatewayMetricsTarget() metricscommon.Target {
	return metricscommon.Target{
		ID:          "telemetry-gateway",
		Name:        "telemetry-gateway",
		URL:         p.cfg.GatewayEndpoint,
		Enabled:     true,
		Interval:    p.cfg.Interval,
		Timeout:     p.cfg.Timeout,
		SourceLabel: k8sGatewayMetricsSource,
		SampleLimit: p.cfg.SampleLimit,
		Kind:        "kubernetes-telemetry-gateway",
		LabelDrop: []string{
			"uid",
			"pod_uid",
			"k8s_pod_uid",
			"container_id",
			"image_id",
			"id",
			"owner_uid",
			"controller_revision_hash",
			"instance",
			"url",
		},
	}
}

func (c *apiClient) listMetricPods(ctx context.Context, namespace string) ([]podItem, error) {
	apiPath := "/api/v1/pods"
	if namespace != "" {
		apiPath = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	}
	var list podList
	if err := c.get(ctx, apiPath, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func appMetricsTarget(pod podItem, cfg MetricsConfig) (metricscommon.Target, bool) {
	ann := pod.Metadata.Annotations
	if !annotationBool(ann["prometheus.io/scrape"]) {
		return metricscommon.Target{}, false
	}
	podIP := strings.TrimSpace(pod.Status.PodIP)
	if podIP == "" {
		return metricscommon.Target{}, false
	}
	port := firstMetricString(strings.TrimSpace(ann["prometheus.io/port"]), firstContainerPort(pod))
	if port == "" {
		return metricscommon.Target{}, false
	}
	scheme := strings.ToLower(firstMetricString(strings.TrimSpace(ann["prometheus.io/scheme"]), "http"))
	if scheme != "http" && scheme != "https" {
		return metricscommon.Target{}, false
	}
	path := firstMetricString(strings.TrimSpace(ann["prometheus.io/path"]), "/metrics")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ownerKind, ownerName := controllerOwner(pod.Metadata.OwnerReferences)
	labels := map[string]string{
		"namespace": pod.Metadata.Namespace,
		"pod":       pod.Metadata.Name,
	}
	if pod.Spec.NodeName != "" {
		labels["node"] = pod.Spec.NodeName
	}
	if ownerKind != "" {
		labels["workload_kind"] = ownerKind
	}
	if ownerName != "" {
		labels["workload_name"] = ownerName
	}
	return metricscommon.Target{
		ID:          pod.Metadata.Namespace + "/" + pod.Metadata.Name,
		Name:        pod.Metadata.Name,
		URL:         scheme + "://" + netJoinHostPort(podIP, port) + path,
		Enabled:     true,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		SourceLabel: k8sAppMetricsSource,
		ExtraLabels: labels,
		SampleLimit: cfg.SampleLimit,
		Kind:        "kubernetes-app",
		LabelDrop: []string{
			"uid",
			"pod_uid",
			"container_id",
			"image_id",
			"id",
			"owner_uid",
			"controller_revision_hash",
			"instance",
			"url",
		},
	}, true
}

func annotationBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "y":
		return true
	default:
		return false
	}
}

func firstContainerPort(pod podItem) string {
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.ContainerPort > 0 {
				return fmt.Sprintf("%d", port.ContainerPort)
			}
		}
	}
	return ""
}

func firstMetricString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
