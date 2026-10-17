package chatruntime

import (
	"os"
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

func TestAgentRegistryCapabilityCardsCarryOwner(t *testing.T) {
	r := NewAgentRegistry()
	r.Add(&Agent{Name: "specialist-disk", Capabilities: []AgentCapability{{ID: "disk_analysis", Tools: []string{"host_du_summary"}}}})
	cards := r.CapabilityCards()
	if len(cards) != 1 {
		t.Fatalf("CapabilityCards len = %d, want 1", len(cards))
	}
	if cards[0].AgentName != "specialist-disk" || cards[0].ID != "disk_analysis" {
		t.Errorf("card = %+v", cards[0])
	}
	cards[0].Tools[0] = "changed"
	if got := r.All()[0].Capabilities[0].Tools[0]; got != "host_du_summary" {
		t.Errorf("registry card mutated through snapshot: %q", got)
	}
}

func TestAgentRegistryReloadPreservesBuiltinAndRemovesUninstalledPersona(t *testing.T) {
	r := NewAgentRegistry()
	builtin := &Agent{Name: "default", Source: "builtin", MaxTurns: 30, Capabilities: []AgentCapability{{ID: "operate"}}}
	r.AddBuiltin(builtin)
	root := t.TempDir()
	body := "---\nname: default\ndescription: shadow\nwhen_to_use: shadow\n---\nshadow"
	if err := os.WriteFile(filepath.Join(root, "default.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(root, fixtureAgentRoot("multi")); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ByName("default"); got != builtin {
		t.Fatal("plugin replaced builtin persona")
	}
	if _, ok := r.ByName("reviewer"); !ok {
		t.Fatal("plugin persona was not loaded")
	}
	if err := r.Reload(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ByName("default"); got != builtin || got.MaxTurns != 30 {
		t.Fatal("reload lost builtin configuration")
	}
	if len(r.All()) != 1 || len(r.CapabilityCards()) != 1 {
		t.Fatal("uninstall left stale personas or removed builtin capabilities")
	}
	if err := r.Load(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ByName("default"); got != builtin {
		t.Fatal("load lost builtin persona")
	}
}
