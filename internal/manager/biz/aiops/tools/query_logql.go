package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

// ToolNameQueryLogQL is the stable wire name the LLM sees for the LogQL tool.
const ToolNameQueryLogQL = "query_logql"

// QueryLogQLDescription is the single-sentence description shown to the LLM.
// Phrased to push the model toward this tool whenever raw log inspection
// is needed beyond what the metric-side tools can express.
const QueryLogQLDescription = "Run a LogQL range query against Loki. " +
	"Use this to investigate log patterns, error counts, or pipe into per-edge filters. " +
	"Returns the raw Loki response (streams or matrix)."

func parseLogQLTime(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "now") {
		return fallback, nil
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(value), "now-"); ok {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return time.Time{}, err
		}
		return time.Now().Add(-d), nil
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(value), "now+"); ok {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return time.Time{}, err
		}
		return time.Now().Add(d), nil
	}
	return time.Parse(time.RFC3339, value)
}

// QueryLogQLSchema is the JSON Schema of the tool's argument object.
var QueryLogQLSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "LogQL expression. Example: \"{edge_id=\\\"1\\\"} |= \\\"error\\\"\"."
    },
    "start": {
      "type": "string",
      "description": "RFC3339 start time. Defaults to now-1h."
    },
    "end": {
      "type": "string",
      "description": "RFC3339 end time. Defaults to now."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 5000,
      "description": "Max number of result rows (default 200)."
    },
    "direction": {
      "type": "string",
      "enum": ["backward", "forward"],
      "description": "Order of results, default \"backward\" (newest first)."
    }
  },
  "required": ["query"]
}`)

// QueryLogQLArgs is the typed form of QueryLogQLSchema.
type QueryLogQLArgs struct {
	Query     string `json:"query"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Direction string `json:"direction,omitempty"`
}

// queryLogqlCallTimeout caps how long a single dispatch may wait. Mirrors
// query_promql for symmetry across signal types.
const queryLogqlCallTimeout = 30 * time.Second

// executeQueryLogQL runs the LogQL range query and hands the raw Loki
// response back to the LLM via ResultJSON.
func (r *Registry) executeQueryLogQL(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.logQuery == nil {
		// Should not happen — when logQuery is nil at NewRegistry the
		// tool is never registered. Defensive guard.
		return ExecuteResult{}, fmt.Errorf("query_logql: log query client not configured")
	}
	var in QueryLogQLArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("query_logql: bad args: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return ExecuteResult{}, fmt.Errorf("query_logql: query required")
	}

	end := time.Now()
	start := end.Add(-time.Hour)
	if in.End != "" {
		t, err := parseLogQLTime(in.End, end)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("query_logql: parse end: %w", err)
		}
		end = t
	}
	if in.Start != "" {
		t, err := parseLogQLTime(in.Start, start)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("query_logql: parse start: %w", err)
		}
		start = t
	} else if in.End != "" {
		// User pinned end but not start — keep the 1h window relative to
		// the supplied end so the call is still bounded.
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

	res, err := r.logQuery.QueryRange(callCtx, logquery.QueryRangeOptions{
		Query:     in.Query,
		Start:     start,
		End:       end,
		Limit:     limit,
		Direction: direction,
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_logql: dispatch: %w", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_logql: marshal response: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}

// LogQuerier is the narrow surface the query_logql executor needs from the
// logquery client. Declared here so tests can inject a fake.
//
// NOTE: this interface is what r.logQuery is typed as. The concrete
// *logquery.Client satisfies it.
type LogQuerier interface {
	QueryRange(ctx context.Context, opts logquery.QueryRangeOptions) (*logquery.QueryRangeResult, error)
}
