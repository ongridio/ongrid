package chatruntime

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadAll_WiresPluginContainersFromSkillsRoot drops a recursive_claude
// pack under skillsRoot and asserts both its skills AND its agents reach
// the LoadResult.
func TestLoadAll_WiresPluginContainersFromSkillsRoot(t *testing.T) {
	root := t.TempDir()
	// Mirror the recursive_claude fixture into root/recursive so that
	// SkillsRoot is the conventional "parent of packs" layout.
	src, err := filepath.Abs(filepath.Join("testdata", "plugin_container", "recursive_claude"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	dst := filepath.Join(root, "recursive")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}

	res, err := LoadAll(LoadAllConfig{SkillsRoot: root})
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(res.Skills) == 0 {
		t.Fatalf("LoadAll returned no skills; warns=%+v", res.Warnings)
	}
	hasHello := false
	hasCmd := false
	for _, sk := range res.Skills {
		if sk.Name == "hello_world" {
			hasHello = true
		}
		if sk.Name == "cmd_commit" {
			hasCmd = true
		}
	}
	if !hasHello {
		t.Errorf("hello_world not loaded from pack under skills root; got %v", skillNames(res.Skills))
	}
	if !hasCmd {
		t.Errorf("cmd_commit not loaded from pack under skills root; got %v", skillNames(res.Skills))
	}
	// Agents from the pack should also surface.
	hasAgent := false
	for _, ag := range res.Agents {
		if ag.Name == "pr_summary" {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Errorf("pr_summary agent should reach LoadAll result; got %d agents", len(res.Agents))
	}
}

func TestLoadAll_NonExistentRoots_NoError(t *testing.T) {
	res, err := LoadAll(LoadAllConfig{
		SkillsRoot: filepath.Join(t.TempDir(), "missing-skills"),
		AgentsRoot: filepath.Join(t.TempDir(), "missing-agents"),
	})
	if err != nil {
		t.Fatalf("LoadAll on missing roots should be nil-error, got %v", err)
	}
	if len(res.Skills) != 0 || len(res.Agents) != 0 {
		t.Errorf("expected empty result; got skills=%d agents=%d", len(res.Skills), len(res.Agents))
	}
}

func TestLoadAll_AgentsRootLoadsLoosePersonas(t *testing.T) {
	src, err := filepath.Abs(filepath.Join("testdata", "agent_registry", "multi"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	res, err := LoadAll(LoadAllConfig{AgentsRoot: src})
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(res.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(res.Agents))
	}
}

// TestLoadAll_ExtraAgentsRoots — regression for the marketplace reload
// path. When the AgentRegistry reloads after a pack install, the
// marketplace passes the builtin agents directory as an extra. The
// previous implementation routed extras through ExtraSkillsRoots which
// silently dropped loose *.md agent personas (only SKILL.md matches
// the wantSkills=true filter). Result: every marketplace install
// emptied the assistant page until manager restart.
func TestLoadAll_ExtraAgentsRoots(t *testing.T) {
	personas, err := filepath.Abs(filepath.Join("testdata", "agent_registry", "multi"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// Treat the persona dir as an EXTRA — primary is empty. Should
	// still load both .md personas.
	res, err := LoadAll(LoadAllConfig{ExtraAgentsRoots: []string{personas}})
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(res.Agents) != 2 {
		t.Errorf("ExtraAgentsRoots: want 2 agents, got %d", len(res.Agents))
	}
	// Sanity: ExtraSkillsRoots on the same dir would NOT yield the
	// agents (only SKILL.md matches the skills filter). This guards
	// against future refactors collapsing the two extra-lists back
	// into one without restoring the semantic split.
	res2, err := LoadAll(LoadAllConfig{ExtraSkillsRoots: []string{personas}})
	if err != nil {
		t.Fatalf("LoadAll (skills extras): %v", err)
	}
	if len(res2.Agents) != 0 {
		t.Errorf("ExtraSkillsRoots: agent personas leaked into skills walk, got %d", len(res2.Agents))
	}
}

// copyTree recursively copies the contents of src to dst. Used by
// LoadAll tests so the test's tempdir is a self-contained skills root
// (no symlink escape).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o600)
	})
}
