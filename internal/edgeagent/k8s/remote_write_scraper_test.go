package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
)

type recordingRemoteWriteWriter struct {
	batches [][]pkgpromwrite.Sample
	err     error
	calls   int
}

func (w *recordingRemoteWriteWriter) Write(_ context.Context, samples []pkgpromwrite.Sample) error {
	w.calls++
	if w.err != nil {
		return w.err
	}
	cp := append([]pkgpromwrite.Sample(nil), samples...)
	w.batches = append(w.batches, cp)
	return nil
}

func TestRemoteWriteScraperPreservesKubernetesLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`kube_pod_status_phase{namespace="default",pod="api",phase="Running",uid="drop",cluster_id="evil",device_id="evil"} 1
`))
	}))
	defer srv.Close()

	writer := &recordingRemoteWriteWriter{}
	registry := prometheus.NewRegistry()
	scraper, err := NewRemoteWriteScraper(writer, RemoteWriteScraperConfig{
		ClusterID:   7,
		Endpoint:    srv.URL,
		Interval:    time.Second,
		Timeout:     time.Second,
		PushTimeout: time.Second,
		MaxRetries:  1,
	}, slog.Default(), registry)
	if err != nil {
		t.Fatalf("NewRemoteWriteScraper() error = %v", err)
	}
	scraper.scrapeAndWrite(context.Background())
	if !scraper.Ready() {
		t.Fatal("scraper did not become ready after a complete successful cycle")
	}

	var found *pkgpromwrite.Sample
	for _, batch := range writer.batches {
		for i := range batch {
			labels := remoteWriteLabelMap(batch[i].Labels)
			if labels["__name__"] == "kube_pod_status_phase" {
				found = &batch[i]
			}
		}
	}
	if found == nil {
		t.Fatal("kube_pod_status_phase was not written")
	}
	labels := remoteWriteLabelMap(found.Labels)
	if labels["cluster_id"] != "7" || labels["ongrid_source"] != k8sMetricsSource {
		t.Fatalf("system labels = %#v", labels)
	}
	if labels["namespace"] != "default" || labels["pod"] != "api" || labels["phase"] != "Running" {
		t.Fatalf("business labels = %#v", labels)
	}
	if _, ok := labels["uid"]; ok {
		t.Fatalf("high-cardinality uid was retained: %#v", labels)
	}
	if _, ok := labels["device_id"]; ok {
		t.Fatalf("host identity was retained: %#v", labels)
	}
	names := make([]string, 0, len(found.Labels))
	for _, label := range found.Labels {
		names = append(names, label.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("labels are not sorted: %v", names)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	var lastSuccess float64
	for _, family := range families {
		if family.GetName() == "ongrid_edge_k8s_metrics_last_success_timestamp_seconds" && len(family.Metric) == 1 {
			lastSuccess = family.Metric[0].GetGauge().GetValue()
		}
	}
	if lastSuccess == 0 {
		t.Fatal("last-success metric was not recorded")
	}
}

func TestRemoteWriteScraperReadyTracksCompleteCycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("kube_node_info{node=\"node-a\"} 1\n"))
	}))
	defer srv.Close()

	writer := &recordingRemoteWriteWriter{err: errors.New("backend unavailable")}
	scraper, err := NewRemoteWriteScraper(writer, RemoteWriteScraperConfig{
		ClusterID:    7,
		Endpoint:     srv.URL,
		Interval:     time.Second,
		Timeout:      time.Second,
		PushTimeout:  time.Second,
		MaxRetries:   1,
		RetryBackoff: time.Nanosecond,
	}, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewRemoteWriteScraper() error = %v", err)
	}
	if scraper.Ready() {
		t.Fatal("scraper is ready before its first cycle")
	}
	if scraper.scrapeAndWrite(context.Background()) || scraper.Ready() {
		t.Fatal("scraper became ready while remote_write was failing")
	}

	writer.err = nil
	if !scraper.scrapeAndWrite(context.Background()) || !scraper.Ready() {
		t.Fatal("scraper did not become ready after remote_write recovered")
	}

	writer.err = errors.New("backend unavailable again")
	if scraper.scrapeAndWrite(context.Background()) || scraper.Ready() {
		t.Fatal("scraper stayed ready after a later incomplete cycle")
	}
}

