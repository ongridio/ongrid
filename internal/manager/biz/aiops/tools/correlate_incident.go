package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

// ToolNameCorrelateIncident is the stable wire name the LLM sees for the
// composite incident-correlation tool.
const ToolNameCorrelateIncident = "correlate_incident"

// CorrelateIncidentDescription is the single-sentence description shown
// to the LLM. Phrased so the model picks this whenever it has an
// incident_id and wants to diagnose without chaining ten round-trips.
const CorrelateIncidentDescription = "Pull all signals around an incident: " +
	"the metric series that fired the rule, error logs from the same host in the same time window, " +
	"slow/error traces, and recent host state changes. " +
	"Returns one bundled JSON the LLM can reason over without further tool calls."

// correlateIncidentTimeout caps the overall tool run. Each sub-call gets
// a smaller per-step timeout below; this is the umbrella cap so the
// agent loop can't wedge on one slow upstream.
const correlateIncidentTimeout = 60 * time.Second

// correlateMaxResponseBytes caps the marshalled bundle so a misbehaving
// upstream can't blow the LLM context. The cap is enforced by trimming
// the noisiest panels (logs, then traces) before re-marshalling.
const correlateMaxResponseBytes = 100 * 1024

// AlertUsecase is the narrow surface this and the other alert-flavoured
// tools (query_incidents / query_alert_rules / get_incident_detail /
// get_host_summary / get_topology) need from the manager/alert biz layer.
// *alertbiz.Usecase satisfies it; tests inject a fake.
//
// Declared once here so registry.alertUC can stay a single field across
// the whole package — the test seam wins over a tighter per-tool
// interface, since most of these methods are read-only and trivial to
// satisfy.
type AlertUsecase interface {
	GetIncident(ctx context.Context, id uint64) (*alertmodel.Incident, error)
	ListIncidents(ctx context.Context, f alertbiz.IncidentFilter) ([]*alertmodel.Incident, error)
	ListEvents(ctx context.Context, incidentID uint64, limit int) ([]*alertmodel.Event, error)
	ListRules(ctx context.Context, scopeType string) ([]*alertmodel.Rule, error)
}

// correlateIncidentBundle is the shape we hand back to the LLM. JSON tags
// are wire-stable; field rearrangement is a breaking change for any
// downstream prompt that pattern-matches on a key.
type correlateIncidentBundle struct {
	Incident    incidentSummary   `json:"incident"`
	Window      windowRange       `json:"window"`
	MetricPanel []metricSeries    `json:"metric_panel,omitempty"`
	LogPanel    []logEntry        `json:"log_panel,omitempty"`
	TracePanel  []traceEntry      `json:"trace_panel,omitempty"`
	Host        *hostSnapshot     `json:"host,omitempty"`
	Skipped     map[string]string `json:"skipped,omitempty"`
	Truncated   map[string]int    `json:"truncated,omitempty"`
}

