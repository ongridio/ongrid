// Package apm provides service-scoped views over existing telemetry backends.
package apm

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

const identityLabels = "service,service_namespace,deployment_environment_name"

type Identity struct {
	ServiceName      string `json:"service_name"`
	ServiceNamespace string `json:"service_namespace"`
	Environment      string `json:"environment"`
}

type Query struct {
	Start, End                           time.Time
	Environment                          *string
	ServiceNamespace                     *string
	ServiceName                          string
	ServiceVersion, InstanceID           string
	DeviceID, ClusterID                  string
	ClusterNodeID                        uint64
	resourceScope                        *ResourceScope
	Operation                            string
	SpanKind                             string
	Page, PageSize                       int
	Sort, Search                         string
	MetricSource, Protocol, MetricFormat string
	SnapshotID                           string
}

func (q *Query) Validate(serviceRequired bool) error {
	if len(q.SnapshotID) > 64 {
		return fmt.Errorf("%w: invalid snapshot ID", errs.ErrInvalid)
	}
	if q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) || q.End.Sub(q.Start) < time.Minute || q.End.Sub(q.Start) > 7*24*time.Hour {
		return fmt.Errorf("%w: time range must be between 1 minute and 7 days", errs.ErrInvalid)
	}
	for _, value := range []string{q.ServiceName, q.ServiceVersion, q.InstanceID, q.DeviceID, q.ClusterID, q.Operation, q.Search, deref(q.Environment), deref(q.ServiceNamespace)} {
		if len(value) > 256 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: service filters must be valid text of at most 256 bytes", errs.ErrInvalid)
		}
	}
	if serviceRequired && (q.ServiceName == "" || q.Environment == nil || q.ServiceNamespace == nil) {
		return fmt.Errorf("%w: service_name, environment and service_namespace are required (empty scope values select unset attributes)", errs.ErrInvalid)
	}
	if q.MetricSource == "" {
		q.MetricSource = "application_metrics"
	}
	if q.MetricSource != "application_metrics" && q.MetricSource != "tempo_spanmetrics" {
		return fmt.Errorf("%w: metric_source must be application_metrics or tempo_spanmetrics", errs.ErrInvalid)
	}
	if q.Protocol == "" {
		q.Protocol = "http"
	}
	if q.Protocol != "http" && q.Protocol != "rpc" && q.Protocol != "all" {
		return fmt.Errorf("%w: protocol must be http, rpc or all", errs.ErrInvalid)
	}
	if q.Protocol == "all" && (serviceRequired || q.MetricSource != "application_metrics" || q.Operation != "") {
		return fmt.Errorf("%w: all protocols is only supported by the application services list", errs.ErrInvalid)
	}
	if q.MetricFormat == "" {
		q.MetricFormat = "otel"
	}
	if q.MetricFormat != "otel" && q.MetricFormat != "legacy" {
		return fmt.Errorf("%w: metric_format must be otel or legacy", errs.ErrInvalid)
	}
	if q.MetricSource == "application_metrics" && q.SpanKind != "" && q.SpanKind != "server" {
		return fmt.Errorf("%w: application request metrics only support server requests", errs.ErrInvalid)
	}
	if q.SpanKind == "" {
		q.SpanKind = "server"
	}
	if q.SpanKind != "server" && q.SpanKind != "consumer" {
		return fmt.Errorf("%w: span_kind must be server or consumer", errs.ErrInvalid)
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 25
	}
	if q.Page < 1 || q.Page > 200 || q.PageSize < 1 || q.PageSize > 100 {
		return fmt.Errorf("%w: page must be 1..200 and page_size 1..100", errs.ErrInvalid)
	}
	if q.Sort == "" {
		q.Sort = "rps"
	}
	switch q.Sort {
	case "rps", "error_rate", "p95_ms", "name":
	default:
		return fmt.Errorf("%w: unsupported sort", errs.ErrInvalid)
	}
	return nil
}

