// supervisor_selfswap.go 实现 supervisor.exe 的进程内 rename-aside 自升级
//。
// 核心：supervisor 无法 kill 自己（KillTree/Restart 自己 = 自杀），所以不能用
// worker swap 模式（KillTree → rename）。改为进程内 rename-aside：
//  1. smokeTestVersion— 跑 supervisor.exe.new --version 验证 binary 可执行
//  2. renameWithAVRetry(supervisor.exe, supervisor.exe.old)
//  3. renameWithAVRetry(supervisor.exe.new, supervisor.exe)
//     失败时 / brick 兜底：
//     a. m.RollbackAndMark() — worker rollback（保证版本一致）
//     b. renameWithAVRetry(supervisor.exe.old, supervisor.exe) — supervisor 恢复
//     c. m.ResetUpgradeIPC() — 清 IPC 状态文件
//  4. WriteSupervisorUpgradeApplied + 删 pending sentinel
//  5. 返回 ErrSupervisorRestartSoon → service.go Execute 返回 (false, 1) → SCM restart
// Step 0 spike 确认：Windows Server 2003+ image loader 用 FILE_SHARE_DELETE，
// rename 运行中 .exe 成功；直接 replace 失败（见 running_exe_rename_windows_test.go）。

package upgrademachine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrSupervisorRestartSoon 哨兵：service.go 收到后返回 (false, 1) 让 SCM 按
// recovery action 重启（samesession=false + 非 0 exitCode → failure 路径）。
// 调用方（BootCheck / superviseWorker）应将其向上传播给 service.Execute，
// 由 Execute 返回 false, 1 触发 SCM restart（exitCode=0 不触发， P5  发现）。
var ErrSupervisorRestartSoon = fmt.Errorf("supervisor self-swap done; restart via SCM")

// SupervisorSelfSwap 在 supervisor 进程内执行 rename-aside 自升级。
// 调用时机：
//   - BootCheck 检测到 supervisor_upgrade.pending sentinel 时（supervisor 崩溃后 SCM 重启）
//   - superviseWorker 检测到 worker swap 完成 + pending sentinel 时（热升级路径）
// 返回 ErrSupervisorRestartSoon 表示 swap 成功，调用方应让 SCM 重启 supervisor。
// 返回其他 error 表示 swap 失败（可能是 brick 兜底成功 — worker + supervisor 已恢复旧版本）。
func (m *Machine) SupervisorSelfSwap() error {
	newPath := filepath.Join(m.binDir, SupervisorBinaryName+".new")
	oldPath := filepath.Join(m.binDir, SupervisorBinaryName+".old")
	supervisorPath := filepath.Join(m.binDir, SupervisorBinaryName)

	// 0. : 冒烟测试 — 跑 supervisor.exe.new --version 验证 binary 可执行
	if err := m.smokeTestVersion(newPath); err != nil {
		return fmt.Errorf("supervisor self-swap aborted (smoke test): %w", err)
	}

	// 1. : rename supervisor.exe → .old（AV retry）
	if err := renameWithAVRetry(supervisorPath, oldPath, m.log); err != nil {
		return fmt.Errorf("supervisor self-swap step 1 (rename to .old): %w", err)
	}

	// 2. : rename .new → supervisor.exe（AV retry）
	if err := renameWithAVRetry(newPath, supervisorPath, m.log); err != nil {
		// / brick 兜底：worker rollback + supervisor 恢复 + IPC reset
		m.log.Error("supervisor self-swap step 2 failed; initiating full rollback", "err", err)

		// 2a. worker rollback（保证版本一致 — ）
		if rerr := m.RollbackAndMark(); rerr != nil {
			return fmt.Errorf("brick: worker rollback failed: %w (supervisor.exe.old preserved at %s)",
				rerr, oldPath)
		}

		// 2b. supervisor.exe.old → supervisor.exe 恢复
		if rerr := renameWithAVRetry(oldPath, supervisorPath, m.log); rerr != nil {
			return fmt.Errorf("brick: supervisor restore failed (worker rolled back): %w", rerr)
		}

		// 2c. : 清 IPC 状态文件（保留 pending 让 BootCheck 重试升级）
		if rerr := m.ResetUpgradeIPC(); rerr != nil {
			m.log.Error("brick recovery: IPC reset partial fail (non-fatal)", "err", rerr)
		}

		return fmt.Errorf("supervisor self-swap aborted; worker rolled back + supervisor restored + IPC reset: %w", err)
	}

	// 3. 成功路径：写 applied sentinel + 删 pending
	if err := WriteSupervisorUpgradeApplied(m.stageDir, ""); err != nil {
		m.log.Error("supervisor self-swap: failed to write applied sentinel", "err", err)
	}
	// best-effort 清理 pending sentinel — swap 已成功，sentinel 残留无意义
	_ = os.Remove(SupervisorUpgradePendingPath(m.stageDir))

	// 4. 返回哨兵让 service.go 触发 SCM restart
	m.log.Info("supervisor self-swap done; exiting for SCM restart", "old_backup", oldPath)
	return ErrSupervisorRestartSoon
}

// smokeTestVersion 跑 supervisor.exe.new --version，验证退出码 0 + stdout 非空。
// 超时 5s。失败时清理 stage + sentinel，不进入 self-swap。
// 防御场景：
//   - bundle 构建 bug 导致 binary 损坏
//   - AV 半清理（quarantine 部分 PE section）
//   - 架构错（amd64 binary 跑在 arm64 Windows）
func (m *Machine) smokeTestVersion(exePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exePath, "--version").Output()
	if err != nil {
		// best-effort 清理坏 binary + pending sentinel — 冒烟失败不进入 self-swap
		_ = os.Remove(exePath)
		_ = os.Remove(SupervisorUpgradePendingPath(m.stageDir))
		return fmt.Errorf("smoke test exit: %w (output: %s)", err, out)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		// best-effort 清理 — 同上，空输出视为坏 binary
		_ = os.Remove(exePath)
		_ = os.Remove(SupervisorUpgradePendingPath(m.stageDir))
		return fmt.Errorf("smoke test: empty version output")
	}
	m.log.Info("supervisor self-swap smoke test passed",
		"version_output", strings.TrimSpace(string(out)))
	return nil
}

// ResetUpgradeIPC 清理升级状态机文件，让 DetectState 回到 StateIdle。
// 用于 brick 兜底成功后让下一次升级从干净状态开始。
// 清理：last_upgrade_ver, last_upgrade_at, healthy_marker, rollback.done
// 保留：supervisor_upgrade.pending（让 BootCheck 可重试升级）
//       incoming/MANIFEST.txt（pending bundle 应保留）
// best-effort：单个文件删除失败不阻断（os.IsNotExist 忽略）。
func (m *Machine) ResetUpgradeIPC() error {
	var firstErr error
	for _, p := range []string{
		LastUpgradeVerPath(m.stageDir),
		LastUpgradeAtPath(m.stageDir),
		HealthyMarkerPath(m.stageDir),
		RollbackDonePath(m.stageDir),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
