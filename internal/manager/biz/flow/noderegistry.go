// noderegistry.go — the node-type registry (HLD-016 node abstraction).
//
// A node type is a first-class, self-describing entity: one NodeSpec
// declares its structural behaviour (Kind), palette grouping (Category),
// control ports, and executor. The engine, graph validation, and trigger
// detection all DERIVE from the registry — there is no per-type switch,
// no "trigger." string-prefix convention, no knownTypes map. Adding a node
// type = RegisterNode(one spec); nothing in the core engine changes.
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NodeKind classifies a node's structural behaviour so the engine and
// validator never special-case by Type string.
type NodeKind string

const (
	KindTrigger NodeKind = "trigger" // entry point: no inbound edge, no error port
	KindAction  NodeKind = "action"  // calls a capability: agent / llm / tool / notify
	KindControl NodeKind = "control" // branches/merges the flow: condition / (future) merge
	KindData    NodeKind = "data"    // shapes data: set / (future) transform
)

// ExecuteFunc is the universal node contract: resolved config + read-only
// run context → (data output, control port) | error. Stateless — the
// Executors seam bundle is passed in, not captured, so a spec can be
// registered globally.
type ExecuteFunc func(ctx context.Context, x Executors, cfg map[string]any, rc *RunContext) (NodeResult, error)

// ConfigFieldSpec declares one config form field — the frontend renders
// the config drawer from these instead of hardcoding per type.
type ConfigFieldSpec struct {
	Key         string   `json:"key"`
	LabelZh     string   `json:"label_zh"`
	LabelEn     string   `json:"label_en"`
	Kind        string   `json:"kind"` // text / textarea / json / select
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"` // for kind=select
}

// NodeSpec is the complete declaration of one node type — structural
// behaviour, palette presentation, config form, and output shape. The
// engine, validator, AND frontend all derive from it (the frontend keeps
// only a type→icon/color visual map).
type NodeSpec struct {
	Type         string            // wire type ("tool" / "llm" / "transform")
	Kind         NodeKind          // structural behaviour
	Category     string            // palette grouping (intent-based)
	LabelZh      string            // display name (zh)
	LabelEn      string            // display name (en)
	Ports        []string          // control output ports (default [next]; condition=[true,false])
	ConfigFields []ConfigFieldSpec // config form fields (empty for tool: args come from BaseTool schema)
	OutputShape  []string          // static output field paths ([] when dynamic, e.g. transform/agent)
	Execute      ExecuteFunc       // executor
}

var nodeRegistry = map[string]*NodeSpec{}

// RegisterNode adds (or replaces) a node type. Ports defaults to [next].
func RegisterNode(s *NodeSpec) {
	if s == nil || s.Type == "" {
		return
	}
	if len(s.Ports) == 0 {
		s.Ports = []string{PortNext}
	}
	nodeRegistry[s.Type] = s
}

// LookupNode returns the spec for a type, or nil if unregistered.
func LookupNode(t string) *NodeSpec { return nodeRegistry[t] }

// AllNodeSpecs returns every registered spec (palette / node-types API).
func AllNodeSpecs() []*NodeSpec {
	out := make([]*NodeSpec, 0, len(nodeRegistry))
	for _, s := range nodeRegistry {
		out = append(out, s)
	}
	return out
}

func init() { registerBuiltins() }

