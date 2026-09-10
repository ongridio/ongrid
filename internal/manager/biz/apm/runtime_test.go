package apm

import (
	"encoding/json"
	"fmt"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Evaluate the generated query in Prometheus, including reset counters, GC
// with no collections, memory pool aggregation, and cross-instance isolation.
func TestRuntimePromQL(t *testing.T) {
	binary := os.Getenv("APM_TEST_PROMTOOL")
	if binary == "" {
		t.Skip("set APM_TEST_PROMTOOL to Prometheus promtool")
	}
	q := testQuery()
	q.InstanceID, q.ServiceVersion = "pod-1", "v1"
	scope := `service_name="orders",service_namespace="trade",deployment_environment_name="production",service_instance_id="pod-1",service_version="v1"`
	inputs := []any{}
	add := func(name, extra, values string) {
		inputs = append(inputs, map[string]any{"series": name + "{" + scope + extra + "}", "values": values})
	}
	add("go_memstats_alloc_bytes_total", "", "0 60 120 0 60 120")
	add("go_gc_duration_seconds_sum", "", "0 0.6 1.2 0 0.6 1.2")
	add("go_gc_duration_seconds_count", "", "0 6 12 0 6 12")
	add("jvm_memory_used_bytes", `,jvm_memory_type="heap",jvm_memory_pool_name="eden"`, "10+0x5")
	add("jvm_memory_used_bytes", `,jvm_memory_type="heap",jvm_memory_pool_name="old"`, "20+0x5")
	add("jvm_memory_used_bytes", `,jvm_memory_type="non_heap",jvm_memory_pool_name="code"`, "5+0x5")
	add("jvm_gc_duration_seconds_sum", `,jvm_gc_name="young"`, "0+0x5")
	add("jvm_gc_duration_seconds_count", `,jvm_gc_name="young"`, "0+0x5")
	inputs = append(inputs, map[string]any{"series": `go_memstats_alloc_bytes_total{service_name="orders",service_namespace="trade",deployment_environment_name="production",service_instance_id="other",service_version="v1"}`, "values": "0+60000x5"})
	samples := []any{}
	for _, item := range []struct {
		name  string
		value float64
	}{
		{"go_memory_allocation_bytes_per_second", .8},
		{"go_gc_mean_duration_seconds", .1},
		{"jvm_heap_memory_used_bytes", 30},
		{"jvm_non_heap_memory_used_bytes", 5},
	} {
		samples = append(samples, map[string]any{"labels": fmt.Sprintf(`{apm_runtime=%q,service_instance_id="pod-1",service_version="v1"}`, item.name), "value": item.value})
	}
	fixture := map[string]any{"evaluation_interval": "1m", "tests": []any{map[string]any{
		"interval": "1m", "input_series": inputs, "promql_expr_test": []any{map[string]any{"expr": "(" + runtimeExpression(q, 5*time.Minute) + ") >= 0", "eval_time": "5m", "exp_samples": samples}},
	}}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "runtime.yml")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), binary, "test", "rules", file).CombinedOutput(); err != nil {
		t.Fatalf("promtool: %v\n%s", err, output)
	}
}

func TestRuntimeUnitsAndMissingGC(t *testing.T) {
	for _, tc := range []struct {
		name, value, unit string
		missing           bool
	}{
		{"go_memory_allocation_bytes_per_second", "1048576", "bytes_per_second", false},
		{"jvm_gc_mean_duration_seconds", "NaN", "seconds", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProm{result: fmt.Sprintf(`[{"metric":{"apm_runtime":%q,"service_instance_id":"one","service_version":"v1"},"value":[1600,%q]}]`, tc.name, tc.value)}
			out, err := New(p, nil, nil).Runtime(t.Context(), testQuery())
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Items) != 1 || out.Items[0].Unit != tc.unit || (out.Items[0].Value == nil) != tc.missing {
				t.Fatalf("invalid runtime value: %+v", out)
			}
			if _, err := json.Marshal(out); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Read-only acceptance: use the current Go demo and an explicitly selected
// historical JVM window if the Java demo has been stopped.
func TestRuntimeMetricsIntegration(t *testing.T) {
	base := os.Getenv("APM_RUNTIME_PROMETHEUS")
	if base == "" {
		t.Skip("set APM_RUNTIME_PROMETHEUS for runtime acceptance")
	}
	svc := New(promquery.New(base, nil), nil, nil)
	env, ns := "development", "apm-demo"
	for _, language := range []string{"go", "java"} {
		t.Run(language, func(t *testing.T) {
			end := time.Now()
			if language == "java" && os.Getenv("APM_RUNTIME_JAVA_END") != "" {
				stamp, err := strconv.ParseInt(os.Getenv("APM_RUNTIME_JAVA_END"), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				end = time.Unix(stamp, 0)
			}
			q := Query{Start: end.Add(-time.Hour), End: end, Environment: &env, ServiceNamespace: &ns, ServiceName: "apm-demo-" + language, Protocol: "http"}
			out, err := svc.Runtime(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			wanted := map[string]string{"go_memory_allocation_bytes_per_second": "bytes_per_second", "go_gc_mean_duration_seconds": "seconds"}
			if language == "java" {
				wanted = map[string]string{"jvm_heap_memory_used_bytes": "bytes", "jvm_non_heap_memory_used_bytes": "bytes", "jvm_gc_mean_duration_seconds": "seconds"}
			}
			for name, unit := range wanted {
				instances := map[string]bool{}
				for _, row := range out.Items {
					if row.Name != name {
						continue
					}
					if row.Unit != unit || row.InstanceID == "" || row.Version == "" {
						t.Fatalf("bad metric metadata: %+v", row)
					}
					for _, point := range row.Points {
						if point.Value != nil && *point.Value > 0 && !math.IsInf(*point.Value, 0) && !math.IsNaN(*point.Value) {
							instances[row.InstanceID] = true
						}
					}
				}
				if len(instances) < 2 {
					t.Fatalf("%s: expected positive real samples for two replicas, got %v", name, instances)
				}
				t.Logf("%s (%s): %v", name, unit, instances)
			}
			if len(out.Instances) == 0 {
				t.Fatal("no selectable instances")
			}
			q.InstanceID, q.ServiceVersion = out.Instances[0].InstanceID, out.Instances[0].Version
			selected, err := svc.Runtime(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			if len(selected.Items) == 0 {
				t.Fatal("no scoped runtime metrics")
			}
			for _, row := range selected.Items {
				if row.InstanceID != q.InstanceID || row.Version != q.ServiceVersion {
					t.Fatalf("scope leaked: %+v", row)
				}
			}
		})
	}
}
