package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type fakeTunnelClient struct {
	resp       tunnel.GetPluginConfigsResponse
	secret     *tunnel.GetPluginSecretResponse
	secrets    map[uint64]tunnel.GetPluginSecretResponse
	err        error
	secretReq  *tunnel.GetPluginSecretRequest
	secretReqs []tunnel.GetPluginSecretRequest
	reportReq  *tunnel.ReportPluginConfigAppliedRequest
}

func (f *fakeTunnelClient) Dial(context.Context) error { return nil }

func (f *fakeTunnelClient) RegisterHandler(string, tunnel.Handler) {}

func (f *fakeTunnelClient) Call(_ context.Context, method string, req, resp any) error {
	if f.err != nil {
		return f.err
	}
	switch method {
	case tunnel.MethodGetPluginConfigs:
		out, ok := resp.(*tunnel.GetPluginConfigsResponse)
		if !ok {
			return fmt.Errorf("unexpected response type %T", resp)
		}
		*out = f.resp
		return nil
	case tunnel.MethodGetPluginSecret:
		in, ok := req.(tunnel.GetPluginSecretRequest)
		if !ok {
			return fmt.Errorf("unexpected secret request type %T", req)
		}
		f.secretReq = &in
		f.secretReqs = append(f.secretReqs, in)
		out, ok := resp.(*tunnel.GetPluginSecretResponse)
		if !ok {
			return fmt.Errorf("unexpected secret response type %T", resp)
		}
		if secret, exists := f.secrets[in.Generation]; exists {
			*out = secret
			return nil
		}
		if f.secret == nil {
			return fmt.Errorf("missing secret response for generation %d", in.Generation)
		}
		*out = *f.secret
		return nil
	case tunnel.MethodReportPluginConfigApplied:
		in, ok := req.(tunnel.ReportPluginConfigAppliedRequest)
		if !ok {
			return fmt.Errorf("unexpected report request type %T", req)
		}
		f.reportReq = &in
		out, ok := resp.(*tunnel.ReportPluginConfigAppliedResponse)
		if !ok {
			return fmt.Errorf("unexpected report response type %T", resp)
		}
		out.OK = true
		return nil
	default:
		return fmt.Errorf("unexpected method %q", method)
	}
}

func (f *fakeTunnelClient) AcceptStream() (tunnel.StreamConn, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeTunnelClient) OnReconnect(func()) {}

func (f *fakeTunnelClient) Close() error { return nil }

func TestTunnelConfigFetcherAppliesKubernetesLogsDefaults(t *testing.T) {
	t.Setenv("ONGRID_EDGE_ID", "42")
	t.Setenv("ONGRID_EDGE_ACCESS_KEY", "ak")
	t.Setenv("ONGRID_EDGE_SECRET_KEY", "sk")
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_K8S_NODE_NAME", "kind-worker")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com/")
	t.Setenv("ONGRID_K8S_ENROLL_TLS_INSECURE", "true")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 100,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {
				Enabled: true, Endpoint: "https://127.0.0.1/loki/api/v1/push",
				Spec: map[string]interface{}{"enable_k8sattributes": true},
			},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["logs"]
	if cfg.EdgeID != 100 {
		t.Fatalf("EdgeID label = %d, want manager-resolved device_id 100", cfg.EdgeID)
	}
	if cfg.Endpoint != "https://manager.example.com/loki/api/v1/push" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.AuthUser != "ak" || cfg.AuthPass != "sk" {
		t.Fatalf("auth = %q/%q, want ak/sk", cfg.AuthUser, cfg.AuthPass)
	}
	assertSpecEqual(t, cfg.Spec, "mode", "kubernetes")
	assertSpecEqual(t, cfg.Spec, "cluster_id", "9")
	assertSpecEqual(t, cfg.Spec, "node_name", "kind-worker")
	assertSpecEqual(t, cfg.Spec, "pod_log_path", "/var/log/pods/*/*/*.log")
	assertSpecEqual(t, cfg.Spec, "enable_journald", false)
	assertSpecEqual(t, cfg.Spec, "enable_k8sattributes", false)
}

