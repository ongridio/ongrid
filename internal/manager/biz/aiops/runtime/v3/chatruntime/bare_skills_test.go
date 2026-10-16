package chatruntime

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectContainer_BareSkillsDir — a skills.sh-style drop is a
// directory with skills/<name>/SKILL.md and NO plugin manifest. Should
// be recognized as ContainerBareSkills.
func TestDetectContainer_BareSkillsDir(t *testing.T) {
	root := t.TempDir()
	writeSkillMd(t, filepath.Join(root, "skills", "find-skills", "SKILL.md"), `---
name: find-skills
description: Find and install agent skills when asked
---

Body of the skill.
`)
	kind, manifest, err := DetectContainer(root)
	if err != nil {
		t.Fatalf("DetectContainer: %v", err)
	}
	if kind != ContainerBareSkills {
		t.Errorf("want ContainerBareSkills, got %q", kind)
	}
	if manifest != "" {
		t.Errorf("bare-skills has no manifest path, got %q", manifest)
	}
}

// TestDetectContainer_BareSkillsRootSkillMd — a single-skill pack drops
// SKILL.md at the root of the cloned directory.
func TestDetectContainer_BareSkillsRootSkillMd(t *testing.T) {
	root := t.TempDir()
	writeSkillMd(t, filepath.Join(root, "SKILL.md"), `---
name: solo-skill
description: A standalone skill at the root
---
body
`)
	kind, _, err := DetectContainer(root)
	if err != nil {
		t.Fatalf("DetectContainer: %v", err)
	}
	if kind != ContainerBareSkills {
		t.Errorf("want ContainerBareSkills, got %q", kind)
	}
}

// TestDetectContainer_ClaudeWins — when both .claude-plugin/plugin.json
// AND skills/ exist, the claude marker wins (existing behaviour).
func TestDetectContainer_ClaudeWins(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"),
		[]byte(`{"id":"x","name":"X","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	writeSkillMd(t, filepath.Join(root, "skills", "s", "SKILL.md"), `---
name: s
description: d
---`)

	kind, _, err := DetectContainer(root)
	if err != nil {
		t.Fatalf("DetectContainer: %v", err)
	}
	if kind != ContainerClaude {
		t.Errorf("want ContainerClaude, got %q", kind)
	}
}

// TestLoadPluginContainer_BareSkillsSynthesizesPack — bare-skill load
// produces a Pack with id derived from the directory basename.
func TestLoadPluginContainer_BareSkillsSynthesizesPack(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "vercel-labs__skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkillMd(t, filepath.Join(root, "skills", "alpha", "SKILL.md"), `---
name: alpha
description: First skill
---
body alpha
`)
	writeSkillMd(t, filepath.Join(root, "skills", "beta", "SKILL.md"), `---
name: beta
description: Second skill
---
body beta
`)

	res, err := LoadPluginContainer(root)
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	if res.Pack == nil {
		t.Fatalf("Pack nil")
	}
	if res.Pack.ID != "vercel-labs-skills" {
		t.Errorf("Pack.ID = %q, want vercel-labs-skills", res.Pack.ID)
	}
	if len(res.Skills) != 2 {
		t.Errorf("want 2 skills loaded, got %d", len(res.Skills))
	}
}

// TestBareSkillsPackID — id normalisation rules.
func TestBareSkillsPackID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"vercel-labs__skills", "vercel-labs-skills"},
		{"My Awesome Pack v2", "my-awesome-pack-v2"},
		{"____", "untitled-skill-pack"},
		{"", "untitled-skill-pack"},
		{"snake_case_repo", "snake-case-repo"},
		{"already-clean", "already-clean"},
	}
	for _, tc := range cases {
		if got := bareSkillsPackID(tc.in); got != tc.want {
			t.Errorf("bareSkillsPackID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func writeSkillMd(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
