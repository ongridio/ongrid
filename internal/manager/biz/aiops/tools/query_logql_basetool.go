package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

// QueryLogQLTool is the BaseTool form of query_logql. Mirrors the closure
// executor in query_logql.go: same args, same timeouts, same output bytes.
type QueryLogQLTool struct {
	logQuery LogQuerier
	log      *slog.Logger
}

// NewQueryLogQLTool builds the BaseTool variant.
func NewQueryLogQLTool(lq LogQuerier, log *slog.Logger) *QueryLogQLTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryLogQLTool{logQuery: lq, log: log}
}

// queryLogQLWhenToUse — the canonical reverse-guard for log search.
// explicit "do NOT use for ..." steers the model away from
// the metric / trace tools when the question is really about log content.
const queryLogQLWhenToUse = "When the user asks about log CONTENT — grep error / panic / fatal, see the line text " +
	"that explains why a service failed, or count log volume over time. " +
	"NOT for filesystem state, file names or file sizes (use a host_files skill). " +
	"NOT for metric trends like cpu/mem (use query_promql). " +
	"NOT for traces / span timelines (use query_traceql)."

// Info returns metadata. Class=read.
func (t *QueryLogQLTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryLogQL,
		Description: QueryLogQLDescription,
		WhenToUse:   queryLogQLWhenToUse,
		Parameters:  QueryLogQLSchema,
		Class:       "read",
	}, nil
}

// InvokableRun runs the LogQL range query.
func (t *QueryLogQLTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.logQuery == nil {
		return "", fmt.Errorf("query_logql: log query client not configured")
	}
	var in QueryLogQLArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_logql: bad args: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("query_logql: query required")
	}

	end := time.Now()
	start := end.Add(-time.Hour)
	if in.End != "" {
		t, err := parseLogQLTime(in.End, end)
		if err != nil {
			return "", fmt.Errorf("query_logql: parse end: %w", err)
		}
		end = t
	}
	if in.Start != "" {
		t, err := parseLogQLTime(in.Start, start)
		if err != nil {
			return "", fmt.Errorf("query_logql: parse start: %w", err)
		}
		start = t
	} else if in.End != "" {
		start = end.Add(-time.Hour)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	direction := in.Direction
	if direction == "" {
		direction = "backward"
	}

	callCtx, cancel := context.WithTimeout(ctx, queryLogqlCallTimeout)
	defer cancel()

	res, err := t.logQuery.QueryRange(callCtx, logquery.QueryRangeOptions{
		Query:     in.Query,
		Start:     start,
		End:       end,
		Limit:     limit,
		Direction: direction,
	})
	if err != nil {
		return "", fmt.Errorf("query_logql: dispatch: %w", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("query_logql: marshal response: %w", err)
	}
	return string(out), nil
}
