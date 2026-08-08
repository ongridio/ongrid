// running_exe_rename_windows_test.go — issue #21 Step 0 spike。
//
// 实测 Windows 文件锁语义，决定 supervisor 自升级方案 A/B 走向：
//   - 场景 1 + 2 通过 → 方案 A（进程内 rename-aside）可行
//   - 都失败 → fallback 方案 B（detached cmd.exe helper）
//
// 决策依据（PLAN.md v3 "关键技术不确定性"）：
//   - 观点 A：Windows Server 2003+ image loader 用 FILE_SHARE_READ | FILE_SHARE_DELETE
//     → rename 运行中 .exe 成功
//   - 观点 B：image loader 只 share READ → rename 失败 ERROR_SHARING_VIOLATION
//
// 5 场景表驱动测试（PLAN 附录 A）：
//  1. 进程 B rename 进程 A 启动中的 .exe（方案 A step 1 核心验证）
//  2. 进程 A 自己 rename 自己的 .exe（supervisor 进程内 self-swap 验证）
//  3. replace 模式（new → 正在运行的 dest，诊断价值：验证直接 replace 是否可行）
//  4. 恢复（.old → supervisor.exe，W2 brick 兜底验证）
//  5. MoveFileEx(MOVEFILE_DELAY_UNTIL_REBOOT) fallback（方案 D 可行性）

//go:build windows

package upgrademachine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestStep0_RunningExeRename 是 Step 0 决策门测试。
// 通过/失败直接决定方案 A/B 走向，结果以 t.Logf 明示（不因场景 3/5 预期失败而 t.Fail）。
func TestStep0_RunningExeRename(t *testing.T) {
	dummy := buildDummy(t)

	scenarios := []struct {
		name     string
		critical bool // critical=true 时失败意味着方案 A 不可行
		fn       func(t *testing.T, dummy string)
	}{
		{"scenario1_B_rename_A_running_exe", true, scenarioB_rename_A_running_exe},
		{"scenario2_A_self_rename", true, scenarioA_self_rename},
		{"scenario3_replace_running_exe", false, scenarioReplaceRunningExe},
		{"scenario4_restore_from_old", true, scenarioRestoreFromOld},
		{"scenario5_movefile_delay_until_reboot", false, scenarioMoveFileDelayUntilReboot},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			sc.fn(t, dummy)
		})
	}
}

