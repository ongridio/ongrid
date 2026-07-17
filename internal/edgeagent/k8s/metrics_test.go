package k8s

import (
	"bufio"
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
	samples := pushedSamplesForSource(t, fc.requests, 41, k8sMetricsSource)
	podSample, ok := findSample(samples, "kube_pod_status_phase")
	if !ok {
		t.Fatalf("kube_pod_status_phase sample missing: %#v", samples)
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
	up, ok := findSample(samples, metricscommon.ScrapeUpMetricName)
	if !ok {
		t.Fatalf("up sample missing: %#v", samples)
	}
	if up.Value != 1 {
		t.Fatalf("up = %v, want 1", up.Value)
	}
	if up.Labels["plugin"] != "k8s" || up.Labels["target_id"] != "kube-state-metrics" {
		t.Fatalf("up labels missing: %#v", up.Labels)
	}
	partial, ok := findSample(samples, metricscommon.ScrapePartialMetricName)
	if !ok || partial.Value != 0 {
		t.Fatalf("partial sample = %#v, found=%t; want partial=0", partial, ok)
	}
	accepted, ok := findSample(samples, metricscommon.ScrapeAcceptedSamplesMetricName)
	if !ok || accepted.Value != 1 {
		t.Fatalf("accepted sample = %#v, found=%t; want accepted=1", accepted, ok)
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
	samples := pushedSamplesForSource(t, fc.requests, 41, k8sGatewayMetricsSource)
	sample, ok := findSample(samples, "ongrid_gateway_metric_smoke_total")
	if !ok {
		t.Fatalf("ongrid_gateway_metric_smoke_total sample missing: %#v", samples)
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
	up, ok := findSample(samples, metricscommon.ScrapeUpMetricName)
	if !ok {
		t.Fatalf("up sample missing: %#v", samples)
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
	if len(req.Samples) != 3 {
		t.Fatalf("samples len = %d, want partial, accepted, and up status", len(req.Samples))
	}
	up, ok := findSample(req.Samples, metricscommon.ScrapeUpMetricName)
	if !ok || up.Value != 0 {
		t.Fatalf("up sample = %#v, found=%t; want up=0", up, ok)
	}
	partial, ok := findSample(req.Samples, metricscommon.ScrapePartialMetricName)
	if !ok || partial.Value != 0 {
		t.Fatalf("partial sample = %#v, found=%t; want partial=0", partial, ok)
	}
	accepted, ok := findSample(req.Samples, metricscommon.ScrapeAcceptedSamplesMetricName)
	if !ok || accepted.Value != 0 {
		t.Fatalf("accepted sample = %#v, found=%t; want accepted=0", accepted, ok)
	}
}

func TestMetricsPusherPushesBoundedSamplesWhenSampleLimitExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := w.Write([]byte(`
# HELP kube_pod_info Kubernetes pod information.
# TYPE kube_pod_info gauge
kube_pod_info{namespace="default",pod="api-1"} 1
kube_pod_info{namespace="default",pod="api-2"} 1
kube_pod_info{namespace="default",pod="api-3"} 1
`)); err != nil {
			return
		}
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	pusher, err := NewMetricsPusher(fc, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint:    srv.URL,
		Interval:    time.Second,
		Timeout:     time.Second,
		SampleLimit: 2,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	samples := pushedSamplesForSource(t, fc.requests, 41, k8sMetricsSource)
	if len(samples) != 5 {
		t.Fatalf("samples len = %d, want 2 bounded samples plus 3 status samples", len(samples))
	}
	pods := make(map[string]struct{})
	for _, sample := range samples {
		if sample.Name == "kube_pod_info" {
			pods[sample.Labels["pod"]] = struct{}{}
		}
	}
	if len(pods) != 2 {
		t.Fatalf("bounded pod samples = %#v, want 2", pods)
	}
	if _, ok := pods["api-3"]; ok {
		t.Fatalf("sample beyond limit was pushed: %#v", pods)
	}
	up, ok := findSample(samples, metricscommon.ScrapeUpMetricName)
	if !ok || up.Value != 1 {
		t.Fatalf("up sample = %#v, found=%t; want up=1", up, ok)
	}
	partial, ok := findSample(samples, metricscommon.ScrapePartialMetricName)
	if !ok || partial.Value != 1 {
		t.Fatalf("partial sample = %#v, found=%t; want partial=1", partial, ok)
	}
	accepted, ok := findSample(samples, metricscommon.ScrapeAcceptedSamplesMetricName)
	if !ok || accepted.Value != 2 {
		t.Fatalf("accepted sample = %#v, found=%t; want accepted=2", accepted, ok)
	}
}

func TestMetricsPusherBatchesBoundedScrapeAndExportsCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := w.Write([]byte(`# TYPE kube_pod_info gauge
kube_pod_info{pod="api-1"} 1
kube_pod_info{pod="api-2"} 1
kube_pod_info{pod="api-3"} 1
kube_pod_info{pod="api-4"} 1
kube_pod_info{pod="api-5"} 1
`)); err != nil {
			return
		}
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	pusher, err := NewMetricsPusher(fc, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint:         srv.URL,
		Interval:         time.Second,
		Timeout:          time.Second,
		SampleLimit:      4,
		BatchSampleLimit: 2,
		BatchByteLimit:   1 << 20,
	}, nil, WithMetricsRegisterer(reg))
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	if len(fc.requests) != 3 {
		t.Fatalf("push requests = %d, want 2 data batches and 1 status batch", len(fc.requests))
	}
	var pushed int
	for i, raw := range fc.requests {
		req, ok := raw.(tunnel.PushPromSamplesRequest)
		if !ok {
			t.Fatalf("request %d = %T, want PushPromSamplesRequest", i, raw)
		}
		if i < 2 && len(req.Samples) > 2 {
			t.Fatalf("request %d samples = %d, want at most 2", i, len(req.Samples))
		}
		pushed += len(req.Samples)
	}
	if pushed != 7 {
		t.Fatalf("pushed samples = %d, want 4 metrics plus 3 status samples", pushed)
	}
	if got := testutil.ToFloat64(pusher.metrics.scrapeSamples.WithLabelValues(k8sMetricsSource)); got != 4 {
		t.Fatalf("accepted counter = %v, want 4", got)
	}
	if got := testutil.ToFloat64(pusher.metrics.scrapeLimitExceeded.WithLabelValues(k8sMetricsSource)); got != 1 {
		t.Fatalf("limit exceeded counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(pusher.metrics.pushBatches.WithLabelValues(k8sMetricsSource, metricsResultSuccess)); got != 3 {
		t.Fatalf("successful batch counter = %v, want 3", got)
	}
	if got := testutil.ToFloat64(pusher.metrics.pushSamples.WithLabelValues(k8sMetricsSource, metricsResultSuccess)); got != 7 {
		t.Fatalf("successful sample counter = %v, want 7", got)
	}
}

func TestMetricsPusherReportsPartialWhenMiddleDataBatchFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := w.Write([]byte(`kube_pod_info{pod="api-1"} 1
kube_pod_info{pod="api-2"} 1
kube_pod_info{pod="api-3"} 1
kube_pod_info{pod="api-4"} 1
`)); err != nil {
			return
		}
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	baseClient := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	client := &failNthPushClient{fakeTunnelClient: baseClient, failAt: 2}
	pusher, err := NewMetricsPusher(client, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint:         srv.URL,
		Interval:         time.Second,
		Timeout:          time.Second,
		PushTimeout:      time.Second,
		SampleLimit:      20,
		BatchSampleLimit: 2,
		BatchByteLimit:   1 << 20,
	}, nil, WithMetricsRegisterer(reg))
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	if len(baseClient.requests) != 3 {
		t.Fatalf("push requests = %d, want 2 data batches and 1 status batch", len(baseClient.requests))
	}
	statusReq, ok := baseClient.lastRequest.(tunnel.PushPromSamplesRequest)
	if !ok {
		t.Fatalf("lastRequest = %T, want PushPromSamplesRequest", baseClient.lastRequest)
	}
	partial, ok := findSample(statusReq.Samples, metricscommon.ScrapePartialMetricName)
	if !ok || partial.Value != 1 {
		t.Fatalf("partial sample = %#v, found=%t; want partial=1", partial, ok)
	}
	accepted, ok := findSample(statusReq.Samples, metricscommon.ScrapeAcceptedSamplesMetricName)
	if !ok || accepted.Value != 2 {
		t.Fatalf("accepted sample = %#v, found=%t; want accepted=2", accepted, ok)
	}
	up, ok := findSample(statusReq.Samples, metricscommon.ScrapeUpMetricName)
	if !ok || up.Value != 1 {
		t.Fatalf("up sample = %#v, found=%t; want scrape up=1", up, ok)
	}
	if got := testutil.ToFloat64(pusher.metrics.pushBatches.WithLabelValues(k8sMetricsSource, metricsResultFailure)); got != 1 {
		t.Fatalf("failed batch counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(pusher.metrics.pushSamples.WithLabelValues(k8sMetricsSource, metricsResultFailure)); got != 2 {
		t.Fatalf("failed sample counter = %v, want 2", got)
	}
}

func TestMetricsPusherHandlesDefaultLargeClusterLimitEndToEnd(t *testing.T) {
	const sampleLimit = defaultK8sMetricsLimit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writer := bufio.NewWriterSize(w, 64<<10)
		for i := 0; i <= sampleLimit; i++ {
			if _, err := writer.WriteString("kube_pod_info{namespace=\"default\",pod=\"api\"} 1\n"); err != nil {
				return
			}
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}))
	defer srv.Close()

	client := &countingPromClient{fakeTunnelClient: &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}}
	pusher, err := NewMetricsPusher(client, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint: srv.URL,
		Interval: 30 * time.Second,
		Timeout:  15 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	if client.dataSamples != sampleLimit {
		t.Fatalf("data samples = %d, want %d", client.dataSamples, sampleLimit)
	}
	if client.maxDataBatch > defaultK8sMetricsBatchSampleLimit {
		t.Fatalf("max data batch = %d, exceeds %d", client.maxDataBatch, defaultK8sMetricsBatchSampleLimit)
	}
	if client.dataBatches != sampleLimit/defaultK8sMetricsBatchSampleLimit {
		t.Fatalf("data batches = %d, want %d", client.dataBatches, sampleLimit/defaultK8sMetricsBatchSampleLimit)
	}
	partial, ok := findSample(client.statusSamples, metricscommon.ScrapePartialMetricName)
	if !ok || partial.Value != 1 {
		t.Fatalf("partial sample = %#v, found=%t; want partial=1", partial, ok)
	}
	accepted, ok := findSample(client.statusSamples, metricscommon.ScrapeAcceptedSamplesMetricName)
	if !ok || accepted.Value != sampleLimit {
		t.Fatalf("accepted sample = %#v, found=%t; want accepted=%d", accepted, ok, sampleLimit)
	}
	up, ok := findSample(client.statusSamples, metricscommon.ScrapeUpMetricName)
	if !ok || up.Value != 1 {
		t.Fatalf("up sample = %#v, found=%t; want up=1", up, ok)
	}
}

