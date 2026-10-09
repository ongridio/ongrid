package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestPodMetricsPreservesTargetInfo(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY")
	if binary == "" {
		t.Skip("requires the bundled Collector via ONGRID_TEST_OTELCOL_BINARY")
	}
	var mu sync.Mutex
	seen := map[string]map[string]string{}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, err = snappy.Decode(nil, raw)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, series := range podMetricsProtoFields(t, raw, 1) {
			labels := map[string]string{}
			for _, label := range podMetricsProtoFields(t, series, 1) {
				keys, values := podMetricsProtoFields(t, label, 1), podMetricsProtoFields(t, label, 2)
				if len(keys) != 1 || len(values) != 1 {
					t.Error("remote_write label must have one name and value")
					continue
				}
				labels[string(keys[0])] = string(values[0])
			}
			seen[labels["__name__"]] = labels
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, `# TYPE target_info gauge
target_info{service_name="orders",service_namespace="shop",build_id="v1"} 1
# TYPE app_requests_total counter
app_requests_total{route="/work"} 9
`)
	}))
	defer target.Close()
	dir := t.TempDir()
	for name, value := range map[string]string{
		"telemetry-cluster-id": "7", "telemetry-remote-write-endpoint": sink.URL,
		"telemetry-app-metrics-scope": `{"namespaces":["shop"]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ONGRID_K8S_METRICS_INTERVAL", "1s")
	t.Setenv("ONGRID_K8S_METRICS_TIMEOUT", "1s")
	configs, err := (&podMetricsFetcher{dir: dir}).Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg := configs[podMetricsPlugin]
	config := cfg.Spec["collector"].(map[string]interface{})
	scrape := config["receivers"].(map[string]interface{})["prometheus"].(map[string]interface{})["config"].(map[string]interface{})["scrape_configs"].([]interface{})[0].(map[string]interface{})
	// Replace only discovery; retain the real relabeling and export pipeline.
	delete(scrape, "kubernetes_sd_configs")
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	scrape["static_configs"] = []interface{}{map[string]interface{}{
		"targets": []string{targetURL.Host},
		"labels": map[string]string{
			"__meta_kubernetes_namespace": "shop", "__meta_kubernetes_pod_uid": "uid-one",
			"__meta_kubernetes_pod_name": "orders-one", "__meta_kubernetes_pod_annotation_prometheus_io_scrape": "true",
		},
	}}
	config["extensions"].(map[string]interface{})["health_check"].(map[string]interface{})["endpoint"] = "127.0.0.1:0"
	raw, err := renderPodMetrics(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "collector.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var logs bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "--config="+path)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ready := seen["target_info"] != nil && seen["app_requests_total"] != nil
		mu.Unlock()
		if ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"target_info", "app_requests_total"} {
		for label, want := range map[string]string{"cluster_id": "7", "ongrid_source": "k8s:app-metrics", "namespace": "shop", "pod": "orders-one"} {
			if got := seen[name][label]; got != want {
				t.Errorf("%s label %s = %q, want %q", name, label, got, want)
			}
		}
	}
	for label, want := range map[string]string{"service_name": "orders", "service_namespace": "shop", "build_id": "v1"} {
		if got := seen["target_info"][label]; got != want {
			t.Errorf("target_info label %s = %q, want %q", label, got, want)
		}
	}
	for _, label := range []string{"job", "instance"} {
		if seen["target_info"][label] == "" || seen["target_info"][label] != seen["app_requests_total"][label] {
			t.Errorf("target_info cannot join application metrics on %s", label)
		}
	}
}

// Read only length-delimited fields from remote_write without adding prompb.
func podMetricsProtoFields(t *testing.T, raw []byte, field protowire.Number) [][]byte {
	t.Helper()
	var fields [][]byte
	for len(raw) > 0 {
		number, kind, n := protowire.ConsumeTag(raw)
		if err := protowire.ParseError(n); err != nil {
			t.Error(err)
			return nil
		}
		raw = raw[n:]
		if kind == protowire.BytesType {
			value, n := protowire.ConsumeBytes(raw)
			if err := protowire.ParseError(n); err != nil {
				t.Error(err)
				return nil
			}
			raw = raw[n:]
			if number == field {
				fields = append(fields, value)
			}
		} else {
			n := protowire.ConsumeFieldValue(number, kind, raw)
			if err := protowire.ParseError(n); err != nil {
				t.Error(err)
				return nil
			}
			raw = raw[n:]
		}
	}
	return fields
}
