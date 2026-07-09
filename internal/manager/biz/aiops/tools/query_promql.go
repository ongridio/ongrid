package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
const queryPromqlCallTimeout = 30 * time.Second

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

// executeQueryPromQL runs the PromQL range query and hands the raw Prom
// response back to the LLM via ResultJSON. EdgeID is intentionally left
// nil — query_promql is not bound to a specific edge.
func (r *Registry) executeQueryPromQL(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.promQuery == nil {
		// Should not happen — when promQuery is nil at NewRegistry the
		// tool is never registered. Defensive guard.
		return ExecuteResult{}, fmt.Errorf("query_promql: prom query client not configured")
	}
	var in QueryPromQLArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("query_promql: bad args: %w", err)
	}
	if in.Expr == "" {
		return ExecuteResult{}, fmt.Errorf("query_promql: expr required")
	}
	if in.LookbackSeconds <= 0 {
		in.LookbackSeconds = 300 // 5 min
	}
	if in.LookbackSeconds > maxQueryPromQLLookbackSeconds {
		in.LookbackSeconds = maxQueryPromQLLookbackSeconds
	}

	end := time.Now()
	start := end.Add(-time.Duration(in.LookbackSeconds) * time.Second)
	step := stepFor(in.LookbackSeconds)

	callCtx, cancel := context.WithTimeout(ctx, queryPromqlCallTimeout)
	defer cancel()

	res, err := r.promQuery.QueryRange(callCtx, in.Expr, start, end, step)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_promql: dispatch: %w", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_promql: marshal response: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}

// PromQuerier is the narrow surface the query_promql executor needs from
// the promquery client. Declared here so tests can inject a fake.
//
// NOTE: this interface is what r.promQuery is typed as. The concrete
// *promquery.Client satisfies it.
type PromQuerier interface {
	QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error)
	// Query is the instant form. correlate_incident.go uses it to grab
	// a single point-in-time vector for cpu_pct / mem_pct / up. Concrete
	// *promquery.Client satisfies it.
	Query(ctx context.Context, expr string, ts time.Time) (*promquery.InstantResult, error)
}