func TestRemoteWriteScraperDiscoversApplicationTargets(t *testing.T) {
	metricsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`app_requests_total{route="/ready",id="one",instance="app",url="/business",cluster_id="spoofed",ongrid_source="spoofed"} 3
app_requests_total{route="/ready",id="two",instance="app",url="/business"} 4
`))
	}))
	defer metricsServer.Close()
	metricsURL, err := url.Parse(metricsServer.URL)
	if err != nil {
		t.Fatalf("parse metrics server URL: %v", err)
	}

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pods" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"metadata": map[string]any{
					"name":      "api",
					"namespace": "default",
					"annotations": map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   metricsURL.Port(),
					},
				},
				"spec":   map[string]any{"nodeName": "node-a"},
				"status": map[string]any{"podIP": metricsURL.Hostname()},
			}},
		}); err != nil {
			t.Errorf("encode pod list: %v", err)
		}
	}))
	defer apiServer.Close()

	writer := &recordingRemoteWriteWriter{}
	scraper, err := newRemoteWriteScraper(writer, RemoteWriteScraperConfig{
		ClusterID:    7,
		DiscoverApps: true,
		AppMetricsScope: func(context.Context) (autoapm.MetricsScope, error) {
			return autoapm.MetricsScope{Namespaces: []string{"default"}}, nil
		},
		Interval:    time.Second,
		Timeout:     time.Second,
		PushTimeout: time.Second,
		MaxRetries:  1,
	}, slog.Default(), nil, &apiClient{baseURL: apiServer.URL, http: apiServer.Client()})
	if err != nil {
		t.Fatalf("newRemoteWriteScraper() error = %v", err)
	}
	if !scraper.scrapeAndWrite(context.Background()) || !scraper.Ready() {
		t.Fatal("application-only scrape cycle did not become ready")
	}

	seen := map[string]float64{}
	for _, batch := range writer.batches {
		for _, sample := range batch {
			labels := remoteWriteLabelMap(sample.Labels)
			if labels["__name__"] == "app_requests_total" {
				if labels["ongrid_source"] != k8sAppMetricsSource || labels["cluster_id"] != "7" || labels["namespace"] != "default" || labels["pod"] != "api" || labels["instance"] != "app" || labels["url"] != "/business" {
					t.Fatalf("application labels = %#v", labels)
				}
				seen[labels["id"]] = sample.Value
			}
		}
	}
	if len(seen) != 2 || seen["one"] != 3 || seen["two"] != 4 {
		t.Fatalf("business series collapsed: %#v", seen)
	}
}

func TestRemoteWriteScraperKeepsCoreReadyWhenAppDiscoveryFails(t *testing.T) {
	metricsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("kube_node_info{node=\"node-a\"} 1\n"))
	}))
	defer metricsServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer apiServer.Close()

	writer := &recordingRemoteWriteWriter{}
	scraper, err := newRemoteWriteScraper(writer, RemoteWriteScraperConfig{
		ClusterID:    7,
		Endpoint:     metricsServer.URL,
		DiscoverApps: true,
		AppMetricsScope: func(context.Context) (autoapm.MetricsScope, error) {
			return autoapm.MetricsScope{Namespaces: []string{"default"}}, nil
		},
		Interval:    time.Second,
		Timeout:     time.Second,
		PushTimeout: time.Second,
		MaxRetries:  1,
	}, slog.Default(), nil, &apiClient{baseURL: apiServer.URL, http: apiServer.Client()})
	if err != nil {
		t.Fatalf("newRemoteWriteScraper() error = %v", err)
	}
	if !scraper.scrapeAndWrite(context.Background()) {
		t.Fatal("core scrape failed when optional application discovery was unavailable")
	}
	if !scraper.Ready() {
		t.Fatal("scraper was not ready after a successful core scrape")
	}
}

func TestRemoteWriteScraperCapsRetries(t *testing.T) {
	writer := &recordingRemoteWriteWriter{err: errors.New("backend unavailable")}
	scraper, err := NewRemoteWriteScraper(writer, RemoteWriteScraperConfig{
		ClusterID:    7,
		Endpoint:     "http://kube-state-metrics.invalid/metrics",
		PushTimeout:  time.Second,
		MaxRetries:   1000,
		RetryBackoff: time.Nanosecond,
	}, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewRemoteWriteScraper() error = %v", err)
	}
	err = scraper.writeWithRetry(context.Background(), k8sMetricsSource, nil)
	if err == nil {
		t.Fatal("writeWithRetry() error = nil")
	}
	if writer.calls != maxRemoteWriteRetries {
		t.Fatalf("writer calls = %d, want capped %d", writer.calls, maxRemoteWriteRetries)
	}
}

func remoteWriteLabelMap(labels []pkgpromwrite.Label) map[string]string {
	out := make(map[string]string, len(labels))
	for _, label := range labels {
		out[label.Name] = label.Value
	}
	return out
}