func TestTunnelConfigFetcherNeverUsesTunnelEdgeIDAsDeviceLabel(t *testing.T) {
	t.Setenv("ONGRID_EDGE_ID", "42")
	t.Setenv("ONGRID_EDGE_DEVICE_ID", "")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 0,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: true, Endpoint: "https://manager.example.com/loki/api/v1/push"},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got["logs"].EdgeID != 0 {
		t.Fatalf("EdgeID label = %d, want 0 when host device is unresolved", got["logs"].EdgeID)
	}
}

func TestTunnelConfigFetcherAllowsExplicitDeviceIDForDevMode(t *testing.T) {
	t.Setenv("ONGRID_EDGE_ID", "42")
	t.Setenv("ONGRID_EDGE_DEVICE_ID", "9001")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 0,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: true, Endpoint: "https://manager.example.com/loki/api/v1/push"},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got["logs"].EdgeID != 9001 {
		t.Fatalf("EdgeID label = %d, want explicit device_id 9001", got["logs"].EdgeID)
	}
}

func TestTunnelConfigFetcherUsesProvidedCredentials(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com/")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 100,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: true},
		},
	}}
	fetcher := NewTunnelConfigFetcherWithCredentials(client, []string{"logs"}, "ak-enrolled", "sk-enrolled")
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["logs"]
	if cfg.AuthUser != "ak-enrolled" || cfg.AuthPass != "sk-enrolled" {
		t.Fatalf("auth = %q/%q, want enrolled credentials", cfg.AuthUser, cfg.AuthPass)
	}
}

func TestTunnelConfigFetcherAppliesKubernetesTracesDefaults(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_K8S_NODE_NAME", "kind-worker")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com/")
	t.Setenv("ONGRID_K8S_ENROLL_TLS_INSECURE", "true")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 100,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"traces": {Enabled: true, Endpoint: "https://127.0.0.1/v1/traces"},
		},
	}}
	fetcher := NewTunnelConfigFetcherWithCredentials(client, []string{"traces"}, "ak-enrolled", "sk-enrolled")
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["traces"]
	if cfg.Endpoint != "https://manager.example.com/v1/traces" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.AuthUser != "ak-enrolled" || cfg.AuthPass != "sk-enrolled" {
		t.Fatalf("auth = %q/%q, want enrolled credentials", cfg.AuthUser, cfg.AuthPass)
	}
	extra := specMap(t, cfg.Spec, "extra_attrs")
	if extra["cluster_id"] != "9" {
		t.Fatalf("extra_attrs.cluster_id = %#v, want 9", extra["cluster_id"])
	}
	if extra["node_name"] != "kind-worker" {
		t.Fatalf("extra_attrs.node_name = %#v, want kind-worker", extra["node_name"])
	}
	assertSpecEqual(t, cfg.Spec, "tls_insecure_skip_verify", true)
}

func TestTunnelConfigFetcherKeepsReachableTracesEndpointAndExplicitAttrs(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_K8S_NODE_NAME", "kind-worker")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"traces": {
				Enabled:  true,
				Endpoint: "https://tempo.example.net/v1/traces",
				Spec: map[string]interface{}{
					"extra_attrs": map[string]interface{}{
						"cluster_id": "custom-cluster",
						"service":    "edge-local",
					},
				},
			},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"traces"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["traces"]
	if cfg.Endpoint != "https://tempo.example.net/v1/traces" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	extra := specMap(t, cfg.Spec, "extra_attrs")
	if extra["cluster_id"] != "custom-cluster" {
		t.Fatalf("extra_attrs.cluster_id = %#v, want explicit custom-cluster", extra["cluster_id"])
	}
	if extra["node_name"] != "kind-worker" {
		t.Fatalf("extra_attrs.node_name = %#v, want kind-worker", extra["node_name"])
	}
	if extra["service"] != "edge-local" {
		t.Fatalf("extra_attrs.service = %#v, want edge-local", extra["service"])
	}
}