type incidentSummary struct {
	ID          uint64            `json:"id"`
	Title       string            `json:"title"`
	RuleKey     string            `json:"rule_key"`
	RuleName    string            `json:"rule_name"`
	Severity    string            `json:"severity"`
	Status      string            `json:"status"`
	ScopeType   string            `json:"scope_type"`
	DeviceID    *uint64           `json:"device_id,omitempty"`
	FiredAt     time.Time         `json:"first_fired_at"`
	LastFiredAt time.Time         `json:"last_fired_at"`
	EventCount  uint64            `json:"event_count"`
	Value       *float64          `json:"value,omitempty"`
	Threshold   *float64          `json:"threshold,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type windowRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type metricSeries struct {
	Labels map[string]string `json:"labels"`
	Values [][2]any          `json:"values"` // [[unix_seconds, value_string], ...]
}

type logEntry struct {
	Timestamp time.Time         `json:"ts"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type traceEntry struct {
	TraceID    string `json:"trace_id"`
	Service    string `json:"service,omitempty"`
	RootName   string `json:"root_name,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	StartTime  string `json:"start_time,omitempty"`
}

type hostSnapshot struct {
	CurrentEdgeID      *uint64      `json:"current_edge_id,omitempty"`
	ID                 uint64       `json:"id"`
	Name               string       `json:"name"`
	Status             string       `json:"status"`
	Roles              []string     `json:"roles,omitempty"`
	LastSeenAt         *time.Time   `json:"last_seen_at,omitempty"`
	CurrentLoad        *currentLoad `json:"current_load,omitempty"`
	RecentIncidents24h int          `json:"recent_incidents_24h"`
}

type currentLoad struct {
	CPUPct *float64 `json:"cpu_pct,omitempty"`
	MemPct *float64 `json:"mem_pct,omitempty"`
	Up     *float64 `json:"up,omitempty"`
}

// promExprForIncident builds a PromQL range expression from the incident's
// rule kind. Returns ("", false) when the kind has no metric source we can
// reason over (log_match, trace_*).
//
// NB: the incident row stores rule_key under .Rule but does NOT carry the
// full ConditionsJSON spec — that lives on the alert_rules row. Without
// importing the alert biz internals we approximate: when the incident's
// rule_key matches a closed-set canonical metric name (cpu_pct, mem_pct,
// …) we use that as the metric. Otherwise we fall back to the labels' or
// annotations' "metric" hint, which the rule writer can set explicitly.
func promExprForIncident(inc *alertmodel.Incident) (string, bool) {
	// Heuristic 1 — rule_key happens to match the closed-set name.
	if expr, ok := metricExprFor(inc.Rule); ok {
		return wrapPerDevice(expr, inc.DeviceID), true
	}
	// Heuristic 2 — label / annotation hint.
	if labels, _ := inc.Labels(); labels != nil {
		if name := strings.TrimSpace(labels["metric"]); name != "" {
			if expr, ok := metricExprFor(name); ok {
				return wrapPerDevice(expr, inc.DeviceID), true
			}
		}
	}
	if ann, _ := inc.Annotations(); ann != nil {
		if name := strings.TrimSpace(ann["metric"]); name != "" {
			if expr, ok := metricExprFor(name); ok {
				return wrapPerDevice(expr, inc.DeviceID), true
			}
		}
	}
	return "", false
}

// metricExprFor mirrors alert/evaluators_phaseA.metricExprFor so we don't
// have to export it from that package. Keep these two in sync — when a new
// closed-set metric is added to the host evaluator vocabulary, mirror it
// here so correlate_incident keeps showing the relevant series.
func metricExprFor(metric string) (string, bool) {
	switch metric {
	case "cpu_pct":
		return `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))`, true
	case "mem_pct":
		return `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`, true
	case "disk_used_pct":
		return `100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})`, true
	case "disk_avail_bytes":
		return `node_filesystem_avail_bytes{mountpoint="/"}`, true
	case "load1":
		return `node_load1`, true
	case "load5":
		return `node_load5`, true
	case "load15":
		return `node_load15`, true
	case "net_rx_bps":
		return `sum by (device_id) (rate(node_network_receive_bytes_total[1m]))`, true
	case "net_tx_bps":
		return `sum by (device_id) (rate(node_network_transmit_bytes_total[1m]))`, true
	}
	return "", false
}

// wrapPerDevice restricts a closed-set metric expression to a single
// device_id by intersecting it with a label-only selector on that id.
// Returns expr unchanged when deviceID is nil. The intersection form
// `(<expr>) and on(device_id) (group by (device_id) ({device_id="<id>"}))`
// works for both raw selectors and `… by (device_id) …` aggregations, so
// callers don't need to know the expression's shape.
func wrapPerDevice(expr string, deviceID *uint64) string {
	if deviceID == nil {
		return expr
	}
	return fmt.Sprintf(`(%s) and on(device_id) (group by (device_id) ({device_id="%d"}))`, expr, *deviceID)
}

func seriesMagnitude(s metricSeries) float64 {
	var max float64
	for _, p := range s.Values {
		if len(p) < 2 {
			continue
		}
		v, ok := p[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		if math.Abs(f) > max {
			max = math.Abs(f)
		}
	}
	return max
}

func logEntriesFromQueryLogQLResult(result logquery.QueryLogQLResult) ([]logEntry, error) {
	var entries []logEntry
	switch typed := result.(type) {
	case *logquery.QueryRangeResult:
		var err error
		entries, err = logEntriesFromLokiResult(typed)
		if err != nil {
			return nil, err
		}
	case *logquery.SearchResult:
		entries = logEntriesFromSearchResult(typed)
	default:
		return nil, fmt.Errorf("unsupported query_logql result type %T", result)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if len(entries) > 50 {
		entries = entries[:50]
	}
	return entries, nil
}

func logEntriesFromLokiResult(result *logquery.QueryRangeResult) ([]logEntry, error) {
	if result == nil || len(result.Result) == 0 {
		return nil, nil
	}
	if result.ResultType != "streams" {
		return nil, fmt.Errorf("query_logql returned Loki result type %q for a log stream query", result.ResultType)
	}
	// Loki streams shape: [{"stream": {labels...}, "values": [[ns, line], ...]}]
	var raw []struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	if err := json.Unmarshal(result.Result, &raw); err != nil {
		return nil, fmt.Errorf("decode loki streams: %w", err)
	}
	entries := make([]logEntry, 0, 64)
	for _, st := range raw {
		for _, v := range st.Values {
			ts := parseLokiNanoTimestamp(v[0])
			line := truncateLine(v[1], 200)
			entries = append(entries, logEntry{Timestamp: ts, Line: line, Labels: st.Stream})
		}
	}
	return entries, nil
}

func logEntriesFromSearchResult(result *logquery.SearchResult) []logEntry {
	if result == nil {
		return nil
	}
	entries := make([]logEntry, 0, len(result.Records))
	for _, record := range result.Records {
		labels := make(map[string]string, len(record.ResourceAttributes)+len(record.Attributes)+2)
		for key, value := range record.ResourceAttributes {
			labels[key] = value
		}
		for key, value := range record.Attributes {
			labels[key] = value
		}
		if record.SeverityText != "" {
			labels["level"] = strings.ToLower(record.SeverityText)
		}
		if record.Backend != "" {
			labels["backend"] = record.Backend
		}
		entries = append(entries, logEntry{
			Timestamp: record.Timestamp,
			Line:      truncateLine(record.Message, 200),
			Labels:    labels,
		})
	}
	return entries
}

func parseLokiNanoTimestamp(s string) time.Time {
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func truncateLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// singleVectorValue extracts the first sample's float value from an
// `instant vector` result. Returns (0, false) when the result is empty
// or non-vector.
func singleVectorValue(res *promquery.InstantResult) (float64, bool) {
	if res == nil || res.ResultType != "vector" {
		return 0, false
	}
	var raw []struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	if err := json.Unmarshal(res.Result, &raw); err != nil || len(raw) == 0 {
		return 0, false
	}
	s, ok := raw[0].Value[1].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// marshalBundleWithCap marshals the bundle, and if it exceeds
// correlateMaxResponseBytes, trims the noisiest panels (logs first,
// then traces) and remarshals. Records what was trimmed in
// bundle.Truncated so the LLM knows it's not seeing the full picture.
func marshalBundleWithCap(bundle *correlateIncidentBundle) ([]byte, error) {
	out, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(out) <= correlateMaxResponseBytes {
		if len(bundle.Truncated) == 0 {
			bundle.Truncated = nil
		}
		return out, nil
	}
	// Trim logs first.
	if len(bundle.LogPanel) > 10 {
		original := len(bundle.LogPanel)
		bundle.LogPanel = bundle.LogPanel[:10]
		bundle.Truncated["log_panel"] = original
	}
	out, err = json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(out) <= correlateMaxResponseBytes {
		return out, nil
	}
	// Trim traces.
	if len(bundle.TracePanel) > 5 {
		original := len(bundle.TracePanel)
		bundle.TracePanel = bundle.TracePanel[:5]
		bundle.Truncated["trace_panel"] = original
	}
	out, err = json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(out) <= correlateMaxResponseBytes {
		return out, nil
	}
	// Last resort — drop metric_panel values (keep labels only).
	if len(bundle.MetricPanel) > 0 {
		bundle.Truncated["metric_panel_values"] = 1
		for i := range bundle.MetricPanel {
			bundle.MetricPanel[i].Values = nil
		}
	}
	return json.Marshal(bundle)
}

// correlate_incident.go — N+15 batch refactor. Outer fan-out
// is added; the per-incident bundle assembly is unchanged (each inner
// already runs prom + log + trace probes concurrently inside its own
// 60s ceiling). schema cap stays at 16 to match the other batch tools,
// but the WhenToUse strongly suggests 2-4 — each inner is heavy.

// CorrelateIncidentTool is the BaseTool form of correlate_incident.
type CorrelateIncidentTool struct {
	alertUC    AlertUsecase
	promQuery  PromQuerier
	logQuery   LogQuerier
	traceQuery TraceQuerier
	edges      *edgebiz.Usecase
	devices    *devicebiz.Usecase
	log        *slog.Logger
}

// NewCorrelateIncidentTool builds the BaseTool variant.
func NewCorrelateIncidentTool(
	alertUC AlertUsecase,
	promQuery PromQuerier,
	logQuery LogQuerier,
	traceQuery TraceQuerier,
	edges *edgebiz.Usecase,
	devices *devicebiz.Usecase,
	log *slog.Logger,
) *CorrelateIncidentTool {
	if log == nil {
		log = slog.Default()
	}
	return &CorrelateIncidentTool{
		alertUC:    alertUC,
		promQuery:  promQuery,
		logQuery:   logQuery,
		traceQuery: traceQuery,
		edges:      edges,
		devices:    devices,
		log:        log,
	}
}

// CorrelateIncidentBatchArgs is the typed form of the batch schema.
type CorrelateIncidentBatchArgs struct {
	IncidentIDs   []uint64 `json:"incident_ids"`
	WindowMinutes int      `json:"window_minutes,omitempty"`
}

// CorrelateIncidentResultEntry is one slot in the batch envelope. On
// success Bundle is populated; on failure Error is populated.
type CorrelateIncidentResultEntry struct {
	IncidentID uint64                   `json:"incident_id"`
	Bundle     *correlateIncidentBundle `json:"bundle,omitempty"`
	Error      string                   `json:"error,omitempty"`
}

// CorrelateIncidentBatchResponse is the wire envelope.
type CorrelateIncidentBatchResponse struct {
	SuccessCount int                            `json:"success_count"`
	ErrorCount   int                            `json:"error_count"`
	Results      []CorrelateIncidentResultEntry `json:"results"`
}

// CorrelateIncidentBatchSchema is the JSON schema for the batched call.
// Cap stays 16 to match the other batch tools but WhenToUse strongly
// nudges 2-4 — each inner is already heavy (3 upstream probes).
var CorrelateIncidentBatchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "incident_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 1,
      "maxItems": 16,
      "description": "告警 id 列表，一次最多 16 个。**典型 2-4 个**——每个 incident 内部已经 3 路并发，给 16 个会成本爆炸。"
    },
    "window_minutes": {
      "type": "integer",
      "minimum": 1,
      "maximum": 240,
      "description": "对每个 incident 的窗口（围绕 first_fired_at），默认 30 分钟，最大 240。共享给所有 id。"
    }
  },
  "required": ["incident_ids"]
}`)

