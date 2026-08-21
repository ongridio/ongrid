package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
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
	"the metric series that fired the rule, error logs from the same edge in the same time window, " +
	"slow/error traces, and recent edge state changes. " +
	"Returns one bundled JSON the LLM can reason over without further tool calls."

// CorrelateIncidentSchema is the JSON Schema of the tool's argument object.
var CorrelateIncidentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "incident_id": {
      "type": "integer",
      "minimum": 1,
      "description": "Primary key of the incident in alert_incidents."
    },
    "window_minutes": {
      "type": "integer",
      "minimum": 1,
      "maximum": 240,
      "description": "Total window centered on incident.first_fired_at (default 30, max 240)."
    }
  },
  "required": ["incident_id"]
}`)

// CorrelateIncidentArgs is the typed form of CorrelateIncidentSchema.
type CorrelateIncidentArgs struct {
	IncidentID    uint64 `json:"incident_id"`
	WindowMinutes int    `json:"window_minutes,omitempty"`
}

// correlateIncidentTimeout caps the overall tool run. Each sub-call gets
// a smaller per-step timeout below; this is the umbrella cap so the
// agent loop can't wedge on one slow upstream.
const correlateIncidentTimeout = 60 * time.Second

// correlateMaxResponseBytes caps the marshalled bundle so a misbehaving
// upstream can't blow the LLM context. The cap is enforced by trimming
// the noisiest panels (logs, then traces) before re-marshalling.
const correlateMaxResponseBytes = 100 * 1024 // 100 KB

// AlertUsecase is the narrow surface this and the other alert-flavoured
// tools (query_incidents / query_alert_rules / get_incident_detail /
// get_edge_summary / get_topology) need from the manager/alert biz layer.
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
	Edge        *edgeSnapshot     `json:"edge,omitempty"`
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

type edgeSnapshot struct {
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

// executeCorrelateIncident assembles the bundle.
func (r *Registry) executeCorrelateIncident(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.alertUC == nil {
		return ExecuteResult{}, fmt.Errorf("correlate_incident: alert usecase not configured")
	}
	var in CorrelateIncidentArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("correlate_incident: bad args: %w", err)
	}
	if in.IncidentID == 0 {
		return ExecuteResult{}, fmt.Errorf("correlate_incident: incident_id required")
	}
	window := in.WindowMinutes
	if window <= 0 {
		window = 30
	}
	if window > 240 {
		window = 240
	}

	callCtx, cancel := context.WithTimeout(ctx, correlateIncidentTimeout)
	defer cancel()

	inc, err := r.alertUC.GetIncident(callCtx, in.IncidentID)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("correlate_incident: get incident: %w", err)
	}
	if inc == nil {
		return ExecuteResult{}, fmt.Errorf("correlate_incident: incident %d not found", in.IncidentID)
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

	var et *uint64
	if inc.DeviceID != nil {
		id := *inc.DeviceID
		et = &id
	}

	// Metric panel — only when we have a Prom client AND we can synthesize
	// a meaningful expression. metric_threshold rules embed conditions
	// keyed by the closed-set metric names (cpu_pct / mem_pct / …); we
	// translate via the same vocabulary the host evaluator uses (see
	// alert/evaluators_phaseA.metricExprFor — duplicated here on purpose
	// to avoid importing a private function and to keep tools/ free of
	// alert evaluator internals).
	if r.promQuery != nil {
		if expr, ok := promExprForIncident(inc); ok {
			series, err := r.queryMetricPanel(callCtx, expr, inc.DeviceID, wStart, wEnd)
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

	// Log panel follows the backend-neutral planner when available and falls
	// back to legacy Loki only for old deployments.
	if r.logSearch != nil || r.logQuery != nil {
		if inc.DeviceID != nil {
			entries, err := r.queryLogPanel(callCtx, *inc.DeviceID, wStart, wEnd)
			if err != nil {
				bundle.Skipped["log_panel"] = "log query failed: " + err.Error()
			} else {
				bundle.LogPanel = entries
			}
		} else {
			bundle.Skipped["log_panel"] = "incident has no edge_id"
		}
	} else {
		bundle.Skipped["log_panel"] = "log query client not configured"
	}

	// Trace panel — needs Tempo + a service label on the incident. Keep a
	// device-scoped incident on that device: service names are commonly shared
	// by workloads across hosts.
	if r.traceQuery != nil {
		service := strings.TrimSpace(labels["service"])
		if service == "" {
			service = strings.TrimSpace(annotations["service"])
		}
		if service != "" {
			entries, err := r.queryTracePanel(callCtx, service, inc.DeviceID, wStart, wEnd)
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

	// Edge state — best-effort. Skip silently when no edge_id.
	if inc.DeviceID != nil && r.edges != nil {
		snap := r.queryEdgeSnapshot(callCtx, *inc.DeviceID, inc.FirstFiredAt)
		bundle.Edge = snap
	}

	if len(bundle.Skipped) == 0 {
		bundle.Skipped = nil
	}

	out, err := marshalBundleWithCap(bundle)
	if err != nil {
		return ExecuteResult{DeviceID: et}, fmt.Errorf("correlate_incident: marshal: %w", err)
	}
	return ExecuteResult{ResultJSON: out, DeviceID: et}, nil
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
		return wrapPerEdge(expr, inc.DeviceID), true
	}
	// Heuristic 2 — label / annotation hint.
	if labels, _ := inc.Labels(); labels != nil {
		if name := strings.TrimSpace(labels["metric"]); name != "" {
			if expr, ok := metricExprFor(name); ok {
				return wrapPerEdge(expr, inc.DeviceID), true
			}
		}
	}
	if ann, _ := inc.Annotations(); ann != nil {
		if name := strings.TrimSpace(ann["metric"]); name != "" {
			if expr, ok := metricExprFor(name); ok {
				return wrapPerEdge(expr, inc.DeviceID), true
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
		return `100 * (1 - avg by (edge_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))`, true
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
		return `sum by (edge_id) (rate(node_network_receive_bytes_total[1m]))`, true
	case "net_tx_bps":
		return `sum by (edge_id) (rate(node_network_transmit_bytes_total[1m]))`, true
	}
	return "", false
}

// wrapPerEdge restricts a closed-set metric expression to a single
// edge_id by intersecting it with a label-only selector on that id.
// Returns expr unchanged when edgeID is nil. The intersection form
// `(<expr>) and on(edge_id) (group by (edge_id) ({edge_id="<id>"}))`
// works for both raw selectors and `… by (edge_id) …` aggregations, so
// callers don't need to know the expression's shape.
func wrapPerEdge(expr string, edgeID *uint64) string {
	if edgeID == nil {
		return expr
	}
	return fmt.Sprintf(`(%s) and on(edge_id) (group by (edge_id) ({edge_id="%d"}))`, expr, *edgeID)
}

// queryMetricPanel runs a range query and reduces the result to top-3 series
// by max magnitude. The series are returned with timestamps in unix-seconds
// and values as strings (Prom's wire format) so the LLM can reason without
// secondary float parsing.
func (r *Registry) queryMetricPanel(ctx context.Context, expr string, edgeID *uint64, start, end time.Time) ([]metricSeries, error) {
	step := stepFor(int(end.Sub(start).Seconds()))
	res, err := r.promQuery.QueryRange(ctx, expr, start, end, step)
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Result) == 0 {
		return nil, nil
	}
	// Wire shape: [{"metric": {...}, "values": [[ts, "v"], ...]}, ...]
	var raw []struct {
		Metric map[string]string `json:"metric"`
		Values [][2]any          `json:"values"`
	}
	if err := json.Unmarshal(res.Result, &raw); err != nil {
		// Could be a vector with `value` instead of `values`; ignore.
		return nil, fmt.Errorf("decode prom matrix: %w", err)
	}
	series := make([]metricSeries, 0, len(raw))
	for _, s := range raw {
		series = append(series, metricSeries{Labels: s.Metric, Values: s.Values})
	}
	// Top 3 by magnitude (max abs value across the series).
	sort.SliceStable(series, func(i, j int) bool {
		return seriesMagnitude(series[i]) > seriesMagnitude(series[j])
	})
	if len(series) > 3 {
		series = series[:3]
	}
	return series, nil
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

// queryLogPanel asks Loki for the 50 most recent error-ish lines from the
// edge's stream. Direction=backward gives the LLM the most recent samples
// first, which is what an operator would scroll to anyway.
func (r *Registry) queryLogPanel(ctx context.Context, edgeID uint64, start, end time.Time) ([]logEntry, error) {
	if r.logSearch != nil {
		return queryStructuredIncidentLogs(ctx, r.logSearch, edgeID, start, end)
	}
	q := fmt.Sprintf(`{edge_id="%d"} |~ "(?i)error|panic|oom|fatal|fail"`, edgeID)
	res, err := r.logQuery.QueryRange(ctx, logquery.QueryRangeOptions{
		Query:     q,
		Start:     start,
		End:       end,
		Limit:     50,
		Direction: "backward",
	})
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Result) == 0 {
		return nil, nil
	}
	// Loki streams shape: [{"stream": {labels...}, "values": [[ns, line], ...]}]
	var raw []struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	if err := json.Unmarshal(res.Result, &raw); err != nil {
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
	// Sort newest first.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if len(entries) > 50 {
		entries = entries[:50]
	}
	return entries, nil
}

func queryStructuredIncidentLogs(ctx context.Context, search logquery.Searcher, deviceID uint64, start, end time.Time) ([]logEntry, error) {
	result, err := search.Search(ctx, logquery.SearchRequest{
		Start: start, End: end, Limit: 50, Direction: logquery.SortBackward,
		Scope: logquery.Scope{DeviceIDs: []uint64{deviceID}},
		Keywords: logquery.Keywords{
			Include: []string{"error", "panic", "oom", "fatal", "fail"},
			Mode:    logquery.MatchAny,
		},
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	entries := make([]logEntry, 0, len(result.Records))
	for _, record := range result.Records {
		labels := make(map[string]string, len(record.ResourceAttributes)+len(record.Attributes)+1)
		for key, value := range record.ResourceAttributes {
			labels[key] = value
		}
		for key, value := range record.Attributes {
			labels[key] = value
		}
		labels["backend"] = record.Backend
		entries = append(entries, logEntry{Timestamp: record.Timestamp, Line: truncateLine(record.Message, 200), Labels: labels})
	}
	return entries, nil
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

// queryTracePanel runs a Tempo search filtered by service.name and, when the
// incident has one, its stable device ID. This prevents a shared service name
// from pulling traces from another host into the incident bundle.
//
// The status / latency hint helps surface the kind of traces the LLM cares
// about. Tempo's response is OTLP-shaped; we lift the few fields that
// matter and drop the rest to keep the bundle tight.
func (r *Registry) queryTracePanel(ctx context.Context, service string, deviceID *uint64, start, end time.Time) ([]traceEntry, error) {
	query, tags, err := traceQLSearchFilters(QueryTraceQLArgs{DeviceID: deviceID, Service: service})
	if err != nil {
		return nil, fmt.Errorf("build incident trace filter: %w", err)
	}
	res, err := r.traceQuery.SearchTraces(ctx, tracequery.SearchOptions{
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
	// Tempo's `traces` field is `[{traceID, rootServiceName, rootTraceName, durationMs, startTimeUnixNano, ...}, ...]`.
	var raw []struct {
		TraceID           string `json:"traceID"`
		RootServiceName   string `json:"rootServiceName"`
		RootTraceName     string `json:"rootTraceName"`
		DurationMS        int64  `json:"durationMs"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
	}
	if err := json.Unmarshal(res.Traces, &raw); err != nil {
		// Some Tempo versions wrap in {"traces":[...]}; we already pulled
		// the inner array so this is a best-effort decode. Don't error —
		// just return empty; the caller logs `skipped`.
		return nil, nil
	}
	entries := make([]traceEntry, 0, len(raw))
	for _, t := range raw {
		entries = append(entries, traceEntry{
			TraceID:    t.TraceID,
			Service:    t.RootServiceName,
			RootName:   t.RootTraceName,
			DurationMS: t.DurationMS,
			StartTime:  t.StartTimeUnixNano,
		})
	}
	if len(entries) > 20 {
		entries = entries[:20]
	}
	return entries, nil
}