func (q Query) Identity() Identity {
	return Identity{q.ServiceName, deref(q.ServiceNamespace), deref(q.Environment)}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (q Query) instanceLabels() []string {
	labels := []string{}
	if q.ServiceVersion != "" {
		labels = append(labels, "service_version="+strconv.Quote(q.ServiceVersion))
	}
	if q.InstanceID != "" {
		labels = append(labels, "service_instance_id="+strconv.Quote(q.InstanceID))
	}
	if q.DeviceID != "" {
		labels = append(labels, "device_id="+strconv.Quote(q.DeviceID))
	}
	if q.ClusterID != "" {
		labels = append(labels, "cluster_id="+strconv.Quote(q.ClusterID))
	}
	if q.resourceScope != nil {
		if q.resourceScope.ClusterID != "" {
			labels = append(labels, "cluster_id="+strconv.Quote(q.resourceScope.ClusterID))
		} else {
			labels = append(labels, "device_id=~"+strconv.Quote(devicePattern(q.resourceScope.DeviceIDs)))
		}
	}
	return labels
}

func (q Query) selector(extra ...string) string {
	labels := []string{`service!=""`, `span_kind="SPAN_KIND_` + strings.ToUpper(q.SpanKind) + `"`}
	if q.ServiceName != "" {
		labels = append(labels, "service="+strconv.Quote(q.ServiceName))
	}
	if q.Environment != nil {
		labels = append(labels, "deployment_environment_name="+strconv.Quote(*q.Environment))
	}
	if q.ServiceNamespace != nil {
		labels = append(labels, "service_namespace="+strconv.Quote(*q.ServiceNamespace))
	}
	if q.Operation != "" {
		labels = append(labels, "span_name="+strconv.Quote(q.Operation))
	}
	return "{" + strings.Join(append(append(labels, q.instanceLabels()...), extra...), ",") + "}"
}

func promDuration(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) + "s" }

func requestRate(q Query, window time.Duration, group string, extra ...string) string {
	return q.aggregate("rate", q.counter(), window, group, extra...)
}

// metricExpressions is shared by summaries, charts and alert templates. All
// errors follow the selected schema; null ratios represent no requests.
func metricExpressions(q Query, window time.Duration, group string) map[string]string {
	rate := requestRate(q, window, group)
	errors := q.errorRate(window, group)
	out := map[string]string{
		"rps":        rate,
		"error_rate": fmt.Sprintf("100 * ((%s or on (%s) (0 * %s)) / (%s > 0))", errors, group, rate, rate),
	}
	for key, quantile := range map[string]string{"p50_ms": "0.5", "p95_ms": "0.95", "p99_ms": "0.99"} {
		out[key] = fmt.Sprintf("%g * histogram_quantile(%s, %s)", q.latencyMultiplier(), quantile, q.aggregate("rate", q.histogram()+"_bucket", window, group+",le"))
	}
	return out
}

// A single labelled union keeps query count independent of service count.
func combineExpressions(exprs map[string]string) string {
	keys := make([]string, 0, len(exprs))
	for key := range exprs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`label_replace((%s), "apm_stat", %q, "", "")`, exprs[key], key))
	}
	return strings.Join(parts, " or ")
}

type ProtocolMetrics struct {
	Protocol   string   `json:"protocol"`
	RPS        *float64 `json:"rps"`
	ErrorRate  *float64 `json:"error_rate"`
	P95Ms      *float64 `json:"p95_ms"`
	DataStatus string   `json:"data_status"`
}

type Summary struct {
	MetricSource string            `json:"metric_source"`
	Identity     Identity          `json:"identity"`
	Operation    string            `json:"operation,omitempty"`
	RPS          *float64          `json:"rps"`
	ErrorRate    *float64          `json:"error_rate"`
	P50Ms        *float64          `json:"p50_ms"`
	P95Ms        *float64          `json:"p95_ms"`
	P99Ms        *float64          `json:"p99_ms"`
	Requests     *float64          `json:"requests"`
	DataStatus   string            `json:"data_status"`
	Protocols    []ProtocolMetrics `json:"protocols,omitempty"`
	Languages    []string          `json:"languages,omitempty"`
}

