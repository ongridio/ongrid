package apm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type RuntimePoint struct {
	Timestamp float64  `json:"timestamp"`
	Value     *float64 `json:"value"`
}

type RuntimeMetric struct {
	Name       string         `json:"name"`
	Unit       string         `json:"unit"`
	InstanceID string         `json:"instance_id"`
	Value      *float64       `json:"value"`
	Version    string         `json:"version"`
	Points     []RuntimePoint `json:"points"`
}

type Runtime struct {
	Items     []RuntimeMetric `json:"items"`
	Metadata  Metadata        `json:"metadata"`
	Instances []Instance      `json:"instances"`
}

// Query only application metrics with an exact resource identity. JVM used
// memory remains a JVM metric; it is never presented as process RSS.
func runtimeExpression(q Query, window time.Duration) string {
	parts := []string{}
	for _, variant := range q.clusterQueries() {
		parts = append(parts, "("+runtimeClusterExpression(variant, window)+")")
	}
	return strings.Join(parts, " or ")
}

func runtimeClusterExpression(q Query, window time.Duration) string {
	group := "service_instance_id,instance,service_version"
	names := "go_goroutines|go_memstats_heap_alloc_bytes|jvm_thread_count|jvm_threads_live_threads|nodejs_eventloop_lag_seconds|go_memory_limit_bytes|go_memory_gc_goal_bytes|go_processor_limit|go_config_gogc_percent|nodejs_eventloop_utilization_ratio|nodejs_eventloop_delay_(min|max|mean|stddev|p50|p90|p99)_seconds"
	scope := fmt.Sprintf(`deployment_environment_name=%q,service_namespace=%q`, *q.Environment, *q.ServiceNamespace)
	if filters := q.instanceLabels(); len(filters) > 0 {
		scope += "," + strings.Join(filters, ",")
	}
	parts := []string{}
	for _, service := range []string{fmt.Sprintf("service_name=%q", q.ServiceName), fmt.Sprintf(`service_name="",service=%q`, q.ServiceName)} {
		selector := "{" + scope + "," + service + "}"
		gauges := fmt.Sprintf(`sum by (__name__,%s) (last_over_time({__name__=~%q,%s,%s}[5m]))`, group, names, scope, service)
		parts = append(parts, fmt.Sprintf(`label_replace(%s, "apm_runtime", "$1", "__name__", "(.+)")`, gauges))
		cpu := []string{}
		// Ordered alternatives prevent double counting SDKs exporting both process
		// and runtime CPU time. Sum user/system rates after accounting for resets.
		for _, name := range []string{"ongrid_apm_process_cpu_seconds_total", "process_cpu_seconds_total", "process_cpu_time_seconds_total", "jvm_cpu_time_seconds_total"} {
			cpu = append(cpu, fmt.Sprintf("sum by (%s) (rate(%s%s[%s]))", group, name, selector, promDuration(window)))
		}
		parts = append(parts, fmt.Sprintf(`label_replace((%s), "apm_runtime", "process_cpu_cores", "", "")`, strings.Join(cpu, " or ")))
		// Edge reads each selected process once, independently of language. Rate
		// before summing preserves per-PID resets for multi-process containers.
		for _, metric := range []struct{ source, name string }{
			{"io_read_bytes_total", "process_io_read_bytes_per_second"},
			{"io_write_bytes_total", "process_io_write_bytes_per_second"},
		} {
			expr := fmt.Sprintf(`sum by (%s) (rate(ongrid_apm_process_%s%s[%s]))`, group, metric.source, selector, promDuration(window))
			parts = append(parts, fmt.Sprintf(`label_replace(%s, "apm_runtime", %q, "", "")`, expr, metric.name))
		}
		for _, name := range []string{"resident_memory_bytes", "virtual_memory_bytes", "threads", "open_fds"} {
			// Do not extend exited workers' gauges for five minutes: sum only
			// samples seen within two scrape intervals at each evaluation time.
			expr := fmt.Sprintf(`sum by (%s) (ongrid_apm_process_%s%s and (timestamp(ongrid_apm_process_%s%s) > time() - 30))`, group, name, selector, name, selector)
			if name == "resident_memory_bytes" {
				// The explicit Edge process reading wins over duplicate SDK RSS.
				expr += fmt.Sprintf(` or sum by (%s) (last_over_time(process_resident_memory_bytes%s[5m])) or sum by (%s) (last_over_time(process_memory_usage_bytes%s[5m]))`, group, selector, group, selector)
			}
			parts = append(parts, fmt.Sprintf(`label_replace((%s), "apm_runtime", %q, "", "")`, expr, "process_"+name))
		}
		// Preserve memory pool semantics; untyped exporters retain the legacy view.
		for _, kind := range []string{"heap", "non_heap", ""} {
			name := "jvm_memory_used_bytes"
			if kind != "" {
				name = "jvm_" + kind + "_memory_used_bytes"
			}
			expr := fmt.Sprintf(`sum by (%s) (last_over_time(jvm_memory_used_bytes{%s,%s,jvm_memory_type=%q}[5m]))`, group, scope, service, kind)
			parts = append(parts, fmt.Sprintf(`label_replace(%s, "apm_runtime", %q, "", "")`, expr, name))
		}
		allocation := fmt.Sprintf(`sum by (%s) (rate(go_memstats_alloc_bytes_total%s[%s])) or sum by (%s) (rate(go_memory_allocated_bytes_total%s[%s]))`, group, selector, promDuration(window), group, selector, promDuration(window))
		parts = append(parts, fmt.Sprintf(`label_replace(%s, "apm_runtime", "go_memory_allocation_bytes_per_second", "", "")`, allocation))
		// Use reset-aware rates of sums/counts, never aggregate summary quantiles.
		// A zero count yields NaN, decoded as null to preserve gaps in the trend.
		for _, runtime := range []string{"go", "jvm"} {
			sum := fmt.Sprintf(`sum by (%s) (rate(%s_gc_duration_seconds_sum%s[%s]))`, group, runtime, selector, promDuration(window))
			count := fmt.Sprintf(`sum by (%s) (rate(%s_gc_duration_seconds_count%s[%s]))`, group, runtime, selector, promDuration(window))
			parts = append(parts, fmt.Sprintf(`label_replace((%s) / (%s), "apm_runtime", %q, "", "")`, sum, count, runtime+"_gc_mean_duration_seconds"))
		}

		// OBI runtime metrics use OTel names. Keep pool/type semantics, and keep
		// Go runtime CPU estimates separate from OS process CPU measurements.
		addRuntime := func(name, expr string) {
			parts = append(parts, fmt.Sprintf(`label_replace((%s), "apm_runtime", %q, "", "")`, expr, name))
		}
		addRuntime("go_goroutines", fmt.Sprintf(`sum by (%s) (last_over_time(go_goroutine_count%s[5m])) unless on (%s) go_goroutines%s`, group, selector, group, selector))
		for _, kind := range []string{"stack", "other"} {
			addRuntime("go_"+kind+"_memory_used_bytes", fmt.Sprintf(`sum by (%s) (last_over_time(go_memory_used_bytes{%s,%s,go_memory_type=%q}[5m]))`, group, scope, service, kind))
		}
		for _, metric := range []struct{ source, name string }{
			{"go_memory_gc_cycles_total", "go_gc_cycles_per_second"},
			{"go_memory_allocations_total", "go_allocations_per_second"},
		} {
			addRuntime(metric.name, fmt.Sprintf(`sum by (%s) (rate(%s%s[%s]))`, group, metric.source, selector, promDuration(window)))
		}
		addRuntime("go_runtime_cpu_cores", fmt.Sprintf(`sum by (%s) (rate(go_cpu_time_seconds_total{%s,%s,go_cpu_state!="idle"}[%s]))`, group, scope, service, promDuration(window)))
		// OBI estimates histogram sums from bucket lower bounds. Do not merge
		// these estimates with SDK duration sums or present them as quantiles.
		for _, metric := range []struct{ source, name string }{
			{"go_memory_gc_pause_duration_seconds", "go_gc_pause_mean_duration_seconds"},
			{"go_schedule_duration_seconds", "go_schedule_mean_duration_seconds"},
		} {
			sum := fmt.Sprintf(`sum by (%s) (rate(%s_sum%s[%s]))`, group, metric.source, selector, promDuration(window))
			count := fmt.Sprintf(`sum by (%s) (rate(%s_count%s[%s]))`, group, metric.source, selector, promDuration(window))
			addRuntime(metric.name, "("+sum+") / ("+count+")")
		}
		for _, metric := range []string{"committed", "limit", "used_after_last_gc"} {
			for _, kind := range []string{"heap", "non_heap"} {
				addRuntime("jvm_"+kind+"_memory_"+metric+"_bytes", fmt.Sprintf(`sum by (%s) (last_over_time(jvm_memory_%s_bytes{%s,%s,jvm_memory_type=%q}[5m]))`, group, metric, scope, service, kind))
			}
		}
		for _, state := range []string{"active", "idle"} {
			addRuntime("nodejs_eventloop_"+state+"_ratio", fmt.Sprintf(`sum by (%s) (rate(nodejs_eventloop_time_seconds_total{%s,%s,nodejs_eventloop_state=%q}[%s]))`, group, scope, service, state, promDuration(window)))
		}
	}
	return strings.Join(parts, " or ")
}

