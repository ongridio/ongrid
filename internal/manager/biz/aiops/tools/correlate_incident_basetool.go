package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

// correlate_incident_basetool.go — N+15 batch refactor. Outer fan-out
// is added; the per-incident bundle assembly is unchanged (each inner
// already runs prom + log + trace probes concurrently inside its own
// 60s ceiling). schema cap stays at 16 to match the other batch tools,
// but the WhenToUse strongly suggests 2-4 — each inner is heavy.
//
// closure path (correlate_incident.go::executeCorrelateIncident) is
// untouched.

// CorrelateIncidentTool is the BaseTool form of correlate_incident.
type CorrelateIncidentTool struct {
	alertUC    AlertUsecase
	promQuery  PromQuerier
	logQuery   LogQuerier
	logSearch  logquery.Searcher
	traceQuery TraceQuerier
	edges      *edgebiz.Usecase
	devices    *devicebiz.Usecase
	log        *slog.Logger
}

func (t *CorrelateIncidentTool) SetLogSearcher(search logquery.Searcher) {
	t.logSearch = search
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
// success Bundle is populated (the rich incident-correlation JSON the
// closure executor returns); on failure Error is.
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
	"NOT for: 单纯查 incident 字段（用 get_incident_detail）/ 没 incident_id 的自由查（用 query_promql / search_logs）/ " +
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

// singleCorrelate runs the per-incident bundle assembly. Same logic as
// the closure executor; failure paths fold into ResultEntry.Error.
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

	if t.logSearch != nil || t.logQuery != nil {
		if inc.DeviceID != nil {
			entries, err := t.queryLogPanel(callCtx, *inc.DeviceID, wStart, wEnd)
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

	if inc.DeviceID != nil && t.edges != nil {
		snap := t.queryEdgeSnapshot(callCtx, *inc.DeviceID, inc.FirstFiredAt)
		bundle.Edge = snap
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

// queryMetricPanel mirrors Registry.queryMetricPanel.
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

// queryLogPanel mirrors Registry.queryLogPanel.
func (t *CorrelateIncidentTool) queryLogPanel(ctx context.Context, edgeID uint64, start, end time.Time) ([]logEntry, error) {
	if t.logSearch != nil {
		return queryStructuredIncidentLogs(ctx, t.logSearch, edgeID, start, end)
	}
	q := fmt.Sprintf(`{edge_id="%d"} |~ "(?i)error|panic|oom|fatal|fail"`, edgeID)
	res, err := t.logQuery.QueryRange(ctx, logquery.QueryRangeOptions{
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
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if len(entries) > 50 {
		entries = entries[:50]
	}
	return entries, nil
}

// queryTracePanel mirrors Registry.queryTracePanel, including device scope
// for incidents whose service names are shared across hosts.
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

// queryEdgeSnapshot mirrors Registry.queryEdgeSnapshot.
func (t *CorrelateIncidentTool) queryEdgeSnapshot(ctx context.Context, edgeID uint64, firedAt time.Time) *edgeSnapshot {
	snap := &edgeSnapshot{ID: edgeID}
	edgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	edge, err := t.edges.Get(edgeCtx, edgeID)
	if err == nil && edge != nil {
		snap.Name = edge.Name
		snap.Status = edge.Status
		snap.LastSeenAt = edge.LastSeenAt
		if t.devices != nil && edge.DeviceID != nil {
			if d, derr := t.devices.Get(edgeCtx, *edge.DeviceID); derr == nil && d != nil {
				snap.Roles = devicemodel.DecodeRoles(d.Roles)
			}
		}
	} else if err != nil && !errors.Is(err, context.Canceled) {
		if t.log != nil {
			t.log.Debug("correlate_incident: edge lookup failed",
				slog.Uint64("edge_id", edgeID), slog.Any("err", err))
		}
	}

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
		probe(fmt.Sprintf(`(100 * (1 - avg by (edge_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))) and on(edge_id) (group by (edge_id) ({edge_id="%d"}))`, edgeID), &load.CPUPct)
		probe(fmt.Sprintf(`(100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)) and on(edge_id) (group by (edge_id) ({edge_id="%d"}))`, edgeID), &load.MemPct)
		probe(fmt.Sprintf(`up{edge_id="%d"}`, edgeID), &load.Up)
		if load.CPUPct != nil || load.MemPct != nil || load.Up != nil {
			snap.CurrentLoad = load
		}
	}

	if t.alertUC != nil {
		// Recent flap counter — pull a wider page filtered to this edge,
		// then post-filter by first_fired_at >= firedAt - 24h.
		func() {
			listCtx, lcancel := context.WithTimeout(ctx, 5*time.Second)
			defer lcancel()
			eid := edgeID
			recent, err := t.alertUC.ListIncidents(listCtx, alertbiz.IncidentFilter{
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
