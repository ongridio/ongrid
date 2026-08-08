package upgrademachine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz 创建一个包含给定文件的 .tar.gz 字节流。
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// writePendingBundle 在 stageDir 下写入 pending（tar.gz）+ pending.sha256。
func writePendingBundle(t *testing.T, stageDir string, files map[string]string) {
	t.Helper()
	data := makeTarGz(t, files)
	if err := os.WriteFile(filepath.Join(stageDir, PendingFileName), data, 0o644); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, PendingSHA256FileName), []byte("fake-sha\n"), 0o644); err != nil {
		t.Fatalf("write pending.sha256: %v", err)
	}
}

// --- HasPendingBundle ---

func TestHasPendingBundle_NoFile(t *testing.T) {
	dir := t.TempDir()
	if HasPendingBundle(dir) {
		t.Error("expected false when pending does not exist")
	}
}

func TestHasPendingBundle_Exists(t *testing.T) {
	dir := t.TempDir()
	writePendingBundle(t, dir, map[string]string{"MANIFEST.txt": "stub"})
	if !HasPendingBundle(dir) {
		t.Error("expected true when pending exists")
	}
}

// --- ExtractPendingBundle ---

func TestExtractPendingBundle_Success(t *testing.T) {
	dir := t.TempDir()
	manifestContent := "abc123  0o755  worker.exe  C:\\bin\\worker.exe"
	writePendingBundle(t, dir, map[string]string{
		"MANIFEST.txt": manifestContent,
		"VERSION":      "v0.9.3",
	})

	if err := ExtractPendingBundle(dir); err != nil {
		t.Fatalf("ExtractPendingBundle: %v", err)
	}

	// 验证 incoming/MANIFEST.txt 存在且内容正确
	manifestPath := ManifestPath(dir)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read extracted MANIFEST.txt: %v", err)
	}
	if string(data) != manifestContent {
		t.Errorf("MANIFEST.txt content mismatch: got %q", string(data))
	}

	// 验证 incoming/VERSION 存在
	versionPath := filepath.Join(dir, IncomingDirName, "VERSION")
	if _, err := os.Stat(versionPath); err != nil {
		t.Errorf("VERSION not extracted: %v", err)
	}
}

func TestExtractPendingBundle_DeletesPendingAndSHA256(t *testing.T) {
	dir := t.TempDir()
	writePendingBundle(t, dir, map[string]string{"MANIFEST.txt": "stub"})

	if err := ExtractPendingBundle(dir); err != nil {
		t.Fatalf("ExtractPendingBundle: %v", err)
	}

	// pending + pending.sha256 应被删除（防止重复解压）
	if _, err := os.Stat(filepath.Join(dir, PendingFileName)); !os.IsNotExist(err) {
		t.Errorf("pending should be deleted after extract, got err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, PendingSHA256FileName)); !os.IsNotExist(err) {
		t.Errorf("pending.sha256 should be deleted after extract, got err: %v", err)
	}
}

func TestExtractPendingBundle_BadGzip(t *testing.T) {
	dir := t.TempDir()
	// 写入损坏数据（不是合法 tar.gz）
	if err := os.WriteFile(filepath.Join(dir, PendingFileName), []byte("not a gzip"), 0o644); err != nil {
		t.Fatalf("write bad pending: %v", err)
	}

	err := ExtractPendingBundle(dir)
	if err == nil {
		t.Fatal("expected error for bad gzip data")
	}
}

func TestExtractPendingBundle_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	// 构造包含路径逃逸的 tar.gz
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// 正常文件
	tw.WriteHeader(&tar.Header{Name: "MANIFEST.txt", Mode: 0o644, Size: 4})
	tw.Write([]byte("safe"))
	// 恶意路径逃逸
	tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1})
	tw.Write([]byte("X"))
	tw.Close()
	gz.Close()

	if err := os.WriteFile(filepath.Join(dir, PendingFileName), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write pending: %v", err)
	}

	err := ExtractPendingBundle(dir)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "..", "escape.txt")); !os.IsNotExist(statErr) {
		t.Errorf("escape file should not exist on disk")
	}
}

func TestExtractPendingBundle_NoPendingFile(t *testing.T) {
	dir := t.TempDir()
	err := ExtractPendingBundle(dir)
	if err == nil {
		t.Fatal("expected error when pending file does not exist")
	}
}

func TestExtractPendingBundle_OverwritesOldIncoming(t *testing.T) {
	dir := t.TempDir()
	// 先放旧 incoming/ 残留
	oldFile := filepath.Join(dir, IncomingDirName, "stale.txt")
	os.MkdirAll(filepath.Dir(oldFile), 0o755)
	os.WriteFile(oldFile, []byte("stale"), 0o644)

	writePendingBundle(t, dir, map[string]string{"MANIFEST.txt": "fresh"})

	if err := ExtractPendingBundle(dir); err != nil {
		t.Fatalf("ExtractPendingBundle: %v", err)
	}

	// 旧文件应被清理
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("stale file in old incoming/ should be removed")
	}
}
