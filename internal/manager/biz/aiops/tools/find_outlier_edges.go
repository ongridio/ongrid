package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
)

// ToolNameFindOutlierEdges is the stable wire name the LLM sees.
const ToolNameFindOutlierEdges = "find_outlier_edges"

// FindOutlierEdgesDescription pushes the model toward this tool when the
// question is about which edges deviate from the fleet baseline.
const FindOutlierEdgesDescription = "Find edges whose metric value is more than N standard deviations above the fleet mean. " +
	"Use this for 'who's an outlier on cpu/mem/disk' style questions. Default sigma is 2."

// FindOutlierEdgesSchema is the JSON Schema of the tool's argument object.
var FindOutlierEdgesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "metric": {
      "type": "string",
      "enum": ["cpu", "mem", "disk"],
      "description": "Which closed-set host metric to compare across the fleet."
    },
    "sigma": {
      "type": "number",
      "minimum": 0.5,
      "maximum": 10,
      "description": "z-score threshold (default 2). Edges with z > sigma are returned."
    }
  },
  "required": ["metric"]
}`)

// FindOutlierEdgesArgs is the typed form of FindOutlierEdgesSchema.
type FindOutlierEdgesArgs struct {
	Metric string  `json:"metric"`
	Sigma  float64 `json:"sigma,omitempty"`
}

// OutlierEdgeRow is one outlier hit.
type OutlierEdgeRow struct {
	EdgeID   uint64  `json:"edge_id"`
	EdgeName string  `json:"edge_name"`
	ZScore   float64 `json:"z_score"`
	Metric   string  `json:"metric"`
}

const outlierCallTimeout = 60 * time.Second

// executeFindOutlierEdges builds a z-score PromQL of the shape
//
//	(per_edge_metric - on() group_left avg(per_edge_metric)) /
//	    on() group_left stddev(per_edge_metric)
//
// then filters > sigma. The result is decorated with edge names like
// rank_edges does.
func (r *Registry) executeFindOutlierEdges(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.promQuery == nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: prom query client not configured")
	}
	if r.edges == nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: edge usecase not configured")
	}

	var in FindOutlierEdgesArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: bad args: %w", err)
	}
	// Whitelist cpu/mem/disk explicitly. rankMetricExpr also accepts
	// load/composite (for rank_edges), but z-score outlier detection on
	// those is not in scope — fail fast with a clear message instead of
	// relying on rankMetricExpr's ok-bool.
	switch in.Metric {
	case "cpu", "mem", "disk":
		// ok
	default:
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: metric must be cpu, mem or disk; got %q", in.Metric)
	}
	base, label, _ := rankMetricExpr(in.Metric)
	if in.Sigma <= 0 {
		in.Sigma = 2
	}
	if in.Sigma > 10 {
		in.Sigma = 10
	}

	// `on() group_left` broadcasts the scalar avg/stddev across every
	// per-edge sample. Without it Prom drops the whole vector because
	// label sets don't match.
	expr := fmt.Sprintf(
		`((%s) - on() group_left() avg(%s)) / on() group_left() stddev(%s) > %g`,
		base, base, base, in.Sigma,
	)

	end := time.Now()
	start := end.Add(-5 * time.Minute)
	step := 30 * time.Second

	callCtx, cancel := context.WithTimeout(ctx, outlierCallTimeout)
	defer cancel()
	res, err := r.promQuery.QueryRange(callCtx, expr, start, end, step)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: dispatch: %w", err)
	}

	rankRows, err := decodeRankSeries(res, label)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: decode: %w", err)
	}
	rows := make([]OutlierEdgeRow, 0, len(rankRows))
	for _, rr := range rankRows {
		rows = append(rows, OutlierEdgeRow{
			EdgeID: rr.EdgeID,
			ZScore: rr.Value,
			Metric: rr.Metric,
		})
	}

	// Decorate with edge name.
	edges, err := r.edges.List(callCtx, edgebiz.ListFilter{Limit: 500})
	if err == nil {
		nameByID := make(map[uint64]string, len(edges))
		for _, e := range edges {
			nameByID[e.ID] = e.Name
		}
		for i := range rows {
			if n, ok := nameByID[rows[i].EdgeID]; ok {
				rows[i].EdgeName = n
			}
		}
	}

	out, err := json.Marshal(map[string]any{
		"outliers": rows,
		"sigma":    in.Sigma,
		"metric":   in.Metric,
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_edges: marshal: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}
