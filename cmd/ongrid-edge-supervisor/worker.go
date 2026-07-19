// worker.go 实现 supervisor.exe 对 worker.exe 的启动 + 监控 + 心跳 watchdog
// （ADR-033 U3）。MVP-1 仅做"崩溃重启 + 心跳超时 kill 重启"，bundle upgrade
// 推 MVP-2。
//
// 健康感知走 health.json 文件 IPC（ADR-033 I2 / U3）：
//   - worker 每 30s 写一次心跳
//   - supervisor 每 30s 读 + 判断超时（90s 阈值，3× 心跳间隔）
//   - 超时 → kill worker → superviseWorker 外层循环重启

//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/edgedirs"
	"github.com/ongridio/ongrid/internal/edgeagent/supervisorhealth"
	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
)

// 部署路径常量统一在 internal/edgeagent/edgedirs 包，与 cmd/ongrid-edge
// 共享（ADR-033 I2）。
//
// 重启间隔是 worker 异常退出后的固定等待时间。
// MVP-1 不做指数退避（YAGNI）；MVP-2 加资源限制 / 指数退避。
const (
	restartDelay     = 5 * time.Second
	workerKillTimeout = 10 * time.Second
)

// workerExe 返回 worker.exe 的绝对路径（edgedirs.BinDir + 文件名）。
func workerExe() string {
	return edgedirs.BinDir + `\` + edgedirs.WorkerBinary
}

// runWorkerOnly 是交互模式（非 Service）入口，直接启动 worker。
// 用于开发调试（RDP 跑 supervisor.exe 看日志）。
func runWorkerOnly(log *slog.Logger) error {
	ctx, cancel := signalInterruptContext()
	defer cancel()
	m := upgrademachine.NewMachine(
		edgedirs.StageDir, edgedirs.BinDir,
		log, &windowsProcessController{},
	)
	return superviseWorker(ctx, log, m)
}

// signalInterruptContext 返回一个在 Ctrl+C 时 cancel 的 ctx。
// 仅用于交互模式（runWorkerOnly）；服务模式由 SCM Stop 回调 cancel。
func signalInterruptContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

// superviseWorker 是 supervisor 主循环：启动 worker → 监控 → 异常退出则等待
// restartDelay 后重启。ctx 取消时优雅停止 worker 并返回 ctx.Err()。
//
// upgrade 集成（ADR-033 U3）：
//   - errUpgradeApplied → 跳过 restartDelay 立即重启 + 下一轮进入 upgrade watch
//   - rollback.done sentinel 存在 → 跳过 upgrade watch（避免死循环）
func superviseWorker(ctx context.Context, log *slog.Logger, m *upgrademachine.Machine) error {
	watchUpgrade := false
	for attempt := 0; ; attempt++ {
		// rollback.done sentinel → 上次 rollback 过，本轮不 watch
		if upgrademachine.RollbackDoneExists(edgedirs.StageDir) {
			watchUpgrade = false
		}

		err := runWorkerOnce(ctx, log, watchUpgrade, m)

		if errors.Is(err, upgrademachine.ErrApplied) {
			// swap 完成 → 检查是否需要 supervisor 自升级
			//（#21：bundle 含 supervisor.exe 时 applyOne 写 pending sentinel）
			if upgrademachine.IsSupervisorUpgradePending(edgedirs.StageDir) {
				log.Info("supervisor self-swap pending; triggering rename-aside")
				swapErr := m.SupervisorSelfSwap()
				if errors.Is(swapErr, upgrademachine.ErrSupervisorRestartSoon) {
					return swapErr // 让 service.go 触发 SCM restart 加载新 supervisor
				}
				if swapErr != nil {
					log.Error("supervisor self-swap failed (worker continues with current supervisor; HealthCheck will catch version mismatch)",
						"err", swapErr)
				}
			}
			// 立即重启新 worker + 进入 upgrade watch
			log.Info("upgrade applied; restarting worker without delay")
			watchUpgrade = true
			continue
		}

		// 普通路径（含 ErrRolledBack）：重置 watch 标志
		if watchUpgrade {
			watchUpgrade = false
		}

		if errors.Is(err, upgrademachine.ErrRolledBack) {
			log.Warn("upgrade rolled back; restarting with previous version", "attempt", attempt)
		} else if err != nil && ctx.Err() == nil {
			log.Error("worker exited unexpectedly", "attempt", attempt, "err", err)
		}

		if ctx.Err() != nil {
			log.Info("supervisor context cancelled; exiting worker loop")
			return ctx.Err()
		}

		log.Info("restarting worker", "after", restartDelay, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(restartDelay):
		}
	}
}

// runWorkerOnce 启动一次 worker.exe，阻塞等待其退出。
// 退出原因有三：(1) worker 自己崩溃（Wait 返回 err）；(2) watchdog 发现心跳
// 超时，cancel workerCtx → kill worker；(3) 父 ctx 取消（服务停止）。
//
// watchUpgrade=true 时，worker 启动后额外启动 upgrade watch goroutine
// （180s 窗口确认 register_edge 成功）。watch 成功 → 继续监控；超时 → rollback。
//
// worker 退出后检测 pending upgrade（checkPendingUpgrade），有则 swap 并返回
// errUpgradeApplied sentinel 让 superviseWorker 跳过 restartDelay。
func runWorkerOnce(ctx context.Context, log *slog.Logger, watchUpgrade bool, m *upgrademachine.Machine) error {
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	// CommandContext 而非 exec.Command：Go 1.20+ 要求设了 cmd.Cancel 的命令
	// 必须用 CommandContext 创建，否则 Start 返回
	// "exec: command with a non-nil Cancel was not created with CommandContext"。
	cmd := exec.CommandContext(workerCtx, workerExe())
	// worker stdout/stderr 重定向到轮转日志文件（supervisor.log 已有自己的 sink）。
	// Windows Service 进程无 console，nil inherit = 丢弃；改用 append-only file
	// 让 worker 的 slog 输出可观测（issue #20 dogfood 调试）。
	if workerStdout, err := openWorkerLog("worker-stdout.log"); err == nil {
		cmd.Stdout = workerStdout
		defer workerStdout.Close()
	} else {
		log.Warn("supervisor: open worker stdout log failed; falling back to discard", "err", err)
		cmd.Stdout = nil
	}
	if workerStderr, err := openWorkerLog("worker-stderr.log"); err == nil {
		cmd.Stderr = workerStderr
		defer workerStderr.Close()
	} else {
		log.Warn("supervisor: open worker stderr log failed; falling back to discard", "err", err)
		cmd.Stderr = nil
	}

	// Go 1.20+ CommandContext 自动 kill 进程当 ctx 取消；指定 Cancel 用优雅方式
	// （taskkill /T 先 send Ctrl-Break，再 workerKillTimeout 后强制 kill）。
	cmd.Cancel = func() error {
		// taskkill /T /F 等于 SIGKILL 整个进程树。
		// MVP-1 不做优雅停止（worker 收到 TerminateProcess 直接退出，状态由
		// health.json + 启动时从 manager 重连 tunnel 恢复）。
		return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	}
	cmd.WaitDelay = workerKillTimeout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start worker %q: %w", workerExe(), err)
	}

	log.Info("worker started", "pid", cmd.Process.Pid, "path", workerExe())

	workerPID := cmd.Process.Pid
	wdErr := make(chan error, 1)
	go func() {
		wdErr <- watchHeartbeat(workerCtx, workerPID, log)
	}()

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()

	// upgrade watch goroutine（仅 swap 后首次启动时活跃）
	var uwErr chan error
	if watchUpgrade {
		uwErr = make(chan error, 1)
		go func() {
			uwErr <- m.HealthCheck(workerCtx, upgradeWatchTimeout, upgradePollInterval)
		}()
		log.Info("upgrade: watch activated", "timeout", upgradeWatchTimeout)
	}

	for {
		select {
		case err := <-waitErr:
			// worker 自己挂了。watchdog goroutine 在 workerCtx cancel 后会返回 nil。
			workerCancel()
			<-wdErr
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// worker 退出后检测 pending upgrade
			if uerr := m.CheckPending(ctx, workerPID); uerr != nil {
				return uerr
			}
			if err != nil {
				return fmt.Errorf("worker wait: %w", err)
			}
			return nil

		case err := <-wdErr:
			// watchdog 触发：kill worker → 等 Wait 返回。
			if err != nil {
				log.Error("watchdog killed worker", "reason", err, "pid", workerPID)
			}
			workerCancel()
			<-waitErr
			// watchdog kill 后也检测 pending upgrade
			if uerr := m.CheckPending(ctx, workerPID); uerr != nil {
				return uerr
			}
			return err

		case err := <-uwErr:
			// upgrade watch 结果（只处理一次，然后置 nil 防止重复 select）
			uwErr = nil
			if err == nil {
				// healthy_marker 匹配 → cleanup 已完成，继续监控 worker
				continue
			}
			if errors.Is(err, upgrademachine.ErrRolledBack) {
				// rollback 完成 → cancel worker → 等 Wait → 返回
				workerCancel()
				<-waitErr
				return err
			}
			// workerCtx.Err()（worker 先退出导致）→ 交给 waitErr/wdErr 处理
			log.Debug("upgrade watch ended with ctx error", "err", err)
			continue
		}
	}
}

// watchHeartbeat 周期性读 health.json 判断 worker 心跳是否过期。
// 发现过期 → 返回 error（触发外层 kill worker）。
// workerCtx 取消时立即返回 nil（worker 已被外层 kill，不需要再报警）。
//
// startupGrace 是 worker 启动到首次写 health.json 的宽限时间（2× HeartbeatTimeout = 180s），
// 超过此窗口 health.json 还未出现 → 视为 worker 启动失败。
func watchHeartbeat(workerCtx context.Context, workerPID int, log *slog.Logger) error {
	startupGrace := 2 * supervisorhealth.HeartbeatTimeout
	startupDeadline := time.Now().Add(startupGrace)
	ticker := time.NewTicker(supervisorhealth.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-workerCtx.Done():
			return nil
		case <-ticker.C:
			h, err := supervisorhealth.Read(edgedirs.HealthFile)
			if err != nil {
				if supervisorhealth.IsNotExist(err) {
					if time.Now().After(startupDeadline) {
						return fmt.Errorf("worker (pid=%d) 启动超过 %v 未写 health.json", workerPID, startupGrace)
					}
					log.Info("health.json not yet written; worker still starting")
					continue
				}
				log.Error("read health.json failed", "err", err)
				continue
			}

			// PID race 检查：health.json 可能是上一个 worker 进程残留。
			if h.WorkerPID != workerPID {
				log.Warn("health.json PID mismatch; ignoring",
					"expected", workerPID, "got", h.WorkerPID)
				continue
			}

			if supervisorhealth.IsStale(h, time.Now(), supervisorhealth.HeartbeatTimeout) {
				return fmt.Errorf("worker (pid=%d) heartbeat stale (last: %s)",
					workerPID, h.LastHeartbeat.Format(time.RFC3339))
			}
		}
	}
}

// openWorkerLog 打开 DataDir 下的 worker 日志文件（append 模式）。
// 用于将 worker stdout/stderr 从 Windows Service nil-sink 救出来。
// 失败时返回 error，调用方降级为 nil（= 丢弃）。
func openWorkerLog(name string) (*os.File, error) {
	path := edgedirs.DataDir + `\` + name
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
