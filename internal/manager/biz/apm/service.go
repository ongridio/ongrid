package apm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type PromQuerier interface {
	Query(context.Context, string, time.Time) (*promquery.InstantResult, error)
	QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (*promquery.InstantResult, error)
}

type TraceQuerier interface {
	SearchTraces(context.Context, tracequery.SearchOptions) (*tracequery.SearchResult, error)
	GetTrace(context.Context, string) (*tracequery.TraceResult, error)
}

type LogCounter interface {
	Count(context.Context, logquery.SearchRequest) (uint64, error)
}

type Service struct {
	clusters ClusterScopeResolver
	source   SourceRevisionResolver
	prom     PromQuerier
	traces   TraceQuerier
	logs     LogCounter
	bindings BindingSettings
	repos    BindingRepositories

	errorSnapshots errorSnapshots
	errorReads     chan struct{}
}

func New(prom PromQuerier, traces TraceQuerier, logs LogCounter) *Service {
	return &Service{prom: prom, traces: traces, logs: logs, errorReads: make(chan struct{}, 16)}
}

func (s *Service) List(ctx context.Context, q Query, operations bool) (*ListResult, error) {
	if err := s.validateQuery(ctx, &q, operations); err != nil {
		return nil, err
	}
	rows, err := s.requestSummaries(ctx, &q, operations)
	if err != nil {
		return nil, err
	}
	if !operations && q.Protocol == "all" && q.MetricSource == "application_metrics" {
		rows, err = s.appendTraceServices(ctx, q, rows)
		if err != nil {
			return nil, err
		}
	}
	envs, namespaces := map[string]bool{}, map[string]bool{}
	filtered := make([]Summary, 0, len(rows))
	for _, row := range rows {
		envs[row.Identity.Environment], namespaces[row.Identity.ServiceNamespace] = true, true
		name := row.Identity.ServiceName
		if operations {
			name = row.Operation
		}
		if strings.Contains(strings.ToLower(name), strings.ToLower(q.Search)) {
			filtered = append(filtered, row)
		}
	}
	meta := metadata(q)
	if q.MetricSource == "application_metrics" && slices.ContainsFunc(rows, func(row Summary) bool { return row.MetricSource == "tempo_spanmetrics" }) {
		meta.MetricSource, meta.Sampling = "mixed", "varies_by_service"
	}
	return &ListResult{
		Items: pageRows(sortedSummaries(filtered, q), q), Total: len(filtered), Page: q.Page, PageSize: q.PageSize,
		Metadata: meta, Environments: sortedKeys(envs), ServiceNamespaces: sortedKeys(namespaces),
	}, nil
}

// Prefer native request metrics. Only HTTP falls back; sampled and full request
// populations are never summed, and diagnostics still inspect native metrics.
func (s *Service) requestSummaries(ctx context.Context, q *Query, operations bool) ([]Summary, error) {
	rows, err := s.summaries(ctx, *q, operations)
	if err != nil || len(rows) > 0 || q.Protocol != "http" || q.MetricSource != "application_metrics" {
		return rows, err
	}
	q.MetricSource = "tempo_spanmetrics"
	return s.summaries(ctx, *q, operations)
}

