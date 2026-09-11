package apm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type Check struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Instance struct {
	InstanceID string `json:"instance_id"`
	DeviceID   string `json:"device_id"`
	ClusterID  string `json:"cluster_id"`
	Pod        string `json:"pod"`
	Version    string `json:"version"`
}

type Diagnostics struct {
	Checks              []Check    `json:"checks"`
	Instances           []Instance `json:"instances"`
	TraceIDs            []string   `json:"trace_ids"`
	SampledTraces       int        `json:"sampled_traces"`
	Metadata            Metadata   `json:"metadata"`
	LastMetricTimestamp *float64   `json:"last_metric_timestamp,omitempty"`
}

func (s *Service) Diagnostics(ctx context.Context, q Query) (*Diagnostics, error) {
	if err := s.validateQuery(ctx, &q, true); err != nil {
		return nil, err
	}
	out := &Diagnostics{Checks: []Check{}, Instances: []Instance{}, TraceIDs: []string{}, Metadata: metadata(q)}
	identityStatus := "observed"
	if *q.Environment == "" || *q.ServiceNamespace == "" || strings.HasPrefix(q.ServiceName, "unknown_service") {
		identityStatus = "incomplete"
	}
	out.Checks = append(out.Checks, Check{"resource_identity", identityStatus, "service_identity"},
		Check{"coverage", "unknown", "expected_instances_unknown"})
	instances := map[Instance]bool{}
	addInstance := func(instance Instance) {
		if (instance.InstanceID != "" || instance.DeviceID != "" || instance.Pod != "") && !instances[instance] {
			instances[instance] = true
			out.Instances = append(out.Instances, instance)
		}
	}
	defer func() {
		for _, key := range []string{"downstream", "context", "logs"} {
			if !slices.ContainsFunc(out.Checks, func(check Check) bool { return check.Key == key }) {
				status, detail := "unknown", "no_sampled_trace"
				if key == "logs" && s.logs == nil {
					status, detail = "unavailable", "backend_disabled"
				}
				out.Checks = append(out.Checks, Check{key, status, detail})
			}
		}
		sort.Slice(out.Instances, func(i, j int) bool { return fmt.Sprint(out.Instances[i]) < fmt.Sprint(out.Instances[j]) })
	}()
	rows, err := s.summaries(ctx, q, false)
	switch {
	case err != nil:
		out.Checks = append(out.Checks, Check{"metrics", "unavailable", "query_failed"})
	case len(rows) == 0:
		out.Checks = append(out.Checks, Check{"metrics", "not_observed", "no_metrics"})
	default:
		out.Checks = append(out.Checks, Check{"metrics", "observed", rows[0].DataStatus})
	}
	if err == nil && len(rows) > 0 && q.MetricSource != "tempo_spanmetrics" {
		// Prometheus sample freshness at the selected end, not request activity.
		fresh, freshErr := s.instant(ctx, "max(timestamp("+q.counter()+q.metricSelector()+"))", q.End)
		freshStatus := "not_observed"
		if freshErr != nil {
			freshStatus = "unavailable"
		} else if len(fresh) > 0 {
			out.LastMetricTimestamp, freshErr = sampleValue(fresh[0].Value)
			if freshErr != nil {
				freshStatus = "unavailable"
			} else if out.LastMetricTimestamp != nil {
				freshStatus = "observed"
			}
		}
		out.Checks = append(out.Checks, Check{"metric_freshness", freshStatus, "prometheus_sample_at_window_end"})
		// Keep instance discovery independent of trace sampling. Scope is still
		// the exact service/environment/namespace and selected metric schema.
		expr := q.aggregate("count_over_time", q.counter(), q.End.Sub(q.Start), "service_instance_id,device_id,cluster_id,k8s_pod_name,service_version")
		series, instanceErr := s.instant(ctx, expr, q.End)
		if instanceErr != nil {
			out.Checks = append(out.Checks, Check{"instance_metrics", "unavailable", "query_failed"})
		} else {
			missingID := false
			for _, sample := range series {
				labels := sample.Metric
				missingID = missingID || labels["service_instance_id"] == ""
				addInstance(Instance{labels["service_instance_id"], labels["device_id"], labels["cluster_id"], labels["k8s_pod_name"], labels["service_version"]})
			}
			status := "observed"
			if len(out.Instances) == 0 {
				status = "incomplete"
			}
			out.Checks = append(out.Checks, Check{"instance_metrics", status, strconv.Itoa(len(out.Instances))})
			idStatus := "observed"
			if len(series) == 0 {
				idStatus = "not_observed"
			} else if missingID {
				idStatus = "incomplete"
			}
			out.Checks = append(out.Checks, Check{"instance_identity", idStatus, "service_instance_id"})
			// Historical placement changes can be legitimate rollouts. Flag reuse
			// for investigation without claiming simultaneous duplicate processes.
			locations := map[string]string{}
			reused := false
			for _, instance := range out.Instances {
				if instance.InstanceID == "" {
					continue
				}
				location := strings.Join([]string{instance.DeviceID, instance.ClusterID, instance.Pod}, "\x00")
				if previous, ok := locations[instance.InstanceID]; ok && previous != location {
					reused = true
				}
				locations[instance.InstanceID] = location
			}
			if reused {
				out.Checks = append(out.Checks, Check{"instance_reuse", "unknown", "multiple_locations_in_window"})
			}
		}
	}
	out.Checks = append(out.Checks, Check{"sampling", "unknown", "upstream_sampling_unknown"})
	if s.traces == nil {
		out.Checks = append(out.Checks, Check{"traces", "unavailable", "backend_disabled"})
		return out, nil
	}
	result, err := s.traces.SearchTraces(ctx, tracequery.SearchOptions{Query: TraceQL(q), Start: q.Start, End: q.End, Limit: 3})
	if err != nil || result == nil {
		out.Checks = append(out.Checks, Check{"traces", "unavailable", "query_failed"})
		return out, nil
	}
	var summaries []struct {
		TraceID string `json:"traceID"`
	}
	if err := json.Unmarshal(result.Traces, &summaries); err != nil {
		out.Checks = append(out.Checks, Check{"traces", "unavailable", "invalid_response"})
		return out, nil
	}
	if len(summaries) == 0 {
		out.Checks = append(out.Checks, Check{"traces", "not_observed", "no_traces"})
		return out, nil
	}
	var serviceSpans, downstream, missingParents int
	for _, summary := range summaries[:min(3, len(summaries))] {
		traceID := canonicalTraceID(summary.TraceID)
		if traceID == "" {
			continue
		}
		trace, err := s.traces.GetTrace(ctx, traceID)
		if err != nil || trace == nil {
			continue
		}
		resources, err := decodeTrace(trace.Body)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, resource := range resources {
			for _, span := range resource.spans() {
				seen[span.SpanID] = true
			}
		}
		matched := false
		for _, resource := range resources {
			attrs := resource.attributes()
			if scope := q.resourceScope; scope != nil {
				if scope.ClusterID != "" && attrs["cluster_id"] != scope.ClusterID {
					continue
				}
				if scope.ClusterID == "" && !slices.Contains(scope.DeviceIDs, attrs["device_id"]) {
					continue
				}
			}
			if (q.DeviceID != "" && attrs["device_id"] != q.DeviceID) || (q.ClusterID != "" && attrs["cluster_id"] != q.ClusterID) {
				continue
			}
			if (q.ServiceVersion != "" && attrs["service.version"] != q.ServiceVersion) || (q.InstanceID != "" && attrs["service.instance.id"] != q.InstanceID) {
				continue
			}
			if attrs["service.name"] != q.ServiceName || attrs["service.namespace"] != *q.ServiceNamespace || attrs["deployment.environment.name"] != *q.Environment {
				continue
			}
			matched = true
			for _, span := range resource.spans() {
				serviceSpans++
				if string(span.Kind) == "3" || string(span.Kind) == `"SPAN_KIND_CLIENT"` || string(span.Kind) == "4" || string(span.Kind) == `"SPAN_KIND_PRODUCER"` {
					downstream++
				}
				if span.ParentSpanID != "" && strings.Trim(span.ParentSpanID, "0") != "" && !seen[span.ParentSpanID] {
					missingParents++
				}
			}
			instance := Instance{attrs["service.instance.id"], attrs["device_id"], attrs["cluster_id"], attrs["k8s.pod.name"], attrs["service.version"]}
			addInstance(instance)
		}
		if matched {
			out.TraceIDs = append(out.TraceIDs, traceID)
			out.SampledTraces++
		}
	}
	if out.SampledTraces == 0 {
		out.Checks = append(out.Checks, Check{"traces", "unavailable", "trace_details_unavailable"})
		return out, nil
	}
	out.Checks = append(out.Checks, Check{"traces", "observed", strconv.Itoa(out.SampledTraces)})
	status := "not_observed"
	if downstream > 0 {
		status = "observed"
	}
	out.Checks = append(out.Checks, Check{"downstream", status, strconv.Itoa(downstream)})
	status = "observed"
	if missingParents > 0 || serviceSpans == 0 {
		status = "incomplete"
	}
	out.Checks = append(out.Checks, Check{"context", status, strconv.Itoa(missingParents)})
	if s.logs == nil {
		out.Checks = append(out.Checks, Check{"logs", "unavailable", "backend_disabled"})
	} else {
		// Exact trace-ID lookup goes through the currently selected backend.
		// A miss is only a sample observation, not proof of broken logging.
		filters := []logquery.FieldFilter{{Field: "trace_id", Operator: logquery.FilterEqual, Values: []string{out.TraceIDs[0]}}, {Field: "service_namespace", Operator: logquery.FilterEqual, Values: []string{*q.ServiceNamespace}}, {Field: "environment", Operator: logquery.FilterEqual, Values: []string{*q.Environment}}}
		for _, field := range []struct{ key, value string }{{"service_version", q.ServiceVersion}, {"instance_id", q.InstanceID}, {"device_id", q.DeviceID}, {"cluster_id", q.ClusterID}} {
			if field.value != "" {
				filters = append(filters, logquery.FieldFilter{Field: field.key, Operator: logquery.FilterEqual, Values: []string{field.value}})
			}
		}
		if scope := q.resourceScope; scope != nil {
			if scope.ClusterID != "" {
				filters = append(filters, logquery.FieldFilter{Field: "cluster_id", Operator: logquery.FilterEqual, Values: []string{scope.ClusterID}})
			} else {
				filters = append(filters, logquery.FieldFilter{Field: "device_id", Operator: logquery.FilterIn, Values: scope.DeviceIDs})
			}
		}
		n, err := s.logs.Count(ctx, logquery.SearchRequest{
			Start: q.Start, End: q.End,
			Scope:   logquery.Scope{ServiceNames: []string{q.ServiceName}},
			Filters: filters,
		})
		status := "not_observed"
		if err != nil {
			status = "unavailable"
		} else if n > 0 {
			status = "observed"
		}
		out.Checks = append(out.Checks, Check{"logs", status, "sample_trace_logs"})
	}
	return out, nil
}

