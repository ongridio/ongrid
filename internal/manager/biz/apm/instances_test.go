package apm

import (
	"slices"
	"strings"
	"testing"
)

func TestInstanceDiscoverySkipsRuntimeCurves(t *testing.T) {
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"one","service_version":"v1"},"value":[1600,"1"]}]`}
	q := testQuery()
	q.InstanceID, q.ServiceVersion = "two", "v2"
	out, err := New(p, nil, nil).Instances(t.Context(), q)
	if err != nil || p.calls != 1 || len(out.Items) != 0 || len(out.Instances) != 1 {
		t.Fatalf("discovery must only query identity metadata: %+v, calls=%d, err=%v", out, p.calls, err)
	}
	if strings.Contains(p.expr, `service_instance_id="two"`) || strings.Contains(p.expr, "process_cpu") || strings.Contains(p.expr, `service_version="v2"`) {
		t.Fatalf("discovery queried selected runtime data: %s", p.expr)
	}
}

func TestMetricInstancesSurviveDisabledTraces(t *testing.T) {
	p := &fakeProm{result: `[
 {"metric":{"service":"orders","service_namespace":"trade","deployment_environment_name":"production","apm_stat":"present","service_instance_id":"unsampled","device_id":"42"},"value":[1600,"2"]},
 {"metric":{"service":"orders","service_namespace":"trade","deployment_environment_name":"production","apm_stat":"present","service_instance_id":"sampled","device_id":"43"},"value":[1600,"2"]}
]`}
	q := testQuery()
	q.MetricSource = "application_metrics"
	result, err := New(p, nil, nil).Diagnostics(t.Context(), q)
	if err != nil || len(result.Instances) != 2 || result.SampledTraces != 0 {
		t.Fatalf("metrics-only instances: %+v %v", result, err)
	}
	if result.Instances[0].InstanceID != "sampled" || result.Instances[1].InstanceID != "unsampled" {
		t.Fatalf("instances not deterministic: %+v", result.Instances)
	}
	for _, part := range []string{`http_server_request_duration_seconds_count`, `service_name="orders"`, `service_namespace="trade"`, `deployment_environment_name="production"`, `service_instance_id,device_id,cluster_id,k8s_pod_name,service_version`} {
		if !strings.Contains(p.expr, part) {
			t.Fatalf("instance query lost scope: %s", p.expr)
		}
	}
}

func TestDiagnosticsExposeUncertaintyWithoutTraces(t *testing.T) {
	for _, tc := range []struct {
		name, rows, identity string
		reused               bool
	}{
		{"missing ID", `[{"metric":{"service":"orders","apm_stat":"present","device_id":"42"},"value":[1600,"2"]}]`, "incomplete", false},
		{"reused ID", `[{"metric":{"service":"orders","apm_stat":"present","service_instance_id":"shared","device_id":"42"},"value":[1600,"2"]},{"metric":{"service":"orders","apm_stat":"present","service_instance_id":"shared","device_id":"43"},"value":[1600,"2"]}]`, "observed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := testQuery()
			q.MetricSource = "application_metrics"
			empty := ""
			q.Environment = &empty
			result, err := New(&fakeProm{result: tc.rows}, nil, nil).Diagnostics(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			for key, status := range map[string]string{"resource_identity": "incomplete", "coverage": "unknown", "instance_identity": tc.identity, "sampling": "unknown", "traces": "unavailable", "context": "unknown", "downstream": "unknown", "logs": "unavailable"} {
				if !slices.ContainsFunc(result.Checks, func(check Check) bool { return check.Key == key && check.Status == status }) {
					t.Fatalf("missing %s=%s: %+v", key, status, result.Checks)
				}
			}
			if got := slices.ContainsFunc(result.Checks, func(check Check) bool { return check.Key == "instance_reuse" && check.Status == "unknown" }); got != tc.reused {
				t.Fatalf("instance reuse: %+v", result.Checks)
			}
		})
	}
}
