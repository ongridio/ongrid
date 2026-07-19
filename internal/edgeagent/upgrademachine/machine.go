// machine.go 实现升级状态机深模块。
// Machine 将原先分散在 cmd/upgrade_windows.go 的编排逻辑（applyAndSwap、
// maybeApplyOnBoot、maybeRollbackOnBoot、watchUpgradeHealth、rollbackAndMark、
// checkPendingUpgrade）集中到一个类型中。
// supervisor 侧（cmd/）通过 NewMachine 创建实例，注入平台专属的 ProcessController，
// 然后调用 4 个高层方法：BootCheck / Apply / HealthCheck / RollbackAndMark。
// 纯 Go（无 Windows 专属依赖），测试可在 Linux CI 跑。

package upgrademachine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrApplied 是 sentinel error，CheckPending 返回它告诉 superviseWorker：
// "bundle swap 已完成，跳过 restartDelay 立即重启 worker"。
var ErrApplied = errors.New("upgrade applied; restart immediately")

// ErrRolledBack 是 sentinel error，HealthCheck 返回它告诉 runWorkerOnce：
// "upgrade 超时已 rollback，worker 应被 cancel，superviseWorker 下一轮跳过 watch"。
var ErrRolledBack = errors.New("upgrade rolled back after timeout")

// ProcessController 抽象进程终止操作，供跨平台 DI。
// Windows 生产实现用 taskkill；测试传 mock。
type ProcessController interface {
	// KillTree 终止 pid 及其所有子进程（taskkill /T /F /PID）。
	// 进程已退出时应返回非 nil error（调用方忽略，幂等）。
	KillTree(pid int) error

	// KillByImage 按镜像名终止所有同名进程（taskkill /F /IM <name>）。
	// 进程不存在时返回非 nil error（调用方忽略，幂等）。
	KillByImage(name string) error
}

// Machine 是升级状态机深模块，封装 supervisor 侧的升级编排逻辑。
// 持有 stageDir（IPC 文件根）和 binDir（swap 目标目录），
// 通过注入的 ProcessController 执行平台专属的进程终止。
type Machine struct {
	stageDir string
	binDir   string
	log      *slog.Logger
	pc       ProcessController
}

// NewMachine 创建升级状态机实例。
// 参数：
//   - stageDir: IPC 文件根目录（incoming/、last_upgrade_ver 等在此下）
//   - binDir: swap 目标目录（worker.exe、.previous 文件在此下）
//   - log: 结构化日志
//   - pc: 平台专属进程控制器（nil 时跳过 kill 操作，仅用于 boot 无 worker 场景）
func NewMachine(stageDir, binDir string, log *slog.Logger, pc ProcessController) *Machine {
	return &Machine{
		stageDir: stageDir,
		binDir:   binDir,
		log:      log,
		pc:       pc,
	}
}

