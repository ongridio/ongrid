package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSession_PersistentAndCreated(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	dir, err := m.Session("sess-abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "sessions", "sess-abc123")
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("session dir not created: %v", err)
	}
	// Same session id → same dir (persistent across turns), and a file written
	// in one call survives to the next.
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, _ := m.Session("sess-abc123")
	if again != dir {
		t.Fatalf("second call = %q, want stable %q", again, dir)
	}
	if _, err := os.Stat(filepath.Join(again, "main.tf")); err != nil {
		t.Fatalf("file did not persist across calls: %v", err)
	}
}

func TestSession_DisabledOrUnsafe(t *testing.T) {
	// Empty root → workspace disabled → "" (caller uses transient temp dir).
	if d, err := New("").Session("sess-1"); err != nil || d != "" {
		t.Fatalf("empty root: got (%q,%v), want (\"\",nil)", d, err)
	}
	m := New(t.TempDir())
	// Empty / unsafe session ids → "" (fall back), never escape the root.
	for _, id := range []string{"", "..", ".", "/", "../../etc"} {
		d, err := m.Session(id)
		if err != nil {
			t.Fatalf("id %q: unexpected err %v", id, err)
		}
		if d != "" && !strings.HasPrefix(d, m.Root()) {
			t.Fatalf("id %q escaped root: %q", id, d)
		}
	}
}

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"abc-123":      "abc-123",
		"../../etc":    "....etc", // separators stripped, stays inside root
		"a/b":          "ab",
		"":             "",
		"..":           "",
		"恶意/../x":      "..x",
		"UUID_v4-AB.9": "UUID_v4-AB.9",
	}
	for in, want := range cases {
		if got := sanitizeID(in); got != want {
			t.Errorf("sanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}