// correlateIncidentWhenToUse — batch-first routing hint (N+15).
const correlateIncidentWhenToUse = "对一组 incident_id 各跑完整 metric+log+trace+edge 关联诊断。" +
	"**典型 2-4 个一次**（每个内部已经 3 路并发）。**别一次给 16 个**——成本爆炸。" +
	"NOT for: 单纯查 incident 字段（用 get_incident_detail）/ 没 incident_id 的自由查（用 query_promql / query_logql）/ " +
	"列 incidents（用 query_incidents）。"

// Info returns metadata. Class=read.
func (t *CorrelateIncidentTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameCorrelateIncident,
		Description: CorrelateIncidentDescription,
		WhenToUse:   correlateIncidentWhenToUse,
		Parameters:  CorrelateIncidentBatchSchema,
		Class:       "read",
	}, nil
}

// singleCorrelate assembles one incident bundle; failures populate ResultEntry.Error.
func (t *CorrelateIncidentTool) singleCorrelate(ctx context.Context, incidentID uint64, window int) CorrelateIncidentResultEntry {
	entry := CorrelateIncidentResultEntry{IncidentID: incidentID}
	if incidentID == 0 {
		entry.Error = "incident_id must be > 0"
		return entry
	}

	callCtx, cancel := context.WithTimeout(ctx, correlateIncidentTimeout)
	defer cancel()

	inc, err := t.alertUC.GetIncident(callCtx, incidentID)
	if err != nil {
		entry.Error = fmt.Sprintf("get incident: %v", err)
		return entry
	}
	if inc == nil {
		entry.Error = fmt.Sprintf("incident %d not found", incidentID)
		return entry
	}

	half := time.Duration(window) * time.Minute / 2
	wStart := inc.FirstFiredAt.Add(-half)
	wEnd := inc.FirstFiredAt.Add(half)

	labels, _ := inc.Labels()
	annotations, _ := inc.Annotations()

	bundle := &correlateIncidentBundle{
		Incident: incidentSummary{
			ID:          inc.ID,
			Title:       inc.Title,
			RuleKey:     inc.Rule,
			RuleName:    inc.RuleName,
			Severity:    inc.Severity,
			Status:      inc.Status,
			ScopeType:   inc.ScopeType,
			DeviceID:    inc.DeviceID,
			FiredAt:     inc.FirstFiredAt,
			LastFiredAt: inc.LastFiredAt,
			EventCount:  inc.EventCount,
			Value:       inc.Value,
			Threshold:   inc.Threshold,
			Labels:      labels,
			Annotations: annotations,
		},
		Window:    windowRange{Start: wStart, End: wEnd},
		Skipped:   map[string]string{},
		Truncated: map[string]int{},
	}

	if t.promQuery != nil {
		if expr, ok := promExprForIncident(inc); ok {
			series, err := t.queryMetricPanel(callCtx, expr, wStart, wEnd)
			if err != nil {
				bundle.Skipped["metric_panel"] = "prom query failed: " + err.Error()
			} else {
				bundle.MetricPanel = series
			}
		} else {
			bundle.Skipped["metric_panel"] = "rule kind unsupported or expression unavailable"
		}
	} else {
		bundle.Skipped["metric_panel"] = "prom query client not configured"
	}

	if t.logQuery != nil {
		if inc.DeviceID != nil {
			entries, err := t.queryLogPanel(callCtx, *inc.DeviceID, wStart, wEnd)
			if err != nil {
				bundle.Skipped["log_panel"] = "log query failed: " + err.Error()
			} else {
				bundle.LogPanel = entries
			}
		} else {
			bundle.Skipped["log_panel"] = "incident has no device_id"
		}
	} else {
		bundle.Skipped["log_panel"] = "log query client not configured"
	}

	if t.traceQuery != nil {
		service := strings.TrimSpace(labels["service"])
		if service == "" {
			service = strings.TrimSpace(annotations["service"])
		}
		if service != "" {
			entries, err := t.queryTracePanel(callCtx, service, inc.DeviceID, wStart, wEnd)
			if err != nil {
				bundle.Skipped["trace_panel"] = "tempo query failed: " + err.Error()
			} else {
				bundle.TracePanel = entries
			}
		} else {
			bundle.Skipped["trace_panel"] = "no service label on incident"
		}
	} else {
		bundle.Skipped["trace_panel"] = "trace query client not configured"
	}

	if inc.DeviceID != nil && t.devices != nil {
		snap := t.queryHostSnapshot(callCtx, *inc.DeviceID, inc.FirstFiredAt)
		bundle.Host = snap
		if snap == nil {
			bundle.Skipped["host"] = "host could not be resolved"
		}
	} else if inc.DeviceID != nil {
		bundle.Skipped["host"] = "host service not configured"
	}

	if len(bundle.Skipped) == 0 {
		bundle.Skipped = nil
	}
	if len(bundle.Truncated) == 0 {
		bundle.Truncated = nil
	}

	entry.Bundle = bundle
	return entry
}

