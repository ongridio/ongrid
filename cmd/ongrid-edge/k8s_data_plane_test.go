package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	edgeplugintraces "github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
)

func TestK8sTelemetryGatewayFetcherBuildsStandaloneConfig(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"telemetry-cluster-id":                "7",
		"telemetry-access-key":                "kt_access",
		"telemetry-secret-key":                "ks_secret",
		"telemetry-traces-endpoint":           "https://manager.example/v1/traces",
		"telemetry-logs-endpoint":             "https://manager.example/loki/api/v1/push",
		"telemetry-remote-write-endpoint":     "https://manager.example/prometheus/api/v1/write",
		"telemetry-remote-write-basic-user":   "kt_access",
		"telemetry-remote-write-basic-pass":   "ks_secret",
		"telemetry-remote-write-tls-insecure": "true",
		"telemetry-remote-write-ca.pem":       "test-ca",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	configs, err := (&k8sTelemetryGatewayFetcher{dir: dir}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	cfg, ok := configs[edgeplugintraces.Name]
	if !ok {
		t.Fatalf("traces config missing: %#v", configs)
	}
	if cfg.EdgeID != 0 || cfg.Endpoint != files["telemetry-traces-endpoint"] || cfg.AuthUser != "kt_access" || cfg.AuthPass != "ks_secret" {
		t.Fatalf("gateway identity config = %#v", cfg)
	}
	for key, want := range map[string]any{
		"omit_device_id":                true,
		"enable_k8sattributes":          true,
		"enable_logs":                   true,
		"enable_metrics":                true,
		"bounded_pipelines":             true,
		"metrics_remote_write_endpoint": files["telemetry-remote-write-endpoint"],
	} {
		if got := cfg.Spec[key]; got != want {
			t.Fatalf("spec[%q] = %#v, want %#v", key, got, want)
		}
	}
	if cfg.Spec["metrics_remote_write_ca_file"] != filepath.Join(dir, "telemetry-remote-write-ca.pem") {
		t.Fatalf("CA path = %#v", cfg.Spec["metrics_remote_write_ca_file"])
	}
	if cfg.Spec["metrics_remote_write_ca_checksum"] == "" {
		t.Fatal("CA checksum is empty; projected CA changes would not reload the Collector")
	}
}

func TestTelemetryRemoteWriteWriterReloadsProjectedConfig(t *testing.T) {
	authA := make(chan string, 1)
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authA <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer serverA.Close()
	authB := make(chan string, 1)
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authB <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer serverB.Close()

	dir := t.TempDir()
	writeConfig := func(endpoint, user, pass string) {
		t.Helper()
		for name, value := range map[string]string{
			"telemetry-cluster-id":                "7",
			"telemetry-remote-write-endpoint":     endpoint,
			"telemetry-remote-write-basic-user":   user,
			"telemetry-remote-write-basic-pass":   pass,
			"telemetry-remote-write-tls-insecure": "false",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	writeConfig(serverA.URL, "writer-a", "secret-a")
	writer := &telemetryRemoteWriteWriter{dir: dir, log: nil}
	samples := []pkgpromwrite.Sample{{
		Labels: []pkgpromwrite.Label{{Name: "__name__", Value: "up"}},
		Value:  1,
		TsMs:   1,
	}}
	if err := writer.Write(context.Background(), samples); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if got := <-authA; got != "Basic d3JpdGVyLWE6c2VjcmV0LWE=" {
		t.Fatalf("first Authorization = %q", got)
	}

	writeConfig(serverB.URL, "writer-b", "secret-b")
	if err := writer.Write(context.Background(), samples); err != nil {
		t.Fatalf("reloaded Write() error = %v", err)
	}
	if got := <-authB; got != "Basic d3JpdGVyLWI6c2VjcmV0LWI=" {
		t.Fatalf("reloaded Authorization = %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "telemetry-remote-write-ca.pem"), []byte("invalid-ca"), 0600); err != nil {
		t.Fatalf("write invalid CA: %v", err)
	}
	if err := writer.Write(context.Background(), samples); err == nil || !strings.Contains(err.Error(), "contained no valid certificates") {
		t.Fatalf("Write() after CA change error = %v", err)
	}
}

func TestReadTelemetryFilesRejectsMissingWriteEndpoint(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{
		"telemetry-cluster-id": "7",
		"telemetry-access-key": "kt_access",
		"telemetry-secret-key": "ks_secret",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, err := readTelemetryFiles(context.Background(), dir); err == nil {
		t.Fatal("readTelemetryFiles() error = nil, want missing remote_write endpoint")
	}
}

func TestReadRemoteWriteFilesDoesNotRequireGatewayCredentials(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{
		"telemetry-cluster-id":                "7",
		"telemetry-remote-write-endpoint":     "https://metrics.example/api/v1/write",
		"telemetry-remote-write-basic-user":   "writer",
		"telemetry-remote-write-basic-pass":   "secret",
		"telemetry-remote-write-tls-insecure": "false",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := readRemoteWriteFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("readRemoteWriteFiles() error = %v", err)
	}
	if got.clusterID != 7 || got.remoteWriteEndpoint != "https://metrics.example/api/v1/write" || got.remoteWriteBasicUser != "writer" {
		t.Fatalf("remote write config = %#v", got)
	}
	if got.accessKey != "" || got.secretKey != "" || got.tracesEndpoint != "" || got.logsEndpoint != "" {
		t.Fatalf("scraper loaded gateway-only fields: %#v", got)
	}
}
