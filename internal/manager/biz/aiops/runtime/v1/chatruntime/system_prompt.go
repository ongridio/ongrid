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
// PR-2 scope: this is a pure string assembly — no per-turn system-reminder
// injection. That happens in the graph layer; the TODO
// inline below points to where it lands.
func ComposeSystemPrompt(basePrompt string, activeSkills []*Skill, agentProfile *Agent) string {
	var parts []string

	if base := strings.TrimSpace(basePrompt); base != "" {
		parts = append(parts, base)
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

	// TODO(PR-graph): per-turn <system-reminder> injection happens at
	// the graph MessageAssembler stage, not here.
	// ComposeSystemPrompt only owns the system-message body.

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
	sb.WriteString("当任务专门属于以下领域时，**优先用 AgentTool 派给对应专家**而不是自己硬刚 — 专家的 toolBag 更聚焦、token 更省、推理更深：\n\n")
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
