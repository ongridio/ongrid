package upgrademachine

import (
	"os"
	"path/filepath"
	"testing"
)

// buildFakeBundle 创建一个完整的 fake bundle：incoming/ 下有 MANIFEST.txt
// + 若干 src 文件。destDir 模拟部署目标目录（已有旧版本文件）。
//
// 返回 (incomingDir, destDir, entries)。
func buildFakeBundle(t *testing.T, incomingDir, destDir string,
	files []struct{ Src, Dest, Content string }) []ManifestEntry {
	t.Helper()

	// 写 src 文件 + 计算 sha
	var lines []string
	for _, f := range files {
		sha := writeTestFile(t, filepath.Join(incomingDir, f.Src), f.Content)
		lines = append(lines, sha+" 0755 "+f.Src+" "+filepath.Join(destDir, f.Dest))
	}
	// 写 MANIFEST.txt
	writeManifest(t, filepath.Join(incomingDir, ManifestFileName), lines)

	// 解析回来（验证 round-trip）
	entries, err := ParseManifest(filepath.Join(incomingDir, ManifestFileName))
	if err != nil {
		t.Fatalf("ParseManifest after build: %v", err)
	}
	return entries
}

func TestApplyBundle_AllSwapped(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	// 先放旧版本文件
	writeTestFile(t, filepath.Join(dest, "worker.exe"), "old-worker")
	writeTestFile(t, filepath.Join(dest, "exporter.exe"), "old-exporter")

	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new-worker"},
		{"exporter.exe", "exporter.exe", "new-exporter"},
	})

	result, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}
	if len(result.Swapped) != 2 {
		t.Fatalf("expected 2 swapped, got %d", len(result.Swapped))
	}

	// 验证新内容
	got, _ := os.ReadFile(filepath.Join(dest, "worker.exe"))
	if string(got) != "new-worker" {
		t.Errorf("worker.exe content = %q, want %q", got, "new-worker")
	}
	got, _ = os.ReadFile(filepath.Join(dest, "exporter.exe"))
	if string(got) != "new-exporter" {
		t.Errorf("exporter.exe content = %q, want %q", got, "new-exporter")
	}

	// 验证 .previous 备份存在且内容是旧版本
	got, _ = os.ReadFile(filepath.Join(dest, "worker.exe.previous"))
	if string(got) != "old-worker" {
		t.Errorf("worker.exe.previous content = %q, want %q", got, "old-worker")
	}
}

func TestApplyBundle_DestNotExists_NoBackup(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	// dest 目录存在但目标文件不存在（首次安装路径）
	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{"new.exe", "new.exe", "fresh-install"},
	})

	result, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}
	if len(result.Swapped) != 1 {
		t.Fatalf("expected 1 swapped, got %d", len(result.Swapped))
	}
	if len(result.BackedUp) != 0 {
		t.Fatalf("expected 0 backed up (dest didn't exist), got %d", len(result.BackedUp))
	}

	// 不应该有 .previous 文件
	if _, err := os.Stat(filepath.Join(dest, "new.exe.previous")); !os.IsNotExist(err) {
		t.Errorf("should not have .previous when dest didn't exist")
	}
}

func TestApplyBundle_SHAVerifyFails_AbortsAll(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	// 旧文件
	oldContent := "old-worker"
	writeTestFile(t, filepath.Join(dest, "worker.exe"), oldContent)

	// 先创建 incoming 目录 + src 文件
	writeTestFile(t, filepath.Join(incoming, "worker.exe"), "new-worker")
	// 故意写错误 sha 到 MANIFEST
	writeManifest(t, filepath.Join(incoming, ManifestFileName), []string{
		"0000000000000000000000000000000000000000000000000000000000000000 0755 worker.exe " + filepath.Join(dest, "worker.exe"),
	})
	entries, err := ParseManifest(filepath.Join(incoming, ManifestFileName))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	_, err = ApplyBundle(stageDir, incoming, entries)
	if err == nil {
		t.Fatal("expected error for SHA mismatch")
	}

	// 验证旧文件未被改动
	got, _ := os.ReadFile(filepath.Join(dest, "worker.exe"))
	if string(got) != oldContent {
		t.Errorf("dest should be unchanged after SHA failure, got %q", got)
	}
}

func TestApplyBundle_Idempotent_DestNewExists(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	// 模拟上次 swap 中途失败：dest.new 已存在但 dest 未替换
	writeTestFile(t, filepath.Join(dest, "worker.exe"), "old-worker")
	writeTestFile(t, filepath.Join(dest, "worker.exe.new"), "new-worker-from-crash")

	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new-worker"},
	})

	result, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle should handle leftover .new: %v", err)
	}
	if len(result.Swapped) != 1 {
		t.Fatalf("expected 1 swapped, got %d", len(result.Swapped))
	}

	// 验证最终文件是新版本
	got, _ := os.ReadFile(filepath.Join(dest, "worker.exe"))
	if string(got) != "new-worker" {
		t.Errorf("worker.exe content = %q, want %q", got, "new-worker")
	}
	// .new 应该被清理（被 rename 掉了）
	if _, err := os.Stat(filepath.Join(dest, "worker.exe.new")); !os.IsNotExist(err) {
		t.Errorf(".new should be gone after successful swap")
	}
}

func TestApplyBundle_CreatesDestDir(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := filepath.Join(t.TempDir(), "deploy")

	// dest 目录不存在 — ApplyBundle 应该创建
	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{"app.exe", "app.exe", "content"},
	})

	_, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle should create dest dir: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dest, "app.exe"))
	if string(got) != "content" {
		t.Errorf("app.exe content = %q", got)
	}
}