// BootCheck 是 supervisor 启动时的 boot hook，合并原 maybeRollbackOnBoot + maybeApplyOnBoot
// +  supervisor 自升级收尾 / brick 恢复 / self-swap 触发。
// 执行顺序（不可反转）：
//  1. 检测上次升级是否健康 → 不健康则 RollbackAndMark
//  2. 检测残留 pending upgrade → 有则 Apply（boot 时无 worker，不 kill）
//  3. supervisor_upgrade.applied sentinel → 清理 .old 备份 + 删 sentinel
//  4. brick recovery（supervisor.exe 缺失 + .old 存在）→ rename .old 恢复
//  5. supervisor_upgrade.pending sentinel → KillByImage 清 orphan worker
//     → SupervisorSelfSwap → 返回 ErrSupervisorRestartSoon 让 SCM 重启
// 返回最后遇到的错误（如有）。返回 ErrSupervisorRestartSoon 时调用方（service.go）
// 应返回 (false, 1) 让 SCM 按 recovery action 重启（exitCode=0 不触发 restart）。
func (m *Machine) BootCheck(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var lastErr error

	// 1. rollback.done sentinel → 上次已 rollback，跳过（避免死循环）
	if RollbackDoneExists(m.stageDir) {
		m.log.Info("upgrade: rollback.done sentinel present; skipping rollback check")
	} else if HasLastUpgrade(m.stageDir) && !IsUpgradeHealthy(m.stageDir) {
		// 上次升级不健康 → rollback
		m.log.Warn("upgrade: last upgrade not confirmed healthy; rolling back")
		if err := m.RollbackAndMark(); err != nil {
			m.log.Error("upgrade: rollback on boot failed", "err", err)
			lastErr = err
		}
	}

	// 2. 残留 pending → apply（boot 时无 worker，PID 传 0）
	// Windows 兼容：pending tar.gz 可能尚未解压（无 systemd ExecStartPre 对等机制），
	// 与 CheckPending 对称处理。
	if !IsPending(m.stageDir) && HasPendingBundle(m.stageDir) {
		m.log.Info("upgrade: pending tar.gz detected on boot; extracting to incoming/")
		if err := ExtractPendingBundle(m.stageDir); err != nil {
			m.log.Error("upgrade: extract pending bundle on boot failed", "err", err)
			lastErr = err
		}
	}
	if IsPending(m.stageDir) {
		m.log.Info("upgrade: pending bundle detected on boot; applying")
		if err := m.Apply(ctx, 0); err != nil {
			m.log.Error("upgrade: apply on boot failed", "err", err)
			lastErr = err
		}
	}

	// 3. supervisor_upgrade.applied → 清理 .old 备份 + 删 sentinel
	if IsSupervisorUpgradeApplied(m.stageDir) {
		m.log.Info("supervisor self-swap: applied sentinel detected; cleaning .old backup")
		oldPath := filepath.Join(m.binDir, SupervisorBinaryName+".old")
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			m.log.Warn("supervisor self-swap: cleanup .old failed (non-fatal)", "err", err)
		}
		// best-effort 删 applied sentinel — 残留下次 BootCheck 会重复清理（幂等）
		_ = os.Remove(SupervisorUpgradeAppliedPath(m.stageDir))
	}

	// 4. brick recovery: supervisor.exe 缺失 + .old 存在 → rename 恢复
	if m.isSupervisorBrickState() {
		m.log.Warn("supervisor brick state: supervisor.exe missing + .old exists; restoring")
		supervisorPath := filepath.Join(m.binDir, SupervisorBinaryName)
		oldPath := supervisorPath + ".old"
		if err := renameWithAVRetry(oldPath, supervisorPath, m.log); err != nil {
			m.log.Error("supervisor brick recovery: restore failed", "err", err)
			lastErr = err
		}
	}

	// 5. supervisor self-swap: pending sentinel → KillByImage → SupervisorSelfSwap
	if IsSupervisorUpgradePending(m.stageDir) {
		// : BootCheck 恢复路径可能存在 orphan worker，先清理（幂等）
		if m.pc != nil {
			m.log.Warn("supervisor self-swap on boot: killing orphan worker first",
				"image", WorkerBinaryName)
			if err := m.pc.KillByImage(WorkerBinaryName); err != nil {
				m.log.Debug("KillByImage returned non-zero (process may not be running)",
					"image", WorkerBinaryName, "err", err)
			}
		}
		err := m.SupervisorSelfSwap()
		if errors.Is(err, ErrSupervisorRestartSoon) {
			return err // 让 service.go 触发 SCM restart
		}
		if err != nil {
			m.log.Error("supervisor self-swap on boot failed", "err", err)
			lastErr = err
		}
	}

	return lastErr
}

