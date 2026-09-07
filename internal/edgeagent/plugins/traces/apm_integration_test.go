package traces

import (
	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"os"
	"testing"
)

// run.sh starts the actual rendered Collector before SDK acceptance requests.
func TestAPMMetricsCollectorConfig(t *testing.T) {
	path := os.Getenv("APM_TEST_COLLECTOR_CONFIG")
	if path == "" {
		t.Skip("run scripts/apm-test/run.sh")
	}
	raw, err := render(plugins.PluginConfig{EdgeID: 42, Endpoint: "http://tempo:4318/v1/traces", Spec: map[string]any{"enable_metrics": true, "metrics_export_endpoint": "0.0.0.0:9464", "http_endpoint": "0.0.0.0:4318", "grpc_endpoint": "0.0.0.0:4317"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
}
