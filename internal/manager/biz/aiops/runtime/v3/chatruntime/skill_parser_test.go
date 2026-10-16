package chatruntime

import (
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSkill returns the absolute path to a SKILL.md fixture under
// testdata/skill_parser/<scenario>/SKILL.md.
func fixtureSkill(scenario string) string {
	return filepath.Join("testdata", "skill_parser", scenario, "SKILL.md")
}

func TestParseSkillMd_Minimal(t *testing.T) {
	sk, warns, err := ParseSkillMd(fixtureSkill("minimal"))
	if err != nil {
		t.Fatalf("ParseSkillMd: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %+v", warns)
	}
	if sk.Name != "hello_world" {
		t.Errorf("Name = %q, want hello_world", sk.Name)
	}
	if sk.Description == "" {
		t.Error("Description is empty")
	}
	// Body's H1 should be normalized to [能力: hello_world].
	if !strings.HasPrefix(sk.PromptBody, "[能力: hello_world]") {
		t.Errorf("PromptBody = %q, want H1 normalized", sk.PromptBody)
	}
	if !strings.Contains(sk.PromptBody, "Hello") {
		t.Errorf("PromptBody lost the body content: %q", sk.PromptBody)
	}
}

func TestParseSkillMd_Typical(t *testing.T) {
	sk, warns, err := ParseSkillMd(fixtureSkill("typical"))
	if err != nil {
		t.Fatalf("ParseSkillMd: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %+v", warns)
	}
	if sk.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", sk.Version)
	}
	wantOS := []string{"darwin", "linux"}
	if got := sk.Metadata.OS; !strSliceEq(got, wantOS) {
		t.Errorf("Metadata.OS = %v, want %v", got, wantOS)
	}
	if got := sk.Metadata.Requires.Bins; !strSliceEq(got, []string{"find", "du"}) {
		t.Errorf("Metadata.Requires.Bins = %v", got)
	}
	if got := sk.Metadata.Requires.Config; !strSliceEq(got, []string{"accountsPath"}) {
		t.Errorf("Metadata.Requires.Config = %v", got)
	}
	if len(sk.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2", len(sk.Tools))
	}
	if sk.Tools[0].Name != "find_files" || sk.Tools[0].Class != ClassRead {
		t.Errorf("Tools[0] = %+v", sk.Tools[0])
	}
	if sk.Tools[0].WhenToUse == "" {
		t.Error("Tools[0].WhenToUse is empty — snake_case decode failed")
	}
}

func TestParseSkillMd_WithWhenToUse(t *testing.T) {
	sk, _, err := ParseSkillMd(fixtureSkill("with_when_to_use"))
	if err != nil {
		t.Fatalf("ParseSkillMd: %v", err)
	}
	if sk.WhenToUse == "" {
		t.Fatal("WhenToUse is empty")
	}
	if !strings.Contains(sk.WhenToUse, "log content") {
		t.Errorf("WhenToUse content unexpected: %q", sk.WhenToUse)
	}
}

func TestParseSkillMd_WithOngridExt(t *testing.T) {
	sk, warns, err := ParseSkillMd(fixtureSkill("with_ongrid_ext"))
	if err != nil {
		t.Fatalf("ParseSkillMd: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %+v", warns)
	}
	if sk.Metadata.Ongrid.Scope != "edge" {
		t.Errorf("Ongrid.Scope = %q, want edge", sk.Metadata.Ongrid.Scope)
	}
	if sk.Metadata.Ongrid.EdgeRuntime != "subprocess" {
		t.Errorf("Ongrid.EdgeRuntime = %q", sk.Metadata.Ongrid.EdgeRuntime)
	}
	if len(sk.Metadata.Ongrid.EdgeCapabilities) != 2 {
		t.Errorf("EdgeCapabilities len = %d, want 2", len(sk.Metadata.Ongrid.EdgeCapabilities))
	}
	if sk.Metadata.Ongrid.MinOngridVersion != ">=0.7.30" {
		t.Errorf("MinOngridVersion = %q", sk.Metadata.Ongrid.MinOngridVersion)
	}
	// metadata.ongrid.activation should win and be reflected in
	// top-level Activation per the parser's reconciliation rule.
	if sk.Activation.Mode != "keyword" {
		t.Errorf("Activation.Mode = %q, want keyword (from ongrid ext)", sk.Activation.Mode)
	}
	if !strSliceEq(sk.Activation.Keywords, []string{"disk", "磁盘", "du"}) {
		t.Errorf("Activation.Keywords = %v", sk.Activation.Keywords)
	}
}

