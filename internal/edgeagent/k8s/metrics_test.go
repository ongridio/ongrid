package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestMetricsPusherScrapesAndPushesKubeStateMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`
# HELP kube_pod_status_phase The pods current phase.
# TYPE kube_pod_status_phase gauge
kube_pod_status_phase{namespace="default",pod="api-1",phase="Running",uid="drop-me",pod_uid="drop-me"} 1
`))
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	pusher, err := NewMetricsPusher(fc, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint:    srv.URL,
		Interval:    time.Second,
		Timeout:     time.Second,
		SampleLimit: 20,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	if fc.lastMethod != tunnel.MethodPushPromSamples {
		t.Fatalf("lastMethod = %q, want %q", fc.lastMethod, tunnel.MethodPushPromSamples)
	}
	req, ok := fc.lastRequest.(tunnel.PushPromSamplesRequest)
	if !ok {
		t.Fatalf("lastRequest = %T, want PushPromSamplesRequest", fc.lastRequest)
	}
	if req.EdgeID != 41 {
		t.Fatalf("EdgeID = %d, want 41", req.EdgeID)
	}
	if req.Source != k8sMetricsSource {
		t.Fatalf("Source = %q, want %q", req.Source, k8sMetricsSource)
	}
	podSample, ok := findSample(req.Samples, "kube_pod_status_phase")
	if !ok {
		t.Fatalf("kube_pod_status_phase sample missing: %#v", req.Samples)
	}
	if podSample.Labels["namespace"] != "default" || podSample.Labels["pod"] != "api-1" || podSample.Labels["phase"] != "Running" {
		t.Fatalf("pod labels missing: %#v", podSample.Labels)
	}
	if _, ok := podSample.Labels["uid"]; ok {
		t.Fatalf("uid label should be dropped: %#v", podSample.Labels)
	}
	if _, ok := podSample.Labels["pod_uid"]; ok {
		t.Fatalf("pod_uid label should be dropped: %#v", podSample.Labels)
	}
	up, ok := findSample(req.Samples, metricscommon.ScrapeUpMetricName)
	if !ok {
		t.Fatalf("up sample missing: %#v", req.Samples)
	}
	if up.Value != 1 {
		t.Fatalf("up = %v, want 1", up.Value)
	}
	if up.Labels["plugin"] != "k8s" || up.Labels["target_id"] != "kube-state-metrics" {
		t.Fatalf("up labels missing: %#v", up.Labels)
	}
}

func TestMetricsPusherScrapesGatewayMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`
# HELP ongrid_gateway_metric_smoke_total Gateway smoke metric.
# TYPE ongrid_gateway_metric_smoke_total counter
ongrid_gateway_metric_smoke_total{service_name="api",pod_uid="drop-me",instance="drop-me"} 5
`))
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	pusher, err := NewMetricsPusher(fc, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		GatewayEndpoint: srv.URL,
		Interval:        time.Second,
		Timeout:         time.Second,
		SampleLimit:     20,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	req, ok := fc.lastRequest.(tunnel.PushPromSamplesRequest)
	if !ok {
		t.Fatalf("lastRequest = %T, want PushPromSamplesRequest", fc.lastRequest)
	}
	if req.Source != k8sGatewayMetricsSource {
		t.Fatalf("Source = %q, want %q", req.Source, k8sGatewayMetricsSource)
	}
	sample, ok := findSample(req.Samples, "ongrid_gateway_metric_smoke_total")
	if !ok {
		t.Fatalf("ongrid_gateway_metric_smoke_total sample missing: %#v", req.Samples)
	}
	if sample.Labels["service_name"] != "api" {
		t.Fatalf("service label missing: %#v", sample.Labels)
	}
	if _, ok := sample.Labels["pod_uid"]; ok {
		t.Fatalf("pod_uid label should be dropped: %#v", sample.Labels)
	}
	if _, ok := sample.Labels["instance"]; ok {
		t.Fatalf("instance label should be dropped: %#v", sample.Labels)
	}
	up, ok := findSample(req.Samples, metricscommon.ScrapeUpMetricName)
	if !ok {
		t.Fatalf("up sample missing: %#v", req.Samples)
	}
	if up.Value != 1 || up.Labels["plugin"] != "k8s_otlp_gateway" || up.Labels["kind"] != "kubernetes-telemetry-gateway" {
		t.Fatalf("unexpected up sample: %#v", up)
	}
}