// InvokableRun parses, validates, fans out, marshals envelope.
func (t *CorrelateIncidentTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.alertUC == nil {
		return "", fmt.Errorf("correlate_incident: alert usecase not configured")
	}
	var in CorrelateIncidentBatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("correlate_incident: bad args: %w", err)
	}
	if err := validateBatchIDs("incident_ids", in.IncidentIDs); err != nil {
		return "", fmt.Errorf("correlate_incident: %w", err)
	}
	window := in.WindowMinutes
	if window <= 0 {
		window = 30
	}
	if window > 240 {
		window = 240
	}

	results := runBatch(ctx, in.IncidentIDs, func(ctx context.Context, id uint64) CorrelateIncidentResultEntry {
		return t.singleCorrelate(ctx, id, window)
	})
	env := CorrelateIncidentBatchResponse{Results: results}
	for _, r := range results {
		if r.Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("correlate_incident: marshal: %w", err)
	}
	return string(out), nil
}

// queryMetricPanel returns the top three metric series by magnitude.
func (t *CorrelateIncidentTool) queryMetricPanel(ctx context.Context, expr string, start, end time.Time) ([]metricSeries, error) {
	step := stepFor(int(end.Sub(start).Seconds()))
	res, err := t.promQuery.QueryRange(ctx, expr, start, end, step)
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Result) == 0 {
		return nil, nil
	}
	var raw []struct {
		Metric map[string]string `json:"metric"`
		Values [][2]any          `json:"values"`
	}
	if err := json.Unmarshal(res.Result, &raw); err != nil {
		return nil, fmt.Errorf("decode prom matrix: %w", err)
	}
	series := make([]metricSeries, 0, len(raw))
	for _, s := range raw {
		series = append(series, metricSeries{Labels: s.Metric, Values: s.Values})
	}
	sort.SliceStable(series, func(i, j int) bool {
		return seriesMagnitude(series[i]) > seriesMagnitude(series[j])
	})
	if len(series) > 3 {
		series = series[:3]
	}
	return series, nil
}

