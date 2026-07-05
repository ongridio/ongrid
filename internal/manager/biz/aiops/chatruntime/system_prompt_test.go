package chatruntime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

func TestComposeSystemPrompt_Empty(t *testing.T) {
	// agentProfile==nil ⇒ coordinator ⇒ the coordinatorToolRouting block is
	// injected, so even an otherwise-empty compose carries it.
	got := ComposeSystemPrompt("", nil, nil)
	if got != coordinatorToolRouting {
		t.Errorf("expected just the routing block, got %q", got)
	}
}

func TestComposeSystemPrompt_BaseOnly(t *testing.T) {
	got := ComposeSystemPrompt("you are ongrid.", nil, nil)
	if got != "you are ongrid.\n\n"+coordinatorToolRouting {
		t.Errorf("got %q", got)
	}
}

func TestComposeSystemPrompt_MultipleSkills(t *testing.T) {
	skills := []*Skill{
		{Name: "alpha", PromptBody: "[能力: alpha]\n\nalpha guide"},
		{Name: "beta", PromptBody: "beta guide"},
	}
	got := ComposeSystemPrompt("base prompt", skills, nil)
	if !strings.Contains(got, "base prompt") {
		t.Error("base missing")
	}
	if !strings.Contains(got, "[能力: alpha]") {
		t.Error("alpha header missing")
	}
	// beta wasn't pre-tagged in PromptBody, ComposeSystemPrompt should
	// prepend the canonical tag.
	if !strings.Contains(got, "[能力: beta]") {
		t.Error("beta header missing")
	}
	if !strings.Contains(got, "alpha guide") || !strings.Contains(got, "beta guide") {
		t.Error("skill bodies missing")
	}
	// Order: base before alpha before beta.
	if strings.Index(got, "base prompt") > strings.Index(got, "alpha guide") {
		t.Error("base should come before alpha")
	}
	if strings.Index(got, "alpha guide") > strings.Index(got, "beta guide") {
		t.Error("alpha should come before beta")
	}
}

func TestComposeSystemPrompt_AgentProfile(t *testing.T) {
	ag := &Agent{
		Name:             "reviewer",
		SystemPrompt:     "you review proposals.",
		CriticalReminder: "do not approve mutations on your own.",
	}
	got := ComposeSystemPrompt("base", nil, ag)
	if !strings.Contains(got, "base") {
		t.Error("base missing")
	}
	if !strings.Contains(got, "you review proposals.") {
		t.Error("system prompt body missing")
	}
	if !strings.Contains(got, "<critical-reminder>") {
		t.Error("critical-reminder block missing")
	}
	if !strings.Contains(got, "do not approve mutations") {
		t.Error("reminder content missing")
	}
}

func TestComposeSystemPrompt_AgentAndSkills(t *testing.T) {
	ag := &Agent{Name: "worker", SystemPrompt: "worker prompt", CriticalReminder: ""}
	skills := []*Skill{{Name: "tool_x", PromptBody: "x guide"}}
	got := ComposeSystemPrompt("base", skills, ag)
	if !strings.Contains(got, "base") || !strings.Contains(got, "worker prompt") || !strings.Contains(got, "[能力: tool_x]") {
		t.Errorf("missing parts; got %q", got)
	}
	// Order: base, then agent, then skills.
	bIdx := strings.Index(got, "base")
	aIdx := strings.Index(got, "worker prompt")
	sIdx := strings.Index(got, "[能力: tool_x]")
	if !(bIdx < aIdx && aIdx < sIdx) {
		t.Errorf("order should be base→agent→skills; bIdx=%d aIdx=%d sIdx=%d", bIdx, aIdx, sIdx)
	}
}

func TestComposeSystemPrompt_NilSkillEntrySkipped(t *testing.T) {
	skills := []*Skill{nil, {Name: "z", PromptBody: "z body"}}
	got := ComposeSystemPrompt("", skills, nil)
	if !strings.Contains(got, "[能力: z]") {
		t.Errorf("z skill should be present despite preceding nil; got %q", got)
	}
}

func TestComposeSystemPrompt_EmptyPromptBody(t *testing.T) {
	skills := []*Skill{{Name: "empty"}}
	got := ComposeSystemPrompt("", skills, nil)
	// nil agentProfile ⇒ coordinator routing block precedes the skill header.
	if got != coordinatorToolRouting+"\n\n[能力: empty]" {
		t.Errorf("got %q", got)
	}
}

func TestBuildToolCapabilityDigest_IncludesDynamicTools(t *testing.T) {
	bag := []basetool.BaseTool{
		&originTool{name: "query_devices", class: "read", origin: basetool.OriginBuiltin},
		&originTool{name: "mcp__k8s__namespaces_list", class: "read", origin: basetool.OriginMCP},
		&originTool{name: "skill__custom__foo", class: "read", origin: basetool.OriginSkill},
		&originTool{name: "internal_big_tool", class: "read", origin: basetool.OriginBuiltin},
	}
	got := buildToolCapabilityDigest(bag)
	for _, want := range []string{
		"本轮可见能力",
		"query_devices [builtin/read]",
		"mcp__k8s__namespaces_list [mcp/read]",
		"skill__custom__foo [skill/read]",
		"mcp/read=1",
		"skill/read=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("capability digest missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "internal_big_tool [builtin/read]") {
		t.Fatalf("digest should not list arbitrary builtin tools; got:\n%s", got)
	}
}

func TestBuildToolCapabilityDigest_TruncatesLargeDynamicSets(t *testing.T) {
	bag := make([]basetool.BaseTool, 0, maxCapabilityDigestTools+3)
	for i := 0; i < maxCapabilityDigestTools+3; i++ {
		bag = append(bag, &originTool{name: fmt.Sprintf("mcp__srv__tool_%02d", i), class: "read", origin: basetool.OriginMCP})
	}
	got := buildToolCapabilityDigest(bag)
	if !strings.Contains(got, "more; use ToolSearch") {
		t.Fatalf("digest should truncate and point to ToolSearch, got:\n%s", got)
	}
}
