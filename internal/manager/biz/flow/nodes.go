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

// NodeResult is what an executor returns: the data output that lands
// in the run context plus the control port that fired.
type NodeResult struct {
	Output any
	Port   string
}

// Executors bundles the seams. Zero value works for engine tests.
type Executors struct {
	Agent  AgentRunner
	Tools  ToolInvoker
	Notify Notifier
}

// defaultNodeTimeout bounds every non-agent node. Agent nodes get the
// longer budget — a ReAct worker legitimately runs minutes.
const (
	defaultNodeTimeout = 2 * time.Minute
	agentNodeTimeout   = 15 * time.Minute
)

// execute runs one node. cfg is the node's config AFTER expression
// resolution (every string leaf already substituted).
func (x Executors) execute(ctx context.Context, node GraphNode, cfg map[string]any, rc *RunContext) (NodeResult, error) {
	switch node.Type {
	case NodeTriggerManual:
		// The trigger's "output" is the trigger payload itself so
		// downstream can use either {{trigger.x}} or {{nodes.<id>.output.x}}.
		return NodeResult{Output: anyMap(rc.Trigger), Port: PortNext}, nil

	case NodeAgent:
		if x.Agent == nil {
			return NodeResult{}, fmt.Errorf("agent node: runner not wired")
		}
		persona, _ := cfg["persona"].(string)
		if persona == "" {
			persona = "default"
		}
		instruction, _ := cfg["instruction"].(string)
		if strings.TrimSpace(instruction) == "" {
			return NodeResult{}, fmt.Errorf("agent node: instruction is empty")
		}
		// Structured gateway (HLD-016): with an output_schema the agent
		// is instructed to answer ONLY the JSON object; we parse it into
		// output.structured so deterministic downstream (condition /
		// tool args) can reference fields. Without one the answer is
		// free text — consumable by agents / notify / humans only.
		schema, hasSchema := cfg["output_schema"].(map[string]any)
		prompt := instruction
		if hasSchema {
			sb, _ := json.Marshal(schema)
			prompt = instruction + "\n\nReturn ONLY a single JSON object matching this JSON Schema (no prose, no code fence):\n" + string(sb)
		}
		actx, cancel := context.WithTimeout(ctx, agentNodeTimeout)
		defer cancel()
		answer, err := x.Agent.RunAgent(actx, persona, prompt)
		if err != nil {
			return NodeResult{}, fmt.Errorf("agent node: %w", err)
		}
		out := map[string]any{"answer": answer}
		if hasSchema {
			structured, perr := parseLooseJSON(answer)
			if perr != nil {
				return NodeResult{}, fmt.Errorf("agent node: output_schema declared but answer is not JSON: %w", perr)
			}
			out["structured"] = structured
		}
		return NodeResult{Output: out, Port: PortNext}, nil

	case NodeTool:
		if x.Tools == nil {
			return NodeResult{}, fmt.Errorf("tool node: invoker not wired")
		}
		name, _ := cfg["tool"].(string)
		if name == "" {
			return NodeResult{}, fmt.Errorf("tool node: missing tool name")
		}
		args, _ := cfg["args"].(map[string]any)
		ab, err := json.Marshal(args)
		if err != nil {
			return NodeResult{}, fmt.Errorf("tool node: args: %w", err)
		}
		tctx, cancel := context.WithTimeout(ctx, defaultNodeTimeout)
		defer cancel()
		res, err := x.Tools.InvokeTool(tctx, name, ab)
		if err != nil {
			return NodeResult{}, fmt.Errorf("tool %s: %w", name, err)
		}
		var out any
		if len(res) > 0 {
			if uerr := json.Unmarshal(res, &out); uerr != nil {
				out = string(res) // non-JSON tool output stays a string
			}
		}
		return NodeResult{Output: map[string]any{"result": out}, Port: PortNext}, nil

	case NodeCondition:
		expr, _ := cfg["expr"].(string)
		if strings.TrimSpace(expr) == "" {
			return NodeResult{}, fmt.Errorf("condition node: missing expr")
		}
		ok, err := rc.EvalCondition(expr)
		if err != nil {
			return NodeResult{}, err
		}
		port := PortFalse
		if ok {
			port = PortTrue
		}
		return NodeResult{Output: map[string]any{"result": ok}, Port: port}, nil

	case NodeNotify:
		if x.Notify == nil {
			return NodeResult{}, fmt.Errorf("notify node: notifier not wired")
		}
		title, _ := cfg["title"].(string)
		message, _ := cfg["message"].(string)
		if strings.TrimSpace(message) == "" {
			return NodeResult{}, fmt.Errorf("notify node: message is empty")
		}
		ids := toUint64s(cfg["channel_ids"])
		if len(ids) == 0 {
			return NodeResult{}, fmt.Errorf("notify node: no channel_ids")
		}
		nctx, cancel := context.WithTimeout(ctx, defaultNodeTimeout)
		defer cancel()
		if err := x.Notify.Notify(nctx, ids, title, message); err != nil {
			return NodeResult{}, fmt.Errorf("notify node: %w", err)
		}
		return NodeResult{Output: map[string]any{"sent": true, "channels": len(ids)}, Port: PortNext}, nil

	case NodeSet:
		name, _ := cfg["name"].(string)
		if name == "" {
			return NodeResult{}, fmt.Errorf("set node: missing var name")
		}
		val := cfg["value"]
		rc.Vars[name] = val
		return NodeResult{Output: map[string]any{"name": name, "value": val}, Port: PortNext}, nil
	}
	return NodeResult{}, fmt.Errorf("unknown node type %q", node.Type)
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