func TestParseSkillMd_WithUnknownFields(t *testing.T) {
	sk, _, err := ParseSkillMd(fixtureSkill("with_unknown_fields"))
	if err != nil {
		t.Fatalf("ParseSkillMd: %v", err)
	}
	if sk.UnknownFields == nil {
		t.Fatal("UnknownFields is nil; expected upstream-only keys preserved")
	}
	if _, ok := sk.UnknownFields["custom_field_one"]; !ok {
		t.Errorf("custom_field_one not preserved; got keys %v", keys(sk.UnknownFields))
	}
	if _, ok := sk.UnknownFields["custom_field_two"]; !ok {
		t.Errorf("custom_field_two not preserved")
	}
	if _, ok := sk.UnknownFields["upstream_only_array"]; !ok {
		t.Errorf("upstream_only_array not preserved")
	}
	// Known fields should NOT leak into UnknownFields.
	if _, ok := sk.UnknownFields["name"]; ok {
		t.Errorf("'name' should not appear in UnknownFields")
	}
}

func TestParseSkillMd_NameWithDash(t *testing.T) {
	sk, warns, err := ParseSkillMd(fixtureSkill("name_with_dash"))
	if err != nil {
		t.Fatalf("ParseSkillMd: %v", err)
	}
	if sk.Name != "hello_world" {
		t.Errorf("Name = %q, want normalized hello_world", sk.Name)
	}
	found := false
	for _, w := range warns {
		if w.Code == "name_normalized" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected name_normalized warning; got %+v", warns)
	}
}

func TestParseSkillMd_MissingDescription(t *testing.T) {
	_, _, err := ParseSkillMd(fixtureSkill("missing_description"))
	if err == nil {
		t.Fatal("expected error for missing description, got nil")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error should mention 'description'; got %v", err)
	}
}

func TestParseSkillMd_FileMissing(t *testing.T) {
	_, _, err := ParseSkillMd(filepath.Join("testdata", "skill_parser", "does_not_exist", "SKILL.md"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSplitFrontmatter_NoFrontmatter(t *testing.T) {
	body := []byte("# Just a body\n\nNo frontmatter here.\n")
	fm, b, err := splitFrontmatter(body)
	if err != nil {
		t.Fatalf("splitFrontmatter: %v", err)
	}
	if fm != nil {
		t.Errorf("expected nil frontmatter, got %q", fm)
	}
	if string(b) != string(body) {
		t.Errorf("body should be untouched")
	}
}

func TestSplitFrontmatter_MalformedUnclosed(t *testing.T) {
	body := []byte("---\nname: x\n\nno closing fence\n")
	_, _, err := splitFrontmatter(body)
	if err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}

func TestNormalizeSkillBodyH1_NoH1Adds(t *testing.T) {
	out := normalizeSkillBodyH1([]byte("Just a paragraph.\n"), "abc")
	if !strings.HasPrefix(out, "[能力: abc]") {
		t.Errorf("expected canonical header prepended; got %q", out)
	}
}

func TestNormalizeSkillBodyH1_AlreadyTagged(t *testing.T) {
	body := []byte("[能力: abc]\n\nbody\n")
	out := normalizeSkillBodyH1(body, "abc")
	if strings.Count(out, "[能力: abc]") != 1 {
		t.Errorf("expected single tag header; got %q", out)
	}
}

// --- helpers ---

func strSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