// Discover server spans, then fill trace-only HTTP services with labelled samples.
// The additional query is bounded and independent of the service count.
func (s *Service) appendTraceServices(ctx context.Context, q Query, rows []Summary) ([]Summary, error) {
	expr := fmt.Sprintf("sum by (%s,telemetry_sdk_language) (count_over_time(traces_spanmetrics_calls_total%s[%s]))", identityLabels, q.selector(), promDuration(q.End.Sub(q.Start)))
	series, err := s.instant(ctx, expr, q.End)
	if err != nil {
		return nil, err
	}
	indices := make(map[Identity]int, len(rows))
	for i := range rows {
		indices[rows[i].Identity] = i
	}
	for _, item := range series {
		value, err := sampleValue(item.Value)
		if err != nil {
			return nil, err
		}
		if value == nil || *value <= 0 {
			continue
		}
		id := identityFromLabels(item.Metric)
		i, exists := indices[id]
		if !exists {
			if len(rows) >= 5000 {
				return nil, fmt.Errorf("%w: narrow the APM service scope", errs.ErrBudgetExceeded)
			}
			i = len(rows)
			indices[id] = i
			rows = append(rows, Summary{Identity: id, DataStatus: "traces_only", MetricSource: "tempo_spanmetrics"})
		}
		if language := item.Metric["telemetry_sdk_language"]; language != "" {
			rows[i].Languages = append(rows[i].Languages, language)
		}
	}
	if slices.ContainsFunc(rows, func(row Summary) bool { return row.DataStatus == "traces_only" }) {
		sampled := q
		sampled.MetricSource, sampled.Protocol = "tempo_spanmetrics", "http"
		httpRows, err := s.summaries(ctx, sampled, false)
		if err != nil {
			return nil, err
		}
		for _, row := range httpRows {
			if i, ok := indices[row.Identity]; ok && rows[i].DataStatus == "traces_only" {
				row.Languages = append(row.Languages, rows[i].Languages...)
				rows[i] = row
			}
		}
	}
	for i := range rows {
		sort.Strings(rows[i].Languages)
		rows[i].Languages = slices.Compact(rows[i].Languages)
	}
	return rows, nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Service) summaries(ctx context.Context, q Query, operations bool) ([]Summary, error) {
	if q.Protocol == "all" {
		return s.protocolSummaries(ctx, q)
	}
	group := identityLabels
	if operations {
		group += ",span_name"
	}
	window := q.End.Sub(q.Start)
	exprs := metricExpressions(q, window, group)
	// Retain identities with a single sample even when rate() cannot be
	// calculated yet. They are "insufficient_samples", never a healthy zero.
	// Retain SDK language only for discovery; RED still aggregates all languages.
	exprs["present"] = q.aggregate("count_over_time", q.counter(), window, group+",telemetry_sdk_language")
	series, err := s.instant(ctx, combineExpressions(exprs), q.End)
	if err != nil {
		return nil, err
	}
	type key struct {
		Identity
		Operation string
	}
	rows := map[key]*Summary{}
	for _, item := range series {
		id := identityFromLabels(item.Metric)
		k := key{id, item.Metric["span_name"]}
		if rows[k] == nil {
			rows[k] = &Summary{Identity: id, Operation: k.Operation}
		}
		value, err := sampleValue(item.Value)
		if err != nil {
			return nil, err
		}
		rows[k].set(item.Metric["apm_stat"], value)
		if language := item.Metric["telemetry_sdk_language"]; item.Metric["apm_stat"] == "present" && language != "" {
			rows[k].Languages = append(rows[k].Languages, language)
		}
	}
	if len(rows) > 5000 {
		return nil, fmt.Errorf("%w: narrow the APM service or operation scope", errs.ErrBudgetExceeded)
	}
	out := make([]Summary, 0, len(rows))
	for _, row := range rows {
		row.finish(window)
		row.MetricSource = q.MetricSource
		out = append(out, *row)
	}
	return out, nil
}

