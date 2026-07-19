package upgrademachine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsUpgradeHealthy_Match(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v0.7.50")
	writeTestFile(t, filepath.Join(dir, HealthyMarkerFile), "v0.7.50")

	if !IsUpgradeHealthy(dir) {
		t.Error("expected healthy when versions match")
	}
}

func TestIsUpgradeHealthy_Mismatch(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v0.7.50")
	writeTestFile(t, filepath.Join(dir, HealthyMarkerFile), "v0.7.49")

	if IsUpgradeHealthy(dir) {
		t.Error("expected NOT healthy when versions mismatch")
	}
}

func TestIsUpgradeHealthy_NoMarker(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v0.7.50")
	// 不写 healthy_marker — 模拟新 worker 启动但未 register_edge

	if IsUpgradeHealthy(dir) {
		t.Error("expected NOT healthy when healthy_marker missing")
	}
}

func TestIsUpgradeHealthy_NoMeta(t *testing.T) {
	dir := t.TempDir()
	// 两个文件都不存在 — 首次安装或未升级

	if IsUpgradeHealthy(dir) {
		t.Error("expected NOT healthy when no upgrade meta exists")
	}
}

func TestWriteUpgradeMeta_WritesBoth(t *testing.T) {
	dir := t.TempDir()
	if err := WriteUpgradeMeta(dir, "v0.7.50"); err != nil {
		t.Fatalf("WriteUpgradeMeta: %v", err)
	}

	ver, err := os.ReadFile(filepath.Join(dir, LastUpgradeVerFile))
	if err != nil {
		t.Fatalf("read last_upgrade_ver: %v", err)
	}
	if strings.TrimSpace(string(ver)) != "v0.7.50" {
		t.Errorf("last_upgrade_ver = %q, want %q", ver, "v0.7.50")
	}

	at, err := os.ReadFile(filepath.Join(dir, LastUpgradeAtFile))
	if err != nil {
		t.Fatalf("read last_upgrade_at: %v", err)
	}
	if strings.TrimSpace(string(at)) == "" {
		t.Error("last_upgrade_at should not be empty")
	}
}

func TestWriteUpgradeMeta_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "upgrade")
	if err := WriteUpgradeMeta(dir, "v0.7.50"); err != nil {
		t.Fatalf("should create nested dir: %v", err)
	}
}

func TestWriteUpgradeMeta_DeletesOldHealthyMarker(t *testing.T) {
	dir := t.TempDir()
	// 先写一个旧 healthy_marker（模拟上次升级的标记）
	writeTestFile(t, filepath.Join(dir, HealthyMarkerFile), "v0.7.49")
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v0.7.49")

	// 执行新升级的 meta 写入
	if err := WriteUpgradeMeta(dir, "v0.7.50"); err != nil {
		t.Fatalf("WriteUpgradeMeta: %v", err)
	}

	// healthy_marker 应被删除（重新武装健康检查）
	if _, err := os.Stat(filepath.Join(dir, HealthyMarkerFile)); !os.IsNotExist(err) {
		t.Error("healthy_marker should be removed after WriteUpgradeMeta")
	}

	// 但 last_upgrade_ver 应更新为新版本
	ver := readTrimmed(filepath.Join(dir, LastUpgradeVerFile))
	if ver != "v0.7.50" {
		t.Errorf("last_upgrade_ver = %q, want v0.7.50", ver)
	}

	// 新版本未写 healthy_marker → IsUpgradeHealthy 应返回 false
	if IsUpgradeHealthy(dir) {
		t.Error("should NOT be healthy — marker was deleted, new worker hasn't registered yet")
	}
}

func TestWriteUpgradeMeta_NoOldMarker_NoError(t *testing.T) {
	dir := t.TempDir()
	// 首次升级，无旧 marker → Remove 报 IsNotExist，应被忽略
	if err := WriteUpgradeMeta(dir, "v0.7.50"); err != nil {
		t.Fatalf("should not error when no old marker: %v", err)
	}
}

func TestIsPending_ManifestExists(t *testing.T) {
	dir := t.TempDir()
	incoming := filepath.Join(dir, IncomingDirName)
	writeTestFile(t, filepath.Join(incoming, ManifestFileName), "fake manifest")

	if !IsPending(dir) {
		t.Error("expected pending when MANIFEST.txt exists")
	}
}

func TestIsPending_ManifestMissing(t *testing.T) {
	dir := t.TempDir()
	// incoming/ 不存在
	if IsPending(dir) {
		t.Error("expected NOT pending when MANIFEST.txt missing")
	}
}

func TestClearPending_RemovesIncomingDir(t *testing.T) {
	dir := t.TempDir()
	incoming := filepath.Join(dir, IncomingDirName)
	writeTestFile(t, filepath.Join(incoming, ManifestFileName), "fake")
	writeTestFile(t, filepath.Join(incoming, "worker.exe"), "binary")

	if err := ClearPending(dir); err != nil {
		t.Fatalf("ClearPending: %v", err)
	}

	if _, err := os.Stat(incoming); !os.IsNotExist(err) {
		t.Error("incoming/ should be removed")
	}
}

func TestClearPending_NoIncomingDir(t *testing.T) {
	dir := t.TempDir()
	// 不存在 incoming/ — 幂等，不报错
	if err := ClearPending(dir); err != nil {
		t.Fatalf("ClearPending on missing dir should not error: %v", err)
	}
}

func TestReadStagedVersion(t *testing.T) {
	dir := t.TempDir()
	incoming := filepath.Join(dir, IncomingDirName)
	writeTestFile(t, filepath.Join(incoming, VersionFileName), "v0.7.50")

	ver, err := ReadStagedVersion(dir)
	if err != nil {
		t.Fatalf("ReadStagedVersion: %v", err)
	}
	if ver != "v0.7.50" {
		t.Errorf("version = %q, want %q", ver, "v0.7.50")
	}
}

func TestReadStagedVersion_NoVersionFile(t *testing.T) {
	dir := t.TempDir()
	// incoming/ 存在但无 VERSION 文件
	writeTestFile(t, filepath.Join(dir, IncomingDirName, ManifestFileName), "fake")

	ver, err := ReadStagedVersion(dir)
	if err != nil {
		t.Fatalf("should not error when VERSION missing: %v", err)
	}
	if ver != "" {
		t.Errorf("version = %q, want empty", ver)
	}
}

func TestRollbackDoneExists_Present(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, RollbackDoneFile), "done")

	if !RollbackDoneExists(dir) {
		t.Error("expected rollback.done to exist")
	}
}

func TestRollbackDoneExists_Missing(t *testing.T) {
	dir := t.TempDir()
	if RollbackDoneExists(dir) {
		t.Error("expected rollback.done to NOT exist")
	}
}
