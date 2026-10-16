package chatruntime

import (
	"path/filepath"
	"strings"
	"testing"
)

// fixtureAgent returns the absolute path to an agent .md fixture.
func fixtureAgent(scenario, file string) string {
	return filepath.Join("testdata", "agent_parser", scenario, file)
}

func TestParseAgentMd_Minimal(t *testing.T) {
	ag, warns, err := ParseAgentMd(fixtureAgent("minimal", "incident-investigator.md"))
	if err != nil {
		t.Fatalf("ParseAgentMd: %v", err)
	}
	// "incident-investigator" uses dash; we warn but don't rewrite.
	foundDashWarn := false
	for _, w := range warns {
		if w.Code == "name_non_snake" {
			foundDashWarn = true
		}
	}
	if !foundDashWarn {
		t.Errorf("expected name_non_snake warning; got %+v", warns)
	}
	if ag.Name != "incident-investigator" {
		t.Errorf("Name = %q (should not be rewritten)", ag.Name)
	}
	if ag.Description == "" {
		t.Error("Description empty")
	}
	if ag.WhenToUse == "" {
		t.Error("WhenToUse empty")
	}
	if !strings.Contains(ag.SystemPrompt, "ongrid alert") {
		t.Errorf("SystemPrompt body lost: %q", ag.SystemPrompt)
	}
}

func TestParseAgentMd_WithDisallowedTools(t *testing.T) {
	ag, _, err := ParseAgentMd(fixtureAgent("with_disallowed_tools", "reviewer.md"))
	if err != nil {
		t.Fatalf("ParseAgentMd: %v", err)
	}
	if ag.Name != "reviewer" {
		t.Errorf("Name = %q", ag.Name)
	}
	wantDis := []string{"*_skill", "run_shell"}
	if !strSliceEq(ag.DisallowedTools, wantDis) {
		t.Errorf("DisallowedTools = %v, want %v", ag.DisallowedTools, wantDis)
	}
	if ag.PermissionMode != "read-only" {
		t.Errorf("PermissionMode = %q", ag.PermissionMode)
	}
	if ag.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d, want 5", ag.MaxTurns)
	}
	if !strings.Contains(ag.Model, "opus") {
		t.Errorf("Model = %q", ag.Model)
	}
	if len(ag.Tools) != 4 {
		t.Errorf("Tools len = %d, want 4", len(ag.Tools))
	}
}

func TestParseAgentMd_MissingWhenToUse(t *testing.T) {
	_, _, err := ParseAgentMd(fixtureAgent("missing_when_to_use", "foo.md"))
	if err == nil {
		t.Fatal("expected error for missing when_to_use, got nil")
	}
	if !strings.Contains(err.Error(), "when_to_use") {
		t.Errorf("error should mention when_to_use; got %v", err)
	}
}

func TestParseAgentMd_WithCriticalReminder(t *testing.T) {
	ag, _, err := ParseAgentMd(fixtureAgent("with_critical_reminder", "auditor.md"))
	if err != nil {
		t.Fatalf("ParseAgentMd: %v", err)
	}
	if ag.CriticalReminder == "" {
		t.Fatal("CriticalReminder empty")
	}
	if !strings.Contains(ag.CriticalReminder, "Do not approve") {
		t.Errorf("CriticalReminder content unexpected: %q", ag.CriticalReminder)
	}
	if ag.MaxTurns != 8 {
		t.Errorf("MaxTurns = %d", ag.MaxTurns)
	}
}

func TestParseAgentMd_FileMissing(t *testing.T) {
	_, _, err := ParseAgentMd(fixtureAgent("does_not_exist", "x.md"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
