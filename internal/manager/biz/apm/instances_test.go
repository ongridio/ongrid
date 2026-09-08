package apm

import (
	"strings"
	"testing"
)

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
