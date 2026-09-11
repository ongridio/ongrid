package profiles

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestRenderPprofProfileMode(t *testing.T) {
	base := plugins.PluginConfig{EdgeID: 42, Endpoint: "https://manager.example/v1development/profiles", AuthUser: "ak", AuthPass: "sk"}
	base.Spec = map[string]any{
		"mode": "pprof",
		"runtime_target": map[string]any{
			"url":                         "http://127.0.0.1:6060/debug/pprof/heap",
			"profile_type":                "heap",
			"service_name":                "orders-api",
			"process_pid":                 1234,
			"collection_interval_seconds": 30,
		},
	}
	runtime, err := renderRuntime(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pprof/runtime:", "collection_interval: 30s", "service.name", "orders-api", "process.pid", "1234", "profile.type", "heap"} {
		if !strings.Contains(string(runtime), want) {
			t.Errorf("runtime config missing %q:\n%s", want, runtime)
		}
	}
}

func TestRenderCPUFromApplicationPprof(t *testing.T) {
	body, err := renderRuntime(plugins.PluginConfig{
		EdgeID: 42, Endpoint: "https://manager.example/v1development/profiles",
		Spec: map[string]any{"runtime_target": map[string]any{
			"url": "http://127.0.0.1:6060/debug/pprof/profile?seconds=30", "profile_type": "cpu", "service_name": "ongrid-edge",
		}},
	})
	if err != nil || !strings.Contains(string(body), "profile.type") || !strings.Contains(string(body), "cpu") {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestRenderRuntimeRejectsUnsafeURL(t *testing.T) {
	_, err := renderRuntime(plugins.PluginConfig{
		EdgeID: 1, Endpoint: "https://manager.example/v1development/profiles",
		Spec: map[string]any{"runtime_target": map[string]any{
			"url": "http://user:secret@127.0.0.1/debug/pprof/heap", "profile_type": "heap",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "without credentials") {
		t.Fatalf("err=%v, want credential rejection", err)
	}
}

func TestProfileServiceIdentityAttributes(t *testing.T) {
	cfg := plugins.PluginConfig{EdgeID: 42, Endpoint: "https://manager.example/v1development/profiles", Spec: map[string]any{"runtime_target": map[string]any{"url": "http://127.0.0.1:6060/debug/pprof/heap", "profile_type": "heap", "service_name": "orders", "environment": "production", "service_namespace": "trade", "instance_id": "pod-1", "service_version": "v2"}}}
	body, err := renderRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"service.namespace", "trade", "deployment.environment.name", "production", "service.instance.id", "pod-1", "service.version", "v2"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %s: %s", want, body)
		}
	}
	cfg.Spec["runtime_target"].(map[string]any)["instance_id"] = "bad\nvalue"
	if _, err := renderRuntime(cfg); err == nil {
		t.Fatal("invalid identity accepted")
	}
}
