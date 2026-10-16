package chatruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
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
const coordinatorToolRouting = `## 工具选型补充
- ongrid device/edge ≠ k8s node。问 ongrid 设备用 ongrid 工具；问 k8s 集群用 ` +
	"`mcp__k8s__*`" + `，不要猜 ` + "`kubectl`" + `。
- 产品行为、交互设计、页面/产物关联、ToolSearch/Operation/AgentLoop 策略解释，默认直接解释；不要为了这类问题发散调用 ` + "`host_bash`" + ` / 拓扑 / 源码工具。若缺少运行时产物索引，直接说明能力缺口。
- 复杂跨域任务可同轮并行多个 ` + "`AgentTool`" + `；简单 topN / 快照 / 列表仍直接查。错误来源连续 2 次不匹配就换路或说明缺口。`

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

const maxCapabilityDigestTools = 12

type capabilityDigestRow struct {
	name, origin, class, desc string
}

// buildToolCapabilityDigest renders a compact, per-request inventory of the
// tools that survived persona / role filtering. This is the dynamic
// counterpart to the static base prompt: built-in routing rules stay small,
// while MCP / installed-skill tools surface automatically as soon as they are
// appended to the runtime toolbag.
func buildToolCapabilityDigest(tools []basetool.BaseTool) string {
	if len(tools) == 0 {
		return ""
	}
	rows := make([]capabilityDigestRow, 0, len(tools))
	counts := map[string]int{}
	for _, t := range tools {
		if t == nil {
			continue
		}
		info, err := t.Info(context.Background())
		if err != nil || info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		origin := strings.TrimSpace(info.Origin)
		if origin == "" {
			origin = "builtin"
		}
		class := strings.TrimSpace(info.Class)
		if class == "" {
			class = "read"
		}
		counts[origin+"/"+class]++
		if info.Origin == "" && !isDigestBuiltin(info.Name) {
			continue
		}
		rows = append(rows, capabilityDigestRow{
			name:   info.Name,
			origin: origin,
			class:  class,
			desc:   compactOneLine(firstNonEmpty(info.WhenToUse, info.Description), 72),
		})
	}
	if len(rows) == 0 && len(counts) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].origin != rows[j].origin {
			return rows[i].origin < rows[j].origin
		}
		return rows[i].name < rows[j].name
	})

	var b strings.Builder
	b.WriteString("## 本轮可见能力（动态）\n")
	if len(counts) > 0 {
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("- counts: ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(fmt.Sprint(counts[k]))
		}
		b.WriteString("\n")
	}
	if direct := directReadToolNames(rows); len(direct) > 0 {
		b.WriteString("- direct_read_tools: ")
		for i, name := range direct {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("`")
			b.WriteString(name)
			b.WriteString("`")
		}
		b.WriteString("\n")
	}
	limit := len(rows)
	truncated := 0
	if limit > maxCapabilityDigestTools {
		truncated = limit - maxCapabilityDigestTools
		limit = maxCapabilityDigestTools
	}
	for i := 0; i < limit; i++ {
		r := rows[i]
		b.WriteString("- ")
		b.WriteString(r.name)
		b.WriteString(" [")
		b.WriteString(r.origin)
		b.WriteString("/")
		b.WriteString(r.class)
		b.WriteString("]")
		if r.desc != "" {
			b.WriteString(": ")
			b.WriteString(r.desc)
		}
		b.WriteString("\n")
	}
	if truncated > 0 {
		b.WriteString("- ... ")
		b.WriteString(fmt.Sprint(truncated))
		b.WriteString(" more; use ToolSearch/select:<tool> if a schema is redacted or the exact tool is unclear.\n")
	}
	return strings.TrimSpace(b.String())
}

func directReadToolNames(rows []capabilityDigestRow) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.origin != "builtin" || r.class != "read" {
			continue
		}
		if !aiopstools.IsCoreToolName(r.name) {
			continue
		}
		names = append(names, r.name)
	}
	sort.Strings(names)
	return names
}

func isDigestBuiltin(name string) bool {
	return aiopstools.IsCoreToolName(name)
}

func compactOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max-1]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// buildAgentCatalog renders optional expert capability cards for the default
// assistant. The cards explain what additional evidence an expert can obtain;
// they do not turn the expert into the owner of the user conversation.
func buildAgentCatalog(reg *AgentRegistry) string {
	if reg == nil {
		return ""
	}
	cards := reg.CapabilityCards()
	if len(cards) == 0 {
		return ""
	}
	type row struct{ agent, id, desc string }
	rows := make([]row, 0, len(cards))
	for _, card := range cards {
		if card.AgentName == "" || card.AgentName == "reviewer" || card.AgentName == "default" {
			continue
		}
		rows = append(rows, row{agent: card.AgentName, id: card.ID, desc: strings.TrimSpace(card.Description)})
	}
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 可选专家能力（AgentTool 的 subagent_type）\n\n")
	sb.WriteString("默认助理负责最终答复并可直接使用本轮工具。需要独立复核、额外领域证据或并行分析时，才调用 AgentTool 请求以下专家工作会话：\n\n")
	for _, r := range rows {
		sb.WriteString("- `")
		sb.WriteString(r.agent)
		sb.WriteString("` (`")
		sb.WriteString(r.id)
		sb.WriteString("`) — ")
		if r.desc != "" {
			sb.WriteString(r.desc)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n派活时：在 prompt 里写清 incident_id / device_id / 用户原话——worker 看不到你的上下文。")
	return sb.String()
}