// Discover options independently of the selected version/instance so the user
// can switch between replicas without clearing their service scope.
func (s *Service) runtimeInstances(ctx context.Context, q Query) ([]Instance, error) {
	q.InstanceID, q.ServiceVersion, q.Operation = "", "", ""
	q.MetricSource = "application_metrics"
	group := "service_instance_id,device_id,cluster_id,k8s_pod_name,service_version,k8s_namespace_name,k8s_cluster_id"
	parts := []string{}
	for _, protocol := range []string{"http", "rpc"} {
		q.Protocol = protocol
		parts = append(parts, q.aggregate("count_over_time", q.counter(), q.End.Sub(q.Start), group))
	}
	q.MetricSource = "tempo_spanmetrics"
	parts = append(parts, q.aggregate("count_over_time", "traces_spanmetrics_calls_total", q.End.Sub(q.Start), group))
	// A selected process can consume resources without receiving requests.
	q.MetricSource = "application_metrics"
	q.Protocol = "http" // process metrics carry no protocol labels
	parts = append(parts, q.aggregate("count_over_time", "ongrid_apm_process_resident_memory_bytes", q.End.Sub(q.Start), group))
	series, err := s.instant(ctx, strings.Join(parts, " or "), q.End)
	if err != nil {
		return nil, err
	}
	instances := []Instance{}
	seen := map[Instance]bool{}
	for _, item := range series {
		labels := item.Metric
		if labels["service_instance_id"] == "" && labels["k8s_pod_name"] == "" {
			continue
		}
		key := Instance{InstanceID: labels["service_instance_id"], DeviceID: labels["device_id"], ClusterID: labels["cluster_id"], K8sClusterID: labels["k8s_cluster_id"], Pod: labels["k8s_pod_name"], Version: labels["service_version"], Namespace: labels["k8s_namespace_name"]}
		if seen[key] {
			continue
		}
		seen[key] = true
		instances = append(instances, key)
	}
	sort.Slice(instances, func(i, j int) bool { return fmt.Sprint(instances[i]) < fmt.Sprint(instances[j]) })
	return instances, nil
}

