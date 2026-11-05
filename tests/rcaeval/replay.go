package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	toolkit "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

var errBudget = errors.New("evaluation budget exhausted")

// Declared variants normalize only JSON formatting and documented defaults.
func queryKey(name string, raw json.RawMessage) (string, error) {
	var args map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&args); err != nil || args == nil || !json.Valid(raw) {
		return "", fmt.Errorf("%s arguments must be a JSON object", name)
	}
	// Only documented defaults are normalized. Device selectors, time windows,
	// expressions, array order and unknown fields remain part of the match.
	defaults := map[string]map[string]any{
		"query_promql":       {"lookback_seconds": json.Number("300")},
		"get_host_processes": {"top_n": json.Number("10"), "sort_by": "cpu"},
		"query_logql":        {"limit": json.Number("200"), "direction": "backward"},
	}
	for k, v := range defaults[name] {
		if _, ok := args[k]; !ok {
			args[k] = v
		}
	}
	b, err := json.Marshal(args)
	return name + ":" + string(b), err
}

type replayCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Fixture   string          `json:"fixture,omitempty"`
	SessionID string          `json:"session_id"`
	Error     string          `json:"error,omitempty"`
}

type replayState struct {
	mu       sync.Mutex
	c        evalCase
	fixtures map[string]fixture
	calls    []replayCall
	maxCalls int
	exceeded bool
}

func newReplay(c evalCase, max int) (*replayState, error) {
	r := &replayState{c: c, fixtures: map[string]fixture{}, maxCalls: max}
	for _, f := range c.Fixtures {
		if len(f.Result) > 0 {
			var compact bytes.Buffer
			if err := json.Compact(&compact, f.Result); err != nil {
				return nil, err
			}
			f.Result = compact.Bytes()
		}
		for _, a := range f.Args {
			key, err := queryKey(f.Tool, a)
			if err != nil {
				return nil, err
			}
			r.fixtures[key] = f
		}
	}
	return r, nil
}

type replayTool struct {
	info     *basetool.ToolInfo
	state    *replayState
	delegate basetool.BaseTool
}

func (t *replayTool) Info(context.Context) (*basetool.ToolInfo, error) { return t.info, nil }

func (t *replayTool) InvokableRun(ctx context.Context, args string, opts ...basetool.InvokeOption) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key, keyErr := queryKey(t.info.Name, json.RawMessage(args))
	c := replayCall{Tool: t.info.Name, Arguments: json.RawMessage(args), SessionID: basetool.SessionIDFromContext(ctx)}
	if !json.Valid(c.Arguments) {
		c.Arguments, _ = json.Marshal(args)
	}
	t.state.mu.Lock()
	if len(t.state.calls) >= t.state.maxCalls {
		t.state.exceeded = true
		t.state.mu.Unlock()
		return "", errBudget
	}
	f, found := t.state.fixtures[key]
	if !found && keyErr == nil && t.info.Name == "query_logql" {
		f, found = replayLogs(ctx, t.state.c, args)
	}
	if t.delegate != nil {
		c.Fixture = "delegation"
	} else if keyErr != nil || !found {
		c.Error = "fixture_coverage_gap"
	} else {
		c.Fixture, c.Error = f.ID, f.Error
	}
	t.state.calls = append(t.state.calls, c)
	t.state.mu.Unlock()
	if t.delegate != nil {
		return t.delegate.InvokableRun(ctx, args, opts...)
	}
	if c.Error == "fixture_coverage_gap" {
		return "", fmt.Errorf("fixture_coverage_gap: no observation for these %s arguments; do not infer a result", t.info.Name)
	}
	if f.Error == "timeout" {
		return "", context.DeadlineExceeded
	}
	if f.Error != "" {
		return "", errors.New(f.Error)
	}
	return string(f.Result), nil
}

// The catalog supplies schemas. Replay uses fixtures or the bounded local log
// backend; no host or external backend is connected.
func toolCatalog() []basetool.BaseTool {
	return []basetool.BaseTool{
		toolkit.NewGetIncidentDetailTool(nil, nil),
		toolkit.NewCorrelateIncidentTool(nil, nil, nil, nil, nil, nil, nil),
		toolkit.NewQueryPromQLTool(nil, nil), toolkit.NewQueryLogQLTool(nil),
		toolkit.NewGetHostLoadTool(nil, nil, nil, nil), toolkit.NewGetProcessListTool(nil, nil, nil, nil),
		toolkit.NewFindLargeFilesTool(nil, nil, nil, nil), toolkit.NewDuSummaryTool(nil, nil, nil, nil),
		toolkit.NewStatFileTool(nil, nil, nil, nil), toolkit.NewQueryTraceQLTool(nil, nil),
		toolkit.NewQueryChangeEventsTool(nil, nil), toolkit.NewExpandTopologyTool(nil, nil, nil),
	}
}

func (r *replayState) tools(ctx context.Context) ([]basetool.BaseTool, error) {
	var out []basetool.BaseTool
	known := map[string]bool{}
	for _, t := range toolCatalog() {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		known[info.Name] = true
		out = append(out, &replayTool{info: info, state: r})
	}
	for _, f := range r.fixtures {
		if !known[f.Tool] {
			return nil, fmt.Errorf("unknown fixture tool %s", f.Tool)
		}
	}
	return out, nil
}