// registerBuiltins wires the built-in node types. New built-ins land here;
// dynamically-discovered types (tools come from BaseTool schemas) keep the
// single `tool` spec and select via config.tool.
func registerBuiltins() {
	RegisterNode(&NodeSpec{
		Type: NodeTriggerManual, Kind: KindTrigger, Category: "trigger",
		LabelZh: "手动触发", LabelEn: "Manual trigger", Execute: execTrigger,
	})
	RegisterNode(&NodeSpec{
		Type: NodeTriggerAlert, Kind: KindTrigger, Category: "trigger",
		LabelZh: "告警触发", LabelEn: "On alert",
		ConfigFields: []ConfigFieldSpec{
			{Key: "rule", LabelZh: "规则名包含（留空=所有告警）", LabelEn: "Rule name contains (blank = all alerts)", Kind: "text", Placeholder: "如 disk / cpu"},
			{Key: "min_severity", LabelZh: "最低严重度（warning/error/critical，留空=不限）", LabelEn: "Min severity (warning/error/critical; blank = any)", Kind: "text", Placeholder: "critical"},
		},
		OutputShape: []string{"incident_id", "rule", "severity", "edge_id", "device_id", "labels", "fired_at"},
		Execute:     execTrigger,
	})
	RegisterNode(&NodeSpec{
		Type: NodeTriggerCron, Kind: KindTrigger, Category: "trigger",
		LabelZh: "定时触发", LabelEn: "On schedule",
		ConfigFields: []ConfigFieldSpec{
			{Key: "cron", LabelZh: "定时表达式（标准 5 段 cron，UTC）", LabelEn: "Cron schedule (standard 5-field, UTC)", Kind: "text", Placeholder: "0 8 * * *  (每天 UTC 08:00)"},
		},
		OutputShape: []string{"fired_at", "cron"},
		Execute:     execTrigger,
	})
	RegisterNode(&NodeSpec{
		Type: NodeAgent, Kind: KindAction, Category: "ai",
		LabelZh: "Agent（自主）", LabelEn: "Agent",
		ConfigFields: []ConfigFieldSpec{
			{Key: "persona", LabelZh: "角色 (persona)", LabelEn: "Persona", Kind: "text", Placeholder: "default / specialist-network / …"},
			{Key: "instruction", LabelZh: "指令（支持 {{…}} 模板）", LabelEn: "Instruction ({{…}} templates)", Kind: "textarea", Placeholder: "诊断 {{trigger.host}} 上的磁盘告警…"},
			{Key: "output_schema", LabelZh: "输出 schema（可选，JSON Schema。声明后下游才能引用 structured 字段）", LabelEn: "Output schema (optional; required for structured downstream refs)", Kind: "json"},
		},
		OutputShape: []string{"answer"},
		Execute:     execAgent,
	})
	RegisterNode(&NodeSpec{
		Type: NodeLLM, Kind: KindAction, Category: "ai",
		LabelZh: "LLM（单次）", LabelEn: "LLM",
		ConfigFields: []ConfigFieldSpec{
			{Key: "system", LabelZh: "系统提示（可选）", LabelEn: "System prompt (optional)", Kind: "textarea", Placeholder: "你是运维助手，简洁回答。"},
			{Key: "prompt", LabelZh: "提示词（支持 {{…}} 模板）", LabelEn: "Prompt ({{…}} templates)", Kind: "textarea", Placeholder: "把这段诊断总结成一句话：{{nodes.diag.output.answer}}"},
			{Key: "output_schema", LabelZh: "输出 schema（可选，JSON Schema。声明后下游才能引用 structured 字段）", LabelEn: "Output schema (optional; required for structured downstream refs)", Kind: "json"},
		},
		OutputShape: []string{"answer"},
		Execute:     execLLM,
	})
	RegisterNode(&NodeSpec{
		Type: NodeTool, Kind: KindAction, Category: "action",
		LabelZh: "工具", LabelEn: "Tool",
		OutputShape: []string{"result"}, // args form comes from the BaseTool schema, not ConfigFields
		Execute:     execTool,
	})
	RegisterNode(&NodeSpec{
		Type: NodeCondition, Kind: KindControl, Category: "flow",
		LabelZh: "条件", LabelEn: "Condition", Ports: []string{PortTrue, PortFalse},
		ConfigFields: []ConfigFieldSpec{
			{Key: "expr", LabelZh: "表达式", LabelEn: "Expression", Kind: "text", Placeholder: `{{nodes.diag.output.structured.severity}} == "critical"`},
		},
		OutputShape: []string{"result"},
		Execute:     execCondition,
	})
	RegisterNode(&NodeSpec{
		Type: NodeNotify, Kind: KindAction, Category: "action",
		LabelZh: "通知", LabelEn: "Notify",
		ConfigFields: []ConfigFieldSpec{
			{Key: "channel_ids", LabelZh: "渠道 ID（JSON 数组）", LabelEn: "Channel ids (JSON array)", Kind: "json", Placeholder: "[1]"},
			{Key: "title", LabelZh: "标题", LabelEn: "Title", Kind: "text"},
			{Key: "message", LabelZh: "内容（支持 {{…}}）", LabelEn: "Message ({{…}} templates)", Kind: "textarea"},
		},
		OutputShape: []string{"sent", "channels"},
		Execute:     execNotify,
	})
	RegisterNode(&NodeSpec{
		Type: NodeSet, Kind: KindData, Category: "data",
		LabelZh: "变量", LabelEn: "Set var",
		ConfigFields: []ConfigFieldSpec{
			{Key: "name", LabelZh: "变量名", LabelEn: "Variable name", Kind: "text"},
			{Key: "value", LabelZh: "值（支持 {{…}}）", LabelEn: "Value ({{…}} templates)", Kind: "text"},
		},
		OutputShape: []string{"name", "value"},
		Execute:     execSet,
	})
	RegisterNode(&NodeSpec{
		Type: NodeTransform, Kind: KindData, Category: "data",
		LabelZh: "字段映射", LabelEn: "Edit Fields",
		ConfigFields: []ConfigFieldSpec{
			{Key: "fields", LabelZh: "字段映射（JSON，每个字段值支持 {{…}}）。把上游数据重组成下游需要的字段。", LabelEn: "Field mapping (JSON; each value accepts {{…}}). Reshape upstream data into the fields a downstream node needs.", Kind: "json"},
		},
		// OutputShape dynamic (the declared field names); frontend reads config.fields keys.
		Execute: execTransform,
	})
	RegisterNode(&NodeSpec{
		Type: NodeHTTP, Kind: KindAction, Category: "action",
		LabelZh: "HTTP 请求", LabelEn: "HTTP Request",
		ConfigFields: []ConfigFieldSpec{
			{Key: "method", LabelZh: "方法", LabelEn: "Method", Kind: "select", Options: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
			{Key: "url", LabelZh: "URL（支持 {{…}}）", LabelEn: "URL ({{…}} templates)", Kind: "text", Placeholder: "https://api.example.com/v1/{{nodes.a.output.result.id}}"},
			{Key: "headers", LabelZh: "请求头（JSON 对象，值支持 {{…}}）", LabelEn: "Headers (JSON object; values accept {{…}})", Kind: "json", Placeholder: `{"Authorization": "Bearer {{vars.token}}"}`},
			{Key: "body", LabelZh: "请求体（JSON / 文本，支持 {{…}}）", LabelEn: "Body (JSON / text; {{…}} templates)", Kind: "textarea", Placeholder: `{"text": "{{nodes.diag.output.answer}}"}`},
			{Key: "timeout_seconds", LabelZh: "超时秒数（默认 30，最大 120）", LabelEn: "Timeout seconds (default 30, max 120)", Kind: "text", Placeholder: "30"},
		},
		OutputShape: []string{"status", "body", "headers"},
		Execute:     execHTTP,
	})
}

// --- built-in executors (migrated verbatim from the old execute switch) ---

func execTrigger(_ context.Context, _ Executors, _ map[string]any, rc *RunContext) (NodeResult, error) {
	// Every trigger's "output" is the trigger payload itself so downstream
	// can use either {{trigger.x}} or {{nodes.<id>.output.x}}. The payload
	// differs by source (manual input / incident context / cron fire time)
	// but the node behaviour is identical.
	return NodeResult{Output: anyMap(rc.Trigger), Port: PortNext}, nil
}

func execAgent(ctx context.Context, x Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
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
	// Structured gateway (HLD-016): with an output_schema the agent is
	// instructed to answer ONLY the JSON object; we parse it into
	// output.structured so deterministic downstream (condition / tool args)
	// can reference fields. Without one the answer is free text.
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
}

func execLLM(ctx context.Context, x Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
	if x.LLM == nil {
		return NodeResult{}, fmt.Errorf("llm node: runner not wired")
	}
	prompt, _ := cfg["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return NodeResult{}, fmt.Errorf("llm node: prompt is empty")
	}
	system, _ := cfg["system"].(string)
	schema, hasSchema := cfg["output_schema"].(map[string]any)
	if hasSchema {
		sb, _ := json.Marshal(schema)
		prompt = prompt + "\n\nReturn ONLY a single JSON object matching this JSON Schema (no prose, no code fence):\n" + string(sb)
	}
	lctx, cancel := context.WithTimeout(ctx, llmNodeTimeout)
	defer cancel()
	answer, err := x.LLM.RunLLM(lctx, system, prompt)
	if err != nil {
		return NodeResult{}, fmt.Errorf("llm node: %w", err)
	}
	out := map[string]any{"answer": answer}
	if hasSchema {
		structured, perr := parseLooseJSON(answer)
		if perr != nil {
			return NodeResult{}, fmt.Errorf("llm node: output_schema declared but answer is not JSON: %w", perr)
		}
		out["structured"] = structured
	}
	return NodeResult{Output: out, Port: PortNext}, nil
}

func execTool(ctx context.Context, x Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
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
}

func execCondition(_ context.Context, _ Executors, cfg map[string]any, rc *RunContext) (NodeResult, error) {
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
}

func execNotify(ctx context.Context, x Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
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
}

func execSet(_ context.Context, _ Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
	name, _ := cfg["name"].(string)
	if name == "" {
		return NodeResult{}, fmt.Errorf("set node: missing var name")
	}
	val := cfg["value"]
	// Hand the var write back to the engine to apply under its lock — do
	// NOT touch rc.Vars here (this executor runs outside the lock).
	return NodeResult{
		Output: map[string]any{"name": name, "value": val},
		Vars:   map[string]any{name: val},
		Port:   PortNext,
	}, nil
}

// execTransform — the "edit fields" data node. config.fields is an
// object {outName: value}; the engine has already template-resolved every
// value, so the node just emits that constructed object. Lets a user
// reshape upstream data into the exact fields a downstream node needs
// (the field-adapter glue for a weakly-typed flow), with no code and no
// new dependency — values are plain {{...}} templates.
func execTransform(_ context.Context, _ Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
	fields, _ := cfg["fields"].(map[string]any)
	if fields == nil {
		fields = map[string]any{}
	}
	return NodeResult{Output: fields, Port: PortNext}, nil
}