// Keep histogram populations separate: HTTP and RPC can use different bucket
// boundaries. Only additive request rates and request-weighted errors combine.
func (s *Service) protocolSummaries(ctx context.Context, q Query) ([]Summary, error) {
	grouped := map[Identity]*Summary{}
	for _, protocol := range []string{"http", "rpc"} {
		part := q
		part.Protocol = protocol
		rows, err := s.summaries(ctx, part, false)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if grouped[row.Identity] == nil {
				grouped[row.Identity] = &Summary{Identity: row.Identity, MetricSource: "application_metrics"}
			}
			service := grouped[row.Identity]
			service.Languages = append(service.Languages, row.Languages...)
			service.Protocols = append(service.Protocols, ProtocolMetrics{
				Protocol: protocol, RPS: row.RPS, ErrorRate: row.ErrorRate,
				P95Ms: row.P95Ms, DataStatus: row.DataStatus,
			})
		}
	}
	if len(grouped) > 5000 {
		return nil, fmt.Errorf("%w: narrow the APM service scope", errs.ErrBudgetExceeded)
	}
	out := make([]Summary, 0, len(grouped))
	for _, row := range grouped {
		rate, errors := 0.0, 0.0
		completeRate, completeErrors := true, true
		for _, protocol := range row.Protocols {
			if protocol.RPS == nil {
				completeRate = false
				continue
			}
			rate += *protocol.RPS
			if *protocol.RPS > 0 {
				if protocol.ErrorRate == nil {
					completeErrors = false
				} else {
					errors += *protocol.RPS * *protocol.ErrorRate / 100
				}
			}
		}
		if completeRate {
			row.RPS = &rate
			if rate > 0 && completeErrors {
				ratio := 100 * errors / rate
				row.ErrorRate = &ratio
			}
		}
		row.finish(q.End.Sub(q.Start))
		out = append(out, *row)
	}
	return out, nil
}

type Point struct {
	Timestamp float64  `json:"timestamp"`
	RPS       *float64 `json:"rps"`
	ErrorRate *float64 `json:"error_rate"`
	P50Ms     *float64 `json:"p50_ms"`
	P95Ms     *float64 `json:"p95_ms"`
	P99Ms     *float64 `json:"p99_ms"`
}

type Overview struct {
	Summary  Summary  `json:"summary"`
	Points   []Point  `json:"points"`
	Metadata Metadata `json:"metadata"`
}

func (s *Service) Summary(ctx context.Context, q Query) (*Overview, error) {
	return s.overviewSummary(ctx, &q)
}

func (s *Service) overviewSummary(ctx context.Context, q *Query) (*Overview, error) {
	if err := s.validateQuery(ctx, q, true); err != nil {
		return nil, err
	}
	rows, err := s.requestSummaries(ctx, q, false)
	if err != nil {
		return nil, err
	}
	out := &Overview{Summary: Summary{Identity: q.Identity(), DataStatus: "no_data"}, Points: []Point{}, Metadata: metadata(*q)}
	if len(rows) == 0 {
		return out, nil
	}
	out.Summary = rows[0]
	return out, nil
}

func (s *Service) Overview(ctx context.Context, q Query) (*Overview, error) {
	out, err := s.overviewSummary(ctx, &q)
	if err != nil || out.Summary.DataStatus == "no_data" {
		return out, err
	}
	step := max(30*time.Second, time.Duration((q.End.Sub(q.Start).Seconds()+239)/240)*time.Second)
	window := max(5*time.Minute, 4*step)
	result, err := s.prom.QueryRange(ctx, combineExpressions(metricExpressions(q, window, identityLabels)), q.Start, q.End, step)
	if err != nil {
		return nil, fmt.Errorf("apm: query trend: %w", err)
	}
	series, err := decodeSeries(result, "matrix")
	if err != nil {
		return nil, err
	}
	points := map[float64]*Point{}
	for _, item := range series {
		if len(item.Values) > 1000 {
			return nil, fmt.Errorf("apm: trend exceeded sample limit")
		}
		for _, pair := range item.Values {
			value, err := sampleValue(pair)
			if err != nil {
				return nil, err
			}
			var ts float64
			if err := json.Unmarshal(pair[0], &ts); err != nil {
				return nil, fmt.Errorf("apm: decode sample time: %w", err)
			}
			if points[ts] == nil {
				points[ts] = &Point{Timestamp: ts}
			}
			p := points[ts]
			switch item.Metric["apm_stat"] {
			case "rps":
				p.RPS = value
			case "error_rate":
				p.ErrorRate = value
			case "p50_ms":
				p.P50Ms = value
			case "p95_ms":
				p.P95Ms = value
			case "p99_ms":
				p.P99Ms = value
			}
		}
	}
	for _, p := range points {
		if p.RPS == nil || *p.RPS == 0 {
			p.ErrorRate, p.P50Ms, p.P95Ms, p.P99Ms = nil, nil, nil, nil
		}
		out.Points = append(out.Points, *p)
	}
	sort.Slice(out.Points, func(i, j int) bool { return out.Points[i].Timestamp < out.Points[j].Timestamp })
	return out, nil
}

