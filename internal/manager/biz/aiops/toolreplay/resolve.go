// Package toolreplay reconstructs the LLM call id pairing between
// assistant messages and the role=tool messages that respond to them,
// so history replay can emit a protocol-compliant message stream.
//
// Background. The chat_messages table stores assistant turns that
// emitted tool_calls (and no natural-language text) as Content=NULL
// rows. The tool_calls metadata sits in chat_tool_calls (joined via
// message_id). Strict providers (DeepSeek v4+) reject the request
// with HTTP 400 "tool must follow tool_calls" when an assistant row
// with Content=NULL is replayed as a bare {role:assistant,content:""}
// — the following role=tool messages are then orphans.
//
// The fix is to hydrate Message.ToolCalls (done by SessionRepo) and
// have the replay layer emit
//
//	{role: assistant, content: "", tool_calls: [...]}
//
// instead. To do that we need each tool_call's LLM-assigned id, which
// historically wasn't stored on the chat_tool_calls row (only the
// assistant message id + tool name + arguments). The id IS recoverable
// from the following role=tool message's tool_call_id column.
//
// This helper produces, for each assistant message, the {id, name,
// args} triples buildMessages-style functions should emit. Two
// callers: the legacy agent.buildMessages (returns llm.Message) and
// the graph chatruntime.buildEinoHistory (returns schema.Message);
// both consume the same (msgID → []ResolvedToolCall) map.
package toolreplay

import (
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// ResolvedToolCall is one entry in the tool_calls slot of an assistant
// message at replay time. ArgsJSON is the verbatim JSON the LLM
// originally produced (chat_tool_calls.arguments_json) — emit as raw
// JSON, not re-marshalled, so float precision / key order match what
// the provider would expect for repeat-content safety.
type ResolvedToolCall struct {
	ID       string
	Name     string
	ArgsJSON string
}

// Resolve walks history and returns, for each assistant message id,
// the slice of tool calls the replay should emit. Three sources, in
// priority order:
//
//  1. Message.ToolCalls[i].LLMCallID — populated by the repo from
//     chat_tool_calls.llm_call_id (rows written post-fix).
//  2. Pair-by-order with the following role=tool messages, using each
//     tool row's stored tool_call_id (back-compat for pre-fix rows
//     with NULL llm_call_id).
//  3. Omit the assistant from the result map — caller drops the
//     assistant row + dependent tool rows so the LLM never sees
//     orphans. This is the conservative MVP behavior, preserved as
//     a safety net.
//
// Pairing-by-order matches the i-th hydrated ToolCall against the i-th
// following role=tool message; mismatched tool names abort pairing
// for that assistant entirely (we'd rather drop both sides than send
// the model a tool result attributed to the wrong call).
func Resolve(history []*aiopsmodel.Message) map[string][]ResolvedToolCall {
	out := make(map[string][]ResolvedToolCall)
	for i, m := range history {
		if m.Role != aiopsmodel.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		calls := make([]ResolvedToolCall, 0, len(m.ToolCalls))
		needsFallback := false
		for _, tc := range m.ToolCalls {
			if tc.LLMCallID != nil && *tc.LLMCallID != "" {
				calls = append(calls, ResolvedToolCall{
					ID:       *tc.LLMCallID,
					Name:     tc.ToolName,
					ArgsJSON: tc.ArgumentsJSON,
				})
				continue
			}
			needsFallback = true
			calls = append(calls, ResolvedToolCall{
				Name:     tc.ToolName,
				ArgsJSON: tc.ArgumentsJSON,
			})
		}
		if needsFallback {
			toolIdx := 0
			ok := true
			for j := i + 1; j < len(history) && toolIdx < len(calls); j++ {
				next := history[j]
				if next.Role != aiopsmodel.RoleTool {
					if next.Role == aiopsmodel.RoleAssistant || next.Role == aiopsmodel.RoleUser {
						break
					}
					continue
				}
				if calls[toolIdx].ID != "" {
					toolIdx++
					continue
				}
				if next.ToolCallID == nil || next.ToolName == nil || *next.ToolName != calls[toolIdx].Name {
					ok = false
					break
				}
				calls[toolIdx].ID = *next.ToolCallID
				toolIdx++
			}
			if !ok {
				continue
			}
			for _, c := range calls {
				if c.ID == "" {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		out[m.ID] = calls
	}
	return out
}

// MarkDependentToolsForSkip flags the next n role=tool messages
// (starting after assistantIdx) into skip so they don't appear
// orphaned. Stops at the next user / assistant boundary — anything
// past that point belongs to a different turn. Callers consult skip
// when iterating history; entries flagged are omitted from the
// outgoing LLM message list.
func MarkDependentToolsForSkip(history []*aiopsmodel.Message, assistantIdx, n int, skip map[int]bool) {
	count := 0
	for j := assistantIdx + 1; j < len(history) && count < n; j++ {
		next := history[j]
		if next.Role == aiopsmodel.RoleTool {
			skip[j] = true
			count++
			continue
		}
		if next.Role == aiopsmodel.RoleAssistant || next.Role == aiopsmodel.RoleUser {
			return
		}
	}
}

// IndexToolMessagesByCallID maps tool_call_id (the LLM-assigned id
// carried on chat_messages.tool_call_id for role=tool rows) to the
// history index of the corresponding role=tool row.
//
// Used to REORDER history at replay time: an assistant turn with
// tool_calls must be immediately followed by tool responses paired by
// tool_call_id, but persistence stores rows by created_at — and
// long-running tools (notably AgentTool spawning a specialist sub-agent)
// finish later than other intermediate turns, so the natural order
// interleaves another assistant between the parent and its response.
// Strict providers (DeepSeek v4+) reject this with HTTP 400 "insufficient
// tool messages following tool_calls message". The fix is to hoist each
// tool result to immediately after its parent assistant; this index lets
// the build functions look the rows up in O(1) when emitting.
//
// Returns a map keyed on tool_call_id only when the row has a non-empty
// tool_call_id (orphan-pattern tool rows are not indexed).
func IndexToolMessagesByCallID(history []*aiopsmodel.Message) map[string]int {
	out := make(map[string]int, len(history))
	for i, m := range history {
		if m.Role != aiopsmodel.RoleTool {
			continue
		}
		if m.ToolCallID == nil || *m.ToolCallID == "" {
			continue
		}
		if _, exists := out[*m.ToolCallID]; exists {
			// First-write-wins; defensive guard against a duplicate id.
			continue
		}
		out[*m.ToolCallID] = i
	}
	return out
}

// MarkAllFollowingToolsForSkip flags every consecutive role=tool
// message after assistantIdx — used when the assistant row carries
// no usable signal at all (content=NULL AND no hydrated ToolCalls,
// i.e. the polluted-data case where chat_tool_calls was never
// written and the assistant is unreplayable). Stops at the next
// user / assistant / system boundary; we drop the whole tool-call
// group rather than send the LLM dangling tool messages strict
// providers (DeepSeek v4+) reject with HTTP 400.
func MarkAllFollowingToolsForSkip(history []*aiopsmodel.Message, assistantIdx int, skip map[int]bool) {
	for j := assistantIdx + 1; j < len(history); j++ {
		next := history[j]
		switch next.Role {
		case aiopsmodel.RoleTool:
			skip[j] = true
		case aiopsmodel.RoleAssistant, aiopsmodel.RoleUser, aiopsmodel.RoleSystem:
			return
		}
	}
}