// isSupervisorBrickState 报告 supervisor brick 状态：supervisor.exe 缺失 + .old 存在。
// 此状态发生在 SupervisorSelfSwap step 1 成功（supervisor.exe → .old）+ step 2 失败 +
//  brick 兜底也失败 + SCM 重启后。BootCheck 步骤 4 尝试 rename .old 恢复。
func (m *Machine) isSupervisorBrickState() bool {
	supervisorPath := filepath.Join(m.binDir, SupervisorBinaryName)
	oldPath := supervisorPath + ".old"
	_, supErr := os.Stat(supervisorPath)
	_, oldErr := os.Stat(oldPath)
	return os.IsNotExist(supErr) && oldErr == nil
}

// Apply 编排 bundle swap 的完整顺序：
//  1. KillTree — 释放文件锁（worker 子进程可能持有 .exe 句柄）
//  2. ParseManifest
//  3. KillManifestExes — 杀孤儿子进程（windows_exporter 等）
//  4. ApplyBundle — 原子 swap + .previous 备份
//  5. WriteUpgradeMeta — 写版本元数据 + 删旧 healthy_marker
//  6. ClearPending — 删 incoming/
// workerPID <= 0 时跳过 KillTree（boot 场景 worker 尚未启动）。
// ctx 遵循 AGENTS.md IO 函数约定；swap 操作不可中途取消（原子性要求），
// ctx 仅用于启动前检查（boot hooks 调用方可在 swap 前取消）。
func (m *Machine) Apply(ctx context.Context, workerPID int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 1. Kill worker tree（释放 .exe 文件锁）
	if workerPID > 0 && m.pc != nil {
		m.log.Info("upgrade: killing worker tree before swap", "pid", workerPID)
		if err := m.pc.KillTree(workerPID); err != nil {
			// 进程已退出时 taskkill 返回非零，忽略（幂等）
			m.log.Warn("upgrade: KillTree returned error (process may have exited)",
				"err", err)
		}
	}

	// 2. Parse manifest
	entries, err := ParseManifest(ManifestPath(m.stageDir))
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	// 3. Kill plugin processes by image name
	m.KillManifestExes(entries)

	// 4. Apply bundle (atomic swap + backup)
	result, err := ApplyBundle(m.stageDir, IncomingDir(m.stageDir), entries)
	if err != nil {
		return fmt.Errorf("apply bundle: %w", err)
	}
	m.log.Info("upgrade: bundle applied",
		"swapped", len(result.Swapped), "backed_up", len(result.BackedUp))

	// 5. Write upgrade meta
	ver, err := ReadStagedVersion(m.stageDir)
	if err != nil {
		return fmt.Errorf("read staged version: %w", err)
	}
	if err := WriteUpgradeMeta(m.stageDir, ver); err != nil {
		return fmt.Errorf("write upgrade meta: %w", err)
	}

	// 6. Clear pending
	if err := ClearPending(m.stageDir); err != nil {
		return fmt.Errorf("clear pending: %w", err)
	}

	m.log.Info("upgrade: meta written + pending cleared", "version", ver)
	return nil
}

// CheckPending 在 worker 退出后检查是否有 pending upgrade，有则 apply。
// 返回 ErrApplied 表示 swap 成功（调用方 superviseWorker 应跳过 restartDelay）。
// 返回其他 error 表示 swap 失败（调用方按普通崩溃处理）。
// 返回 nil 表示无 pending upgrade（调用方按普通崩溃重启）。
// Windows 兼容：worker agent_upgrade RPC 下载 bundle
// 到 {stageDir}/pending（tar.gz），Linux 由 systemd ExecStartPre 脚本解压到
// incoming/；Windows 无对等机制，这里自动检测 pending tar.gz 并解压。
func (m *Machine) CheckPending(ctx context.Context, workerPID int) error {
	if !IsPending(m.stageDir) {
		// Windows: pending tar.gz 未解压 → 先解压到 incoming/
		if !HasPendingBundle(m.stageDir) {
			return nil
		}
		m.log.Info("upgrade: pending tar.gz detected; extracting to incoming/")
		if err := ExtractPendingBundle(m.stageDir); err != nil {
			m.log.Error("upgrade: extract pending bundle failed", "err", err)
			return err
		}
		if !IsPending(m.stageDir) {
			m.log.Error("upgrade: pending extracted but MANIFEST.txt missing")
			return fmt.Errorf("extract pending: no MANIFEST.txt after extraction")
		}
	}
	m.log.Info("upgrade: pending bundle detected after worker exit; applying swap")
	if err := m.Apply(ctx, workerPID); err != nil {
		m.log.Error("upgrade: Apply failed", "err", err)
		return err
	}
	return ErrApplied
}

