package marketplace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
)

// TestBuildCapabilityDeclaration_BareSkillCredentials guards the terraform-runner
// regression: a pack that ships only a .claude-plugin/marketplace.json (NOT a
// plugin.json) plus a root-level SKILL.md is loaded via the bare-skills path.
// The skill's metadata.requires.credentials must still surface in the capability
// summary so the design-time binding UI (HLD-017) can offer the slot.
func TestBuildCapabilityDeclaration_BareSkillCredentials(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"),
		`{"name":"terraform-runner","plugins":[{"name":"terraform-runner","source":"./","version":"1.9.8"}]}`)
	mustWriteFile(t, filepath.Join(dir, "SKILL.md"), `---
name: terraform-runner
description: "run terraform"
metadata:
  requires:
    bins: [terraform]
    credentials:
      - slot: cloud
        label: cloud creds
        fields: [secret_id, secret_key]
        inject:
          env:
            TF_VAR_secret_id: "{{secret_id}}"
  ongrid:
    scope: manager
    activation:
      mode: keyword
      keywords: [terraform]
---
# Terraform Runner
body
`)

	res, err := chatruntime.LoadPluginContainer(dir)
	if err != nil {
		t.Fatalf("LoadPluginContainer: %v", err)
	}
	caps := buildCapabilityDeclaration("terraform-runner", "0.0.0", res)
	if len(caps.Summary.CredentialSlots) != 1 {
		t.Fatalf("want 1 credential slot, got %d (%+v)", len(caps.Summary.CredentialSlots), caps.Summary.CredentialSlots)
	}
	if slot := caps.Summary.CredentialSlots[0]; slot.Slot != "cloud" || len(slot.Fields) != 2 {
		t.Errorf("slot = %+v, want slot=cloud with 2 fields", slot)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
