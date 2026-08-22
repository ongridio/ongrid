// apply_supervisor_test.go 测试  applyOne 的 supervisor special-case。
// supervisor dest 不能走标准 backup → stage → rename 路径（Windows 锁定
// 运行中的 .exe）。applyOne 检测 supervisor dest → 只 stage .new + 写
// pending sentinel，让 Machine.SupervisorSelfSwap 后续做 rename-aside。

package upgrademachine

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApplyBundle_SupervisorDest_StageOnly 验证 supervisor dest 走 special-case。
func TestApplyBundle_SupervisorDest_StageOnly(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	// dest 下已有运行中 supervisor.exe（旧版本）
	writeTestFile(t, filepath.Join(dest, SupervisorBinaryName), "old-supervisor")

	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{SupervisorBinaryName, SupervisorBinaryName, "new-supervisor"},
	})

	result, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}

	// supervisor dest 应被 Staged，不是 Swapped
	if len(result.Swapped) != 0 {
		t.Errorf("supervisor dest 不应被 swap，got Swapped=%v", result.Swapped)
	}
	if len(result.Staged) != 1 {
		t.Fatalf("expected 1 staged, got %d (%v)", len(result.Staged), result.Staged)
	}
	stagedPath := filepath.Join(dest, SupervisorBinaryName+".new")
	if result.Staged[0] != stagedPath {
		t.Errorf("staged[0] = %q, want %q", result.Staged[0], stagedPath)
	}

	// supervisor.exe（原文件）应未被改动
	got, _ := os.ReadFile(filepath.Join(dest, SupervisorBinaryName))
	if string(got) != "old-supervisor" {
		t.Errorf("supervisor.exe 不应被改动，got %q", got)
	}

	// .new 应存在且内容是新版本
	got, _ = os.ReadFile(stagedPath)
	if string(got) != "new-supervisor" {
		t.Errorf("supervisor.exe.new content = %q, want %q", got, "new-supervisor")
	}

	// pending sentinel 应被写入
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("supervisor_upgrade.pending sentinel 应被写入")
	}

	// 不应有 .previous 备份（supervisor 路径跳过 backup）
	if _, err := os.Stat(filepath.Join(dest, SupervisorBinaryName+PreviousSuffix)); !os.IsNotExist(err) {
		t.Errorf("supervisor 路径不应创建 .previous 备份")
	}
}

// TestApplyBundle_SupervisorMixedWithWorker 验证同一 bundle 中 supervisor +
// worker 混合时各自走正确路径。
func TestApplyBundle_SupervisorMixedWithWorker(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	writeTestFile(t, filepath.Join(dest, SupervisorBinaryName), "old-supervisor")
	writeTestFile(t, filepath.Join(dest, WorkerBinaryName), "old-worker")

	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{SupervisorBinaryName, SupervisorBinaryName, "new-supervisor"},
		{WorkerBinaryName, WorkerBinaryName, "new-worker"},
	})

	result, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}

	// worker 走 swap，supervisor 走 stage
	if len(result.Swapped) != 1 || len(result.Staged) != 1 {
		t.Fatalf("expected Swapped=1 + Staged=1, got Swapped=%d Staged=%d",
			len(result.Swapped), len(result.Staged))
	}

	// worker.exe 应是新版本
	got, _ := os.ReadFile(filepath.Join(dest, WorkerBinaryName))
	if string(got) != "new-worker" {
		t.Errorf("worker.exe = %q, want new-worker", got)
	}

	// supervisor.exe 应保持旧版本（未被 swap）
	got, _ = os.ReadFile(filepath.Join(dest, SupervisorBinaryName))
	if string(got) != "old-supervisor" {
		t.Errorf("supervisor.exe 应保持旧版本，got %q", got)
	}

	// pending sentinel 存在
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应被写入")
	}
}

// TestApplyBundle_SupervisorDest_NoOldExe 验证 supervisor dest 不存在时仍 stage。
// 场景：首次安装 / brick 后 supervisor.exe 缺失。
func TestApplyBundle_SupervisorDest_NoOldExe(t *testing.T) {
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	dest := t.TempDir()

	// dest 下无 supervisor.exe（首次或 brick 后恢复）
	entries := buildFakeBundle(t, incoming, dest, []struct{ Src, Dest, Content string }{
		{SupervisorBinaryName, SupervisorBinaryName, "fresh-supervisor"},
	})

	result, err := ApplyBundle(stageDir, incoming, entries)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}

	if len(result.Staged) != 1 {
		t.Fatalf("expected 1 staged, got %d", len(result.Staged))
	}
	got, _ := os.ReadFile(filepath.Join(dest, SupervisorBinaryName+".new"))
	if string(got) != "fresh-supervisor" {
		t.Errorf("supervisor.exe.new = %q, want fresh-supervisor", got)
	}
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应被写入")
	}
}
