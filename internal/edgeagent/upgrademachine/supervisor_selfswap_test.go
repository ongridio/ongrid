// supervisor_selfswap_test.go 测试  SupervisorSelfSwap + smokeTestVersion +
// ResetUpgradeIPC（跨平台可测部分，Linux CI 可跑）。
// Windows 专属场景在
// supervisor_self_swap_windows_test.go（Step 4 完成后补）。

package upgrademachine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- smokeTestVersion ---

func TestSmokeTestVersion_Success(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()
	exePath := filepath.Join(binDir, SupervisorBinaryName+".new")
	copyFileExe(t, dummy, exePath)
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	if err := m.smokeTestVersion(exePath); err != nil {
		t.Fatalf("smokeTestVersion: %v", err)
	}

	// 成功时 exePath + pending sentinel 应保留
	if _, err := os.Stat(exePath); err != nil {
		t.Errorf("exePath 应保留： %v", err)
	}
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应保留")
	}
}

func TestSmokeTestVersion_BadBinary_CleansStageAndSentinel(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	exePath := filepath.Join(binDir, SupervisorBinaryName+".new")
	// 写一个非可执行文件（内容是文本）
	writeTestFile(t, exePath, "not-an-exe")
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.smokeTestVersion(exePath)
	if err == nil {
		t.Fatal("smokeTestVersion 应失败（非可执行文件）")
	}

	// : 失败时清理 exePath + pending sentinel
	if _, err := os.Stat(exePath); !os.IsNotExist(err) {
		t.Errorf("exePath 应被清理（err=%v）", err)
	}
	if IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应被清理")
	}
}

func TestSmokeTestVersion_NotExist_Fails(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	exePath := filepath.Join(binDir, SupervisorBinaryName+".new") // 不存在
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.smokeTestVersion(exePath)
	if err == nil {
		t.Fatal("smokeTestVersion 应失败（文件不存在）")
	}
}

// --- ResetUpgradeIPC ---

func TestResetUpgradeIPC_ClearsFourFiles_KeepsPending(t *testing.T) {
	stageDir := t.TempDir()
	// 铺 4 个 IPC 文件
	writeTestFile(t, LastUpgradeVerPath(stageDir), "v0.9.2")
	writeTestFile(t, LastUpgradeAtPath(stageDir), "2026-07-16T00:00:00Z")
	writeTestFile(t, HealthyMarkerPath(stageDir), "v0.9.2")
	writeTestFile(t, RollbackDonePath(stageDir), "done")
	// 铺 pending sentinel（应保留）
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, t.TempDir(), testLogger(), nil)
	if err := m.ResetUpgradeIPC(); err != nil {
		t.Fatalf("ResetUpgradeIPC: %v", err)
	}

	// 4 个 IPC 文件应被清
	for _, path := range []string{
		LastUpgradeVerPath(stageDir),
		LastUpgradeAtPath(stageDir),
		HealthyMarkerPath(stageDir),
		RollbackDonePath(stageDir),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s 应被清理（err=%v）", path, err)
		}
	}
	// pending sentinel 应保留
	if !IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应保留（让 BootCheck 重试升级）")
	}
}

func TestResetUpgradeIPC_Idempotent_NoFiles(t *testing.T) {
	// 无任何 IPC 文件时不报错
	stageDir := t.TempDir()
	m := NewMachine(stageDir, t.TempDir(), testLogger(), nil)
	if err := m.ResetUpgradeIPC(); err != nil {
		t.Fatalf("ResetUpgradeIPC 空目录应 nil： %v", err)
	}
}

// --- SupervisorSelfSwap 正常路径 ---

func TestSupervisorSelfSwap_HappyPath(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺地板：supervisor.exe（旧版本，内容标记）+ supervisor.exe.new（dummy binary）
	supervisorPath := filepath.Join(binDir, SupervisorBinaryName)
	newPath := filepath.Join(binDir, SupervisorBinaryName+".new")
	oldPath := filepath.Join(binDir, SupervisorBinaryName+".old")
	copyFileExe(t, dummy, supervisorPath)
	appendMarker(t, supervisorPath, "OLD-VERSION\n")
	copyFileExe(t, dummy, newPath)
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.SupervisorSelfSwap()

	// 应返回 ErrSupervisorRestartSoon
	if !errors.Is(err, ErrSupervisorRestartSoon) {
		t.Fatalf("期望 ErrSupervisorRestartSoon，got %v", err)
	}

	// supervisor.exe 应被替换为新版本（dummy binary，无 OLD-VERSION marker）
	got, _ := os.ReadFile(supervisorPath)
	if strings.Contains(string(got), "OLD-VERSION") {
		t.Errorf("supervisor.exe 仍是旧版本（含 OLD-VERSION marker）")
	}

	// .old 应存在（旧版本备份）
	got, _ = os.ReadFile(oldPath)
	if !strings.Contains(string(got), "OLD-VERSION") {
		t.Errorf(".old 应含旧版本 marker，got %q", got)
	}

	// .new 应不存在（已被 rename 走）
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf(".new 应不存在（err=%v）", err)
	}

	// applied sentinel 应写入
	if !IsSupervisorUpgradeApplied(stageDir) {
		t.Errorf("applied sentinel 应写入")
	}
	// pending sentinel 应删除
	if IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应删除")
	}
}

// --- SupervisorSelfSwap 冒烟失败 ---

func TestSupervisorSelfSwap_SmokeFail_AbortsBeforeRename(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	supervisorPath := filepath.Join(binDir, SupervisorBinaryName)
	newPath := filepath.Join(binDir, SupervisorBinaryName+".new")
	// supervisor.exe（旧版本）
	writeTestFile(t, supervisorPath, "old-supervisor")
	// supervisor.exe.new 是非可执行文件（冒烟失败）
	writeTestFile(t, newPath, "bad-binary")
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.SupervisorSelfSwap()

	if err == nil {
		t.Fatal("期望冒烟失败错误，got nil")
	}
	if errors.Is(err, ErrSupervisorRestartSoon) {
		t.Errorf("冒烟失败不应返回 ErrSupervisorRestartSoon，got %v", err)
	}
	// supervisor.exe 应未被改动
	got, _ := os.ReadFile(supervisorPath)
	if string(got) != "old-supervisor" {
		t.Errorf("supervisor.exe 应未被改动，got %q", got)
	}
	// .new + pending 应被清理（smokeTestVersion 内部清理）
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf(".new 应被清理（err=%v）", err)
	}
	if IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应被清理")
	}
}

// --- SupervisorSelfSwap step 1 失败（supervisor.exe 不存在）---

func TestSupervisorSelfSwap_Step1Fail_SupervisorMissing(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 只有 .new，没有 supervisor.exe（brick 恢复场景）
	newPath := filepath.Join(binDir, SupervisorBinaryName+".new")
	copyFileExe(t, dummy, newPath)
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.SupervisorSelfSwap()

	if err == nil {
		t.Fatal("期望 step 1 失败错误（supervisor.exe 不存在），got nil")
	}
	// 错误信息应提及 step 1
	if !strings.Contains(err.Error(), "step 1") {
		t.Errorf("错误应提及 step 1，got %q", err.Error())
	}
}
