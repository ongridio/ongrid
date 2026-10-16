package apm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestReplicaScopeAcrossQueries(t *testing.T) {
	q := testQuery()
	q.InstanceID, q.ServiceVersion = `pod"1`, "1.1.0"
	q.DeviceID, q.ClusterID = `42"`, "7"
	if err := q.Validate(true); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"application_metrics", "tempo_spanmetrics"} {
		q.MetricSource = source
		for _, protocol := range []string{"http", "rpc"} {
			q.Protocol = protocol
			expr := q.metricSelector()
			for _, want := range []string{`device_id="42\""`, `cluster_id="7"`, `service_instance_id="pod\"1"`, `service_version="1.1.0"`} {
				if !strings.Contains(expr, want) {
					t.Fatalf("missing %s: %s", want, expr)
				}
			}
			traceQL := TraceQL(q)
			if !strings.Contains(traceQL, `resource.device_id = "42\""`) || !strings.Contains(traceQL, `resource.cluster_id = "7"`) {
				t.Fatal(traceQL)
			}
			if !strings.Contains(traceQL, `resource.service.instance.id = "pod\"1"`) || !strings.Contains(traceQL, `resource.service.version = "1.1.0"`) {
				t.Fatal(traceQL)
			}
		}
	}
	expr := runtimeExpression(q, 5*time.Minute)
	for _, want := range []string{`device_id="42\""`, `cluster_id="7"`, `service_instance_id="pod\"1"`, `service_version="1.1.0"`, `rate(process_cpu_seconds_total`, `rate(jvm_cpu_time_seconds_total`, `last_over_time`, `service_instance_id,instance,service_version`} {
		if !strings.Contains(expr, want) {
			t.Fatalf("missing %s: %s", want, expr)
		}
	}
	q.InstanceID = "bad\ninstance"
	if q.Validate(true) == nil {
		t.Fatal("invalid instance accepted")
	}
}

type runtimeProm struct{}

func (runtimeProm) Query(_ context.Context, expr string, _ time.Time) (*promquery.InstantResult, error) {
	data := `[{"metric":{"service_instance_id":"pod-1","service_version":"1.0"},"value":[1600,"1"]},{"metric":{"service_instance_id":"pod-2","service_version":"1.1"},"value":[1600,"1"]}]`
	if strings.Contains(expr, "apm_runtime") {
		data = `[{"metric":{"apm_runtime":"process_cpu_cores","service_instance_id":"pod-2","service_version":"1.1"},"value":[1600,"0"]}]`
	}
	return &promquery.InstantResult{ResultType: "vector", Result: json.RawMessage(data)}, nil
}
func (runtimeProm) QueryRange(_ context.Context, _ string, _, _ time.Time, _ time.Duration) (*promquery.InstantResult, error) {
	return &promquery.InstantResult{ResultType: "matrix", Result: json.RawMessage(`[{"metric":{"apm_runtime":"process_cpu_cores","service_instance_id":"pod-2","service_version":"1.1"},"values":[[1500,"0.02"],[1600,"0"]]}]`)}, nil
}
func TestRuntimeKeepsVersionAndZeroValues(t *testing.T) {
	q := testQuery()
	q.InstanceID, q.ServiceVersion = "pod-2", "1.1"
	out, err := New(runtimeProm{}, nil, nil).Runtime(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || len(out.Instances) != 2 {
		t.Fatalf("items/options = %+v", out)
	}
	row := out.Items[0]
	if row.Unit != "cores" || row.Version != "1.1" || row.InstanceID != "pod-2" || row.Value == nil || *row.Value != 0 || len(row.Points) != 2 || *row.Points[0].Value != 0.02 {
		t.Fatalf("runtime = %+v", row)
	}
}

func TestAPMExcludesRawPodMetrics(t *testing.T) {
	q := testQuery()
	q.MetricSource = "application_metrics"
	for _, expression := range []string{requestRate(q, 5*time.Minute, identityLabels), q.errorRate(5*time.Minute, identityLabels), runtimeExpression(q, 5*time.Minute)} {
		if !strings.Contains(expression, `ongrid_source!="k8s:app-metrics"`) {
			t.Fatalf("raw Pod metrics can enter APM: %s", expression)
		}
	}
}