func (s *Service) Instances(ctx context.Context, q Query) (*Runtime, error) {
	if err := s.validateQuery(ctx, &q, true); err != nil {
		return nil, err
	}
	out := &Runtime{Items: []RuntimeMetric{}, Instances: []Instance{}, Metadata: metadata(q)}
	out.Metadata.MetricSource, out.Metadata.Sampling = "application_metrics", "not_applicable"
	var err error
	out.Instances, err = s.runtimeInstances(ctx, q)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) Runtime(ctx context.Context, q Query) (*Runtime, error) {
	if err := s.validateQuery(ctx, &q, true); err != nil {
		return nil, err
	}
	out := &Runtime{Items: []RuntimeMetric{}, Instances: []Instance{}, Metadata: metadata(q)}
	out.Metadata.MetricSource, out.Metadata.Sampling = "application_metrics", "not_applicable"
	var err error
	out.Instances, err = s.runtimeInstances(ctx, q)
	if err != nil {
		return nil, err
	}
	step := max(30*time.Second, time.Duration((q.End.Sub(q.Start).Seconds()+239)/240)*time.Second)
	expr := runtimeExpression(q, max(5*time.Minute, 4*step))
	current, err := s.instant(ctx, expr, q.End)
	if err != nil {
		return nil, err
	}
	result, err := s.prom.QueryRange(ctx, expr, q.Start, q.End, step)
	if err != nil {
		return nil, fmt.Errorf("apm: runtime trend: %w", err)
	}
	series, err := decodeSeries(result, "matrix")
	if err != nil {
		return nil, err
	}
	rows := map[[3]string]*RuntimeMetric{}
	rowFor := func(labels map[string]string) *RuntimeMetric {
		name := labels["apm_runtime"]
		if name == "" {
			name = labels["__name__"]
		}
		instance := labels["service_instance_id"]
		if instance == "" {
			instance = labels["instance"]
		}
		key := [3]string{name, instance, labels["service_version"]}
		if rows[key] == nil {
			unit := "count"
			if strings.HasSuffix(name, "_bytes_per_second") {
				unit = "bytes_per_second"
			} else if strings.HasSuffix(name, "_bytes") {
				unit = "bytes"
			} else if strings.HasSuffix(name, "_cores") {
				unit = "cores"
			} else if strings.HasSuffix(name, "_ratio") {
				unit = "ratio"
			} else if strings.HasSuffix(name, "_percent") {
				unit = "percent"
			} else if strings.HasSuffix(name, "_per_second") {
				unit = "per_second"
			} else if strings.HasSuffix(name, "_seconds") {
				unit = "seconds"
			}
			rows[key] = &RuntimeMetric{Name: name, Unit: unit, InstanceID: instance, Version: key[2], Points: []RuntimePoint{}}
		}
		return rows[key]
	}
	for _, item := range current {
		value, err := sampleValue(item.Value)
		if err != nil {
			return nil, err
		}
		rowFor(item.Metric).Value = value
	}
	for _, item := range series {
		if len(item.Values) > 1000 {
			return nil, fmt.Errorf("%w: runtime sample limit exceeded", errs.ErrBudgetExceeded)
		}
		row := rowFor(item.Metric)
		for _, pair := range item.Values {
			value, err := sampleValue(pair)
			if err != nil {
				return nil, err
			}
			var ts float64
			if err := json.Unmarshal(pair[0], &ts); err != nil {
				return nil, fmt.Errorf("apm: runtime sample time: %w", err)
			}
			row.Points = append(row.Points, RuntimePoint{ts, value})
		}
	}
	if len(rows) > 500 {
		return nil, fmt.Errorf("%w: narrow the runtime version/instance scope", errs.ErrBudgetExceeded)
	}
	for _, row := range rows {
		sort.Slice(row.Points, func(i, j int) bool { return row.Points[i].Timestamp < row.Points[j].Timestamp })
		out.Items = append(out.Items, *row)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		return out.Items[i].Name+out.Items[i].InstanceID+out.Items[i].Version < out.Items[j].Name+out.Items[j].InstanceID+out.Items[j].Version
	})
	return out, nil
}
