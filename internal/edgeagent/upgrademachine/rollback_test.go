package upgrademachine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollback_RestoresPrevious(t *testing.T) {
	dest := t.TempDir()

	// 模拟 swap 后状态：新版本 + .previous 备份
	writeTestFile(t, filepath.Join(dest, "worker.exe"), "new-broken-worker")
	writeTestFile(t, filepath.Join(dest, "worker.exe.previous"), "old-good-worker")
	writeTestFile(t, filepath.Join(dest, "exporter.exe"), "new-exporter")
	writeTestFile(t, filepath.Join(dest, "exporter.exe.previous"), "old-exporter")

	restored, err := Rollback([]string{dest})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored != 2 {
		t.Fatalf("expected 2 restored, got %d", restored)
	}

	// 验证恢复为旧内容
	got, _ := os.ReadFile(filepath.Join(dest, "worker.exe"))
	if string(got) != "old-good-worker" {
		t.Errorf("worker.exe = %q, want %q", got, "old-good-worker")
	}
	got, _ = os.ReadFile(filepath.Join(dest, "exporter.exe"))
	if string(got) != "old-exporter" {
		t.Errorf("exporter.exe = %q, want %q", got, "old-exporter")
	}

	// .previous 应被 rename 掉（不再存在）
	if _, err := os.Stat(filepath.Join(dest, "worker.exe.previous")); !os.IsNotExist(err) {
		t.Errorf("worker.exe.previous should be gone after rollback")
	}
}

func TestRollback_NoPreviousFiles(t *testing.T) {
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "worker.exe"), "only-version")

	restored, err := Rollback([]string{dest})
	if err != nil {
		t.Fatalf("Rollback with no .previous should not error: %v", err)
	}
	if restored != 0 {
		t.Fatalf("expected 0 restored, got %d", restored)
	}
}

func TestRollback_MultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeTestFile(t, filepath.Join(dir1, "a.exe"), "new-a")
	writeTestFile(t, filepath.Join(dir1, "a.exe.previous"), "old-a")
	writeTestFile(t, filepath.Join(dir2, "b.exe"), "new-b")
	writeTestFile(t, filepath.Join(dir2, "b.exe.previous"), "old-b")

	restored, err := Rollback([]string{dir1, dir2})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored != 2 {
		t.Fatalf("expected 2 restored across dirs, got %d", restored)
	}
}

func TestRollback_IgnoresNonPreviousFiles(t *testing.T) {
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "worker.exe"), "current")
	writeTestFile(t, filepath.Join(dest, "readme.txt"), "info")
	writeTestFile(t, filepath.Join(dest, "worker.exe.previous"), "old")

	restored, _ := Rollback([]string{dest})
	if restored != 1 {
		t.Errorf("expected 1 restored (only .previous), got %d", restored)
	}

	// readme.txt 不应被动
	got, _ := os.ReadFile(filepath.Join(dest, "readme.txt"))
	if string(got) != "info" {
		t.Errorf("readme.txt should be untouched")
	}
}

func TestRollback_DirNotExist(t *testing.T) {
	restored, err := Rollback([]string{"/nonexistent/path/xyz"})
	if err != nil {
		t.Fatalf("Rollback on missing dir should not error: %v", err)
	}
	if restored != 0 {
		t.Errorf("expected 0 restored, got %d", restored)
	}
}

func TestCleanupPrevious_DeletesBackupFiles(t *testing.T) {
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "worker.exe"), "good-new")
	writeTestFile(t, filepath.Join(dest, "worker.exe.previous"), "old-to-delete")
	writeTestFile(t, filepath.Join(dest, "exporter.exe.previous"), "old-to-delete")

	removed, err := CleanupPrevious([]string{dest})
	if err != nil {
		t.Fatalf("CleanupPrevious: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}

	// .previous 应被删除
	for _, f := range []string{"worker.exe.previous", "exporter.exe.previous"} {
		if _, err := os.Stat(filepath.Join(dest, f)); !os.IsNotExist(err) {
			t.Errorf("%s should be deleted", f)
		}
	}
	// 非 .previous 文件不动
	got, _ := os.ReadFile(filepath.Join(dest, "worker.exe"))
	if !strings.Contains(string(got), "good-new") {
		t.Errorf("worker.exe should be untouched")
	}
}
