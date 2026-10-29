package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

// ToolNameQueryPromQL is the stable wire name the LLM sees for the PromQL tool.
const ToolNameQueryPromQL = "query_promql"

// QueryPromQLDescription is the single-sentence description shown to the LLM.
// Phrased to push the model toward this tool whenever the host-load /
// process-list tools are too narrow.
const QueryPromQLDescription = "Run a PromQL range query against the cluster's Prometheus. " +
	"Use this when you need any host or container metric beyond the few host-level fields the basic tools return. " +
	"For fleet or multi-device questions, write one vectorized PromQL expression with by(device_id, ...) / regex selectors / topk instead of one query per device or metric. " +
	"Returns the raw Prom HTTP API response."

// QueryPromQLSchema is the JSON Schema of the tool's argument object.
var QueryPromQLSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "expr": {
      "type": "string",
      "description": "PromQL expression. Prefer one vectorized expression for multiple devices/labels. Example: \"avg by (device_id) (rate(node_cpu_seconds_total{mode!=\\\"idle\\\"}[5m]))\". For filesystem percent, combine numerator/denominator in one expression instead of separate used/size queries."
    },
    "lookback_seconds": {
      "type": "integer",
      "minimum": 60,
      "maximum": 604800,
      "description": "How far back to query in seconds (default 300 = 5 minutes; max 604800 = 7d). Use one 7d range query for weekly trends instead of repeating short lookbacks."
    }
  },
  "required": ["expr"]
}`)

// QueryPromQLArgs is the typed form of QueryPromQLSchema.
type QueryPromQLArgs struct {
	Expr            string `json:"expr"`
	LookbackSeconds int    `json:"lookback_seconds,omitempty"`
}

// queryPromqlCallTimeout caps how long a single dispatch may wait. Same
// rationale as the other tool timeouts.
const queryPromqlCallTimeout = 60 * time.Second

const maxQueryPromQLLookbackSeconds = 7 * 24 * 3600

// stepFor picks a sensible step size for a given lookback. The math
// targets ~30 datapoints per range, capped at the Prom defaults.
//   - <= 5min   -> 15s
//   - <= 1h     -> 1m
//   - <= 6h     -> 5m
//   - <= 24h    -> 15m
//   - <= 7d     -> 1h
//   - else      -> 1h
//
// The model can override lookback but not step; that keeps the cost
// envelope predictable.
func stepFor(lookbackSeconds int) time.Duration {
	switch {
	case lookbackSeconds <= 300:
		return 15 * time.Second
	case lookbackSeconds <= 3600:
		return time.Minute
	case lookbackSeconds <= 6*3600:
		return 5 * time.Minute
	case lookbackSeconds <= 24*3600:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

// PromQuerier is the narrow surface the query_promql executor needs from
// the promquery client. Declared here so tests can inject a fake.
//
// NOTE: this interface is what r.promQuery is typed as. The concrete
// *promquery.Client satisfies it.
type PromQuerier interface {
	QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error)
	// Query is the instant form. correlate_incident uses it to grab
	// a single point-in-time vector for cpu_pct / mem_pct / up. Concrete
	// *promquery.Client satisfies it.
	Query(ctx context.Context, expr string, ts time.Time) (*promquery.InstantResult, error)
}

// QueryPromQLTool runs PromQL queries with a bounded time range.
type QueryPromQLTool struct {
	promQuery PromQuerier
	log       *slog.Logger
}

// NewQueryPromQLTool builds a new BaseTool-shape query_promql tool. log
// may be nil (the tool degrades to slog.Default()).
func NewQueryPromQLTool(p PromQuerier, log *slog.Logger) *QueryPromQLTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryPromQLTool{promQuery: p, log: log}
}

// queryPromQLWhenToUse is the routing hint shown to the LLM under a
// "When to use" header in the system prompt. + —
// kept distinct from the Description field so skill manifests can
// override one without rewriting the other.
const queryPromQLWhenToUse = "When the user asks about metric values, time-series trends, " +
	"per-edge resource usage, or anything that boils down to a Prometheus range query. " +
	"NOT for log content (use query_logql) or filesystem state (use host-level tools). " +
	"For fleet / multi-device / multi-mountpoint questions, prefer one PromQL call with by(device_id, ...) " +
	"or topk/ranking over repeated per-device queries. " +
	"Prefer query_promql over the narrower get_host_load / get_process_list when the " +
	"question spans more than one host or asks for derivatives / aggregates."

// Info returns the tool metadata. — Info is pure (no I/O).
// The Class field marks this as "read" — query_promql never mutates
// state, so it's exempt from any future destructive-action gating.
func (t *QueryPromQLTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryPromQL,
		Description: QueryPromQLDescription,
		WhenToUse:   queryPromQLWhenToUse,
		Parameters:  QueryPromQLSchema,
		Class:       "read",
	}, nil
}

// InvokableRun runs the PromQL range query and returns its JSON response.
func (t *QueryPromQLTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.promQuery == nil {
		return "", fmt.Errorf("query_promql: prom query client not configured")
	}

	var in QueryPromQLArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_promql: bad args: %w", err)
	}
	if in.Expr == "" {
		return "", fmt.Errorf("query_promql: expr required")
	}
	if in.LookbackSeconds <= 0 {
		in.LookbackSeconds = 300
	}
	if in.LookbackSeconds > maxQueryPromQLLookbackSeconds {
		in.LookbackSeconds = maxQueryPromQLLookbackSeconds
	}

	end := basetool.ObservationTime(ctx)
	start := end.Add(-time.Duration(in.LookbackSeconds) * time.Second)
	step := stepFor(in.LookbackSeconds)

	// A closer caller deadline still takes precedence over this timeout.
	callCtx, cancel := context.WithTimeout(ctx, queryPromqlCallTimeout)
	defer cancel()

	res, err := t.promQuery.QueryRange(callCtx, in.Expr, start, end, step)
	if err != nil {
		return "", fmt.Errorf("query_promql: dispatch: %w", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("query_promql: marshal response: %w", err)
	}
	return string(out), nil
}