// HealthCheck 在新 worker 启动后监控 healthy_marker，确认升级成功。
// 此方法阻塞，直到以下之一发生：
//   - IsUpgradeHealthy = true → CleanupPrevious → 返回 nil（成功）
//   - timeout 到期 → RollbackAndMark → 返回 ErrRolledBack
//   - workerCtx 取消（worker 提前退出或 supervisor 停止）→ 返回 workerCtx.Err()
// pollInterval 是轮询 IsUpgradeHealthy 的间隔（测试可传短值）。
func (m *Machine) HealthCheck(ctx context.Context, timeout, pollInterval time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			m.log.Warn("upgrade: watch timeout; rolling back", "timeout", timeout)
			_ = m.RollbackAndMark()
			return ErrRolledBack
		case <-ticker.C:
			if IsUpgradeHealthy(m.stageDir) {
				m.log.Info("upgrade: confirmed healthy; cleaning up .previous")
				n, err := CleanupPrevious([]string{m.binDir})
				if err != nil {
					m.log.Error("upgrade: cleanup failed", "err", err)
				} else {
					m.log.Info("upgrade: cleaned up .previous files", "count", n)
				}
				return nil
			}
		}
	}
}

// RollbackAndMark 执行 rollback 并写 rollback.done 哨兵。
// 被 BootCheck（启动时不健康）和 HealthCheck（超时）共用。
func (m *Machine) RollbackAndMark() error {
	n, err := Rollback([]string{m.binDir})
	if err != nil {
		m.log.Error("upgrade: rollback failed", "err", err)
		return err
	}
	m.log.Info("upgrade: rolled back files", "count", n)

	if err := WriteRollbackDone(m.stageDir); err != nil {
		m.log.Error("upgrade: failed to write rollback.done sentinel", "err", err)
		// sentinel 写失败不阻断 — 下次启动可能再次 rollback（best-effort）
	}
	return nil
}

// KillManifestExes 遍历 MANIFEST 条目，对每个 .exe dest 用 KillByImage 杀进程。
// 解决场景：worker 干净退出后子进程（windows_exporter.exe 等）被
// orphaned（reparented to PID 1），KillTree 无法触达。
// 这些孤儿进程持有 .exe 文件锁，导致 ApplyBundle 的 rename 失败。
// 幂等：进程不存在时 KillByImage 返回非 nil，忽略。
func (m *Machine) KillManifestExes(entries []ManifestEntry) {
	if m.pc == nil {
		return
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		name := filepath.Base(e.Dest)
		if !strings.HasSuffix(name, ".exe") || seen[name] {
			continue
		}
		// 跳过 supervisor 自己：supervisor binary 在 MANIFEST 里用于 rename-aside
		// 自升级，不能 kill 自己；SupervisorSelfSwap 在 superviseWorker
		// 里单独处理。不跳过会导致 supervisor 自杀 → SCM restart 死循环。
		if name == SupervisorBinaryName {
			continue
		}
		seen[name] = true
		m.log.Info("upgrade: killing plugin process by image name", "image", name)
		if err := m.pc.KillByImage(name); err != nil {
			m.log.Debug("upgrade: KillByImage returned non-zero (process may not be running)",
				"image", name, "err", err)
		}
	}
}
