package chatruntime

import (
	"path/filepath"
	"strings"
	"testing"
)

func fixturePack(name string) string {
	return filepath.Join("testdata", "plugin_container", name)
}

func TestDetectContainer_Claude(t *testing.T) {
	kind, path, err := DetectContainer(fixturePack("claude_pack"))
	if err != nil {
		t.Fatalf("DetectContainer: %v", err)
	}
	if kind != ContainerClaude {
		t.Errorf("kind = %q, want claude", kind)
	}
	if filepath.Base(path) != "plugin.json" {
		t.Errorf("path = %q", path)
	}
}

func TestDetectContainer_Openclaw(t *testing.T) {
	kind, path, err := DetectContainer(fixturePack("openclaw_pack"))
	if err != nil {
		t.Fatalf("DetectContainer: %v", err)
	}
	if kind != ContainerOpenclaw {
		t.Errorf("kind = %q, want openclaw", kind)
	}
	if filepath.Base(path) != "openclaw.plugin.json" {
		t.Errorf("path = %q", path)
	}
}

func TestDetectContainer_None(t *testing.T) {
	kind, _, err := DetectContainer(t.TempDir())
	if err != nil {
		t.Fatalf("DetectContainer: %v", err)
	}
	if kind != ContainerNone {
		t.Errorf("kind = %q, want none", kind)
	}
}

