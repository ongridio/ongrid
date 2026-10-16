package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	chatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
)

// TestIntegration_InstallReloadVisibleInRealRegistry exercises the
// end-to-end happy path against a *real*
// chatruntime.SkillRegistry / AgentRegistry — not the in-memory
// stubs the unit tests use. The point is to lock in the deliverable
// guarantee: after Install, the next chat sees the skill via
// Resolve(); after Uninstall, the same query sees nothing.
//
// This is also where we prove "Reload result == fresh LoadAll result"
// — by comparing Resolve()'s output against a sibling LoadAll call we
// detect if Reload ever drifts (it goes through LoadAll itself, so by
// construction this is true; the test is the regression seatbelt).
func TestIntegration_InstallReloadVisibleInRealRegistry(t *testing.T) {
	tmp := t.TempDir()
	systemRoot := filepath.Join(tmp, "system_skills")
	staging := filepath.Join(tmp, "staging")
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(systemRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPack(t, src, "etcd-tools")

	skillReg := chatruntime.NewSkillRegistry()
	agentReg := chatruntime.NewAgentRegistry()
	repo := newFakeRepo()
	uc := NewUsecase(repo, skillReg, agentReg, Config{
		SystemSkillsRoot: systemRoot,
		StagingDir:       staging,
		AllowedSources:   []string{"local"},
		DevMode:          true,
	}, nil)

	// Pre-install: the registry sees nothing.
	if got := skillReg.All(); len(got) != 0 {
		t.Fatalf("registry should be empty pre-install, got %d", len(got))
	}

	caller := Caller{UserID: 1, Role: "admin"}
	if _, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Post-install: skill appears.
	gotSkills := skillReg.All()
	if len(gotSkills) != 1 || gotSkills[0].Name != "test_skill" {
		t.Fatalf("registry after install: %+v", gotSkills)
	}
	gotAgents := agentReg.All()
	if len(gotAgents) != 1 || gotAgents[0].Name != "test_agent" {
		t.Fatalf("agent registry after install: %+v", gotAgents)
	}

	// Reload result == fresh LoadAll result. The marketplace call
	// reused Reload internally, so a follow-up direct LoadAll must
	// produce the same skill set.
	res, err := chatruntime.LoadAll(chatruntime.LoadAllConfig{SkillsRoot: systemRoot})
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(res.Skills) != len(gotSkills) {
		t.Fatalf("LoadAll vs Reload skills mismatch: %d vs %d", len(res.Skills), len(gotSkills))
	}

	// Resolve() should also pick the skill up under the default policy.
	policy := chatruntime.Policy{AllowedClasses: []string{"*"}}
	resolved := skillReg.Resolve("anything", policy)
	if len(resolved) != 1 {
		t.Fatalf("Resolve len = %d want 1", len(resolved))
	}

	// Uninstall → registry empties.
	if err := uc.Uninstall(context.Background(), caller, "etcd-tools"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := skillReg.All(); len(got) != 0 {
		t.Fatalf("registry should be empty post-uninstall, got %+v", got)
	}
	if got := agentReg.All(); len(got) != 0 {
		t.Fatalf("agent registry should be empty post-uninstall, got %+v", got)
	}
}
