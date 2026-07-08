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
		// otlphttp exporter endpoint is the base URL; it appends /v1/traces.
		"endpoint: https://manager.example.com",
		// Resource attribute injection: device_id is the load-bearing label.
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

func TestNormalizeOTLPHTTPBaseEndpoint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "root", in: "https://manager.example.com", want: "https://manager.example.com"},
		{name: "trace path", in: "https://manager.example.com/v1/traces", want: "https://manager.example.com"},
		{name: "trace path trailing slash", in: "https://manager.example.com/v1/traces/", want: "https://manager.example.com"},
		{name: "custom base path", in: "https://collector.example.com/otlp", want: "https://collector.example.com/otlp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeOTLPHTTPBaseEndpoint(tc.in); got != tc.want {
				t.Fatalf("normalizeOTLPHTTPBaseEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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

func TestRenderTLSInsecureSkipVerify(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/v1/traces",
		Spec: map[string]interface{}{
			"tls_insecure_skip_verify": true,
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	for _, want := range []string{
		"tls:",
		"insecure_skip_verify: true",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q\n--- full body ---\n%s", want, body)
		}
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

func TestRenderOmitDeviceIDForGateway(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		Endpoint: "https://manager.example.com/v1/traces",
		Spec: map[string]interface{}{
			"omit_device_id":          true,
			"enable_k8sattributes":    true,
			"enable_logs":             true,
			"enable_metrics":          true,
			"logs_endpoint":           "https://manager.example.com/loki/api/v1/push",
			"metrics_export_endpoint": "127.0.0.1:9464",
			"grpc_endpoint":           "0.0.0.0:4317",
			"http_endpoint":           "0.0.0.0:4318",
			"extra_attrs": map[string]interface{}{
				"cluster_id": "1",
			},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if strings.Contains(body, "key: device_id") {
		t.Fatalf("gateway config must not inject device_id:\n%s", body)
	}
	for _, want := range []string{
		"endpoint: 0.0.0.0:4317",
		"endpoint: 0.0.0.0:4318",
		"k8sattributes:",
		"auth_type: serviceAccount",
		"k8s.namespace.name",
		"k8s.deployment.name",
		"key: loki.resource.labels",
		"resource/loki_labels:",
		"endpoint: https://manager.example.com/loki/api/v1/push",
		"loki/manager:",
		"logs:",
		"exporters: [loki/manager]",
		"prometheus/gateway:",
		"endpoint: 127.0.0.1:9464",
		"resource_to_telemetry_conversion:",
		"metrics:",
		"exporters: [prometheus/gateway]",
		"processors: [k8sattributes, resource/device, batch]",
		"processors: [k8sattributes, resource/device, resource/loki_labels, batch]",
		"key: cluster_id",
		`value: "1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered gateway config missing %q\n--- full body ---\n%s", want, body)
		}
	}
}

func TestRenderTLSInsecureSkipVerifyDefaultsOn(t *testing.T) {
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
	// Default-on so the standard self-signed manager cert doesn't fail the
	// OTLP/HTTPS push (issue #144).
	if !strings.Contains(body, "insecure_skip_verify: true") {
		t.Errorf("expected tls.insecure_skip_verify by default, got:\n%s", body)
	}
}

func TestRenderRejectsGatewayMetricsWithoutEndpoint(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		Endpoint: "https://manager.example.com/v1/traces",
		Spec: map[string]interface{}{
			"omit_device_id": true,
			"enable_metrics": true,
		},
	}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject enable_metrics without metrics_export_endpoint")
	}
}

func TestRenderRejectsGatewayLogsWithoutEndpoint(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		Endpoint: "https://manager.example.com/v1/traces",
		Spec: map[string]interface{}{
			"omit_device_id": true,
			"enable_logs":    true,
		},
	}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject enable_logs without logs_endpoint")
	}
}

func TestRenderTLSInsecureSkipVerifyDisabled(t *testing.T) {
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
	body := string(out)
	// With a real cert the operator opts out; the tls block must be absent
	// so otelcol verifies the cert chain.
	if strings.Contains(body, "insecure_skip_verify") {
		t.Errorf("tls block must be absent when disabled, got:\n%s", body)
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
