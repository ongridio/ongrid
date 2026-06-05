package agent

import (
	"encoding/json"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func sp(s string) *string { return &s }

// TestBuildMessages_ToolCallReplay covers the v0.7.170 fix:
// assistant turns with content=NULL but populated ToolCalls must
// replay as {role:assistant, content:"", tool_calls:[...]} so the
// following role=tool messages remain paired. DeepSeek v4+ rejected
// orphan tool messages with HTTP 400; OpenAI silently tolerated them
// pre-fix.
func TestBuildMessages_ToolCallReplay(t *testing.T) {
	a := &Agent{cfg: Config{SystemPrompt: "S"}}
	history := []*aiops.Message{
		{ID: "u1", Role: aiops.RoleUser, Content: sp("你好")},
		{ID: "a1", Role: aiops.RoleAssistant, Content: sp("你好,有什么可以帮你")},
		{ID: "u2", Role: aiops.RoleUser, Content: sp("查一下机器")},
		{
			ID: "a2", Role: aiops.RoleAssistant, Content: nil,
			ToolCalls: []aiops.ToolCall{
				{ToolName: "query_devices", ArgumentsJSON: `{}`, LLMCallID: sp("call_abc")},
			},
		},
		{ID: "t1", Role: aiops.RoleTool, Content: sp(`{"devices":[]}`), ToolCallID: sp("call_abc"), ToolName: sp("query_devices")},
		{ID: "a3", Role: aiops.RoleAssistant, Content: sp("你这边没有注册的设备")},
		{ID: "u3", Role: aiops.RoleUser, Content: sp("继续")},
	}
	out := a.buildMessages(history)
	// system + all 7 history rows (a2 now emits with tool_calls)
	if len(out) != 8 {
		t.Fatalf("len(out) = %d, want 8", len(out))
	}
	if out[0].Role != "system" {
		t.Errorf("out[0].Role = %q, want system", out[0].Role)
	}
	asst := out[4]
	if asst.Role != aiops.RoleAssistant {
		t.Fatalf("out[4].Role = %q, want assistant", asst.Role)
	}
	if asst.Content != "" {
		t.Errorf("assistant tool-call turn content = %q, want empty", asst.Content)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_abc" {
		t.Errorf("assistant.ToolCalls = %+v, want one with id=call_abc", asst.ToolCalls)
	}
	if string(asst.ToolCalls[0].Args) != `{}` {
		t.Errorf("assistant.ToolCalls[0].Args = %q, want {}", string(asst.ToolCalls[0].Args))
	}
	tool := out[5]
	if tool.Role != aiops.RoleTool || tool.ToolCallID != "call_abc" {
		t.Errorf("tool message position/id wrong: %+v", tool)
	}
}

// TestBuildMessages_ToolCallReplay_BackcompatPairByOrder covers
// rows written before chat_tool_calls.llm_call_id existed: the call
// id is recoverable from the following role=tool message's
// tool_call_id column.
func TestBuildMessages_ToolCallReplay_BackcompatPairByOrder(t *testing.T) {
	a := &Agent{cfg: Config{}}
	history := []*aiops.Message{
		{ID: "u1", Role: aiops.RoleUser, Content: sp("查一下")},
		{
			ID: "a1", Role: aiops.RoleAssistant, Content: nil,
			ToolCalls: []aiops.ToolCall{
				{ToolName: "tool_a", ArgumentsJSON: `{"x":1}`}, // LLMCallID nil — legacy
				{ToolName: "tool_b", ArgumentsJSON: `{"y":2}`},
			},
		},
		{ID: "t1", Role: aiops.RoleTool, Content: sp(`r1`), ToolCallID: sp("legacy_call_1"), ToolName: sp("tool_a")},
		{ID: "t2", Role: aiops.RoleTool, Content: sp(`r2`), ToolCallID: sp("legacy_call_2"), ToolName: sp("tool_b")},
		{ID: "a2", Role: aiops.RoleAssistant, Content: sp("好的")},
	}
	out := a.buildMessages(history)
	if len(out) != 5 {
		t.Fatalf("len(out) = %d, want 5", len(out))
	}
	asst := out[1]
	if len(asst.ToolCalls) != 2 {
		t.Fatalf("assistant.ToolCalls = %d entries, want 2", len(asst.ToolCalls))
	}
	if asst.ToolCalls[0].ID != "legacy_call_1" || asst.ToolCalls[1].ID != "legacy_call_2" {
		t.Errorf("pairing failed: got ids %q,%q", asst.ToolCalls[0].ID, asst.ToolCalls[1].ID)
	}
	if asst.ToolCalls[0].Name != "tool_a" || asst.ToolCalls[1].Name != "tool_b" {
		t.Errorf("names = %q,%q", asst.ToolCalls[0].Name, asst.ToolCalls[1].Name)
	}
	// tool messages also kept
	if out[2].ToolCallID != "legacy_call_1" || out[3].ToolCallID != "legacy_call_2" {
		t.Errorf("tool order/id wrong")
	}
}

// TestBuildMessages_ToolCallReplay_UnresolvableDropsAssistantAndTools
// — if neither LLMCallID nor pairing can recover ids, drop the
// assistant + dependent tool rows so the LLM never sees an orphan
// tool. This is the pre-fix MVP behavior, kept as a safety net.
func TestBuildMessages_ToolCallReplay_UnresolvableDropsAssistantAndTools(t *testing.T) {
	a := &Agent{cfg: Config{}}
	history := []*aiops.Message{
		{ID: "u1", Role: aiops.RoleUser, Content: sp("hi")},
		{
			ID: "a1", Role: aiops.RoleAssistant, Content: nil,
			ToolCalls: []aiops.ToolCall{{ToolName: "x", ArgumentsJSON: `{}`}},
		},
		// no following role=tool — corrupt session
		{ID: "u2", Role: aiops.RoleUser, Content: sp("are you there?")},
	}
	out := a.buildMessages(history)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (u1 + u2)", len(out))
	}
	for _, m := range out {
		if m.Role != aiops.RoleUser {
			t.Errorf("unexpected %s in output: %+v", m.Role, m)
		}
	}
}

// Smoke that ToolCalls Args JSON round-trips through json.RawMessage
// (a regression would be invalid JSON in the outgoing request body).
func TestBuildMessages_ToolCallArgsRoundtrip(t *testing.T) {
	a := &Agent{cfg: Config{}}
	args := `{"x":1,"nested":{"k":"v"}}`
	history := []*aiops.Message{
		{ID: "u1", Role: aiops.RoleUser, Content: sp("go")},
		{
			ID: "a1", Role: aiops.RoleAssistant, Content: nil,
			ToolCalls: []aiops.ToolCall{
				{ToolName: "n", ArgumentsJSON: args, LLMCallID: sp("c1")},
			},
		},
		{ID: "t1", Role: aiops.RoleTool, Content: sp(`{}`), ToolCallID: sp("c1"), ToolName: sp("n")},
	}
	out := a.buildMessages(history)
	if len(out[1].ToolCalls) != 1 {
		t.Fatalf("missing tool_call")
	}
	var probe any
	if err := json.Unmarshal(out[1].ToolCalls[0].Args, &probe); err != nil {
		t.Fatalf("args not valid JSON: %v (raw=%q)", err, string(out[1].ToolCalls[0].Args))
	}
}
