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

// RankEdgesTool is the BaseTool form of rank_edges. Mirrors
// executeRankEdges in rank_edges.go.
type RankEdgesTool struct {
	promQuery PromQuerier
	edges     *edgebiz.Usecase
	log       *slog.Logger
}

// NewRankEdgesTool builds the BaseTool variant.
func NewRankEdgesTool(promQuery PromQuerier, edges *edgebiz.Usecase, log *slog.Logger) *RankEdgesTool {
	if log == nil {
		log = slog.Default()
	}
	return &RankEdgesTool{promQuery: promQuery, edges: edges, log: log}
}

// rankEdgesWhenToUse — top/bottom-N pivot for closed-set host metrics.
const rankEdgesWhenToUse = "When the user wants the TOP-N or BOTTOM-N hosts ranked by a closed-set metric " +
	"(cpu / mem / disk / load / composite). Example: '帮我找出最忙的 5 台机器'. " +
	"NOT for outlier detection / who's anomalously deviating (use find_outlier_edges). " +
	"NOT for general PromQL ranking on free-form metrics (use query_promql with topk/bottomk). " +
	"NOT for a single host's stats (use get_host_load)."

// Info returns metadata. Class=read.
func (t *RankEdgesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameRankEdges,
		Description: RankEdgesDescription,
		WhenToUse:   rankEdgesWhenToUse,
		Parameters:  RankEdgesSchema,
		Class:       "read",
	}, nil
}

// InvokableRun runs topk/bottomk PromQL and decorates with edge names.
func (t *RankEdgesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.promQuery == nil {
		return "", fmt.Errorf("rank_edges: prom query client not configured")
	}
	if t.edges == nil {
		return "", fmt.Errorf("rank_edges: edge usecase not configured")
	}

	var in RankEdgesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("rank_edges: bad args: %w", err)
	}
	if in.By == "" {
		return "", fmt.Errorf("rank_edges: by required")
	}
	base, label, ok := rankMetricExpr(in.By)
	if !ok {
		return "", fmt.Errorf("rank_edges: unsupported by=%q", in.By)
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
		return "", fmt.Errorf("rank_edges: direction must be top or bottom (got %q)", in.Direction)
	}
	expr := fmt.Sprintf(`%s(%d, %s)`, op, in.Limit, base)

	end := time.Now()
	start := end.Add(-5 * time.Minute)
	step := 30 * time.Second

	callCtx, cancel := context.WithTimeout(ctx, rankEdgesCallTimeout)
	defer cancel()
	res, err := t.promQuery.QueryRange(callCtx, expr, start, end, step)
	if err != nil {
		return "", fmt.Errorf("rank_edges: dispatch: %w", err)
	}

	rows, err := decodeRankSeries(res, label)
	if err != nil {
		return "", fmt.Errorf("rank_edges: decode: %w", err)
	}

	decorateRankRows(callCtx, t.edges, rows)

	out, err := json.Marshal(map[string]any{
		"results":   rows,
		"metric":    label,
		"by":        in.By,
		"direction": op,
	})
	if err != nil {
		return "", fmt.Errorf("rank_edges: marshal: %w", err)
	}
	return string(out), nil
}
