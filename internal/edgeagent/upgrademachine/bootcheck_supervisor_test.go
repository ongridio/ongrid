// bootcheck_supervisor_test.go 测试 issue #21 BootCheck 的 supervisor 自升级集成。
//
// 覆盖步骤 3（applied sentinel 清理）/ 4（brick recovery）/ 5（pending → W1 KillByImage → self-swap）。

package upgrademachine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// --- 步骤 3：applied sentinel 清理 ---

func TestBootCheck_AppliedSentinel_CleansOldAndSentinel(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 .old 备份 + applied sentinel
	oldPath := filepath.Join(binDir, SupervisorBinaryName+".old")
	writeTestFile(t, oldPath, "old-supervisor-binary")
	WriteSupervisorUpgradeApplied(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.BootCheck(context.Background())
	if err != nil {
		t.Fatalf("BootCheck 不应返回错误（applied 清理是非关键路径）： %v", err)
	}

	// .old 应被清理
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf(".old 应被清理（err=%v）", err)
	}
	// applied sentinel 应被删除
	if IsSupervisorUpgradeApplied(stageDir) {
		t.Errorf("applied sentinel 应被删除")
	}
}

// --- 步骤 4：brick recovery ---

func TestBootCheck_BrickState_RestoresOldToSupervisor(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// brick 状态：supervisor.exe 缺失 + .old 存在
	oldPath := filepath.Join(binDir, SupervisorBinaryName+".old")
	writeTestFile(t, oldPath, "rescued-supervisor-binary")
	// supervisor.exe 不存在

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.BootCheck(context.Background())
	if err != nil {
		t.Fatalf("BootCheck brick recovery 不应失败： %v", err)
	}

	// supervisor.exe 应被恢复（.old rename 回来）
	supervisorPath := filepath.Join(binDir, SupervisorBinaryName)
	got, rerr := os.ReadFile(supervisorPath)
	if rerr != nil {
		t.Fatalf("supervisor.exe 应存在： %v", rerr)
	}
	if string(got) != "rescued-supervisor-binary" {
		t.Errorf("supervisor.exe 内容 = %q, want rescued-supervisor-binary", got)
	}
	// .old 应不存在（已被 rename 走）
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf(".old 应不存在（err=%v）", err)
	}
}

func TestBootCheck_NoBrickState_SupervisorExists(t *testing.T) {
	// supervisor.exe 存在 + .old 不存在 → 非 brick 状态，不应触发恢复
	stageDir := t.TempDir()
	binDir := t.TempDir()
	writeTestFile(t, filepath.Join(binDir, SupervisorBinaryName), "current")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 不应 panic 或误恢复
	_ = m.BootCheck(context.Background())

	got, _ := os.ReadFile(filepath.Join(binDir, SupervisorBinaryName))
	if string(got) != "current" {
		t.Errorf("supervisor.exe 内容不应被改动，got %q", got)
	}
}

// --- 步骤 5：pending sentinel → W1 KillByImage → self-swap ---

func TestBootCheck_PendingSentinel_KillsWorkerBeforeSelfSwap(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 supervisor.exe + supervisor.exe.new（dummy binary）
	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName))
	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName+".new"))
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	var pc mockProcessController
	m := NewMachine(stageDir, binDir, testLogger(), &pc)

	err := m.BootCheck(context.Background())

	// 应返回 ErrSupervisorRestartSoon（self-swap 成功）
	if !errors.Is(err, ErrSupervisorRestartSoon) {
		t.Fatalf("期望 ErrSupervisorRestartSoon，got %v", err)
	}

	// W1: KillByImage 应被调用，参数是 WorkerBinaryName
	if pc.killImageCalls.Load() != 1 {
		t.Errorf("KillByImage 应被调用 1 次，got %d", pc.killImageCalls.Load())
	}
	if len(pc.killImageNames) != 1 || pc.killImageNames[0] != WorkerBinaryName {
		t.Errorf("KillByImage 参数应是 %q，got %v", WorkerBinaryName, pc.killImageNames)
	}
}

func TestBootCheck_PendingSentinel_NilPC_SkipsKillButStillSelfSwap(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()

	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName))
	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName+".new"))
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	// pc == nil（测试场景 / 无 worker 启动场景）
	m := NewMachine(stageDir, binDir, testLogger(), nil)

	err := m.BootCheck(context.Background())
	if !errors.Is(err, ErrSupervisorRestartSoon) {
		t.Fatalf("nil pc 也应完成 self-swap，got %v", err)
	}
}

// --- 步骤 5：self-swap 失败记录 lastErr 但不阻断 ---

func TestBootCheck_SelfSwapFails_RecordsLastError(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 supervisor.exe（旧版本）+ supervisor.exe.new 是坏 binary（冒烟失败）
	writeTestFile(t, filepath.Join(binDir, SupervisorBinaryName), "old-supervisor")
	writeTestFile(t, filepath.Join(binDir, SupervisorBinaryName+".new"), "bad-binary")
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	var pc mockProcessController
	m := NewMachine(stageDir, binDir, testLogger(), &pc)

	err := m.BootCheck(context.Background())
	// 冒烟失败不是 ErrSupervisorRestartSoon，应是普通 error
	if errors.Is(err, ErrSupervisorRestartSoon) {
		t.Errorf("冒烟失败不应返回 ErrSupervisorRestartSoon")
	}
	if err == nil {
		t.Errorf("冒烟失败应返回错误（lastErr）")
	}

	// W1: KillByImage 仍被调用（在 self-swap 之前）
	if pc.killImageCalls.Load() != 1 {
		t.Errorf("KillByImage 应被调用（W1 在 self-swap 前），got %d", pc.killImageCalls.Load())
	}

	// supervisor.exe 应未被改动
	got, _ := os.ReadFile(filepath.Join(binDir, SupervisorBinaryName))
	if string(got) != "old-supervisor" {
		t.Errorf("supervisor.exe 应保持旧版本，got %q", got)
	}
}
