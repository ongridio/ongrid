package apm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Query one schema at a time. Combining a SDK's dual-exported old/new metrics
// counts requests twice and mixing milliseconds/seconds corrupts quantiles.
func (q Query) histogram() string {
	if q.MetricSource == "tempo_spanmetrics" {
		return "traces_spanmetrics_latency"
	}
	if q.MetricFormat == "legacy" {
		return q.Protocol + "_server_duration_milliseconds"
	}
	if q.Protocol == "rpc" {
		return "rpc_server_call_duration_seconds"
	}
	return "http_server_request_duration_seconds"
}
func (q Query) counter() string {
	if q.MetricSource == "tempo_spanmetrics" {
		return "traces_spanmetrics_calls_total"
	}
	return q.histogram() + "_count"
}
func (q Query) latencyMultiplier() float64 {
	if q.MetricSource != "tempo_spanmetrics" && q.MetricFormat == "legacy" {
		return 1
	}
	return 1000
}
func (q Query) metricSelector(extra ...string) string {
	if q.MetricSource == "tempo_spanmetrics" {
		return q.selector(extra...)
	}
	labels := []string{`service_name!=""`}
	if q.ServiceName != "" {
		labels = append(labels, "service_name="+strconv.Quote(q.ServiceName))
	}
	if q.Environment != nil {
		labels = append(labels, "deployment_environment_name="+strconv.Quote(*q.Environment))
	}
	if q.ServiceNamespace != nil {
		labels = append(labels, "service_namespace="+strconv.Quote(*q.ServiceNamespace))
	}
	if q.Protocol == "rpc" && q.MetricFormat == "legacy" {
		labels = append(labels, `rpc_system="grpc"`)
	}
	if q.Operation != "" {
		if q.Protocol == "http" {
			method, route, _ := strings.Cut(q.Operation, " ")
			key := "http_request_method"
			if q.MetricFormat == "legacy" {
				key = "http_method"
			}
			labels = append(labels, key+"="+strconv.Quote(method), "http_route="+strconv.Quote(route))
		} else if q.MetricFormat == "legacy" {
			service, method, _ := strings.Cut(q.Operation, "/")
			labels = append(labels, "rpc_service="+strconv.Quote(service), "rpc_method=~"+strconv.Quote(regexp.QuoteMeta(method)+"|"+regexp.QuoteMeta(q.Operation)))
		} else {
			labels = append(labels, "rpc_method="+strconv.Quote(q.Operation))
		}
	}
	return "{" + strings.Join(append(append(labels, q.instanceLabels()...), extra...), ",") + "}"
}

// Apply rate before dropping instance labels so restarts remain correct.
// Alias resource/operation labels only in query results; raw metrics stay intact.
func (q Query) aggregate(fn, metric string, window time.Duration, group string, extra ...string) string {
	selectors := []string{q.metricSelector(extra...)}
	if q.MetricSource == "tempo_spanmetrics" && q.Protocol == "http" && q.SpanKind == "server" {
		// The union deduplicates spans exporting both semantic-convention versions.
		selectors = []string{
			q.metricSelector(append(append([]string{}, extra...), `http_request_method!=""`)...),
			q.metricSelector(append(append([]string{}, extra...), `http_method!=""`)...),
		}
	}
	parts := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		parts = append(parts, fmt.Sprintf("%s(%s%s[%s])", fn, metric, selector, promDuration(window)))
	}
	return q.aggregateExpression("("+strings.Join(parts, " or ")+")", group)
}

func (q Query) aggregateExpression(expr, group string) string {
	if q.MetricSource != "tempo_spanmetrics" {
		expr = fmt.Sprintf(`label_replace(%s, "service", "$1", "service_name", "(.+)")`, expr)
		if strings.Contains(group, "span_name") {
			if q.Protocol == "http" {
				method := "http_request_method"
				if q.MetricFormat == "legacy" {
					method = "http_method"
				}
				expr = fmt.Sprintf(`label_join(%s, "span_name", " ", %q, "http_route")`, expr, method)
			} else if q.MetricFormat == "legacy" {
				expr = fmt.Sprintf(`label_join(%s, "span_name", "/", "rpc_service", "rpc_method")`, expr)
				// Some dual-export SDKs use the full method even in the legacy histogram.
				expr = fmt.Sprintf(`label_replace(%s, "span_name", "$1", "rpc_method", "(.+/.+)")`, expr)
			} else {
				expr = fmt.Sprintf(`label_replace(%s, "span_name", "$1", "rpc_method", "(.*)")`, expr)
			}
		}
	}
	return fmt.Sprintf("sum by (%s) (%s)", group, expr)
}

// Union raw series before aggregation: SDK versions use numeric gRPC status,
// canonical RPC status, or error.type; a series matching both is counted once.
func (q Query) errorRate(window time.Duration, group string) string {
	if q.MetricSource == "tempo_spanmetrics" {
		return requestRate(q, window, group, `status_code="STATUS_CODE_ERROR"`)
	}
	selectors := []string{q.metricSelector(`error_type!=""`)}
	if q.Protocol == "http" {
		status := "http_response_status_code"
		if q.MetricFormat == "legacy" {
			status = "http_status_code"
		}
		selectors = append(selectors, q.metricSelector(status+`=~"5.."`))
	} else {
		system := `rpc_system_name="grpc"`
		if q.MetricFormat == "legacy" {
			system = `rpc_system="grpc"`
			selectors = append(selectors, q.metricSelector(`rpc_grpc_status_code=~"[1-9]|1[0-6]"`))
		}
		selectors = append(selectors, q.metricSelector(system, `rpc_response_status_code!=""`, `rpc_response_status_code!="OK"`))
	}
	parts := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		parts = append(parts, fmt.Sprintf("rate(%s%s[%s])", q.counter(), selector, promDuration(window)))
	}
	return q.aggregateExpression("("+strings.Join(parts, " or ")+")", group)
}
