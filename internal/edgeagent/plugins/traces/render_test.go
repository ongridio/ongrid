package traces

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestRenderHappyPath(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   42,
		Endpoint: "https://manager.example.com/v1/traces",
		AuthUser: "ak-edge42",
		AuthPass: "sk-secret",
		Spec: map[string]interface{}{
			"grpc_endpoint": "0.0.0.0:4317",
			"http_endpoint": "0.0.0.0:4318",
			"extra_attrs": map[string]interface{}{
				"deployment_env": "test",
			},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	for _, want := range []string{
		// Receivers — both protocols must show up.
		"otlp:",
		"grpc:",
		"http:",
		"endpoint: 0.0.0.0:4317",
		"endpoint: 0.0.0.0:4318",
		// Exporter URL pointing at manager public endpoint.
		"endpoint: https://manager.example.com/v1/traces",
		// Resource attribute injection: edge_id is the load-bearing label.
		"key: device_id",
		`value: "42"`,
		"key: ongrid_source",
		// Extra attribute echoes through.
		"key: deployment_env",
		// Pipeline shape: traces only, otlp -> resource/device -> batch ->
		// otlphttp/manager.
		"pipelines:",
		"traces:",
		"receivers: [otlp]",
		"processors: [resource/device, batch]",
		"exporters: [otlphttp/manager]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q\n--- full body ---\n%s", want, body)
		}
	}
}

func TestRenderTLSInsecureSkipVerifyDefaultsTrue(t *testing.T) {
	// The standard install ships a self-signed manager cert, so the OTLP push
	// to the HTTPS /v1/traces endpoint must skip verification by default —
	// otherwise the exporter fails x509 and no edge span ever lands.
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/v1/traces",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "tls:") || !strings.Contains(body, "insecure_skip_verify: true") {
		t.Errorf("expected tls.insecure_skip_verify: true by default\n--- body ---\n%s", body)
	}
}

func TestRenderTLSInsecureSkipVerifyOptOut(t *testing.T) {
	// Operators with a real cert can turn verification back on.
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/v1/traces",
		Spec:     map[string]interface{}{"tls_insecure_skip_verify": false},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if body := string(out); !strings.Contains(body, "insecure_skip_verify: false") {
		t.Errorf("expected insecure_skip_verify: false when opted out\n--- body ---\n%s", body)
	}
}

func TestRenderDefaultEndpoints(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/v1/traces",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	// Defaults bind to localhost so the receiver isn't accidentally
	// reachable from the public network on multi-homed hosts.
	if !strings.Contains(body, "endpoint: 127.0.0.1:4317") {
		t.Errorf("default gRPC endpoint missing: %s", body)
	}
	if !strings.Contains(body, "endpoint: 127.0.0.1:4318") {
		t.Errorf("default HTTP endpoint missing: %s", body)
	}
}

func TestRenderBearerWhenNoUser(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/v1/traces",
		AuthPass: "tok-abc",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "Bearer tok-abc") {
		t.Errorf("expected Bearer auth header when AuthUser empty, got:\n%s", body)
	}
}

func TestRenderBasicWhenUserSet(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/v1/traces",
		AuthUser: "ak",
		AuthPass: "sk",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "Basic ") {
		t.Errorf("expected Basic auth header when both user+pass set, got:\n%s", body)
	}
}

func TestRenderRejectsMissingEndpoint(t *testing.T) {
	cfg := plugins.PluginConfig{Enabled: true, EdgeID: 1}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject missing endpoint")
	}
}

func TestRenderRejectsMissingEdgeID(t *testing.T) {
	cfg := plugins.PluginConfig{Enabled: true, Endpoint: "https://x/v1/traces"}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject missing edge_id")
	}
}
