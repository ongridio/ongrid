package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPodMetricsCollectorConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("telemetry-cluster-id", "7")
	write("telemetry-remote-write-endpoint", "https://manager.example/prometheus/api/v1/write")
	write("telemetry-remote-write-basic-user", "test")
	write("telemetry-remote-write-basic-pass", "test")
	t.Setenv("ONGRID_K8S_METRICS_INTERVAL", "5s")
	t.Setenv("ONGRID_K8S_METRICS_TIMEOUT", "15s")
	t.Setenv("ONGRID_K8S_METRICS_SAMPLE_LIMIT", "123")
	fetcher := &podMetricsFetcher{dir: dir}
	configs, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg := configs[podMetricsPlugin]
	raw, err := renderPodMetrics(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scrape_interval: 5s", "scrape_timeout: 5s", "sample_limit: 123", "role: pod", "honor_labels: false", "replacement: /$$1", "k8s:app-metrics", "Basic dGVzdDp0ZXN0", "num_consumers: 1"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing Collector setting %q", want)
		}
	}
	// Exercise native validation against the exact Collector shipped in Edge.
	if binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY"); binary != "" {
		path := filepath.Join(t.TempDir(), "collector.yaml")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.CommandContext(t.Context(), binary, "validate", "--config="+path).CombinedOutput(); err != nil {
			t.Fatalf("Collector validation: %v\n%s", err, output)
		}
	}
	write("telemetry-remote-write-bearer", "test$literal")
	updated, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err = renderPodMetrics(updated[podMetricsPlugin])
	if err != nil || !strings.Contains(string(raw), "Bearer test$$literal") || strings.Contains(string(raw), "Basic ") {
		t.Fatalf("bearer precedence/literal escaping failed: %v", err)
	}
	write("telemetry-remote-write-ca.pem", "first-test-ca")
	first, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	write("telemetry-remote-write-ca.pem", "second-test-ca")
	second, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first, second) {
		t.Fatal("projected CA replacement did not trigger configuration reload")
	}
}

func TestPodMetricsScopeFiltersBeforeScraping(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{
		"telemetry-cluster-id": "7", "telemetry-remote-write-endpoint": "http://manager.example/api/v1/write",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fetcher := &podMetricsFetcher{dir: dir}
	for _, tc := range []struct {
		name, scope string
		allowed     []string
	}{
		{"old controller", "", nil},
		{"namespace", `{"namespaces":["shop"]}`, []string{"shop;uid-one", "shop;future-uid"}},
		{"workload", `{"pod_uids":["uid-one"]}`, []string{"shop;uid-one"}},
		{"mixed", `{"namespaces":["shop"],"pod_uids":["uid-other"]}`, []string{"shop;uid-one", "shop;future-uid", "other;uid-other"}},
		{"clear", `{}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.scope != "" {
				if err := os.WriteFile(filepath.Join(dir, "telemetry-app-metrics-scope"), []byte(tc.scope), 0600); err != nil {
					t.Fatal(err)
				}
			}
			configs, err := fetcher.Fetch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			config := configs[podMetricsPlugin].Spec["collector"].(map[string]interface{})
			scrape := config["receivers"].(map[string]interface{})["prometheus"].(map[string]interface{})["config"].(map[string]interface{})["scrape_configs"].([]interface{})[0].(map[string]interface{})
			rule := scrape["relabel_configs"].([]interface{})[0].(map[string]interface{})
			if rule["action"] != "keep" || !reflect.DeepEqual(rule["source_labels"], []string{"__meta_kubernetes_namespace", "__meta_kubernetes_pod_uid"}) {
				t.Fatal("scope must filter target metadata before scraping")
			}
			pattern := regexp.MustCompile("^(?:" + rule["regex"].(string) + ")$")
			for _, target := range []string{"shop;uid-one", "shop;future-uid", "other;uid-other", "shopping;uid-one-other"} {
				want := false
				for _, allowed := range tc.allowed {
					want = want || target == allowed
				}
				if pattern.MatchString(target) != want {
					t.Errorf("target %q: allowed=%v, want %v", target, pattern.MatchString(target), want)
				}
			}
		})
	}
	for _, invalid := range []string{`{"namespaces":[".*"]}`, `{"pod_uids":[""]}`, `{"unknown":true}`, `{} {}`} {
		if err := os.WriteFile(filepath.Join(dir, "telemetry-app-metrics-scope"), []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := fetcher.Fetch(context.Background()); err == nil {
			t.Fatalf("invalid scope accepted: %s", invalid)
		}
	}
}
