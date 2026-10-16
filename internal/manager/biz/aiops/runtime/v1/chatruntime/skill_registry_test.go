package chatruntime

import (
	"path/filepath"
	"testing"
)

func fixtureSkillRoot(scenario string) string {
	return filepath.Join("testdata", "skill_registry", scenario)
}

func TestSkillRegistry_LoadMulti(t *testing.T) {
	r := NewSkillRegistry()
	if err := r.Load(fixtureSkillRoot("multi")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := r.All()
	if len(all) != 3 {
		t.Fatalf("All() len = %d, want 3 (always_skill, keyword_skill, buried_skill)", len(all))
	}
	names := map[string]bool{}
	for _, sk := range all {
		names[sk.Name] = true
		if sk.Dir == "" {
			t.Errorf("skill %q has empty Dir", sk.Name)
		}
	}
	for _, expected := range []string{"always_skill", "keyword_skill", "buried_skill"} {
		if !names[expected] {
			t.Errorf("skill %q not loaded", expected)
		}
	}
}

func TestSkillRegistry_LoadNonExistent(t *testing.T) {
	r := NewSkillRegistry()
	if err := r.Load(fixtureSkillRoot("does_not_exist")); err != nil {
		t.Fatalf("Load on missing dir should be nil-error, got %v", err)
	}
	if len(r.All()) != 0 {
		t.Errorf("expected zero skills")
	}
}

func TestSkillRegistry_BadSkillIsWarning(t *testing.T) {
	r := NewSkillRegistry()
	if err := r.Load(fixtureSkillRoot("with_bad_skill")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.All()) != 1 {
		t.Errorf("All() len = %d, want 1 (only 'good' parses)", len(r.All()))
	}
	hasParseFailed := false
	for _, w := range r.Warnings() {
		if w.Code == "parse_failed" {
			hasParseFailed = true
		}
	}
	if !hasParseFailed {
		t.Errorf("expected parse_failed warning for the bad skill; got %+v", r.Warnings())
	}
}

func TestSkillRegistry_ResolveAlwaysVsKeyword(t *testing.T) {
	r := NewSkillRegistry()
	if err := r.Load(fixtureSkillRoot("multi")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	policy := Policy{AllowedClasses: []string{"*"}}

	// Without any keyword in the query, only always-mode + buried (default
	// always) should resolve. keyword_skill should be excluded.
	got := r.Resolve("hello world", policy)
	names := skillNames(got)
	if hasName(names, "keyword_skill") {
		t.Errorf("keyword_skill should NOT resolve without keyword; got %v", names)
	}
	if !hasName(names, "always_skill") {
		t.Errorf("always_skill should resolve; got %v", names)
	}
	if !hasName(names, "buried_skill") {
		t.Errorf("buried_skill (default-always) should resolve; got %v", names)
	}

	// With "logs" in the query, keyword_skill should activate.
	got = r.Resolve("show me the logs from edge X", policy)
	if !hasName(skillNames(got), "keyword_skill") {
		t.Errorf("keyword_skill should activate on 'logs'; got %v", skillNames(got))
	}
}

func TestSkillRegistry_ResolvePolicyDropsTools(t *testing.T) {
	r := NewSkillRegistry()
	if err := r.Load(fixtureSkillRoot("multi")); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Read-only policy on a 'logs' query: keyword_skill activates, but its
	// destructive tool (delete_logs) should be filtered out.
	policy := Policy{AllowedClasses: []string{"read"}}
	got := r.Resolve("logs please", policy)
	for _, sk := range got {
		if sk.Name != "keyword_skill" {
			continue
		}
		if len(sk.Tools) != 1 {
			t.Fatalf("keyword_skill Tools len = %d, want 1 (delete_logs filtered)", len(sk.Tools))
		}
		if sk.Tools[0].Name != "query_logs" {
			t.Errorf("expected query_logs to survive; got %q", sk.Tools[0].Name)
		}
	}
}

func TestSkillRegistry_ResolveDropsSkillIfAllToolsFiltered(t *testing.T) {
	r := NewSkillRegistry()
	// Load a synthetic skill with only destructive tools; under a
	// read-only policy the skill should be dropped entirely.
	r.Add(&Skill{
		Name:        "destructive_only",
		Description: "Has only destructive tools.",
		Activation:  Activation{Mode: "always"},
		Tools: []ToolDecl{
			{Name: "wipe", Impl: "x", Class: ClassDestructive, Description: "Wipes the disk."},
		},
	})
	policy := Policy{AllowedClasses: []string{"read"}}
	got := r.Resolve("hi", policy)
	if hasName(skillNames(got), "destructive_only") {
		t.Errorf("destructive_only should be dropped; got %v", skillNames(got))
	}
}

func TestSkillRegistry_ResolvePromptOnlySkillSurvives(t *testing.T) {
	r := NewSkillRegistry()
	r.Add(&Skill{
		Name:        "pure_prompt",
		Description: "Prompt-only skill, no tools.",
		Activation:  Activation{Mode: "always"},
		PromptBody:  "guidance text",
	})
	policy := Policy{AllowedClasses: []string{"read"}}
	got := r.Resolve("anything", policy)
	if !hasName(skillNames(got), "pure_prompt") {
		t.Errorf("pure_prompt skill should survive policy with no tools; got %v", skillNames(got))
	}
}

// --- helpers ---

func skillNames(skills []*Skill) []string {
	out := make([]string, 0, len(skills))
	for _, sk := range skills {
		out = append(out, sk.Name)
	}
	return out
}

func hasName(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}
