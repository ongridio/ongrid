package chatruntime

import (
	"path/filepath"
	"testing"
)

func fixtureAgentRoot(scenario string) string {
	return filepath.Join("testdata", "agent_registry", scenario)
}

func TestAgentRegistry_LoadMulti(t *testing.T) {
	r := NewAgentRegistry()
	if err := r.Load(fixtureAgentRoot("multi")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := r.All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2 (README.md skipped)", len(all))
	}
	if _, ok := r.ByName("incident_investigator"); !ok {
		t.Errorf("ByName(incident_investigator) miss")
	}
	if _, ok := r.ByName("reviewer"); !ok {
		t.Errorf("ByName(reviewer) miss")
	}
	if _, ok := r.ByName("does_not_exist"); ok {
		t.Errorf("ByName should miss for unknown name")
	}
}

func TestAgentRegistry_LoadNonExistent(t *testing.T) {
	r := NewAgentRegistry()
	if err := r.Load(fixtureAgentRoot("does_not_exist")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.All()) != 0 {
		t.Errorf("expected zero agents")
	}
}

func TestAgentRegistry_AddAndByName(t *testing.T) {
	r := NewAgentRegistry()
	r.Add(&Agent{Name: "general_purpose", Description: "default", WhenToUse: "fallback"})
	if got, ok := r.ByName("general_purpose"); !ok || got.Name != "general_purpose" {
		t.Errorf("ByName failed for added agent")
	}
}
