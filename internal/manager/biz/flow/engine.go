// engine.go — the DAG executor. Deterministic skeleton, probabilistic
// node interiors (HLD-016): the engine itself never asks an LLM
// anything; only agent-node interiors are non-deterministic.
//
// Scheduling semantics (MVP):
//   - execution starts at every trigger node;
//   - when a node finishes it fires ONE control port; every edge on
//     that port activates its target (fan-out runs branches
//     concurrently, capped);
//   - OR-join + execute-once: a node runs the first time any incoming
//     edge fires, later activations are no-ops. No parallel-join /
//     merge node yet (P2).
//   - a node error fires its "error" port if connected, otherwise the
//     run fails (other in-flight branches finish, then the run is
//     marked failed).
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

// maxConcurrentNodes caps fan-out so a wide graph can't spawn an
// unbounded number of agent workers at once.
const maxConcurrentNodes = 4

var flowTracer = otel.Tracer("github.com/ongridio/ongrid/internal/manager/biz/flow")

// Engine executes a parsed graph against a run row.
type Engine struct {
	exec Executors
	runs RunRepo
	log  *slog.Logger
}

// NewEngine wires the executor seams + run persistence.
func NewEngine(exec Executors, runs RunRepo, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{exec: exec, runs: runs, log: log}
}

type runState struct {
	mu       sync.Mutex
	rc       *RunContext
	executed map[string]bool
	failed   bool
	firstErr error
	wg       sync.WaitGroup
	sem      chan struct{}
}

// Execute runs the graph to completion and returns the terminal run
// status. It is synchronous — the usecase calls it from a goroutine.
//
// entryType scopes which trigger nodes start the run: "" starts every
// trigger (legacy manual behaviour), a specific type (e.g.
// trigger.alert_fired) starts only matching triggers so a flow with both
// a manual and an alert trigger doesn't double-fire the manual branch on
// an alert.
func (e *Engine) Execute(ctx context.Context, run *model.FlowRun, g *Graph, entryType string) (status string, runErr error) {
	ctx, span := flowTracer.Start(ctx, "flow.Engine.Execute",
		trace.WithAttributes(attribute.String("flow.entry_type", entryTypeLabel(entryType))),
	)
	defer func() {
		span.SetAttributes(attribute.String("flow.status", status))
		tracing.EndSpan(span, runErr)
	}()
	defer func() {
		if r := recover(); r != nil {
			status = model.RunStatusFailed
			runErr = fmt.Errorf("engine panic: %v", r)
			e.log.Error("flow engine panic", slog.String("run_id", run.ID), slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
		}
	}()

	all := g.Triggers()
	triggers := all[:0:0]
	for _, t := range all {
		if entryType == "" || t.Type == entryType {
			triggers = append(triggers, t)
		}
	}
	if len(triggers) == 0 {
		return model.RunStatusFailed, fmt.Errorf("graph has no %s trigger node", entryTypeLabel(entryType))
	}

	var trigger map[string]any
	if run.TriggerJSON != "" {
		_ = json.Unmarshal([]byte(run.TriggerJSON), &trigger)
	}
	st := &runState{
		rc:       &RunContext{Trigger: trigger, Nodes: map[string]any{}, Vars: map[string]any{}},
		executed: map[string]bool{},
		sem:      make(chan struct{}, maxConcurrentNodes),
	}

	byID := make(map[string]GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	for _, t := range triggers {
		e.activate(ctx, run, g, byID, st, t.ID)
	}
	st.wg.Wait()

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.failed {
		return model.RunStatusFailed, st.firstErr
	}
	return model.RunStatusSucceeded, nil
}

// activate schedules node id if it hasn't executed yet.
func (e *Engine) activate(ctx context.Context, run *model.FlowRun, g *Graph, byID map[string]GraphNode, st *runState, id string) {
	st.mu.Lock()
	// Execute-once OR-join; after a run-level failure no NEW nodes
	// start (handled error-port branches never set failed, so their
	// handlers activate normally).
	if st.executed[id] || st.failed {
		st.mu.Unlock()
		return
	}
	st.executed[id] = true
	st.mu.Unlock()

	node, ok := byID[id]
	if !ok {
		return
	}
	st.wg.Add(1)
	go func() {
		defer st.wg.Done()
		// Per-goroutine recover: the Execute-level recover only guards the
		// main goroutine (trigger activation + wg.Wait); a panic in THIS
		// fan-out goroutine would otherwise crash the whole manager
		// process. Mark the run failed and let in-flight branches drain.
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("flow node panic",
					slog.String("run_id", run.ID),
					slog.String("node_id", node.ID),
					slog.String("node_type", node.Type),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())))
				st.mu.Lock()
				if !st.failed {
					st.failed = true
					st.firstErr = fmt.Errorf("node %s (%s) panic: %v", node.ID, node.Type, r)
				}
				st.mu.Unlock()
			}
		}()
		st.sem <- struct{}{}
		defer func() { <-st.sem }()
		e.runNode(ctx, run, g, byID, st, node)
	}()
}

