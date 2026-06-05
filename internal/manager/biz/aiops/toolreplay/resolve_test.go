package toolreplay

import (
	"testing"

	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func sp(s string) *string { return &s }

func TestResolve_PostFixLLMCallID(t *testing.T) {
	history := []*aiopsmodel.Message{
		{ID: "u1", Role: aiopsmodel.RoleUser, Content: sp("hi")},
		{
			ID: "a1", Role: aiopsmodel.RoleAssistant, Content: nil,
			ToolCalls: []aiopsmodel.ToolCall{
				{ToolName: "x", ArgumentsJSON: `{"k":1}`, LLMCallID: sp("call_abc")},
			},
		},
		{ID: "t1", Role: aiopsmodel.RoleTool, Content: sp("ok"), ToolCallID: sp("call_abc"), ToolName: sp("x")},
	}
	got := Resolve(history)
	if len(got["a1"]) != 1 || got["a1"][0].ID != "call_abc" || got["a1"][0].Name != "x" || got["a1"][0].ArgsJSON != `{"k":1}` {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestResolve_BackcompatPairByOrder(t *testing.T) {
	history := []*aiopsmodel.Message{
		{
			ID: "a1", Role: aiopsmodel.RoleAssistant, Content: nil,
			ToolCalls: []aiopsmodel.ToolCall{
				{ToolName: "a", ArgumentsJSON: `{"x":1}`},
				{ToolName: "b", ArgumentsJSON: `{"y":2}`},
			},
		},
		{ID: "t1", Role: aiopsmodel.RoleTool, Content: sp("r1"), ToolCallID: sp("legacy1"), ToolName: sp("a")},
		{ID: "t2", Role: aiopsmodel.RoleTool, Content: sp("r2"), ToolCallID: sp("legacy2"), ToolName: sp("b")},
	}
	got := Resolve(history)
	calls := got["a1"]
	if len(calls) != 2 {
		t.Fatalf("got %d entries, want 2", len(calls))
	}
	if calls[0].ID != "legacy1" || calls[1].ID != "legacy2" {
		t.Errorf("pairing wrong: %q %q", calls[0].ID, calls[1].ID)
	}
}

func TestResolve_MismatchedToolName_DropsAssistant(t *testing.T) {
	history := []*aiopsmodel.Message{
		{
			ID: "a1", Role: aiopsmodel.RoleAssistant, Content: nil,
			ToolCalls: []aiopsmodel.ToolCall{
				{ToolName: "expected", ArgumentsJSON: `{}`},
			},
		},
		// next tool answers a DIFFERENT tool — could be from a different
		// turn or corrupt data. Drop a1 entirely rather than mispairing.
		{ID: "t1", Role: aiopsmodel.RoleTool, Content: sp("r"), ToolCallID: sp("x"), ToolName: sp("different")},
	}
	got := Resolve(history)
	if _, ok := got["a1"]; ok {
		t.Errorf("a1 should be dropped on tool name mismatch, got %+v", got["a1"])
	}
}

func TestResolve_NoFollowingTool_DropsAssistant(t *testing.T) {
	history := []*aiopsmodel.Message{
		{
			ID: "a1", Role: aiopsmodel.RoleAssistant, Content: nil,
			ToolCalls: []aiopsmodel.ToolCall{{ToolName: "x", ArgumentsJSON: `{}`}},
		},
		// User came back before any tool result — corrupt session.
		{ID: "u1", Role: aiopsmodel.RoleUser, Content: sp("hello?")},
	}
	got := Resolve(history)
	if _, ok := got["a1"]; ok {
		t.Errorf("a1 should be dropped, got %+v", got["a1"])
	}
}

func TestResolve_MixedSources(t *testing.T) {
	// Assistant has two tool_calls: first has llm_call_id; second is
	// legacy and must be paired from the following tool message.
	history := []*aiopsmodel.Message{
		{
			ID: "a1", Role: aiopsmodel.RoleAssistant, Content: nil,
			ToolCalls: []aiopsmodel.ToolCall{
				{ToolName: "a", ArgumentsJSON: `{}`, LLMCallID: sp("call_new")},
				{ToolName: "b", ArgumentsJSON: `{}`},
			},
		},
		{ID: "t1", Role: aiopsmodel.RoleTool, Content: sp("r1"), ToolCallID: sp("call_new"), ToolName: sp("a")},
		{ID: "t2", Role: aiopsmodel.RoleTool, Content: sp("r2"), ToolCallID: sp("legacy_b"), ToolName: sp("b")},
	}
	got := Resolve(history)
	calls := got["a1"]
	if len(calls) != 2 {
		t.Fatalf("got %d, want 2", len(calls))
	}
	if calls[0].ID != "call_new" || calls[1].ID != "legacy_b" {
		t.Errorf("ids = %q,%q", calls[0].ID, calls[1].ID)
	}
}

func TestIndexToolMessagesByCallID(t *testing.T) {
	history := []*aiopsmodel.Message{
		{Role: aiopsmodel.RoleUser},
		{Role: aiopsmodel.RoleAssistant},
		{Role: aiopsmodel.RoleTool, ToolCallID: sp("call_a"), ToolName: sp("tool_a")},
		{Role: aiopsmodel.RoleTool, ToolCallID: sp("call_b"), ToolName: sp("tool_b")},
		{Role: aiopsmodel.RoleTool, ToolCallID: sp("call_agent"), ToolName: sp("AgentTool")},
		// orphan from polluted-data era — must NOT land under "".
		{Role: aiopsmodel.RoleTool, ToolCallID: nil},
	}
	got := IndexToolMessagesByCallID(history)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got["call_a"] != 2 || got["call_b"] != 3 || got["call_agent"] != 4 {
		t.Errorf("indices wrong: %+v", got)
	}
	if _, found := got[""]; found {
		t.Errorf("nil ToolCallID indexed under empty string")
	}
}

func TestMarkAllFollowingToolsForSkip(t *testing.T) {
	// Polluted-data shape: assistant content=NULL, no hydrated
	// ToolCalls, followed by N tool rows with synthetic ids — drop
	// all of them or DeepSeek orphans them with HTTP 400.
	history := []*aiopsmodel.Message{
		{Role: aiopsmodel.RoleAssistant, Content: nil}, // idx 0
		{Role: aiopsmodel.RoleTool},                    // idx 1 — drop
		{Role: aiopsmodel.RoleTool},                    // idx 2 — drop
		{Role: aiopsmodel.RoleTool},                    // idx 3 — drop
		{Role: aiopsmodel.RoleAssistant},               // idx 4 — boundary
		{Role: aiopsmodel.RoleTool},                    // idx 5 — keep (next turn)
	}
	skip := map[int]bool{}
	MarkAllFollowingToolsForSkip(history, 0, skip)
	if !skip[1] || !skip[2] || !skip[3] {
		t.Errorf("expected 1,2,3 flagged, got %+v", skip)
	}
	if skip[5] {
		t.Errorf("idx 5 belongs to next turn, should NOT be flagged")
	}
}

func TestMarkDependentToolsForSkip(t *testing.T) {
	history := []*aiopsmodel.Message{
		{Role: aiopsmodel.RoleAssistant},
		{Role: aiopsmodel.RoleTool},
		{Role: aiopsmodel.RoleTool},
		{Role: aiopsmodel.RoleAssistant}, // boundary
		{Role: aiopsmodel.RoleTool},      // not ours
	}
	skip := map[int]bool{}
	MarkDependentToolsForSkip(history, 0, 2, skip)
	if !skip[1] || !skip[2] {
		t.Errorf("expected idx 1 and 2 flagged, got %+v", skip)
	}
	if skip[4] {
		t.Errorf("idx 4 belongs to next turn, should not be flagged")
	}
}