func TestMetricsPusherStartsPushTimeoutAfterScrapeProducesData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := w.Write([]byte("kube_pod_info 1\n")); err != nil {
			return
		}
	}))
	defer srv.Close()

	baseClient := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	client := &contextAwareTunnelClient{fakeTunnelClient: baseClient}
	pusher, err := NewMetricsPusher(client, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint:    srv.URL,
		Interval:    time.Second,
		Timeout:     time.Second,
		PushTimeout: 50 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	if baseClient.lastMethod != tunnel.MethodPushPromSamples {
		t.Fatalf("lastMethod = %q, want %q", baseClient.lastMethod, tunnel.MethodPushPromSamples)
	}
}

func TestMetricsPusherUsesFreshTimeoutForStatusAfterDataTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := w.Write([]byte("kube_pod_info 1\n")); err != nil {
			return
		}
	}))
	defer srv.Close()

	baseClient := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	client := &timeoutFirstPushClient{fakeTunnelClient: baseClient}
	pusher, err := NewMetricsPusher(client, tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}, func() uint64 { return 41 }, MetricsConfig{
		Endpoint:    srv.URL,
		Interval:    time.Second,
		Timeout:     time.Second,
		PushTimeout: 20 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsPusher() error = %v", err)
	}

	pusher.scrapeAndPush(context.Background(), 41)
	if client.calls != 2 {
		t.Fatalf("push calls = %d, want timed-out data batch and fresh status batch", client.calls)
	}
	statusReq, ok := baseClient.lastRequest.(tunnel.PushPromSamplesRequest)
	if !ok {
		t.Fatalf("lastRequest = %T, want PushPromSamplesRequest", baseClient.lastRequest)
	}
	partial, ok := findSample(statusReq.Samples, metricscommon.ScrapePartialMetricName)
	if !ok || partial.Value != 1 {
		t.Fatalf("partial sample = %#v, found=%t; want partial=1", partial, ok)
	}
	accepted, ok := findSample(statusReq.Samples, metricscommon.ScrapeAcceptedSamplesMetricName)
	if !ok || accepted.Value != 0 {
		t.Fatalf("accepted sample = %#v, found=%t; want accepted=0", accepted, ok)
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
	if len(fc.requests) != 1 {
		t.Fatalf("app metric push requests = %d, want data and up in one batch", len(fc.requests))
	}
	samples := pushedSamplesForSource(t, fc.requests, 41, k8sAppMetricsSource)
	sample, ok := findSample(samples, "demo_requests_total")
	if !ok {
		t.Fatalf("demo_requests_total sample missing: %#v", samples)
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
	up, ok := findSample(samples, metricscommon.ScrapeUpMetricName)
	if !ok {
		t.Fatalf("up sample missing: %#v", samples)
	}
	if up.Value != 1 || up.Labels["plugin"] != "k8s_app" || up.Labels["kind"] != "kubernetes-app" {
		t.Fatalf("unexpected up sample: %#v", up)
	}
	for _, name := range []string{metricscommon.ScrapePartialMetricName, metricscommon.ScrapeAcceptedSamplesMetricName} {
		if _, exists := findSample(samples, name); exists {
			t.Fatalf("high-cardinality app target must not emit %q: %#v", name, samples)
		}
	}
}

func pushedSamplesForSource(t *testing.T, requests []any, edgeID uint64, source string) []tunnel.PromSample {
	t.Helper()
	var samples []tunnel.PromSample
	for i, raw := range requests {
		req, ok := raw.(tunnel.PushPromSamplesRequest)
		if !ok {
			t.Fatalf("request %d = %T, want PushPromSamplesRequest", i, raw)
		}
		if req.Source != source {
			continue
		}
		if req.EdgeID != edgeID {
			t.Fatalf("request %d edge_id = %d, want %d", i, req.EdgeID, edgeID)
		}
		samples = append(samples, req.Samples...)
	}
	return samples
}

func findSample(samples []tunnel.PromSample, name string) (tunnel.PromSample, bool) {
	for _, sample := range samples {
		if sample.Name == name {
			return sample, true
		}
	}
	return tunnel.PromSample{}, false
}

type contextAwareTunnelClient struct {
	*fakeTunnelClient
}

type failNthPushClient struct {
	*fakeTunnelClient
	failAt int
	calls  int
}

type timeoutFirstPushClient struct {
	*fakeTunnelClient
	calls int
}

type countingPromClient struct {
	*fakeTunnelClient
	dataBatches   int
	dataSamples   int
	maxDataBatch  int
	statusSamples []tunnel.PromSample
}

func (c *countingPromClient) Call(ctx context.Context, method string, req any, resp any) error {
	if method != tunnel.MethodPushPromSamples {
		return c.fakeTunnelClient.Call(ctx, method, req, resp)
	}
	in, ok := req.(tunnel.PushPromSamplesRequest)
	if !ok {
		return fmt.Errorf("unexpected push request %T", req)
	}
	if _, status := findSample(in.Samples, metricscommon.ScrapeUpMetricName); status {
		c.statusSamples = append(c.statusSamples[:0], in.Samples...)
	} else {
		c.dataBatches++
		c.dataSamples += len(in.Samples)
		c.maxDataBatch = max(c.maxDataBatch, len(in.Samples))
	}
	if out, ok := resp.(*tunnel.PushPromSamplesResponse); ok {
		out.Accepted = len(in.Samples)
	}
	return nil
}

func (c *timeoutFirstPushClient) Call(ctx context.Context, method string, req any, resp any) error {
	c.calls++
	if c.calls == 1 {
		c.lastMethod = method
		c.lastRequest = req
		c.requests = append(c.requests, req)
		<-ctx.Done()
		return ctx.Err()
	}
	return c.fakeTunnelClient.Call(ctx, method, req, resp)
}

func (c *failNthPushClient) Call(ctx context.Context, method string, req any, resp any) error {
	c.calls++
	if c.calls == c.failAt {
		c.lastMethod = method
		c.lastRequest = req
		c.requests = append(c.requests, req)
		return fmt.Errorf("injected push failure at call %d", c.calls)
	}
	return c.fakeTunnelClient.Call(ctx, method, req, resp)
}

func (c *contextAwareTunnelClient) Call(ctx context.Context, method string, req any, resp any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.fakeTunnelClient.Call(ctx, method, req, resp)
}