type Metadata struct {
	Start         time.Time      `json:"start"`
	End           time.Time      `json:"end"`
	MetricSource  string         `json:"metric_source"`
	Sampling      string         `json:"sampling"`
	SpanKind      string         `json:"span_kind"`
	Protocol      string         `json:"protocol"`
	MetricFormat  string         `json:"metric_format"`
	ResourceScope *ResourceScope `json:"resource_scope,omitempty"`
}

func metadata(q Query) Metadata {
	sampling := "independent_of_trace_sampling"
	if q.MetricSource == "tempo_spanmetrics" {
		sampling = "unknown"
	}
	return Metadata{q.Start, q.End, q.MetricSource, sampling, q.SpanKind, q.Protocol, q.MetricFormat, q.resourceScope}
}

type ListResult struct {
	Items             []Summary `json:"items"`
	Total             int       `json:"total"`
	Page              int       `json:"page"`
	PageSize          int       `json:"page_size"`
	Metadata          Metadata  `json:"metadata"`
	Environments      []string  `json:"environments"`
	ServiceNamespaces []string  `json:"service_namespaces"`
}

type promSeries struct {
	Metric map[string]string   `json:"metric"`
	Value  []json.RawMessage   `json:"value"`
	Values [][]json.RawMessage `json:"values"`
}

func decodeSeries(result *promquery.InstantResult, kind string) ([]promSeries, error) {
	if result == nil || result.ResultType != kind {
		return nil, fmt.Errorf("apm: expected Prometheus %s result", kind)
	}
	var series []promSeries
	if err := json.Unmarshal(result.Result, &series); err != nil {
		return nil, fmt.Errorf("apm: decode Prometheus result: %w", err)
	}
	if len(series) > 40_000 {
		return nil, fmt.Errorf("%w: narrow the APM query scope", errs.ErrBudgetExceeded)
	}
	return series, nil
}

func sampleValue(pair []json.RawMessage) (*float64, error) {
	if len(pair) != 2 {
		return nil, fmt.Errorf("apm: invalid Prometheus sample")
	}
	var raw string
	if err := json.Unmarshal(pair[1], &raw); err != nil {
		return nil, fmt.Errorf("apm: decode sample value: %w", err)
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("apm: parse sample value: %w", err)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, nil
	}
	return &n, nil
}

func identityFromLabels(labels map[string]string) Identity {
	return Identity{labels["service"], labels["service_namespace"], labels["deployment_environment_name"]}
}

func (s *Summary) set(key string, value *float64) {
	switch key {
	case "rps":
		s.RPS = value
	case "error_rate":
		s.ErrorRate = value
	case "p50_ms":
		s.P50Ms = value
	case "p95_ms":
		s.P95Ms = value
	case "p99_ms":
		s.P99Ms = value
	}
}

func (s *Summary) finish(window time.Duration) {
	slices.Sort(s.Languages)
	s.Languages = slices.Compact(s.Languages)
	s.DataStatus = "insufficient_samples"
	if s.RPS == nil {
		return
	}
	requests := *s.RPS * window.Seconds()
	s.Requests = &requests
	s.DataStatus = "observed"
	if *s.RPS == 0 {
		s.DataStatus = "no_requests"
		s.ErrorRate, s.P50Ms, s.P95Ms, s.P99Ms = nil, nil, nil, nil
	}
}

