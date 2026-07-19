// ipc_supervisor_test.go 测试  supervisor 自升级 sentinel helper。
// 覆盖 IsSupervisorUpgradePending / WriteSupervisorUpgradePending /
// IsSupervisorUpgradeApplied / WriteSupervisorUpgradeApplied 四个 helper
// + Path 函数。

package upgrademachine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSupervisorUpgradeSentinels_RoundTrip(t *testing.T) {
	stageDir := t.TempDir()

	// 初始：两个哨兵都不存在
	if IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 不应初始存在")
	}
	if IsSupervisorUpgradeApplied(stageDir) {
		t.Errorf("applied sentinel 不应初始存在")
	}

	// 写 pending（含版本号）
	if err := WriteSupervisorUpgradePending(stageDir, "v0.9.2"); err != nil {
		t.Fatalf("WriteSupervisorUpgradePending: %v", err)
	}
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 写入后应存在")
	}
	got, _ := os.ReadFile(SupervisorUpgradePendingPath(stageDir))
	if string(got) != "v0.9.2\n" {
		t.Errorf("pending 内容 = %q, want %q", got, "v0.9.2\n")
	}

	// 写 applied（空版本号 — 允许）
	if err := WriteSupervisorUpgradeApplied(stageDir, ""); err != nil {
		t.Fatalf("WriteSupervisorUpgradeApplied: %v", err)
	}
	if !IsSupervisorUpgradeApplied(stageDir) {
		t.Errorf("applied sentinel 写入后应存在")
	}
	got, _ = os.ReadFile(SupervisorUpgradeAppliedPath(stageDir))
	if string(got) != "" {
		t.Errorf("applied 空版本号应写空内容，got %q", got)
	}
}

func TestSupervisorUpgradeSentinels_ParentDirMissing(t *testing.T) {
	// stageDir 不存在时 Write 应自动 mkdir
	stageDir := filepath.Join(t.TempDir(), "nested", "stage")
	if err := WriteSupervisorUpgradePending(stageDir, "v1"); err != nil {
		t.Fatalf("WriteSupervisorUpgradePending 自动 mkdir 失败：%v", err)
	}
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 写入后应存在")
	}
}

func TestSupervisorUpgradeSentinels_NewlineIdempotent(t *testing.T) {
	// 带现有 \n 的版本号不应被加双重换行
	stageDir := t.TempDir()
	if err := WriteSupervisorUpgradePending(stageDir, "v0.9.2\n"); err != nil {
		t.Fatalf("WriteSupervisorUpgradePending: %v", err)
	}
	got, _ := os.ReadFile(SupervisorUpgradePendingPath(stageDir))
	if string(got) != "v0.9.2\n" {
		t.Errorf("idempotent newline = %q, want %q", got, "v0.9.2\n")
	}
}