// queryLogPanel returns recent error logs for the device.
func (t *CorrelateIncidentTool) queryLogPanel(ctx context.Context, edgeID uint64, start, end time.Time) ([]logEntry, error) {
	q := fmt.Sprintf(`{device_id="%d"} |~ "(?i)error|panic|oom|fatal|fail"`, edgeID)
	res, err := t.logQuery.QueryLogQL(ctx, logquery.QueryRangeOptions{
		Query:     q,
		Start:     start,
		End:       end,
		Limit:     50,
		Direction: "backward",
	})
	if err != nil {
		return nil, err
	}
	return logEntriesFromQueryLogQLResult(res)
}

// queryTracePanel scopes shared service names to the incident device.
func (t *CorrelateIncidentTool) queryTracePanel(ctx context.Context, service string, deviceID *uint64, start, end time.Time) ([]traceEntry, error) {
	query, tags, err := traceQLSearchFilters(QueryTraceQLArgs{DeviceID: deviceID, Service: service})
	if err != nil {
		return nil, fmt.Errorf("build incident trace filter: %w", err)
	}
	res, err := t.traceQuery.SearchTraces(ctx, tracequery.SearchOptions{
		Query: query,
		Tags:  tags,
		Limit: 20,
		Start: start,
		End:   end,
	})
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Traces) == 0 {
		return nil, nil
	}
	var raw []struct {
		TraceID           string `json:"traceID"`
		RootServiceName   string `json:"rootServiceName"`
		RootTraceName     string `json:"rootTraceName"`
		DurationMS        int64  `json:"durationMs"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
	}
	if err := json.Unmarshal(res.Traces, &raw); err != nil {
		return nil, nil
	}
	entries := make([]traceEntry, 0, len(raw))
	for _, x := range raw {
		entries = append(entries, traceEntry{
			TraceID:    x.TraceID,
			Service:    x.RootServiceName,
			RootName:   x.RootTraceName,
			DurationMS: x.DurationMS,
			StartTime:  x.StartTimeUnixNano,
		})
	}
	if len(entries) > 20 {
		entries = entries[:20]
	}
	return entries, nil
}

// queryHostSnapshot reads Host identity and observations independently of Edge installation.
func (t *CorrelateIncidentTool) queryHostSnapshot(ctx context.Context, deviceID uint64, firedAt time.Time) *hostSnapshot {
	snap := &hostSnapshot{ID: deviceID}
	hostCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if t.devices == nil {
		return nil
	}
	host, err := t.devices.Get(hostCtx, deviceID)
	if err != nil || host == nil {
		return nil
	}
	snap.Name = host.Name
	snap.LastSeenAt = host.LastSeenAt
	snap.Roles = devicemodel.DecodeRoles(host.Roles)
	snap.Status = "offline"
	if host.Online {
		snap.Status = "online"
	}
	snap.CurrentEdgeID = host.CurrentEdgeID

	if t.promQuery != nil {
		load := &currentLoad{}
		probe := func(expr string, sink **float64) {
			pCtx, pCancel := context.WithTimeout(ctx, 3*time.Second)
			defer pCancel()
			res, err := t.promQuery.Query(pCtx, expr, time.Now())
			if err != nil || res == nil {
				return
			}
			if v, ok := singleVectorValue(res); ok {
				vc := v
				*sink = &vc
			}
		}
		for _, metric := range []struct {
			name string
			sink **float64
		}{{"cpu_pct", &load.CPUPct}, {"mem_pct", &load.MemPct}} {
			expr, _ := metricExprFor(metric.name)
			probe(wrapPerDevice(expr, &deviceID), metric.sink)
		}
		probe(fmt.Sprintf(`up{device_id="%d"}`, deviceID), &load.Up)
		if load.CPUPct != nil || load.MemPct != nil || load.Up != nil {
			snap.CurrentLoad = load
		}
	}

	if t.alertUC != nil {
		// Recent flap counter — pull a wider page filtered to this device,
		// then post-filter by first_fired_at >= firedAt - 24h.
		func() {
			listCtx, lcancel := context.WithTimeout(ctx, 5*time.Second)
			defer lcancel()
			recent, err := t.alertUC.ListIncidents(listCtx, alertbiz.IncidentFilter{
				DeviceID: &deviceID,
				Limit:    100,
			})
			if err != nil {
				return
			}
			cutoff := firedAt.Add(-24 * time.Hour)
			n := 0
			for _, it := range recent {
				if it.FirstFiredAt.After(cutoff) {
					n++
				}
			}
			snap.RecentIncidents24h = n
		}()
	}
	return snap
}