func TestTunnelConfigFetcherAppliesKubernetesGatewayTracesDefaults(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "controller")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_K8S_POD_NAMESPACE", "ongrid-system")
	t.Setenv("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", "true")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com/")
	t.Setenv("ONGRID_K8S_ENROLL_TLS_INSECURE", "true")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 0,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"traces": {Enabled: true, Endpoint: "https://127.0.0.1/v1/traces"},
		},
	}}
	fetcher := NewTunnelConfigFetcherWithCredentials(client, []string{"traces"}, "ak-enrolled", "sk-enrolled")
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["traces"]
	if !cfg.Enabled {
		t.Fatalf("traces gateway should remain enabled")
	}
	if cfg.Endpoint != "https://manager.example.com/v1/traces" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	assertSpecEqual(t, cfg.Spec, "grpc_endpoint", "0.0.0.0:4317")
	assertSpecEqual(t, cfg.Spec, "http_endpoint", "0.0.0.0:4318")
	assertSpecEqual(t, cfg.Spec, "omit_device_id", true)
	assertSpecEqual(t, cfg.Spec, "enable_k8sattributes", true)
	assertSpecEqual(t, cfg.Spec, "enable_logs", true)
	assertSpecEqual(t, cfg.Spec, "enable_metrics", true)
	assertSpecEqual(t, cfg.Spec, "logs_endpoint", "https://manager.example.com/loki/api/v1/push")
	assertSpecEqual(t, cfg.Spec, "metrics_export_endpoint", "127.0.0.1:9464")
	assertSpecEqual(t, cfg.Spec, "tls_insecure_skip_verify", true)
	extra := specMap(t, cfg.Spec, "extra_attrs")
	if extra["cluster_id"] != "9" {
		t.Fatalf("extra_attrs.cluster_id = %#v, want 9", extra["cluster_id"])
	}
	if extra["telemetry_gateway"] != "kubernetes" {
		t.Fatalf("extra_attrs.telemetry_gateway = %#v, want kubernetes", extra["telemetry_gateway"])
	}
	if extra["gateway_namespace"] != "ongrid-system" {
		t.Fatalf("extra_attrs.gateway_namespace = %#v, want ongrid-system", extra["gateway_namespace"])
	}
}

func TestTunnelConfigFetcherAppliesKubernetesHostMetricDefaults(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"metrics": {Enabled: true},
			"hostmetrics": {
				Enabled: true,
				Spec: map[string]interface{}{
					"extra_args": []interface{}{"--collector.cpu"},
				},
			},
			"procmetrics": {Enabled: true},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"metrics", "hostmetrics", "procmetrics"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	hostArgs := specStringSlice(t, got["hostmetrics"].Spec, "extra_args")
	for _, want := range []string{
		"--collector.cpu",
		"--path.procfs=/proc",
		"--path.sysfs=/sys",
		"--path.rootfs=/",
		"--collector.filesystem.mount-points-exclude=^/(dev|proc|sys|run|var/lib/containerd/.+)($|/)",
	} {
		if !containsString(hostArgs, want) {
			t.Fatalf("hostmetrics extra_args missing %q in %#v", want, hostArgs)
		}
	}
	assertSpecEqual(t, got["procmetrics"].Spec, "procfs", "/proc")
	assertSpecEqual(t, got["metrics"].Spec, "dedupe_filesystems_by_device", true)
}

func TestTunnelConfigFetcherPreservesExplicitFilesystemDedupeSetting(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"metrics": {
				Enabled: true,
				Spec: map[string]interface{}{
					"dedupe_filesystems_by_device": false,
				},
			},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"metrics"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	assertSpecEqual(t, got["metrics"].Spec, "dedupe_filesystems_by_device", false)
}

func TestTunnelConfigFetcherPreservesExplicitKubernetesHostMetricPaths(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"hostmetrics": {
				Enabled: true,
				Spec: map[string]interface{}{
					"extra_args": []interface{}{"--path.procfs=/custom/proc"},
				},
			},
			"procmetrics": {
				Enabled: true,
				Spec: map[string]interface{}{
					"procfs": "/custom/proc",
				},
			},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"hostmetrics", "procmetrics"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	hostArgs := specStringSlice(t, got["hostmetrics"].Spec, "extra_args")
	if !containsString(hostArgs, "--path.procfs=/custom/proc") {
		t.Fatalf("explicit procfs arg missing in %#v", hostArgs)
	}
	if containsString(hostArgs, "--path.procfs=/proc") {
		t.Fatalf("default procfs should not override explicit arg: %#v", hostArgs)
	}
	assertSpecEqual(t, got["procmetrics"].Spec, "procfs", "/custom/proc")
}