// runNode resolves config, executes, persists the FlowRunNode row, and
// fires the resulting control port.
func (e *Engine) runNode(ctx context.Context, run *model.FlowRun, g *Graph, byID map[string]GraphNode, st *runState, node GraphNode) {
	ctx, span := flowTracer.Start(ctx, "flow.Node."+node.Type,
		trace.WithAttributes(attribute.String("flow.node.type", node.Type)),
	)
	var execErr error
	defer func() { tracing.EndSpan(span, execErr) }()
	started := time.Now().UTC()
	row := &model.FlowRunNode{
		RunID:    run.ID,
		NodeID:   node.ID,
		NodeType: node.Type,
		NodeName: node.Name,
		Status:   model.NodeStatusRunning,
		// TEXT NOT NULL columns — always supply a value.
		InputJSON:  "{}",
		OutputJSON: "{}",
		StartedAt:  &started,
	}

	// Resolve config templates under the lock (context reads), execute
	// outside it (slow: agents/tools). The executor gets a private copy of
	// the context: other branches keep writing the shared maps meanwhile.
	var cfg map[string]any
	var resolveErr error
	st.mu.Lock()
	if len(node.Config) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(node.Config, &raw); err != nil {
			resolveErr = fmt.Errorf("node %s: config: %w", node.ID, err)
		} else if resolved, err := st.rc.ResolveValue(raw); err != nil {
			resolveErr = fmt.Errorf("node %s: %w", node.ID, err)
		} else {
			cfg, _ = resolved.(map[string]any)
		}
	}
	rcSnapshot := st.rc.snapshot()
	st.mu.Unlock()
	if cfg == nil {
		cfg = map[string]any{}
	}
	if b, err := json.Marshal(cfg); err == nil {
		row.InputJSON = string(b)
	}
	if e.runs != nil {
		_ = e.runs.CreateNode(ctx, row)
	}

	var res NodeResult
	if resolveErr != nil {
		execErr = resolveErr
	} else {
		res, execErr = e.exec.execute(ctx, node, cfg, rcSnapshot)
	}

	finished := time.Now().UTC()
	row.FinishedAt = &finished
	if execErr != nil {
		row.Status = model.NodeStatusFailed
		row.Error = truncate(execErr.Error(), 2000)
		row.FiredPort = PortError
	} else {
		row.Status = model.NodeStatusSucceeded
		row.FiredPort = res.Port
		st.mu.Lock()
		st.rc.Nodes[node.ID] = res.Output
		// Var writes (set node) are applied here under the lock — executors
		// run outside it, so this is the ONLY place rc.Vars is mutated.
		for k, v := range res.Vars {
			st.rc.Vars[k] = v
		}
		st.mu.Unlock()
		if b, err := json.Marshal(res.Output); err == nil {
			row.OutputJSON = string(b)
		}
	}
	if e.runs != nil {
		_ = e.runs.UpdateNode(ctx, row)
	}

	if execErr != nil {
		targets := g.EdgesFrom(node.ID, PortError)
		if len(targets) == 0 {
			st.mu.Lock()
			if !st.failed {
				st.failed = true
				st.firstErr = fmt.Errorf("node %s (%s): %w", node.ID, node.Type, execErr)
			}
			st.mu.Unlock()
			return
		}
		// Handled error: expose it to the handler branch then continue.
		st.mu.Lock()
		st.rc.Nodes[node.ID] = map[string]any{"error": execErr.Error()}
		st.mu.Unlock()
		for _, t := range targets {
			e.activate(ctx, run, g, byID, st, t)
		}
		return
	}
	for _, t := range g.EdgesFrom(node.ID, res.Port) {
		e.activate(ctx, run, g, byID, st, t)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// entryTypeLabel renders entryType for error messages ("any" when empty).
func entryTypeLabel(t string) string {
	if t == "" {
		return "any"
	}
	return t
}

// RunSingle executes ONE node in isolation (the node-level "test run" the
// editor uses to surface a node's real output before it's wired into the
// flow). It resolves the node's config against rc, runs the executor, and
// returns the result — no persistence, no edge traversal.
func (e *Engine) RunSingle(ctx context.Context, node GraphNode, rc *RunContext) (result NodeResult, retErr error) {
	ctx, span := flowTracer.Start(ctx, "flow.Node."+node.Type,
		trace.WithAttributes(attribute.String("flow.node.type", node.Type)),
	)
	defer func() { tracing.EndSpan(span, retErr) }()
	var cfg map[string]any
	if len(node.Config) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(node.Config, &raw); err != nil {
			return NodeResult{}, fmt.Errorf("config: %w", err)
		}
		resolved, err := rc.ResolveValue(raw)
		if err != nil {
			return NodeResult{}, err
		}
		cfg, _ = resolved.(map[string]any)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return e.exec.execute(ctx, node, cfg, rc)
}
