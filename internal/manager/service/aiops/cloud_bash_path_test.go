package aiops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillBinPATH_PrependsInstalledSkillBins verifies that cloud_bash's PATH
// exposes every installed skill's bin dir (so a CLI an extension ships, e.g.
// tccli, is callable) while still falling back to the system PATH.
func TestSkillBinPATH_PrependsInstalledSkillBins(t *testing.T) {
	root := t.TempDir()
	// pack-a ships a bin/; pack-b is bin-less (markdown-only skill); a stray
	// file at the root must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "pack-a", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pack-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := skillBinPATH(root)
	wantBin := filepath.Join(root, "pack-a", "bin")
	if !strings.HasPrefix(got, wantBin+":") {
		t.Errorf("PATH should start with %q, got %q", wantBin, got)
	}
	if !strings.HasSuffix(got, cloudBashSystemPATH) {
		t.Errorf("PATH should end with the system PATH, got %q", got)
	}
	if strings.Contains(got, filepath.Join(root, "pack-b")) {
		t.Errorf("bin-less pack must not be on PATH, got %q", got)
	}
}

// Missing root / no skill bins → the system PATH unchanged.
func TestSkillBinPATH_Fallback(t *testing.T) {
	if got := skillBinPATH(""); got != cloudBashSystemPATH {
		t.Errorf("empty root: got %q want system PATH", got)
	}
	if got := skillBinPATH(filepath.Join(t.TempDir(), "does-not-exist")); got != cloudBashSystemPATH {
		t.Errorf("missing root: got %q want system PATH", got)
	}
	if got := skillBinPATH(t.TempDir()); got != cloudBashSystemPATH {
		t.Errorf("empty dir: got %q want system PATH", got)
	}
}
