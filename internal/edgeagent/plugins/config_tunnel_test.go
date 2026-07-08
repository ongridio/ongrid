package plugins

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type fakeTunnelClient struct {
	resp tunnel.GetPluginConfigsResponse
	err  error
}

func (f *fakeTunnelClient) Dial(context.Context) error { return nil }

func (f *fakeTunnelClient) RegisterHandler(string, tunnel.Handler) {}

func (f *fakeTunnelClient) Call(_ context.Context, method string, _, resp any) error {
	if f.err != nil {
		return f.err
	}
	if method != tunnel.MethodGetPluginConfigs {
		return fmt.Errorf("unexpected method %q", method)
	}
	out, ok := resp.(*tunnel.GetPluginConfigsResponse)
	if !ok {
		return fmt.Errorf("unexpected response type %T", resp)
	}
	*out = f.resp
	return nil
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
			"logs": {Enabled: true, Endpoint: "https://127.0.0.1/loki/api/v1/push"},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cfg := got["logs"]
	if cfg.EdgeID != 42 {
		t.Fatalf("EdgeID = %d, want env override 42", cfg.EdgeID)
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
	assertSpecEqual(t, cfg.Spec, "pod_log_path", "/host/var/log/pods/*/*/*.log")
	assertSpecEqual(t, cfg.Spec, "enable_journald", false)
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
	t.Setenv("ONGRID_K8S_ROLE", "serverless-controller")
	t.Setenv("ONGRID_K8S_MODE", "serverless")
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
			"hostmetrics": {
				Enabled: true,
				Spec: map[string]interface{}{
					"extra_args": []interface{}{"--collector.cpu"},
				},
			},
			"procmetrics": {Enabled: true},
		},
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"hostmetrics", "procmetrics"})
	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	hostArgs := specStringSlice(t, got["hostmetrics"].Spec, "extra_args")
	for _, want := range []string{
		"--collector.cpu",
		"--path.procfs=/host/proc",
		"--path.sysfs=/host/sys",
		"--path.rootfs=/host/root",
		"--collector.filesystem.mount-points-exclude=^/(dev|proc|sys|run|var/lib/containerd/.+)($|/)",
	} {
		if !containsString(hostArgs, want) {
			t.Fatalf("hostmetrics extra_args missing %q in %#v", want, hostArgs)
		}
	}
	assertSpecEqual(t, got["procmetrics"].Spec, "procfs", "/host/proc")
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
	if containsString(hostArgs, "--path.procfs=/host/proc") {
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
	if cfg.Endpoint != "https://manager.example.com/loki/api/v1/push" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	assertSpecEqual(t, cfg.Spec, "mode", "kubernetes")
	assertSpecEqual(t, cfg.Spec, "cluster_id", "9")
	assertSpecEqual(t, cfg.Spec, "node_name", "kind-worker")
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