func TestTunnelConfigFetcherKeepsReachableLogsEndpoint(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: true, Endpoint: "https://loki.example.net/loki/api/v1/push"},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if cfg := got["logs"]; cfg.Endpoint != "https://loki.example.net/loki/api/v1/push" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
}

func TestTunnelConfigFetcherDoesNotOverrideExplicitHostLogsMode(t *testing.T) {
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com")

	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {
				Enabled: true,
				Spec: map[string]interface{}{
					"mode": "host",
				},
			},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["logs"]
	if cfg.Endpoint != "https://manager.example.com/loki/api/v1/push" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	assertSpecEqual(t, cfg.Spec, "mode", "host")
	if _, ok := cfg.Spec["cluster_id"]; ok {
		t.Fatalf("cluster_id should not be injected when mode=host: %#v", cfg.Spec)
	}
}

func TestTunnelConfigFetcherAppliesKubernetesDefaultsToEnvFallback(t *testing.T) {
	t.Setenv("ONGRID_EDGE_ID", "42")
	t.Setenv("ONGRID_EDGE_ACCESS_KEY", "ak")
	t.Setenv("ONGRID_EDGE_SECRET_KEY", "sk")
	t.Setenv("ONGRID_EDGE_PLUGIN_LOGS_ENABLED", "true")
	t.Setenv("ONGRID_K8S_ROLE", "node")
	t.Setenv("ONGRID_K8S_MODE", "full-node")
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "9")
	t.Setenv("ONGRID_K8S_NODE_NAME", "kind-worker")
	t.Setenv("ONGRID_MANAGER_PUBLIC_URL", "https://manager.example.com")

	client := &fakeTunnelClient{err: errors.New("temporary tunnel failure")}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["logs"]
	if !cfg.Enabled {
		t.Fatalf("logs fallback config should remain enabled")
	}
	if cfg.EdgeID != 0 {
		t.Fatalf("fallback device label = %d, want unresolved 0", cfg.EdgeID)
	}
	if cfg.Endpoint != "https://manager.example.com/loki/api/v1/push" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	assertSpecEqual(t, cfg.Spec, "mode", "kubernetes")
	assertSpecEqual(t, cfg.Spec, "cluster_id", "9")
	assertSpecEqual(t, cfg.Spec, "node_name", "kind-worker")
	assertSpecEqual(t, cfg.Spec, "enable_k8sattributes", false)
}