func (s *Service) instant(ctx context.Context, expr string, at time.Time) ([]promSeries, error) {
	if s.prom == nil {
		return nil, fmt.Errorf("%w: prometheus disabled", errs.ErrNotWiredYet)
	}
	result, err := s.prom.Query(ctx, expr, at)
	if err != nil {
		return nil, fmt.Errorf("apm: query metrics: %w", err)
	}
	return decodeSeries(result, "vector")
}

type Dependency struct {
	Client         Identity `json:"client"`
	Server         Identity `json:"server"`
	ConnectionType string   `json:"connection_type"`
	RPS            *float64 `json:"rps"`
	ErrorRate      *float64 `json:"error_rate"`
	P95Ms          *float64 `json:"p95_ms"`
}

type Dependencies struct {
	Items     []Dependency `json:"items"`
	Metadata  Metadata     `json:"metadata"`
	Truncated bool         `json:"truncated"`
}

func (s *Service) Dependencies(ctx context.Context, q Query) (*Dependencies, error) {
	if err := s.validateQuery(ctx, &q, q.ServiceName != ""); err != nil {
		return nil, err
	}
	if q.ServiceVersion != "" || q.InstanceID != "" || q.DeviceID != "" || q.ClusterID != "" || q.ClusterNodeID != 0 {
		return nil, fmt.Errorf("%w: dependency graphs are service-wide; clear version, instance, device and cluster filters", errs.ErrInvalid)
	}

	group := "client,server,client_service_namespace,server_service_namespace,client_deployment_environment_name,server_deployment_environment_name,connection_type"
	rate := func(metric string, histogram bool) string {
		parts := []string{}
		for _, side := range []string{"client", "server"} {
			filters := []string{`client!=""`, `server!=""`}
			if q.ServiceName != "" {
				filters = append(filters, fmt.Sprintf(`%s=%q`, side, q.ServiceName))
			}
			if q.ServiceNamespace != nil {
				filters = append(filters, fmt.Sprintf(`%s_service_namespace=%q`, side, *q.ServiceNamespace))
			}
			if q.Environment != nil {
				filters = append(filters, fmt.Sprintf(`%s_deployment_environment_name=%q`, side, *q.Environment))
			}
			selector := "{" + strings.Join(filters, ",") + "}"
			labels := group
			if histogram {
				labels += ",le"
			}
			parts = append(parts, fmt.Sprintf("sum by (%s) (rate(%s%s[%s]))", labels, metric, selector, promDuration(q.End.Sub(q.Start))))
		}
		// Self-edges matching both sides are deduplicated by PromQL's or.
		return "(" + strings.Join(parts, " or ") + ")"
	}
	calls := rate("traces_service_graph_request_total", false)
	errors := rate("traces_service_graph_request_failed_total", false)
	expr := combineExpressions(map[string]string{
		"rps":        calls,
		"error_rate": fmt.Sprintf("100 * ((%s or on (%s) (0 * %s)) / (%s > 0))", errors, group, calls, calls),
		"p95_ms":     "1000 * histogram_quantile(0.95, " + rate("traces_service_graph_request_client_seconds_bucket", true) + ")",
	})
	series, err := s.instant(ctx, expr, q.End)
	if err != nil {
		return nil, err
	}
	type key struct {
		client, server Identity
		kind           string
	}
	rows := map[key]*Dependency{}
	for _, item := range series {
		l := item.Metric
		k := key{Identity{l["client"], l["client_service_namespace"], l["client_deployment_environment_name"]}, Identity{l["server"], l["server_service_namespace"], l["server_deployment_environment_name"]}, l["connection_type"]}
		// Tempo uses user for an unidentified caller, not an observed service.
		if k.kind == "virtual_node" && k.client == (Identity{ServiceName: "user"}) {
			continue
		}
		if rows[k] == nil {
			rows[k] = &Dependency{Client: k.client, Server: k.server, ConnectionType: k.kind}
		}
		value, err := sampleValue(item.Value)
		if err != nil {
			return nil, err
		}
		switch l["apm_stat"] {
		case "rps":
			rows[k].RPS = value
		case "error_rate":
			rows[k].ErrorRate = value
		case "p95_ms":
			rows[k].P95Ms = value
		}
	}
	out := &Dependencies{Items: []Dependency{}, Metadata: metadata(q), Truncated: len(rows) > 200}
	out.Metadata.MetricSource, out.Metadata.Sampling = "tempo_service_graphs", "unknown"
	for _, row := range rows {
		out.Items = append(out.Items, *row)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.RPS != nil && b.RPS != nil && *a.RPS != *b.RPS {
			return *a.RPS > *b.RPS
		}
		return fmt.Sprint(a.Client, a.Server, a.ConnectionType) < fmt.Sprint(b.Client, b.Server, b.ConnectionType)
	})
	out.Items = out.Items[:min(len(out.Items), 200)]
	return out, nil
}

