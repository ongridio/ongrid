package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

// ToolNameRankEdges is the stable wire name the LLM sees.
const ToolNameRankEdges = "rank_edges"

// RankEdgesDescription pushes the model toward this tool whenever the
// question is "which top/bottom N machines by a host metric".
const RankEdgesDescription = "Rank edges by a closed-set host metric (cpu, mem, disk, load, composite) and return top-N or bottom-N. " +
	"Use this for 'find the most/least loaded N machines' style questions. " +
	"Composite is the unweighted mean of cpu_pct + mem_pct + disk_used_pct."

// RankEdgesSchema is the JSON Schema of the tool's argument object.
var RankEdgesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "by": {
      "type": "string",
      "enum": ["cpu", "mem", "disk", "load", "composite"],
      "description": "Which metric to rank on. cpu/mem/disk are the same closed-set the alert evaluator uses."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "How many edges to return (default 5)."
    },
    "direction": {
      "type": "string",
      "enum": ["top", "bottom"],
      "description": "top = highest values; bottom = lowest values (default top)."
    }
  },
  "required": ["by"]
}`)

// RankEdgesArgs is the typed form of RankEdgesSchema.
type RankEdgesArgs struct {
	By        string `json:"by"`
	Limit     int    `json:"limit,omitempty"`
	Direction string `json:"direction,omitempty"`
}

// RankEdgeRow is one ranked entry. Prometheus host metrics conventionally
// carry device_id, while a few legacy series carry edge_id; retain both so a
// follow-up host tool receives the identifier it actually accepts.
type RankEdgeRow struct {
	DeviceID uint64  `json:"device_id,omitempty"`
	EdgeID   uint64  `json:"edge_id,omitempty"`
	EdgeName string  `json:"edge_name"`
	Value    float64 `json:"value"`
	Metric   string  `json:"metric"`
}

const rankEdgesCallTimeout = 60 * time.Second

// rankMetricExpr returns the PromQL fragment that yields a per-device
// scalar for the requested rank-by name. The closed set must stay in
// sync with manager/biz/alert.metricExprFor so an LLM-driven rank uses
// the same vocabulary as alert thresholds.
func rankMetricExpr(by string) (expr, label string, ok bool) {
	switch by {
	case "cpu":
		return `100 * (1 - avg by (device_id, edge_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))`, "cpu_pct", true
	case "mem":
		return `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`, "mem_pct", true
	case "disk":
		return `100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})`, "disk_used_pct", true
	case "load":
		return `node_load1`, "load1", true
	case "composite":
		// Mean of cpu/mem/disk in percent. Preserve device_id and legacy edge_id
		// because the underlying series carry differing label shapes
		// (cpu has a mode label aggregated away; mem has none; disk
		// carries a mountpoint filter). Aligning on the identity labels collapses
		// to one row per host.
		return `(` +
			`avg by (device_id, edge_id) (100 * (1 - avg by (device_id, edge_id, instance) (rate(node_cpu_seconds_total{mode="idle"}[5m]))))` +
			` + avg by (device_id, edge_id) (100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))` +
			` + avg by (device_id, edge_id) (100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}))` +
			`) / 3`, "composite_pct", true
	}
	return "", "", false
}

// executeRankEdges builds a topk()/bottomk() PromQL, runs it through the
// PromQuerier, and decorates each row with the edge's friendly name.
func (r *Registry) executeRankEdges(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.promQuery == nil {
		return ExecuteResult{}, fmt.Errorf("rank_edges: prom query client not configured")
	}
	if r.edges == nil {
		return ExecuteResult{}, fmt.Errorf("rank_edges: edge usecase not configured")
	}

	var in RankEdgesArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("rank_edges: bad args: %w", err)
	}
	if in.By == "" {
		return ExecuteResult{}, fmt.Errorf("rank_edges: by required")
	}
	base, label, ok := rankMetricExpr(in.By)
	if !ok {
		return ExecuteResult{}, fmt.Errorf("rank_edges: unsupported by=%q", in.By)
	}
	if in.Limit <= 0 {
		in.Limit = 5
	}
	if in.Limit > 50 {
		in.Limit = 50
	}
	op := "topk"
	switch in.Direction {
	case "", "top":
		op = "topk"
	case "bottom":
		op = "bottomk"
	default:
		return ExecuteResult{}, fmt.Errorf("rank_edges: direction must be top or bottom (got %q)", in.Direction)
	}
	expr := fmt.Sprintf(`%s(%d, %s)`, op, in.Limit, base)

	end := time.Now()
	start := end.Add(-5 * time.Minute)
	step := 30 * time.Second

	callCtx, cancel := context.WithTimeout(ctx, rankEdgesCallTimeout)
	defer cancel()
	res, err := r.promQuery.QueryRange(callCtx, expr, start, end, step)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("rank_edges: dispatch: %w", err)
	}

	rows, err := decodeRankSeries(res, label)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("rank_edges: decode: %w", err)
	}

	decorateRankRows(callCtx, r.edges, rows)

	out, err := json.Marshal(map[string]any{
		"results":   rows,
		"metric":    label,
		"by":        in.By,
		"direction": op,
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("rank_edges: marshal: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}

// decorateRankRows keeps telemetry device IDs separate from Edge primary keys.
func decorateRankRows(ctx context.Context, edges *edgebiz.Usecase, rows []RankEdgeRow) {
	// shortcut: enrich the first 500 edges; paginate when larger fleets need complete names.
	items, err := edges.List(ctx, edgebiz.ListFilter{Limit: 500})
	if err != nil {
		return // Names and Edge links are best-effort; retain the metric identity.
	}
	byEdge := make(map[uint64]*edgemodel.Edge, len(items))
	byDevice := make(map[uint64]*edgemodel.Edge, len(items))
	for _, edge := range items {
		byEdge[edge.ID] = edge
		if edge.DeviceID != nil {
			byDevice[*edge.DeviceID] = edge
		}
	}
	for i := range rows {
		edge := byEdge[rows[i].EdgeID]
		if rows[i].DeviceID != 0 {
			edge = byDevice[rows[i].DeviceID]
		}
		if edge != nil {
			rows[i].EdgeID = edge.ID
			rows[i].EdgeName = edge.Name
		}
	}
}

// promRangeSeries is the per-series envelope inside a Prom matrix
// response. Decoded only as much as we need.
type promRangeSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

// decodeRankSeries preserves device_id or legacy edge_id and the last value.
// Series without either numeric identity cannot be attributed to a host.
func decodeRankSeries(res interface { /* satisfied by *promquery.InstantResult */
}, metricLabel string) ([]RankEdgeRow, error) {
	type irShape struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	// The PromQuerier returns a *promquery.InstantResult; rather than
	// hard-binding to that concrete type here (which would create a
	// dependency cycle issue if the package layout shifts), re-encode
	// and re-decode through json. This costs one extra marshal; the
	// payload is small (<= 50 rows).
	blob, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var ir irShape
	if err := json.Unmarshal(blob, &ir); err != nil {
		return nil, err
	}
	switch ir.ResultType {
	case "matrix":
		var series []promRangeSeries
		if err := json.Unmarshal(ir.Result, &series); err != nil {
			return nil, err
		}
		rows := make([]RankEdgeRow, 0, len(series))
		for _, s := range series {
			deviceID, edgeID, ok := rankTargetIDs(s.Metric)
			if !ok {
				continue
			}
			val, ok := lastSampleValue(s.Values)
			if !ok {
				continue
			}
			rows = append(rows, RankEdgeRow{DeviceID: deviceID, EdgeID: edgeID, Value: val, Metric: metricLabel})
		}
		return rows, nil
	case "vector":
		var series []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(ir.Result, &series); err != nil {
			return nil, err
		}
		rows := make([]RankEdgeRow, 0, len(series))
		for _, s := range series {
			deviceID, edgeID, ok := rankTargetIDs(s.Metric)
			if !ok {
				continue
			}
			val, ok := promSampleFloat(s.Value[1])
			if !ok {
				continue
			}
			rows = append(rows, RankEdgeRow{DeviceID: deviceID, EdgeID: edgeID, Value: val, Metric: metricLabel})
		}
		return rows, nil
	}
	return nil, nil
}

func rankTargetIDs(labels map[string]string) (deviceID, edgeID uint64, ok bool) {
	if deviceID, ok = numericLabel(labels, "device_id"); ok {
		return deviceID, 0, true
	}
	edgeID, ok = numericLabel(labels, "edge_id")
	return 0, edgeID, ok
}

// numericLabel pulls a uint64-ish label out of the Prom metric map. A
// non-numeric or missing label returns ok=false.
func numericLabel(m map[string]string, key string) (uint64, bool) {
	v, ok := m[key]
	if !ok || v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// lastSampleValue returns the last numeric sample of a Prom matrix series.
// Each sample is [unix_ts, "<value>"]; the value comes back as a string
// inside JSON.
func lastSampleValue(values [][2]any) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	last := values[len(values)-1]
	return promSampleFloat(last[1])
}

func promSampleFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return x, true
	}
	return 0, false
}