func sortedSummaries(rows []Summary, q Query) []Summary {
	sort.SliceStable(rows, func(i, j int) bool {
		if q.Sort != "name" {
			value := func(row Summary) *float64 {
				switch q.Sort {
				case "error_rate":
					return row.ErrorRate
				case "p95_ms":
					// A cross-protocol percentile cannot be reconstructed from quantiles.
					// Sort grouped services by their slowest protocol, without calling it a combined P95.
					value := row.P95Ms
					for _, protocol := range row.Protocols {
						if protocol.P95Ms != nil && (value == nil || *protocol.P95Ms > *value) {
							value = protocol.P95Ms
						}
					}
					return value
				default:
					return row.RPS
				}
			}
			a, b := value(rows[i]), value(rows[j])
			if a != nil && b == nil {
				return true
			}
			if a == nil && b != nil {
				return false
			}
			if a != nil && b != nil && *a != *b {
				return *a > *b
			}
		}
		a, b := rows[i], rows[j]
		return strings.Join([]string{a.Identity.ServiceName, a.Identity.ServiceNamespace, a.Identity.Environment, a.Operation}, "\x00") <
			strings.Join([]string{b.Identity.ServiceName, b.Identity.ServiceNamespace, b.Identity.Environment, b.Operation}, "\x00")
	})
	return rows
}

func pageRows(rows []Summary, q Query) []Summary {
	start := min((q.Page-1)*q.PageSize, len(rows))
	return rows[start:min(start+q.PageSize, len(rows))]
}

// TraceQL scopes values separately from query syntax. Missing attributes are
// explicitly included only for an empty scope, matching Prometheus selectors.
func TraceQL(q Query) string {
	clauses := []string{"resource.service.name = " + strconv.Quote(q.ServiceName)}
	for _, field := range []struct {
		name  string
		value *string
	}{{"service.namespace", q.ServiceNamespace}, {"deployment.environment.name", q.Environment}} {
		if field.value == nil {
			continue
		}
		clause := "resource." + field.name + " = " + strconv.Quote(*field.value)
		if *field.value == "" {
			clause = "(" + clause + " || resource." + field.name + " = nil)"
		}
		clauses = append(clauses, clause)
	}
	for _, field := range []struct{ name, value string }{{"service.version", q.ServiceVersion}, {"service.instance.id", q.InstanceID}, {"device_id", q.DeviceID}, {"cluster_id", q.ClusterID}} {
		if field.value != "" {
			clauses = append(clauses, "resource."+field.name+" = "+strconv.Quote(field.value))
		}
	}
	clauses = append(clauses, "kind = "+q.SpanKind)
	if q.resourceScope != nil {
		if q.resourceScope.ClusterID != "" {
			clauses = append(clauses, "resource.cluster_id = "+strconv.Quote(q.resourceScope.ClusterID))
		} else {
			clauses = append(clauses, "resource.device_id =~ "+strconv.Quote("^("+devicePattern(q.resourceScope.DeviceIDs)+")$"))
		}
	}
	if q.MetricSource == "tempo_spanmetrics" {
		if q.Protocol == "http" && q.SpanKind == "server" {
			clauses = append(clauses, `(span.http.request.method != nil || span.http.method != nil)`)
		}
		if q.Operation != "" {
			clauses = append(clauses, "name = "+strconv.Quote(q.Operation))
		}
	} else if q.Protocol == "rpc" {
		clauses = append(clauses, `(span.rpc.system.name != nil || span.rpc.system != nil)`)
		if q.Operation != "" {
			service, method, _ := strings.Cut(q.Operation, "/")
			clauses = append(clauses, "(span.rpc.method = "+strconv.Quote(q.Operation)+" || (span.rpc.service = "+strconv.Quote(service)+" && span.rpc.method = "+strconv.Quote(method)+"))")
		}
	} else {
		clauses = append(clauses, `(span.http.request.method != nil || span.http.method != nil)`)
		if q.Operation != "" {
			method, route, _ := strings.Cut(q.Operation, " ")
			clauses = append(clauses, "(span.http.request.method = "+strconv.Quote(method)+" || span.http.method = "+strconv.Quote(method)+")")
			if route != "" {
				clauses = append(clauses, "span.http.route = "+strconv.Quote(route))
			}
		}
	}
	return "{ " + strings.Join(clauses, " && ") + " }"
}
