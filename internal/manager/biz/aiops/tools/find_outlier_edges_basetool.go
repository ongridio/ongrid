package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
)

// FindOutlierEdgesTool is the BaseTool form of find_outlier_edges.
// Mirrors executeFindOutlierEdges in find_outlier_edges.go.
type FindOutlierEdgesTool struct {
	promQuery PromQuerier
	edges     *edgebiz.Usecase
	log       *slog.Logger
}

// NewFindOutlierEdgesTool builds the BaseTool variant.
func NewFindOutlierEdgesTool(promQuery PromQuerier, edges *edgebiz.Usecase, log *slog.Logger) *FindOutlierEdgesTool {
	if log == nil {
		log = slog.Default()
	}
	return &FindOutlierEdgesTool{promQuery: promQuery, edges: edges, log: log}
}

// findOutlierEdgesWhenToUse — anomaly-detection pivot. Reverse-guards
// against confusing this with simple ranking.
const findOutlierEdgesWhenToUse = "When the user asks who's an OUTLIER on cpu / mem / disk — i.e. who's deviating " +
	"more than N sigma from the fleet baseline. " +
	"NOT for a top-N ranking by raw value (use rank_edges). " +
	"NOT for deviation on free-form metrics (use query_promql with stddev). " +
	"NOT for single-host details (use get_host_load)."

// Info returns metadata. Class=read.
func (t *FindOutlierEdgesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameFindOutlierEdges,
		Description: FindOutlierEdgesDescription,
		WhenToUse:   findOutlierEdgesWhenToUse,
		Parameters:  FindOutlierEdgesSchema,
		Class:       "read",
	}, nil
}

// InvokableRun builds the z-score PromQL and filters > sigma.
func (t *FindOutlierEdgesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.promQuery == nil {
		return "", fmt.Errorf("find_outlier_edges: prom query client not configured")
	}
	if t.edges == nil {
		return "", fmt.Errorf("find_outlier_edges: edge usecase not configured")
	}

	var in FindOutlierEdgesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("find_outlier_edges: bad args: %w", err)
	}
	// Whitelist cpu/mem/disk explicitly. rankMetricExpr also accepts
	// load/composite (for rank_edges), but z-score outlier detection on
	// those is not in scope — fail fast with a clear message instead of
	// relying on rankMetricExpr's ok-bool.
	switch in.Metric {
	case "cpu", "mem", "disk":
		// ok
	default:
		return "", fmt.Errorf("find_outlier_edges: metric must be cpu, mem or disk; got %q", in.Metric)
	}
	base, label, _ := rankMetricExpr(in.Metric)
	if in.Sigma <= 0 {
		in.Sigma = 2
	}
	if in.Sigma > 10 {
		in.Sigma = 10
	}

	expr := fmt.Sprintf(
		`((%s) - on() group_left() avg(%s)) / on() group_left() stddev(%s) > %g`,
		base, base, base, in.Sigma,
	)

	end := time.Now()
	start := end.Add(-5 * time.Minute)
	step := 30 * time.Second

	callCtx, cancel := context.WithTimeout(ctx, outlierCallTimeout)
	defer cancel()
	res, err := t.promQuery.QueryRange(callCtx, expr, start, end, step)
	if err != nil {
		return "", fmt.Errorf("find_outlier_edges: dispatch: %w", err)
	}

	rankRows, err := decodeRankSeries(res, label)
	if err != nil {
		return "", fmt.Errorf("find_outlier_edges: decode: %w", err)
	}
	decorateRankRows(callCtx, t.edges, rankRows)
	rows := make([]OutlierEdgeRow, 0, len(rankRows))
	for _, rr := range rankRows {
		rows = append(rows, OutlierEdgeRow{
			DeviceID: rr.DeviceID,
			EdgeID:   rr.EdgeID,
			EdgeName: rr.EdgeName,
			ZScore:   rr.Value,
			Metric:   rr.Metric,
		})
	}

	out, err := json.Marshal(map[string]any{
		"outliers": rows,
		"sigma":    in.Sigma,
		"metric":   in.Metric,
	})
	if err != nil {
		return "", fmt.Errorf("find_outlier_edges: marshal: %w", err)
	}
	return string(out), nil
}
