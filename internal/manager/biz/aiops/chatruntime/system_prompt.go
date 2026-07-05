package chatruntime

import (
	"strings"
)

// ComposeSystemPrompt assembles the SystemPrompt block ChatRuntime feeds
// to the LLM. Layered (the ComposeSystemPrompt arrow
// in the main reference figure):
//
//  1. basePrompt (the runtime's universal preamble; can be empty)
//  2. agentProfile.SystemPrompt + agentProfile.CriticalReminder
//     (only when a worker is being constructed; nil for coordinator
//     unless the coordinator itself is also persona-driven)
//  3. for each active skill, a `[能力: <name>]` header + skill PromptBody
//
// Pure string assembly — no per-turn system-reminder injection. That
// happens in the graph layer (graph.buildSystemReminder is called by
// graph.assembleMessages on every turn so the block survives long-
// session attention drift), not here.
// coordinatorToolRouting steers the coordinator to the RIGHT tool family so it
// doesn't (a) rabbit-hole through k8s tools for an ongrid-device question, or
// (b) fall back to an uninstalled `kubectl` for a genuine k8s question instead
// of the MCP tools. Injected only into the coordinator prompt (agentProfile==nil).
const coordinatorToolRouting = `## 工具选型（重要）
- **ongrid 管理的设备 / 主机 / 边端**的轻量事实查询 → 直接用 ongrid 原生只读工具：` +
	"`query_devices`、`get_topology`、`query_incidents`、`get_edge_summary`、`get_host_load`、`get_host_processes`、`rank_edges`、`find_outlier_edges`" + `。
- **复杂诊断 / 根因 / 影响面 / 处置建议** → 用 ` +
	"`AgentTool`" + ` 派给对应 specialist。不要把多步专家诊断硬塞在 coordinator 里串行跑。
- **host_bash / query_promql / query_logql / correlate_incident / 文件和网络探针** 默认属于专家深查工具；除非当前工具列表明确提供且用户只要非常窄的事实，否则派 specialist。
- **外部系统**（k8s 集群、云厂商如腾讯云/AWS）→ 用对应的外部工具：k8s 用 ` +
	"`mcp__k8s__*`（如 `mcp__k8s__namespaces_list` / `pods_list`），云厂商用 `cloud_bash`（tccli / awscli，凭证已注入）" + `。
- **不要混淆**：k8s 的 node ≠ ongrid 的 device。问「ongrid 设备使用率」绝不要用 ` +
	"`mcp__k8s__nodes_*`；问「k8s 集群」绝不要用 ongrid 设备工具、也不要用 `kubectl`（环境没装），直接用 `mcp__k8s__*`" + `。
- **不要钻牛角尖**：某个工具/来源返回的数据明显跟用户目标不符，立刻换工具或换思路，**不要在同一个错误来源里反复探**（同一类工具连试 2 次还不对就停，换路子或直接告诉用户缺什么）。

## 多专家并行（重要）
跨多个**独立领域**的综合任务（例：「全面体检」= 计算 + 磁盘 + 网络）——在同一个回合里同时发起多个 AgentTool 调用（每个领域一个 specialist），最后汇总专家结论。简单 topN / 快照 / 列表查询不要派 AgentTool。`

func ComposeSystemPrompt(basePrompt string, activeSkills []*Skill, agentProfile *Agent) string {
	var parts []string

	if base := strings.TrimSpace(basePrompt); base != "" {
		parts = append(parts, base)
	}

	// Coordinator-only tool-routing rules (workers are already scoped to a focused
	// toolbag by their persona, so they don't need this).
	if agentProfile == nil {
		parts = append(parts, coordinatorToolRouting)
	}

	if agentProfile != nil {
		if body := strings.TrimSpace(agentProfile.SystemPrompt); body != "" {
			parts = append(parts, body)
		}
		if reminder := strings.TrimSpace(agentProfile.CriticalReminder); reminder != "" {
			// — the per-turn injection wraps this in
			// <system-reminder>...</system-reminder>; here we just plant
			// it once into the system prompt as the persona-level
			// constant. The graph layer will additionally re-inject
			// per-turn.
			parts = append(parts, "<critical-reminder>\n"+reminder+"\n</critical-reminder>")
		}
	}

	for _, sk := range activeSkills {
		if sk == nil {
			continue
		}
		header := "[能力: " + sk.Name + "]"
		body := strings.TrimSpace(sk.PromptBody)
		// PromptBody is already H1-normalized at parse time (
		// If the body already starts with the canonical header
		// we skip prepending again.
		if strings.HasPrefix(body, header) {
			parts = append(parts, body)
			continue
		}
		if body == "" {
			parts = append(parts, header)
			continue
		}
		parts = append(parts, header+"\n\n"+body)
	}

	return strings.Join(parts, "\n\n")
}

// buildAgentCatalog renders a markdown list of the registered agent
// personas the coordinator can spawn via AgentTool. Injected into the
// coordinator's system prompt at runtime so the LLM knows the valid
// subagent_type values + when to pick which.
//
// Only "user" + "disk" sourced personas are listed. The coordinator
// never spawns reviewer (only review_gate decorator does); skipping it
// here keeps the LLM from accidentally trying to use it for ad-hoc
// reviews.
func buildAgentCatalog(reg *AgentRegistry) string {
	if reg == nil {
		return ""
	}
	all := reg.All()
	if len(all) == 0 {
		return ""
	}
	type row struct{ name, when, desc string }
	rows := make([]row, 0, len(all))
	for _, ag := range all {
		if ag == nil || ag.Name == "" {
			continue
		}
		// reviewer is reserved for the SOP twin-sign decorator; don't
		// expose it to the coordinator's free-form AgentTool routing.
		// "default" is the virtual top-level persona — listing it as a
		// spawnable sub-agent would let the coordinator recursively
		// spawn itself.
		if ag.Name == "reviewer" || ag.Name == "default" {
			continue
		}
		rows = append(rows, row{name: ag.Name, when: strings.TrimSpace(ag.WhenToUse), desc: strings.TrimSpace(ag.Description)})
	}
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 可用的 specialist 助理（AgentTool 的 subagent_type）\n\n")
	sb.WriteString("当任务需要多步诊断、根因判断、跨域分析或处置建议时，用 AgentTool 派给对应专家；简单 topN / 快照 / 列表查询由 coordinator 直接调用只读工具完成：\n\n")
	for _, r := range rows {
		sb.WriteString("- `")
		sb.WriteString(r.name)
		sb.WriteString("` — ")
		if r.desc != "" {
			sb.WriteString(r.desc)
		}
		if r.when != "" {
			// Take just the first non-empty line of when_to_use so
			// the prompt stays compact.
			firstLine := strings.SplitN(r.when, "\n", 2)[0]
			if firstLine != "" && firstLine != r.desc {
				sb.WriteString("。何时派：")
				sb.WriteString(firstLine)
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n派活时：在 prompt 里写清 incident_id / device_id / 用户原话——worker 看不到你的上下文。")
	return sb.String()
}