// queryEdgeSnapshot resolves the edge row + (when Prom is available)
// current cpu_pct / mem_pct / up + a 24h flap counter. All sub-calls are
// best-effort: a slow Prom probe MUST NOT block the whole bundle, so each
// gets its own short deadline.
func (r *Registry) queryEdgeSnapshot(ctx context.Context, edgeID uint64, firedAt time.Time) *edgeSnapshot {
	snap := &edgeSnapshot{ID: edgeID}
	edgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	edge, err := r.edges.Get(edgeCtx, edgeID)
	if err == nil && edge != nil {
		snap.Name = edge.Name
		snap.Status = edge.Status
		snap.LastSeenAt = edge.LastSeenAt
		// Roles now live on the host Device (post device-split). Look it
		// up best-effort; bundle stays useful even when device repo is
		// missing or the device row hasn't been created yet.
		if r.devices != nil && edge.DeviceID != nil {
			if d, derr := r.devices.Get(edgeCtx, *edge.DeviceID); derr == nil && d != nil {
				snap.Roles = devicemodel.DecodeRoles(d.Roles)
			}
		}
	} else if err != nil && !errors.Is(err, context.Canceled) {
		// Don't surface as fatal; bundle stays useful even with edge name missing.
		if r.log != nil {
			r.log.Debug("correlate_incident: edge lookup failed",
				slog.Uint64("edge_id", edgeID), slog.Any("err", err))
		}
	}

	if r.promQuery != nil {
		load := &currentLoad{}
		probe := func(expr string, sink **float64) {
			pCtx, pCancel := context.WithTimeout(ctx, 3*time.Second)
			defer pCancel()
			res, err := r.promQuery.Query(pCtx, expr, time.Now())
			if err != nil || res == nil {
				return
			}
			if v, ok := singleVectorValue(res); ok {
				vc := v
				*sink = &vc
			}
		}
		probe(fmt.Sprintf(`(100 * (1 - avg by (edge_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))) and on(edge_id) (group by (edge_id) ({edge_id="%d"}))`, edgeID), &load.CPUPct)
		probe(fmt.Sprintf(`(100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)) and on(edge_id) (group by (edge_id) ({edge_id="%d"}))`, edgeID), &load.MemPct)
		probe(fmt.Sprintf(`up{edge_id="%d"}`, edgeID), &load.Up)
		if load.CPUPct != nil || load.MemPct != nil || load.Up != nil {
			snap.CurrentLoad = load
		}
	}

	if r.alertUC != nil {
		// Recent flap counter — pull a wider page filtered to this edge,
		// then post-filter by first_fired_at >= firedAt - 24h. The biz
		// IncidentFilter has no time bound today; cap by limit to keep the
		// query bounded.
		//
		// IIFE so the WithTimeout cancel runs at end of this block instead
		// of accumulating with the outer edgeCtx defer until function exit.
		func() {
			listCtx, lcancel := context.WithTimeout(ctx, 5*time.Second)
			defer lcancel()
			eid := edgeID
			recent, err := r.alertUC.ListIncidents(listCtx, alertbiz.IncidentFilter{
				DeviceID: &eid,
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
