package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

// QueryTraceQLTool is the BaseTool form of query_traceql. Mirrors the
// closure executor in query_traceql.go.
type QueryTraceQLTool struct {
	traceQuery TraceQuerier
	log        *slog.Logger
}

// NewQueryTraceQLTool builds the BaseTool variant.
func NewQueryTraceQLTool(tq TraceQuerier, log *slog.Logger) *QueryTraceQLTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryTraceQLTool{traceQuery: tq, log: log}
}

// queryTraceQLWhenToUse — reverse-guard against picking trace search for
// log/metric questions.
const queryTraceQLWhenToUse = "When the user wants TRACES — span chains across services, latency outliers, " +
	"specific trace IDs, or 'which call took 5 seconds'. " +
	"NOT for log lines (use search_logs), NOT for metric trends (use query_promql), " +
	"NOT for live host stats (use get_host_load). " +
	"At least one filter (query / service / operation / duration) is required — Tempo unfiltered search is too expensive."

// Info returns metadata. Class=read.
func (t *QueryTraceQLTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryTraceQL,
		Description: QueryTraceQLDescription,
		WhenToUse:   queryTraceQLWhenToUse,
		Parameters:  QueryTraceQLSchema,
		Class:       "read",
	}, nil
}

// InvokableRun runs the TraceQL search.
func (t *QueryTraceQLTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.traceQuery == nil {
		return "", fmt.Errorf("query_traceql: trace query client not configured")
	}
	var in QueryTraceQLArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_traceql: bad args: %w", err)
	}

	if strings.TrimSpace(in.Query) == "" &&
		in.DeviceID == nil &&
		strings.TrimSpace(in.Service) == "" &&
		strings.TrimSpace(in.Operation) == "" &&
		strings.TrimSpace(in.MinDuration) == "" &&
		strings.TrimSpace(in.MaxDuration) == "" {
		return "", fmt.Errorf("query_traceql: at least one of query/service/operation/min_duration/max_duration required")
	}

	end := time.Now()
	start := end.Add(-time.Hour)
	if in.End != "" {
		t, err := time.Parse(time.RFC3339, in.End)
		if err != nil {
			return "", fmt.Errorf("query_traceql: parse end: %w", err)
		}
		end = t
	}
	if in.Start != "" {
		t, err := time.Parse(time.RFC3339, in.Start)
		if err != nil {
			return "", fmt.Errorf("query_traceql: parse start: %w", err)
		}
		start = t
	} else if in.End != "" {
		start = end.Add(-time.Hour)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}

	var minDur, maxDur time.Duration
	if in.MinDuration != "" {
		d, err := time.ParseDuration(in.MinDuration)
		if err != nil {
			return "", fmt.Errorf("query_traceql: parse min_duration: %w", err)
		}
		minDur = d
	}
	if in.MaxDuration != "" {
		d, err := time.ParseDuration(in.MaxDuration)
		if err != nil {
			return "", fmt.Errorf("query_traceql: parse max_duration: %w", err)
		}
		maxDur = d
	}

	query, tags, err := traceQLSearchFilters(in)
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, queryTraceqlCallTimeout)
	defer cancel()

	res, err := t.traceQuery.SearchTraces(callCtx, tracequery.SearchOptions{
		Query:       query,
		Tags:        tags,
		Limit:       limit,
		Start:       start,
		End:         end,
		MinDuration: minDur,
		MaxDuration: maxDur,
	})
	if err != nil {
		return "", fmt.Errorf("query_traceql: dispatch: %w", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("query_traceql: marshal response: %w", err)
	}
	return string(out), nil
}