func TestMetricsPusherPushesDownSampleOnScrapeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	pusher, err := NewMetricsPusher(fc, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint: srv.URL,
		Interval: time.Second,
		Timeout:  time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	req, ok := fc.lastRequest.(tunnel.PushPromSamplesRequest)
	if !ok {
		t.Fatalf("lastRequest = %T, want PushPromSamplesRequest", fc.lastRequest)
	}
	if len(req.Samples) != 1 {
		t.Fatalf("samples len = %d, want 1", len(req.Samples))
	}
	if req.Samples[0].Name != metricscommon.ScrapeUpMetricName || req.Samples[0].Value != 0 {
		t.Fatalf("sample = %#v, want up=0", req.Samples[0])
	}
}

func TestMetricsPusherDiscoversAnnotatedPodMetrics(t *testing.T) {
	var metricsHits int
	var serverHost, serverPort string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/pods":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"metadata":{"resourceVersion":"10"},
				"items":[{
					"metadata":{
						"namespace":"default",
						"name":"api-1",
						"uid":"pod-1",
						"annotations":{
							"prometheus.io/scrape":"true",
							"prometheus.io/port":%q,
							"prometheus.io/path":"/metrics"
						},
						"ownerReferences":[{"kind":"ReplicaSet","name":"api-rs","controller":true}]
					},
					"spec":{"nodeName":"node-a","containers":[{"name":"api","ports":[{"containerPort":8080}]}]},
					"status":{"phase":"Running","podIP":%q}
				}]
			}`, serverPort, serverHost)
		case "/metrics":
			metricsHits++
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = w.Write([]byte(`
# HELP demo_requests_total Demo requests.
# TYPE demo_requests_total counter
demo_requests_total{instance="pod-ip",pod_uid="drop-me"} 3
`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	serverHost, serverPort, err = net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	pusher := &MetricsPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		edgeID: func() uint64 { return 41 },
		cfg: MetricsConfig{
			Interval:     time.Second,
			Timeout:      time.Second,
			SampleLimit:  20,
			DiscoverApps: true,
		},
		log: slog.Default(),
		api: &apiClient{baseURL: srv.URL, token: "tok", http: srv.Client()},
	}

	pusher.discoverAndPushAppMetrics(context.Background(), 41)
	if metricsHits != 1 {
		t.Fatalf("metricsHits = %d, want 1", metricsHits)
	}
	req, ok := fc.lastRequest.(tunnel.PushPromSamplesRequest)
	if !ok {
		t.Fatalf("lastRequest = %T, want PushPromSamplesRequest", fc.lastRequest)
	}
	if req.Source != k8sAppMetricsSource {
		t.Fatalf("Source = %q, want %q", req.Source, k8sAppMetricsSource)
	}
	sample, ok := findSample(req.Samples, "demo_requests_total")
	if !ok {
		t.Fatalf("demo_requests_total sample missing: %#v", req.Samples)
	}
	if sample.Labels["namespace"] != "default" || sample.Labels["pod"] != "api-1" || sample.Labels["node"] != "node-a" {
		t.Fatalf("k8s labels missing: %#v", sample.Labels)
	}
	if sample.Labels["workload_kind"] != "ReplicaSet" || sample.Labels["workload_name"] != "api-rs" {
		t.Fatalf("workload labels missing: %#v", sample.Labels)
	}
	if _, ok := sample.Labels["pod_uid"]; ok {
		t.Fatalf("pod_uid label should be dropped: %#v", sample.Labels)
	}
	if _, ok := sample.Labels["instance"]; ok {
		t.Fatalf("instance label should be dropped: %#v", sample.Labels)
	}
	up, ok := findSample(req.Samples, metricscommon.ScrapeUpMetricName)
	if !ok {
		t.Fatalf("up sample missing: %#v", req.Samples)
	}
	if up.Value != 1 || up.Labels["plugin"] != "k8s_app" || up.Labels["kind"] != "kubernetes-app" {
		t.Fatalf("unexpected up sample: %#v", up)
	}
}

func findSample(samples []tunnel.PromSample, name string) (tunnel.PromSample, bool) {
	for _, sample := range samples {
		if sample.Name == name {
			return sample, true
		}
	}
	return tunnel.PromSample{}, false
}