func TestLoadPluginContainer_Claude(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("claude_pack"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	if res.Pack == nil {
		t.Fatal("Pack is nil")
	}
	if res.Pack.ID != "acme-tools" {
		t.Errorf("ID = %q", res.Pack.ID)
	}
	if res.Pack.Version != "1.0.0" {
		t.Errorf("Version = %q", res.Pack.Version)
	}
	if res.Pack.DisplayName != "Acme Tools" {
		t.Errorf("DisplayName = %q", res.Pack.DisplayName)
	}
	if len(res.Pack.ConfigSchema) == 0 {
		t.Error("ConfigSchema empty")
	}
	if res.Pack.ManifestSHA256 == "" {
		t.Error("ManifestSHA256 empty")
	}
	if res.Pack.SignatureState != "unsigned" {
		t.Errorf("SignatureState = %q", res.Pack.SignatureState)
	}
}

func TestLoadPluginContainer_OpenclawLegacyPreserved(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("openclaw_pack"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	if res.Pack.ID != "openclaw-bundle" {
		t.Errorf("ID = %q", res.Pack.ID)
	}
	legacy, ok := res.Pack.UIMetadata["openclaw_legacy"].(map[string]any)
	if !ok {
		t.Fatalf("openclaw_legacy missing or wrong type: %#v", res.Pack.UIMetadata)
	}
	for _, k := range []string{"providers", "channels", "cliBackends", "providerAuthChoices"} {
		if _, ok := legacy[k]; !ok {
			t.Errorf("openclaw_legacy missing key %q; got %v", k, mapKeys(legacy))
		}
	}
	hasLegacyWarn := false
	for _, w := range res.Warnings {
		if w.Code == "openclaw_legacy_preserved" {
			hasLegacyWarn = true
		}
	}
	if !hasLegacyWarn {
		t.Errorf("expected openclaw_legacy_preserved warning; got %+v", res.Warnings)
	}
}

func TestLoadPluginContainer_HooksAndMcpWarnings(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("claude_pack_with_extras"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	codes := map[string]bool{}
	for _, w := range res.Warnings {
		codes[w.Code] = true
	}
	if !codes["hooks_unsupported"] {
		t.Errorf("expected hooks_unsupported warning; got %+v", res.Warnings)
	}
	if !codes["mcp_unsupported"] {
		t.Errorf("expected mcp_unsupported warning; got %+v", res.Warnings)
	}
}

func TestLoadPluginContainer_NoMarker(t *testing.T) {
	_, err := LoadPluginContainer(t.TempDir())
	if err == nil {
		t.Fatal("expected error when no marker present")
	}
}

// --- recursive load (PR-skill-load) ---

func TestLoadPluginContainer_ClaudeRecursive_Skills(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("recursive_claude"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	// Expect: 1 SKILL.md (hello_world) + 1 command-converted skill (cmd_commit).
	var helloFound, cmdCommitFound bool
	for _, sk := range res.Skills {
		if sk.Name == "hello_world" {
			helloFound = true
			if sk.Dir == "" {
				t.Errorf("hello_world.Dir empty")
			}
			if sk.Activation.Mode != "always" {
				t.Errorf("hello_world activation = %q, want always (default)", sk.Activation.Mode)
			}
		}
		if sk.Name == "cmd_commit" {
			cmdCommitFound = true
		}
	}
	if !helloFound {
		t.Errorf("hello_world skill not loaded; got %v", skillNames(res.Skills))
	}
	if !cmdCommitFound {
		t.Errorf("cmd_commit (from commands/) not loaded; got %v", skillNames(res.Skills))
	}
}

func TestLoadPluginContainer_ClaudeRecursive_Agents(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("recursive_claude"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	if len(res.Agents) != 1 {
		t.Fatalf("Agents len = %d, want 1", len(res.Agents))
	}
	if res.Agents[0].Name != "pr_summary" {
		t.Errorf("agent name = %q, want pr_summary", res.Agents[0].Name)
	}
	if res.Agents[0].Dir == "" {
		t.Errorf("agent.Dir empty")
	}
}

func TestLoadPluginContainer_ClaudeRecursive_Commands(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("recursive_claude"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	var cmd *Skill
	for _, sk := range res.Skills {
		if sk.Name == "cmd_commit" {
			cmd = sk
			break
		}
	}
	if cmd == nil {
		t.Fatalf("cmd_commit not in skills; got %v", skillNames(res.Skills))
	}
	if cmd.Activation.Mode != "keyword" {
		t.Errorf("cmd_commit activation = %q, want keyword", cmd.Activation.Mode)
	}
	wantKw := map[string]bool{"/commit": false, "commit": false}
	for _, kw := range cmd.Activation.Keywords {
		if _, ok := wantKw[kw]; ok {
			wantKw[kw] = true
		}
	}
	for kw, found := range wantKw {
		if !found {
			t.Errorf("cmd_commit missing keyword %q; got %v", kw, cmd.Activation.Keywords)
		}
	}
	// Body should carry the soft-hint about claude allowed-tools.
	if !strings.Contains(cmd.PromptBody, "Bash") || !strings.Contains(cmd.PromptBody, "Edit") {
		t.Errorf("cmd_commit prompt body should include allowed-tools hint; got:\n%s", cmd.PromptBody)
	}
	if !strings.HasPrefix(strings.TrimSpace(cmd.PromptBody), "[能力: cmd_commit]") {
		t.Errorf("cmd_commit body should start with [能力: cmd_commit] header; got:\n%s", cmd.PromptBody)
	}
}

func TestLoadPluginContainer_HookSubdir_Warning(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("recursive_claude"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	codes := map[string]int{}
	for _, w := range res.Warnings {
		codes[w.Code]++
	}
	if codes["hooks_unsupported"] == 0 {
		t.Errorf("expected hooks_unsupported warning; got %v", codes)
	}
	if codes["hooks_dropped"] == 0 {
		t.Errorf("expected hooks_dropped warning per hook file; got %v", codes)
	}
}

func TestLoadPluginContainer_McpFile_Warning(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("recursive_claude"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	hasMcp := false
	for _, w := range res.Warnings {
		if w.Code == "mcp_unsupported" {
			hasMcp = true
		}
	}
	if !hasMcp {
		t.Errorf("expected mcp_unsupported warning; got %+v", res.Warnings)
	}
}

func TestLoadPluginContainer_OpenclawRecursive_RelativeSkills(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("recursive_openclaw"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	if len(res.Skills) != 2 {
		t.Fatalf("Skills len = %d, want 2 (foo, bar); got %v", len(res.Skills), skillNames(res.Skills))
	}
	names := map[string]bool{}
	for _, sk := range res.Skills {
		names[sk.Name] = true
	}
	for _, expected := range []string{"foo", "bar"} {
		if !names[expected] {
			t.Errorf("skill %q not loaded; got %v", expected, skillNames(res.Skills))
		}
	}
}

func TestLoadPluginContainer_PathTraversal_Rejected(t *testing.T) {
	res, err := LoadPluginContainer(fixturePack("path_traversal"))
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	// The pwned skill (under a symlink that escapes the pack root) MUST
	// NOT make it into res.Skills.
	for _, sk := range res.Skills {
		if sk.Name == "pwned" {
			t.Fatalf("pwned skill loaded despite path-traversal symlink: %+v", sk)
		}
	}
	// Expect an escapes_root warning.
	hasEscape := false
	for _, w := range res.Warnings {
		if w.Code == "escapes_root" {
			hasEscape = true
			if !strings.Contains(strings.ToLower(w.Reason), "path traversal") &&
				!strings.Contains(strings.ToLower(w.Reason), "escapes") {
				t.Errorf("escapes_root reason should mention traversal/escapes; got %q", w.Reason)
			}
		}
	}
	if !hasEscape {
		t.Errorf("expected escapes_root warning; got %+v", res.Warnings)
	}
}

// --- helpers ---

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