// Tempo search omits leading zeroes; logs store the canonical 128-bit ID.
func canonicalTraceID(id string) string {
	if len(id) < 1 || len(id) > 32 || strings.Trim(id, "0") == "" {
		return ""
	}
	padded := strings.Repeat("0", 32-len(id)) + id
	if _, err := hex.DecodeString(padded); err != nil {
		return ""
	}
	return strings.ToLower(padded)
}

type traceSpan struct {
	Status struct {
		Code json.RawMessage `json:"code"`
	} `json:"status"`
	SpanID       string           `json:"spanId"`
	ParentSpanID string           `json:"parentSpanId"`
	Kind         json.RawMessage  `json:"kind"`
	Name         string           `json:"name"`
	StartTime    string           `json:"startTimeUnixNano"`
	Attributes   []errorAttribute `json:"attributes"`
	Events       []struct {
		Name       string           `json:"name"`
		Attributes []errorAttribute `json:"attributes"`
	} `json:"events"`
}

type scopeSpans struct {
	Spans []traceSpan `json:"spans"`
}

type traceResource struct {
	Resource struct {
		Attributes []struct {
			Key   string `json:"key"`
			Value struct {
				StringValue string `json:"stringValue"`
			} `json:"value"`
		} `json:"attributes"`
	} `json:"resource"`
	ScopeSpans                  []scopeSpans `json:"scopeSpans"`
	InstrumentationLibrarySpans []scopeSpans `json:"instrumentationLibrarySpans"`
}

func (r traceResource) spans() []traceSpan {
	spans := []traceSpan{}
	for _, scope := range append(r.ScopeSpans, r.InstrumentationLibrarySpans...) {
		spans = append(spans, scope.Spans...)
	}
	return spans
}

func (r traceResource) attributes() map[string]string {
	attrs := map[string]string{}
	for _, attr := range r.Resource.Attributes {
		attrs[attr.Key] = attr.Value.StringValue
	}
	return attrs
}

func decodeTrace(body []byte) ([]traceResource, error) {
	var trace struct {
		Batches       []traceResource `json:"batches"`
		ResourceSpans []traceResource `json:"resourceSpans"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		return nil, fmt.Errorf("apm: decode trace resources: %w", err)
	}
	resources := append(trace.Batches, trace.ResourceSpans...)
	if len(resources) > 1000 {
		return nil, fmt.Errorf("apm: trace exceeds resource limit")
	}
	return resources, nil
}
