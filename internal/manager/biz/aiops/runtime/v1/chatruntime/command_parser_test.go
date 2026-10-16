package chatruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemp writes content to a fresh tempdir/file and returns the path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestConvertCommandFile_Minimal(t *testing.T) {
	path := writeTemp(t, "commit.md", `---
description: Commit the staged changes.
---

Run git commit with a conventional message.
`)
	sk, warns, err := ConvertCommandFile(path)
	if err != nil {
		t.Fatalf("ConvertCommandFile: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %+v", warns)
	}
	if sk.Name != "cmd_commit" {
		t.Errorf("Name = %q, want cmd_commit", sk.Name)
	}
	if sk.Description != "Commit the staged changes." {
		t.Errorf("Description = %q", sk.Description)
	}
	if sk.Activation.Mode != "keyword" {
		t.Errorf("Activation.Mode = %q, want keyword", sk.Activation.Mode)
	}
	if !hasName(sk.Activation.Keywords, "/commit") {
		t.Errorf("missing /commit keyword; got %v", sk.Activation.Keywords)
	}
	if !hasName(sk.Activation.Keywords, "commit") {
		t.Errorf("missing commit keyword; got %v", sk.Activation.Keywords)
	}
	// metadata.ongrid.activation should mirror the top-level activation
	// so downstream consumers reading either key see keyword mode.
	if sk.Metadata.Ongrid.Activation.Mode != "keyword" {
		t.Errorf("metadata.ongrid.activation.mode = %q, want keyword", sk.Metadata.Ongrid.Activation.Mode)
	}
	if len(sk.Tools) != 0 {
		t.Errorf("Tools should be empty; got %d", len(sk.Tools))
	}
	if !strings.HasPrefix(strings.TrimSpace(sk.PromptBody), "[能力: cmd_commit]") {
		t.Errorf("body should start with [能力: cmd_commit]; got:\n%s", sk.PromptBody)
	}
}

func TestConvertCommandFile_AllowedToolsAppendedToBody(t *testing.T) {
	path := writeTemp(t, "deploy.md", `---
description: Deploy to staging.
allowed-tools: [Bash, Edit, Read]
---

Run the deploy script.
`)
	sk, _, err := ConvertCommandFile(path)
	if err != nil {
		t.Fatalf("ConvertCommandFile: %v", err)
	}
	for _, tool := range []string{"Bash", "Edit", "Read"} {
		if !strings.Contains(sk.PromptBody, tool) {
			t.Errorf("body should mention allowed tool %q; got:\n%s", tool, sk.PromptBody)
		}
	}
	// Soft-hint, not enforcement: Tools slice is still empty.
	if len(sk.Tools) != 0 {
		t.Errorf("Tools should remain empty (soft hint, not hard restriction); got %d", len(sk.Tools))
	}
}

func TestConvertCommandFile_DashedBasename_SnakeAlias(t *testing.T) {
	path := writeTemp(t, "review-pr.md", `---
description: Review the PR diff.
---

Walk through the diff and call out issues.
`)
	sk, _, err := ConvertCommandFile(path)
	if err != nil {
		t.Fatalf("ConvertCommandFile: %v", err)
	}
	if sk.Name != "cmd_review_pr" {
		t.Errorf("Name = %q, want cmd_review_pr", sk.Name)
	}
	for _, kw := range []string{"/review-pr", "review-pr", "review_pr"} {
		if !hasName(sk.Activation.Keywords, kw) {
			t.Errorf("missing keyword %q; got %v", kw, sk.Activation.Keywords)
		}
	}
}

func TestConvertCommandFile_NonMarkdown_Error(t *testing.T) {
	path := writeTemp(t, "weird.txt", `not markdown`)
	if _, _, err := ConvertCommandFile(path); err == nil {
		t.Fatalf("expected error for .txt file")
	}
}

func TestConvertCommandFile_NoFrontmatter_GeneratesDescription(t *testing.T) {
	path := writeTemp(t, "raw.md", "Just a plain markdown body, no frontmatter.\n")
	sk, warns, err := ConvertCommandFile(path)
	if err != nil {
		t.Fatalf("ConvertCommandFile: %v", err)
	}
	if sk.Name != "cmd_raw" {
		t.Errorf("Name = %q, want cmd_raw", sk.Name)
	}
	if sk.Description == "" {
		t.Errorf("Description should be generated when missing")
	}
	hasMissingDesc := false
	for _, w := range warns {
		if w.Code == "command_missing_description" {
			hasMissingDesc = true
		}
	}
	if !hasMissingDesc {
		t.Errorf("expected command_missing_description warning; got %+v", warns)
	}
}

func TestConvertCommandFile_NestedFlattens(t *testing.T) {
	// A file at commands/git/commit.md should flatten to cmd_commit by
	// basename — confirms the loader's flatten policy at unit level.
	dir := t.TempDir()
	nested := filepath.Join(dir, "git")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(nested, "commit.md")
	if err := os.WriteFile(path, []byte(`---
description: nested commit cmd
---
body`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sk, _, err := ConvertCommandFile(path)
	if err != nil {
		t.Fatalf("ConvertCommandFile: %v", err)
	}
	if sk.Name != "cmd_commit" {
		t.Errorf("Name = %q, want cmd_commit (basename flatten)", sk.Name)
	}
}
