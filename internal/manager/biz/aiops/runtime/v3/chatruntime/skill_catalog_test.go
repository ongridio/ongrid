package chatruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

func TestSkillCatalogActivationAndSnapshot(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Add(&Skill{Name: "manual", Description: "manual help", PromptBody: "original", Activation: Activation{Mode: "on_demand"}})
	reg.Add(&Skill{Name: "legacy", Description: "legacy help", PromptBody: "always"})
	active, loader, catalog := reg.catalog("hello", Policy{AllowedClasses: []string{"*"}})
	defer loader.close()
	if len(active) != 1 || active[0].Name != "legacy" || !strings.Contains(catalog, "manual help") {
		t.Fatalf("active=%v catalog=%s", active, catalog)
	}
	reg.All()[0].PromptBody = "modified by caller"
	out, err := loader.InvokableRun(context.Background(), `{"name":"manual"}`)
	if err != nil || !strings.Contains(out, "original") {
		t.Fatalf("load=%s err=%v", out, err)
	}
	if len(loader.refs()) != 1 || loader.refs()[0].Name != "manual" {
		t.Fatal("load did not record actual skill")
	}
	if err := reg.Reload(""); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.InvokableRun(context.Background(), `{"name":"manual"}`); err == nil {
		t.Fatal("removed skill still loads")
	}
}

func TestSkillAssetUpgradeRetentionAndUninstall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: demo\ndescription: demo\nversion: v1\n---\nold instructions"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.sh"), []byte("old script"), 0o700); err != nil {
		t.Fatal(err)
	}
	reg := NewSkillRegistry()
	if err := reg.Load(root); err != nil {
		t.Fatal(err)
	}
	_, loader, _ := reg.catalog("hello", Policy{AllowedClasses: []string{"*"}})
	sk := reg.All()[0]
	ref, err := loader.load(context.Background(), sk)
	if err != nil {
		t.Fatal(err)
	}
	release, err := reg.AcquireSkillAssets(context.Background(), []basetool.SkillAssetRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.sh"), []byte("new script"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(root); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(ref.Directory, "script.sh"))
	if err != nil || string(old) != "old script" {
		t.Fatalf("old=%s err=%v", old, err)
	}
	if err := reg.Reload(""); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.AcquireSkillAssets(context.Background(), []basetool.SkillAssetRef{ref}); err == nil {
		t.Fatal("revoked snapshot acquired")
	}
	loader.close()
	if _, err := os.Stat(ref.Directory); err != nil {
		t.Fatal("running command lost assets", err)
	}
	release()
	if _, err := os.Stat(ref.Directory); !os.IsNotExist(err) {
		t.Fatalf("snapshot leaked: %v", err)
	}
}

func TestSkillSnapshotIncludesSharedPluginAssets(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"openclaw.plugin.json": `{"id":"shared-assets","name":"shared-assets","version":"1","skills":["skills"]}`,
		"skills/demo/SKILL.md": "---\nname: demo\ndescription: demo\n---\nUse ../../bin/shared.sh",
		"bin/shared.sh":        "echo shared",
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewSkillRegistry()
	if err := reg.Load(root); err != nil {
		t.Fatal(err)
	}
	_, loader, _ := reg.catalog("test", Policy{AllowedClasses: []string{"*"}})
	defer loader.close()
	ref, err := loader.load(context.Background(), reg.All()[0])
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ref.Directory, "../../bin/shared.sh"))
	if err != nil || string(data) != "echo shared" {
		t.Fatalf("shared asset=%s err=%v", data, err)
	}
	if ref.PackageDirectory == ref.Directory {
		t.Fatal("nested skill must retain its package root")
	}
}
