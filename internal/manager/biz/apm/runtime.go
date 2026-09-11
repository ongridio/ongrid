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
	group := "service_instance_id,instance,service_version"
	names := "go_goroutines|go_memstats_heap_alloc_bytes|process_resident_memory_bytes|process_memory_usage_bytes|jvm_thread_count|jvm_threads_live_threads|nodejs_eventloop_lag_seconds"
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
		for _, name := range []string{"process_cpu_seconds_total", "process_cpu_time_seconds_total", "jvm_cpu_time_seconds_total"} {
			cpu = append(cpu, fmt.Sprintf("sum by (%s) (rate(%s%s[%s]))", group, name, selector, promDuration(window)))
		}
		parts = append(parts, fmt.Sprintf(`label_replace((%s), "apm_runtime", "process_cpu_cores", "", "")`, strings.Join(cpu, " or ")))
		// Preserve memory pool semantics; untyped exporters retain the legacy view.
		for _, kind := range []string{"heap", "non_heap", ""} {
			name := "jvm_memory_used_bytes"
			if kind != "" {
				name = "jvm_" + kind + "_memory_used_bytes"
			}
			expr := fmt.Sprintf(`sum by (%s) (last_over_time(jvm_memory_used_bytes{%s,%s,jvm_memory_type=%q}[5m]))`, group, scope, service, kind)
			parts = append(parts, fmt.Sprintf(`label_replace(%s, "apm_runtime", %q, "", "")`, expr, name))
		}
		allocation := fmt.Sprintf(`sum by (%s) (rate(go_memstats_alloc_bytes_total%s[%s]))`, group, selector, promDuration(window))
		parts = append(parts, fmt.Sprintf(`label_replace(%s, "apm_runtime", "go_memory_allocation_bytes_per_second", "", "")`, allocation))
		// Use reset-aware rates of sums/counts, never aggregate summary quantiles.
		// A zero count yields NaN, decoded as null to preserve gaps in the trend.
		for _, runtime := range []string{"go", "jvm"} {
			sum := fmt.Sprintf(`sum by (%s) (rate(%s_gc_duration_seconds_sum%s[%s]))`, group, runtime, selector, promDuration(window))
			count := fmt.Sprintf(`sum by (%s) (rate(%s_gc_duration_seconds_count%s[%s]))`, group, runtime, selector, promDuration(window))
			parts = append(parts, fmt.Sprintf(`label_replace((%s) / (%s), "apm_runtime", %q, "", "")`, sum, count, runtime+"_gc_mean_duration_seconds"))
		}
	}
	return strings.Join(parts, " or ")
}

// Discover options independently of the selected version/instance so the user
// can switch between replicas without clearing their service scope.
func (s *Service) runtimeInstances(ctx context.Context, q Query) ([]Instance, error) {
	q.InstanceID, q.ServiceVersion, q.Operation = "", "", ""
	q.MetricSource = "application_metrics"
	group := "service_instance_id,device_id,cluster_id,k8s_pod_name,service_version"
	parts := []string{}
	for _, protocol := range []string{"http", "rpc"} {
		q.Protocol = protocol
		parts = append(parts, q.aggregate("count_over_time", q.counter(), q.End.Sub(q.Start), group))
	}
	q.MetricSource = "tempo_spanmetrics"
	parts = append(parts, fmt.Sprintf("sum by (%s) (count_over_time(traces_spanmetrics_calls_total%s[%s]))", group, q.selector(), promDuration(q.End.Sub(q.Start))))
	series, err := s.instant(ctx, strings.Join(parts, " or "), q.End)
	if err != nil {
		return nil, err
	}
	instances := []Instance{}
	seen := map[[2]string]bool{}
	for _, item := range series {
		labels := item.Metric
		if labels["service_instance_id"] == "" && labels["k8s_pod_name"] == "" {
			continue
		}
		key := [2]string{labels["service_instance_id"], labels["service_version"]}
		if seen[key] {
			continue
		}
		seen[key] = true
		instances = append(instances, Instance{labels["service_instance_id"], labels["device_id"], labels["cluster_id"], labels["k8s_pod_name"], labels["service_version"]})
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
			} else if name == "process_cpu_cores" {
				unit = "cores"
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
