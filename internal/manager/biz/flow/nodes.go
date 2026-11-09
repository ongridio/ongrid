// nodes.go — the node executors and the seams they call through.
// Each executor: resolved config in → (data output, control port) out.
// Seams are wired in cmd/ongrid/main.go over the existing subsystems
// (chatruntime worker spawn, tools.Registry, notification channels) —
// nil seams degrade that node type to a config-time error, the engine
// itself stays testable with fakes.
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AgentRunner runs one synchronous agent worker and returns its final
// answer. Implemented in main.go over chatruntime.Runtime.SpawnWorker
// (mirrors biz/alert/investigator.WorkerSpawner).
type AgentRunner interface {
	RunAgent(ctx context.Context, persona, prompt string) (answer string, err error)
}

// ToolInvoker dispatches one BaseTool call by name. Implemented over
// tools.Registry.Invoke.
type ToolInvoker interface {
	InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// Notifier fans a message out to notification channels. Mirrors
// biz/report.Deliverer (implemented in main.go over the notify router
// + channel store).
type Notifier interface {
	Notify(ctx context.Context, channelIDs []uint64, title, message string) error
}

// LLMRunner does ONE chat completion (system + user → text), no tools,
// no persona, no multi-turn. The `llm` node uses it for cheap pure-text
// transforms (summarize / rewrite / classify / extract) where a full
// ReAct agent node is overkill. Implemented in main.go over the routing
// llm.Client (provider/model omitted → DefaultResolver picks the
// configured default). The client owns the request timeout. nil → the
// llm node degrades with a clear error.
type LLMRunner interface {
	RunLLM(ctx context.Context, system, user string) (string, error)
}

// NodeResult is what an executor returns: the data output that lands
// in the run context plus the control port that fired.
type NodeResult struct {
	Output any
	Port   string
	// Vars carries set-node variable writes. The engine applies them to
	// the shared RunContext UNDER ITS LOCK — executors run OUTSIDE the
	// engine lock (concurrent fan-out), so an executor must never mutate
	// rc.Vars directly or it races every other branch. nil for the common
	// case of a node that sets no vars.
	Vars map[string]any
}

// Executors bundles the seams. Zero value works for engine tests.
type Executors struct {
	Agent  AgentRunner
	LLM    LLMRunner
	Tools  ToolInvoker
	Notify Notifier
}

// Tool and notify nodes use the default timeout; agent workers get a
// longer budget. LLM nodes use the client's configured request timeout.
const (
	defaultNodeTimeout = 2 * time.Minute
	agentNodeTimeout   = 15 * time.Minute
)

// execute runs one node by dispatching to its registered NodeSpec. cfg is
// the node's config AFTER expression resolution. The per-type behaviour
// lives in noderegistry.go — this is pure dispatch, no switch.
func (x Executors) execute(ctx context.Context, node GraphNode, cfg map[string]any, rc *RunContext) (NodeResult, error) {
	spec := LookupNode(node.Type)
	if spec == nil || spec.Execute == nil {
		return NodeResult{}, fmt.Errorf("unknown node type %q", node.Type)
	}
	return spec.Execute(ctx, x, cfg, rc)
}

// parseLooseJSON accepts a raw JSON object possibly wrapped in a code
// fence or surrounded by stray prose — models do that even when told
// not to. It extracts the outermost {...} and decodes it.
func parseLooseJSON(s string) (map[string]any, error) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func toUint64s(v any) []uint64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]uint64, 0, len(arr))
	for _, e := range arr {
		if f, ok := toFloat(e); ok && f > 0 {
			out = append(out, uint64(f))
		}
	}
	return out
}