func TestTunnelConfigFetcherMaterializesExternalElasticsearchSecret(t *testing.T) {
	secret := "ZXMta2V5LWlkOmVzLWtleS1zZWNyZXQ="
	digest := sha256.Sum256([]byte(secret))
	client := &fakeTunnelClient{
		resp: tunnel.GetPluginConfigsResponse{EdgeID: 42, Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: true, Spec: map[string]interface{}{
				"backend":                   "external_elasticsearch",
				"backend_generation":        float64(3),
				"elasticsearch_secret_slot": "elasticsearch_api_key",
				"elasticsearch_ca_pem":      "test-ca",
				"log_probe_id":              "ongrid-log-probe-abcdefghijklmnopqrstuvwx",
			}},
		}},
		secret: &tunnel.GetPluginSecretResponse{
			Generation: 3,
			Content:    secret,
			SHA256:     hex.EncodeToString(digest[:]),
		},
	}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	fetcher.secretBaseDir = t.TempDir()
	logsDir := filepath.Join(fetcher.secretBaseDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir logs runtime: %v", err)
	}
	staleFiles := []string{
		"elasticsearch_api_key.g1", "elasticsearch_api_key.g1.generation",
		"elasticsearch_ca.g1.pem", "logs_probe.g1.0123456789abcdef.log",
	}
	for _, name := range staleFiles {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("stale"), 0o600); err != nil {
			t.Fatalf("write stale runtime file %s: %v", name, err)
		}
	}
	unrelatedPath := filepath.Join(logsDir, "operator-note.txt")
	if err := os.WriteFile(unrelatedPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unrelated runtime file: %v", err)
	}

	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if client.secretReq == nil || client.secretReq.Generation != 3 || client.secretReq.Slot != "elasticsearch_api_key" {
		t.Fatalf("secret request = %+v", client.secretReq)
	}
	cfg := got["logs"]
	keyPath, _ := cfg.Spec["elasticsearch_api_key_file"].(string)
	if keyPath == "" || strings.Contains(fmt.Sprint(cfg.Spec), secret) {
		t.Fatalf("materialized spec leaks or omits secret path: %#v", cfg.Spec)
	}
	content, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if string(content) != secret {
		t.Fatalf("key content = %q", content)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
	caPath, _ := cfg.Spec["elasticsearch_ca_file"].(string)
	if caPath == "" {
		t.Fatal("materialized CA path missing")
	}
	if _, err := os.Stat(caPath); err != nil {
		t.Fatalf("CA file missing: %v", err)
	}
	if _, ok := cfg.Spec["elasticsearch_ca_pem"]; ok {
		t.Fatal("CA PEM remained in supervisor snapshot")
	}
	probePath, _ := cfg.Spec["log_probe_file"].(string)
	probe, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatalf("read probe file: %v", err)
	}
	if string(probe) != "ongrid-log-probe-abcdefghijklmnopqrstuvwx\n" {
		t.Fatalf("probe content = %q", probe)
	}
	if err := fetcher.ReportPluginConfigApplied(context.Background(), "logs", cfg, nil); err != nil {
		t.Fatalf("ReportPluginConfigApplied: %v", err)
	}
	if client.reportReq == nil || !client.reportReq.Applied || client.reportReq.Generation != 3 || client.reportReq.ProbeID == "" {
		t.Fatalf("report request = %+v", client.reportReq)
	}
	for _, name := range staleFiles {
		if _, err := os.Stat(filepath.Join(logsDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale runtime file %s still exists: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(unrelatedPath); err != nil || string(raw) != "keep" {
		t.Fatalf("unrelated runtime file = %q, err=%v", raw, err)
	}
}

func TestMaterializeLogsRuntimeUsesUniqueProbePathForSameGenerationRetry(t *testing.T) {
	fetcher := NewTunnelConfigFetcher(nil, []string{"logs"})
	fetcher.secretBaseDir = t.TempDir()

	materialize := func(probeID string) (PluginConfig, string) {
		t.Helper()
		cfg, err := fetcher.materializeLogsRuntime(context.Background(), PluginConfig{Enabled: true, Spec: map[string]interface{}{
			"backend":            "builtin_loki",
			"backend_generation": float64(1),
			"log_probe_id":       probeID,
		}})
		if err != nil {
			t.Fatalf("materialize probe %q: %v", probeID, err)
		}
		path, _ := cfg.Spec["log_probe_file"].(string)
		if path == "" {
			t.Fatalf("probe %q path is empty", probeID)
		}
		return cfg, path
	}

	firstID := "ongrid-log-probe-abcdefghijklmnopqrstuvwx"
	secondID := "ongrid-log-probe-zyxwvutsrqponmlkjihgfedc"
	_, firstPath := materialize(firstID)
	_, secondPath := materialize(secondID)
	if firstPath == secondPath {
		t.Fatalf("same-generation retry reused probe path %q", firstPath)
	}
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("superseded first probe still exists: %v", err)
	}
	if raw, err := os.ReadFile(secondPath); err != nil || string(raw) != secondID+"\n" {
		t.Fatalf("second probe = %q, err=%v", raw, err)
	}
}

func TestMaterializeLogsRuntimePrunesManagedFilesWhenElasticsearchIsInactive(t *testing.T) {
	fetcher := NewTunnelConfigFetcher(nil, []string{"logs"})
	fetcher.secretBaseDir = t.TempDir()
	logsDir := filepath.Join(fetcher.secretBaseDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir logs runtime: %v", err)
	}
	managed := []string{
		"elasticsearch_api_key.g9", "elasticsearch_api_key.g9.generation",
		"elasticsearch_ca.g9.pem", "logs_probe.g9.0123456789abcdef.log",
	}
	for _, name := range managed {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("stale"), 0o600); err != nil {
			t.Fatalf("write managed runtime file %s: %v", name, err)
		}
	}
	unrelatedPath := filepath.Join(logsDir, "operator-note.txt")
	if err := os.WriteFile(unrelatedPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unrelated runtime file: %v", err)
	}

	if _, err := fetcher.materializeLogsRuntime(t.Context(), PluginConfig{Enabled: true, Spec: map[string]interface{}{
		"backend": "builtin_loki",
	}}); err != nil {
		t.Fatalf("materialize inactive Elasticsearch config: %v", err)
	}
	for _, name := range managed {
		if _, err := os.Stat(filepath.Join(logsDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed runtime file %s still exists: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(unrelatedPath); err != nil || string(raw) != "keep" {
		t.Fatalf("unrelated runtime file = %q, err=%v", raw, err)
	}
}

func TestTunnelConfigFetcherPrunesManagedLogsFilesWhenPluginIsDisabled(t *testing.T) {
	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: false},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	fetcher.secretBaseDir = t.TempDir()
	logsDir := filepath.Join(fetcher.secretBaseDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir logs runtime: %v", err)
	}
	stalePath := filepath.Join(logsDir, "elasticsearch_api_key.g3")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale credential: %v", err)
	}

	got, err := fetcher.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got["logs"].Enabled {
		t.Fatal("disabled logs plugin became enabled")
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled plugin retained managed credential: %v", err)
	}
}

func TestTunnelConfigFetcherCleanupFailureDoesNotBlockPluginDisable(t *testing.T) {
	client := &fakeTunnelClient{resp: tunnel.GetPluginConfigsResponse{
		EdgeID: 42,
		Configs: map[string]tunnel.GetPluginConfigsEntry{
			"logs": {Enabled: false},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	fetcher.secretBaseDir = t.TempDir()
	blockedPath := filepath.Join(fetcher.secretBaseDir, "logs", "elasticsearch_api_key.g3")
	if err := os.MkdirAll(blockedPath, 0o700); err != nil {
		t.Fatalf("mkdir non-empty managed path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blockedPath, "child"), []byte("keep directory non-empty"), 0o600); err != nil {
		t.Fatalf("write non-empty managed path: %v", err)
	}

	got, err := fetcher.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch must not fail when cleanup fails: %v", err)
	}
	if got["logs"].Enabled {
		t.Fatal("cleanup failure prevented plugin disable")
	}
	if _, err := os.Stat(blockedPath); err != nil {
		t.Fatalf("test did not exercise a retained cleanup failure path: %v", err)
	}
}

func TestMaterializeGenerationFileRejectsRollbackAndSymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	path := filepath.Join(dir, "elasticsearch_api_key")
	if err := materializeGenerationFile(dir, path, 5, []byte("new")); err != nil {
		t.Fatalf("write generation 5: %v", err)
	}
	if err := materializeGenerationFile(dir, path, 4, []byte("old")); err == nil {
		t.Fatal("older generation was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := materializeGenerationFile(dir, path, 6, []byte("next")); err == nil {
		t.Fatal("secret symlink was replaced")
	}
}

func TestConfigApplyErrorClassIdentifiesSecretMaterializationFailure(t *testing.T) {
	err := fmt.Errorf("write Elasticsearch API key: %w", os.ErrPermission)
	if got := configApplyErrorClass(err); got != "secret_materialization_failed" {
		t.Fatalf("configApplyErrorClass() = %q, want secret_materialization_failed", got)
	}
	if got := configApplyErrorClass(errors.New("collector readiness deadline exceeded")); got != "collector_not_ready" {
		t.Fatalf("readiness configApplyErrorClass() = %q, want collector_not_ready", got)
	}
}

func assertSpecEqual(t *testing.T, spec map[string]interface{}, key string, want interface{}) {
	t.Helper()
	got, ok := spec[key]
	if !ok {
		t.Fatalf("spec[%q] missing in %#v", key, spec)
	}
	if got != want {
		t.Fatalf("spec[%q] = %#v, want %#v", key, got, want)
	}
}

func specStringSlice(t *testing.T, spec map[string]interface{}, key string) []string {
	t.Helper()
	raw, ok := spec[key]
	if !ok {
		t.Fatalf("spec[%q] missing in %#v", key, spec)
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		t.Fatalf("spec[%q] has type %T, want string slice", key, raw)
		return nil
	}
}

func specMap(t *testing.T, spec map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	raw, ok := spec[key]
	if !ok {
		t.Fatalf("spec[%q] missing in %#v", key, spec)
	}
	switch v := raw.(type) {
	case map[string]interface{}:
		return v
	case map[string]string:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out
	default:
		t.Fatalf("spec[%q] has type %T, want map", key, raw)
		return nil
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
