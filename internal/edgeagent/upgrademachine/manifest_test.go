package upgrademachine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestFile 创建一个文件，内容为 content，返回其 sha256。
func writeTestFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// writeManifest 写一个 MANIFEST.txt（每行 `<sha> <mode> <src> <dest>`）。
func writeManifest(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write manifest %s: %v", path, err)
	}
}

func TestParseManifest_ValidSingleEntry(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "MANIFEST.txt")
	writeManifest(t, manPath, []string{
		"abc123def4567890abcdef1234567890abcdef1234567890abcdef1234567890ab 0755 bin/worker.exe /usr/local/bin/worker",
	})

	entries, err := ParseManifest(manPath)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.SHA256 != "abc123def4567890abcdef1234567890abcdef1234567890abcdef1234567890ab" {
		t.Errorf("SHA256 = %q", e.SHA256)
	}
	if e.Mode != "0755" {
		t.Errorf("Mode = %q", e.Mode)
	}
	if e.Src != "bin/worker.exe" {
		t.Errorf("Src = %q", e.Src)
	}
	if e.Dest != "/usr/local/bin/worker" {
		t.Errorf("Dest = %q", e.Dest)
	}
}

func TestParseManifest_MultipleEntriesWithComments(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "MANIFEST.txt")
	writeManifest(t, manPath, []string{
		"# ongrid-edge bundle v0.7.50",
		"",
		"aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa1111bbbb2222 0755 worker.exe C:\\bin\\worker.exe",
		"# plugin section",
		"bbbb3333cccc4444dddd5555eeee6666ffff7777aaaa8888bbbb3333cccc4444 0644 exporter.exe C:\\bin\\exporter.exe",
	})

	entries, err := ParseManifest(manPath)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (skip comment + blank), got %d", len(entries))
	}
	if entries[0].Src != "worker.exe" {
		t.Errorf("entries[0].Src = %q", entries[0].Src)
	}
	if entries[1].Src != "exporter.exe" {
		t.Errorf("entries[1].Src = %q", entries[1].Src)
	}
}

func TestParseManifest_MalformedLine(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "MANIFEST.txt")
	writeManifest(t, manPath, []string{
		"abc 0755 only_three_fields",
	})

	_, err := ParseManifest(manPath)
	if err == nil {
		t.Fatal("expected error for malformed line, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error should mention malformed, got: %v", err)
	}
}

func TestParseManifest_EmptyManifest(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "MANIFEST.txt")
	writeManifest(t, manPath, []string{
		"# only comments",
		"",
		"   ",
	})

	_, err := ParseManifest(manPath)
	if err == nil {
		t.Fatal("expected error for zero-file manifest, got nil")
	}
	if !strings.Contains(err.Error(), "zero") {
		t.Errorf("error should mention zero files, got: %v", err)
	}
}

func TestParseManifest_DestWithSpaces(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "MANIFEST.txt")
	writeManifest(t, manPath, []string{
		"aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa1111bbbb2222 0755 worker.exe C:\\Program Files\\ongrid-edge\\bin\\worker.exe",
		"bbbb3333cccc4444dddd5555eeee6666ffff7777aaaa8888bbbb3333cccc4444 0755 exporter.exe C:\\Program Files\\ongrid-edge\\bin\\exporter.exe",
	})

	entries, err := ParseManifest(manPath)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	want0 := `C:\Program Files\ongrid-edge\bin\worker.exe`
	if entries[0].Dest != want0 {
		t.Errorf("entries[0].Dest = %q, want %q", entries[0].Dest, want0)
	}
	want1 := `C:\Program Files\ongrid-edge\bin\exporter.exe`
	if entries[1].Dest != want1 {
		t.Errorf("entries[1].Dest = %q, want %q", entries[1].Dest, want1)
	}
}

func TestParseManifest_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseManifest(filepath.Join(dir, "nonexistent.txt"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestVerifyEntry_SHA_MATCH(t *testing.T) {
	dir := t.TempDir()
	content := "fake binary content"
	srcPath := filepath.Join(dir, "worker.exe")
	realSHA := writeTestFile(t, srcPath, content)

	entry := ManifestEntry{SHA256: realSHA, Src: "worker.exe"}
	if err := VerifyEntry(dir, entry); err != nil {
		t.Fatalf("VerifyEntry should pass for matching SHA: %v", err)
	}
}

func TestVerifyEntry_SHA_MISMATCH(t *testing.T) {
	dir := t.TempDir()
	content := "fake binary content"
	writeTestFile(t, filepath.Join(dir, "worker.exe"), content)

	entry := ManifestEntry{SHA256: "0000000000000000000000000000000000000000000000000000000000000000", Src: "worker.exe"}
	err := VerifyEntry(dir, entry)
	if err == nil {
		t.Fatal("expected error for SHA mismatch")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should mention mismatch, got: %v", err)
	}
}

func TestVerifyEntry_SRC_MISSING(t *testing.T) {
	dir := t.TempDir()
	entry := ManifestEntry{SHA256: "abc", Src: "nonexistent.exe"}
	err := VerifyEntry(dir, entry)
	if err == nil {
		t.Fatal("expected error for missing src file")
	}
}

func TestVerifyAll_AllMatch(t *testing.T) {
	incoming := t.TempDir()
	sha1 := writeTestFile(t, filepath.Join(incoming, "a.exe"), "content-a")
	sha2 := writeTestFile(t, filepath.Join(incoming, "b.exe"), "content-b")

	entries := []ManifestEntry{
		{SHA256: sha1, Src: "a.exe"},
		{SHA256: sha2, Src: "b.exe"},
	}
	if err := VerifyAll(incoming, entries); err != nil {
		t.Fatalf("VerifyAll should pass: %v", err)
	}
}

func TestVerifyAll_OneFails(t *testing.T) {
	incoming := t.TempDir()
	sha1 := writeTestFile(t, filepath.Join(incoming, "a.exe"), "content-a")
	writeTestFile(t, filepath.Join(incoming, "b.exe"), "content-b")

	entries := []ManifestEntry{
		{SHA256: sha1, Src: "a.exe"},
		{SHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", Src: "b.exe"},
	}
	err := VerifyAll(incoming, entries)
	if err == nil {
		t.Fatal("expected error for one mismatch")
	}
}