// scenarioB_rename_A_running_exe：外部进程 B rename 正在运行的 A.exe。
// 这是方案 A step 1 (supervisor.exe → supervisor.exe.old) 的核心验证。
func scenarioB_rename_A_running_exe(t *testing.T, dummy string) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "dummyA.exe")
	copyFileExe(t, dummy, exePath)

	cmd := exec.Command(exePath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dummyA: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(200 * time.Millisecond) // 等待 image 完全加载

	oldPath := exePath + ".old"
	err := os.Rename(exePath, oldPath)

	if err != nil {
		t.Errorf("场景 1 失败（方案 A step 1 不可行）：os.Rename(%s, %s) = %v",
			exePath, oldPath, err)
		return
	}

	// 验证 rename 成功：原路径不存在，.old 存在
	if _, err := os.Stat(exePath); !os.IsNotExist(err) {
		t.Errorf("场景 1 异常：rename 后原路径仍存在（err=%v）", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("场景 1 异常：.old 不存在（err=%v）", err)
	}
	t.Logf("场景 1 通过：进程 B 可以 rename 运行中进程 A 的 .exe → 方案 A step 1 可行")
}

// scenarioA_self_rename：进程 A 自己 rename 自己的 .exe。
// 这是 supervisor 进程内 SupervisorSelfSwap step 1 的核心验证。
func scenarioA_self_rename(t *testing.T, dummy string) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "dummySelf.exe")
	copyFileExe(t, dummy, exePath)

	// dummy 启动后自己做 os.Rename(exePath, exePath+".old")
	cmd := exec.Command(exePath, "--self-rename", exePath)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("场景 2 启动 dummy --self-rename 失败：%v (stderr: %s)", err, out)
	}

	var result struct {
		OK   bool   `json:"ok"`
		Err  string `json:"err"`
		Src  string `json:"src"`
		Dst  string `json:"dst"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("场景 2 解析 dummy stdout 失败：%v (raw: %s)", err, out)
	}

	if !result.OK {
		t.Errorf("场景 2 失败（方案 A 进程内 self-swap 不可行）：dummy self-rename err=%q",
			result.Err)
		return
	}
	t.Logf("场景 2 通过：进程 A 可以自己 rename 自己的 .exe → 方案 A 进程内 self-swap 可行")
}

// scenarioReplaceRunningExe：把 .new rename 到正在运行的 dest（直接 replace）。
// 诊断价值：若通过，方案可简化为单步 replace；若失败，确认必须用 rename-aside 两步。
// 预期 Windows 行为：失败（ERROR_SHARING_VIOLATION / ACCESS_DENIED）— image loader 不 share WRITE。
func scenarioReplaceRunningExe(t *testing.T, dummy string) {
	dir := t.TempDir()
	destExe := filepath.Join(dir, "dummyDest.exe")
	newExe := filepath.Join(dir, "dummyDest.exe.new")
	copyFileExe(t, dummy, destExe)
	copyFileExe(t, dummy, newExe)
	appendMarker(t, newExe, "NEW-CONTENT\n")

	cmd := exec.Command(destExe)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dummyDest: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(200 * time.Millisecond)

	err := os.Rename(newExe, destExe)
	if err == nil {
		t.Logf("场景 3 通过（意外）：直接 replace 运行中 .exe 可行 — 方案可简化为单步")
		// 验证内容确实被替换
		got, _ := os.ReadFile(destExe)
		if !strings.Contains(string(got), "NEW-CONTENT") {
			t.Errorf("场景 3 异常：rename 成功但内容未替换")
		}
		return
	}
	// 预期失败：这是方案 A 用 rename-aside（两步）而非直接 replace 的根因
	t.Logf("场景 3 预期失败（符合 Windows image loader 语义）：os.Rename(new, running_dest) = %v", err)
	t.Logf("→ 确认必须用 rename-aside 两步（step 1: dest→.old; step 2: .new→dest）")
}

// scenarioRestoreFromOld：模拟 W2 brick 兜底 — step 1 成功后，把 .old rename 回原路径。
// 验证：进程 A 持有的 image 被 rename 到 .old 后，能否再次被 rename 回原路径。
func scenarioRestoreFromOld(t *testing.T, dummy string) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "supervisor.exe")
	copyFileExe(t, dummy, exePath)

	cmd := exec.Command(exePath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(200 * time.Millisecond)

	oldPath := exePath + ".old"
	// step 1: supervisor.exe → .old（预期成功，场景 1 已验证）
	if err := os.Rename(exePath, oldPath); err != nil {
		t.Fatalf("场景 4 前置 step 1 失败：%v", err)
	}

	// brick 兜底：.old → supervisor.exe（恢复）
	if err := os.Rename(oldPath, exePath); err != nil {
		t.Errorf("场景 4 失败（W2 brick 兜底不可行）：os.Rename(%s, %s) = %v",
			oldPath, exePath, err)
		return
	}
	if _, err := os.Stat(exePath); err != nil {
		t.Errorf("场景 4 异常：恢复后 supervisor.exe 不存在（err=%v）", err)
	}
	t.Logf("场景 4 通过：进程持有的 .old 可以 rename 回原路径 → W2 brick 兜底可行")
}

// scenarioMoveFileDelayUntilReboot：调用 MoveFileExW + MOVEFILE_DELAY_UNTIL_REBOOT。
// 方案 D（延迟到下次 reboot）的可行性验证。仅验证 API 可调用，不验证重启效果。
// 预期：API 调用成功（返回 nil），但实际 rename 延迟到下次 reboot。
func scenarioMoveFileDelayUntilReboot(t *testing.T, dummy string) {
	dir := t.TempDir()
	src := filepath.Join(dir, "delay_src.exe")
	dst := filepath.Join(dir, "delay_dst.exe")
	copyFileExe(t, dummy, src)

	srcW, err := windows.UTF16PtrFromString(src)
	if err != nil {
		t.Fatalf("场景 5 UTF16 src： %v", err)
	}
	dstW, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		t.Fatalf("场景 5 UTF16 dst： %v", err)
	}

	// MOVEFILE_DELAY_UNTIL_REBOOT = 0x4（需管理员权限）
	const MOVEFILE_DELAY_UNTIL_REBOOT = 0x4
	err = windows.MoveFileEx(srcW, dstW, MOVEFILE_DELAY_UNTIL_REBOOT)
	if err != nil {
		t.Logf("场景 5 失败（预期非管理员或系统不支持）：MoveFileEx DELAY_UNTIL_REBOOT = %v", err)
		return
	}
	t.Logf("场景 5 通过：MoveFileEx DELAY_UNTIL_REBOOT API 调用成功（方案 D 可行，需管理员权限）")
	// 清理：src 仍存在（rename 延迟），测试 tempDir 自动清理
}