type AlertTemplate struct {
	Expr        string `json:"expr"`
	Metric      string `json:"metric"`
	RunbookPath string `json:"runbook_path"`
}

func (s *Service) AlertTemplate(ctx context.Context, q Query, metric string, threshold, minRequests float64, forSeconds int) (*AlertTemplate, error) {
	if err := s.validateQuery(ctx, &q, true); err != nil {
		return nil, err
	}
	return BuildAlertTemplate(q, metric, threshold, minRequests, forSeconds)
}

func BuildAlertTemplate(q Query, metric string, threshold, minRequests float64, forSeconds int) (*AlertTemplate, error) {
	if q.ClusterNodeID != 0 && q.resourceScope == nil {
		return nil, fmt.Errorf("%w: resolve the cluster scope before building an alert", errs.ErrInvalid)
	}
	if err := q.Validate(true); err != nil {
		return nil, err
	}
	if q.MetricSource != "application_metrics" {
		return nil, fmt.Errorf("%w: request alerts require application metrics; trace samples are not a full request population", errs.ErrInvalid)
	}
	if (metric != "error_rate" && metric != "p95_ms") || !(threshold > 0 && threshold <= 3_600_000) || !(minRequests >= 1 && minRequests <= 1e9) || forSeconds < 0 || forSeconds > 1800 || forSeconds%30 != 0 {
		return nil, fmt.Errorf("%w: invalid APM alert metric, threshold, minimum requests or duration", errs.ErrInvalid)
	}
	if metric == "error_rate" && threshold > 100 {
		return nil, fmt.Errorf("%w: error rate must not exceed 100", errs.ErrInvalid)
	}
	window := 5 * time.Minute
	exprs := metricExpressions(q, window, identityLabels)
	predicate := fmt.Sprintf("(%s > %s) and on (%s) ((%s * 300) >= %s)", exprs[metric], strconv.FormatFloat(threshold, 'f', -1, 64), identityLabels, exprs["rps"], strconv.FormatFloat(minRequests, 'f', -1, 64))
	if forSeconds > 0 {
		// Count the predicate at each 30s evaluation, so a missing/healthy
		// evaluation resets the dwell window instead of min_over_time
		// silently ignoring absent (false) samples. Count a constant subquery
		// for coverage: Prometheus versions differ in left-boundary inclusion.
		predicate = fmt.Sprintf("(%s) and on (%s) (count_over_time((%s)[%ds:30s]) == scalar(count_over_time((vector(1))[%ds:30s])))", predicate, identityLabels, predicate, forSeconds, forSeconds)
	}
	return &AlertTemplate{Expr: predicate, Metric: metric, RunbookPath: "docs/runbooks/apm-service-performance.md"}, nil
}
