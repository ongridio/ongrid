package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// QueryPromQLTool is the BaseTool 试点 implementation of query_promql,
// running in parallel with the closure-style executeQueryPromQL on the
// Registry (see registry.go). 改进点 #1 (每个 tool 是接口
// 对象 — struct 持依赖) — promQuery is held on the struct rather than
// captured in a closure.
//
// PR-3 of (this PR) migrates only this one tool to validate the
// pattern. The closure path stays so existing wiring + tests are
// unaffected; later PRs migrate the remaining 13 tools (
// #2 + the PR table in the same doc).
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

// InvokableRun parses argsJSON, runs the PromQL range query, and
// marshals the response back to a JSON string. The input/output shape
// matches the closure executor (executeQueryPromQL in query_promql.go)
// exactly — they consult the same QueryPromQLArgs / stepFor /
// queryPromqlCallTimeout — so the two paths return identical bytes for
// equivalent inputs.
//
// opts are accepted but ignored: query_promql is tenant-agnostic and
// not edge-scoped (DeviceID stays nil in the audit row). The decorator
// chain still consumes them upstream (tenant_bind for ratelimit/audit
// keying).
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

	end := time.Now()
	start := end.Add(-time.Duration(in.LookbackSeconds) * time.Second)
	step := stepFor(in.LookbackSeconds)

	// Mirror the closure executor's per-call timeout so behaviour is
	// identical when this tool is wired without the decorators.Timeout
	// wrapper. When the wrapper IS present, whichever ctx deadline
	// fires first wins — context.WithTimeout on a parent that already
	// has a closer deadline keeps the closer one.
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
