package apm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	Checks        []Check    `json:"checks"`
	Instances     []Instance `json:"instances"`
	TraceIDs      []string   `json:"trace_ids"`
	SampledTraces int        `json:"sampled_traces"`
	Metadata      Metadata   `json:"metadata"`
}

func (s *Service) Diagnostics(ctx context.Context, q Query) (*Diagnostics, error) {
	if err := q.Validate(true); err != nil {
		return nil, err
	}
	out := &Diagnostics{Checks: []Check{}, Instances: []Instance{}, TraceIDs: []string{}, Metadata: metadata(q)}
	rows, err := s.summaries(ctx, q, false)
	switch {
	case err != nil:
		out.Checks = append(out.Checks, Check{"metrics", "unavailable", "query_failed"})
	case len(rows) == 0:
		out.Checks = append(out.Checks, Check{"metrics", "not_observed", "no_metrics"})
	default:
		out.Checks = append(out.Checks, Check{"metrics", "observed", rows[0].DataStatus})
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
	instances := map[Instance]bool{}
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
			if instance.InstanceID != "" || instance.DeviceID != "" || instance.Pod != "" {
				instances[instance] = true
			}
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
	identityStatus := "observed"
	if *q.Environment == "" || *q.ServiceNamespace == "" || strings.HasPrefix(q.ServiceName, "unknown_service") {
		identityStatus = "incomplete"
	}
	out.Checks = append(out.Checks, Check{"resource_identity", identityStatus, "service_identity"})
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
	for instance := range instances {
		out.Instances = append(out.Instances, instance)
	}
	sort.Slice(out.Instances, func(i, j int) bool { return fmt.Sprint(out.Instances[i]) < fmt.Sprint(out.Instances[j]) })
	if s.logs == nil {
		out.Checks = append(out.Checks, Check{"logs", "unavailable", "backend_disabled"})
	} else {
		// Exact trace-ID lookup goes through the currently selected backend.
		// A miss is only a sample observation, not proof of broken logging.
		n, err := s.logs.Count(ctx, logquery.SearchRequest{
			Start: q.Start, End: q.End,
			Scope:   logquery.Scope{ServiceNames: []string{q.ServiceName}},
			Filters: []logquery.FieldFilter{{Field: "trace_id", Operator: logquery.FilterEqual, Values: []string{out.TraceIDs[0]}}, {Field: "service_namespace", Operator: logquery.FilterEqual, Values: []string{*q.ServiceNamespace}}, {Field: "environment", Operator: logquery.FilterEqual, Values: []string{*q.Environment}}},
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
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId"`
	Kind         json.RawMessage `json:"kind"`
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

type RuntimeMetric struct {
	Name       string   `json:"name"`
	Unit       string   `json:"unit"`
	InstanceID string   `json:"instance_id"`
	Value      *float64 `json:"value"`
}

type Runtime struct {
	Items    []RuntimeMetric `json:"items"`
	Metadata Metadata        `json:"metadata"`
}

func (s *Service) Runtime(ctx context.Context, q Query) (*Runtime, error) {
	if err := q.Validate(true); err != nil {
		return nil, err
	}
	// Only already-exported gauges are queried; no host metrics are guessed
	// to belong to an application. Missing instance labels mean aggregate.
	names := "go_goroutines|go_memstats_heap_alloc_bytes|process_resident_memory_bytes|jvm_memory_used_bytes|jvm_threads_live_threads|nodejs_eventloop_lag_seconds"
	scope := fmt.Sprintf(`deployment_environment_name=%q,service_namespace=%q`, *q.Environment, *q.ServiceNamespace)
	parts := []string{}
	for _, match := range []string{fmt.Sprintf("service_name=%q", q.ServiceName), fmt.Sprintf(`service_name="",service=%q`, q.ServiceName)} {
		parts = append(parts, fmt.Sprintf(`sum by (__name__,service_instance_id,instance) (last_over_time({__name__=~%q,%s,%s}[5m]))`, names, scope, match))
	}
	series, err := s.instant(ctx, strings.Join(parts, " or "), q.End)
	if err != nil {
		return nil, err
	}
	out := &Runtime{Items: []RuntimeMetric{}, Metadata: metadata(q)}
	out.Metadata.MetricSource, out.Metadata.Sampling = "application_metrics", "not_applicable"
	for _, item := range series {
		value, err := sampleValue(item.Value)
		if err != nil {
			return nil, err
		}
		name := item.Metric["__name__"]
		unit := "count"
		if strings.HasSuffix(name, "_bytes") {
			unit = "bytes"
		} else if strings.HasSuffix(name, "_seconds") {
			unit = "seconds"
		}
		instance := item.Metric["service_instance_id"]
		if instance == "" {
			instance = item.Metric["instance"]
		}
		out.Items = append(out.Items, RuntimeMetric{name, unit, instance, value})
	}
	sort.Slice(out.Items, func(i, j int) bool {
		return out.Items[i].Name+out.Items[i].InstanceID < out.Items[j].Name+out.Items[j].InstanceID
	})
	return out, nil
}
