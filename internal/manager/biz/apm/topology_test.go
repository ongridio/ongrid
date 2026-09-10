package apm

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceDeploymentsUseCurrentTelemetryAndDeviceIdentity(t *testing.T) {
	p := &fakeProm{result: `[
{"metric":{"service":"checkout","service_namespace":"shop","deployment_environment_name":"prod","device_id":"42"},"value":[1800,"1790"]},
{"metric":{"service":"checkout","service_namespace":"shop","deployment_environment_name":"stage","device_id":"77"},"value":[1800,"1780"]},
{"metric":{"service":"old","device_id":"42"},"value":[1800,"1490"]},
{"metric":{"service":"unbound"},"value":[1800,"1790"]}
]`}
	s := New(p, nil, nil)
	rows, err := s.ServiceDeployments(t.Context(), time.Unix(1800, 0))
	if err != nil || len(rows) != 3 {
		t.Fatalf("deployments = %+v, %v", rows, err)
	}
	if rows[0].DeviceID != 42 || rows[1].Environment != "stage" || rows[2].DeviceID != 0 {
		t.Fatalf("identity changed: %+v", rows)
	}
	for _, part := range []string{"timestamp(", "topk(5001", "service_namespace", "deployment_environment_name", "device_id", "traces_spanmetrics_calls_total"} {
		if !strings.Contains(p.expr, part) {
			t.Fatalf("missing %s in %s", part, p.expr)
		}
	}
	p.err = errors.New("query unavailable")
	if _, err := s.ServiceDeployments(t.Context(), time.Unix(1800, 0)); err == nil {
		t.Fatal("query failure accepted as empty inventory")
	}
	p.err = nil
	p.result = `[{"metric":{"service":"bad","device_id":"not-an-id"},"value":[1800,"1790"]}]`
	if _, err := s.ServiceDeployments(t.Context(), time.Unix(1800, 0)); err == nil {
		t.Fatal("invalid device ID accepted")
	}
}

func TestServiceDeploymentPromQL(t *testing.T) {
	binary := os.Getenv("APM_TEST_PROMTOOL")
	if binary == "" {
		t.Skip("set APM_TEST_PROMTOOL to Prometheus promtool")
	}
	p := &fakeProm{result: `[]`}
	if _, err := New(p, nil, nil).ServiceDeployments(t.Context(), time.Unix(120, 0)); err != nil {
		t.Fatal(err)
	}
	fixture := map[string]any{"evaluation_interval": "1m", "tests": []any{map[string]any{
		"interval": "1m", "input_series": []any{
			map[string]any{"series": `http_server_request_duration_seconds_count{service_name="checkout",service_namespace="shop",deployment_environment_name="prod",device_id="42"}`, "values": "1+0x7"},
			map[string]any{"series": `http_server_request_duration_seconds_sum{service_name="checkout",service_namespace="shop",deployment_environment_name="prod",device_id="42"}`, "values": "5+0x7"},
			map[string]any{"series": `process_memory_usage_bytes{service_name="checkout",service_namespace="shop",deployment_environment_name="stage",device_id="77"}`, "values": "1+0x7"},
			map[string]any{"series": `process_memory_usage_bytes{service_name="stopped",device_id="42"}`, "values": "1 stale _"},
			map[string]any{"series": `traces_spanmetrics_calls_total{service="trace-only",device_id="42"}`, "values": "1+0x7"},
		}, "promql_expr_test": []any{map[string]any{"expr": p.expr, "eval_time": "2m", "exp_samples": []any{
			map[string]any{"labels": `{service="checkout",service_namespace="shop",deployment_environment_name="prod",device_id="42"}`, "value": 120},
			map[string]any{"labels": `{service="checkout",service_namespace="shop",deployment_environment_name="stage",device_id="77"}`, "value": 120},
			map[string]any{"labels": `{service="trace-only",device_id="42"}`, "value": 120},
		}}, map[string]any{"expr": p.expr, "eval_time": "7m", "exp_samples": []any{
			map[string]any{"labels": `{service="checkout",service_namespace="shop",deployment_environment_name="prod",device_id="42"}`, "value": 420},
			map[string]any{"labels": `{service="checkout",service_namespace="shop",deployment_environment_name="stage",device_id="77"}`, "value": 420},
		}}},
	}}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "service-deployments.yml")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), binary, "test", "rules", file).CombinedOutput(); err != nil {
		t.Fatalf("promtool: %v\n%s", err, output)
	}
}
