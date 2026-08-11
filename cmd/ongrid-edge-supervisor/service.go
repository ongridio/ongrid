// service.go 实现 Windows Service 的 svc.Handler 接口。
// serviceHandler.Execute 是 SCM 调用的入口：
//   - 启动 worker supervisor goroutine（superviseWorker）
//   - 接受 Stop / Shutdown 请求，优雅取消 ctx，等 worker 退出
//   - worker supervisor 异常退出 → 服务停止 + 报告错误码（依赖 SCM recovery action 重启）

//go:build windows

package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ongridio/ongrid/internal/edgeagent/edgedirs"
	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
	"golang.org/x/sys/windows/svc"
)

// serviceHandler 实现 svc.Handler。
type serviceHandler struct {
	log *slog.Logger
}

// Execute 是 SCM 调用的服务主循环。返回 (samesession, exitCode)：
//   - samesession=true 表示非致命错误，SCM 不重启（用于 graceful shutdown）
//   - samesession=false 表示需要 SCM 介入（按 recovery action 决定是否重启）
// ：worker 挂了 supervisor 自动重启（superviseWorker 内部循环），只有
// supervisor 自身循环异常退出才返回 samesession=false。
// Upgrade boot hooks：
//  1. maybeRollbackOnBoot — 先回滚未健康的升级（上次 swap 后 worker 没活过 180s）
//  2. maybeApplyOnBoot — 再 apply 残留的 pending bundle（断电恢复）
func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, Accepts: 0}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// upgrade boot hook：rollback 不健康升级 → apply 残留 pending bundle
	// → supervisor self-swap
	m := upgrademachine.NewMachine(
		edgedirs.StageDir, edgedirs.BinDir,
		h.log, &windowsProcessController{},
	)
	if err := m.BootCheck(ctx); err != nil {
		if errors.Is(err, upgrademachine.ErrSupervisorRestartSoon) {
			// supervisor self-swap 完成 — 必须在 worker goroutine 启动前退出，
			// 让 SCM 按 recovery action 重启加载新 supervisor.exe。
			// exitCode=1（非 0）让 SCM 视为 failure → 触发 restart action；
			// exitCode=0 会被视为"正常停止"→ SCM 不 restart（P5  发现）。
			h.log.Info("supervisor self-swap done; exiting for SCM restart")
			status <- svc.Status{State: svc.StopPending, Accepts: 0}
			status <- svc.Status{State: svc.Stopped, Accepts: 0}
			return false, 1 // samesession=false + 非0 exitCode → SCM restart
		}
		h.log.Error("upgrade: BootCheck failed", "err", err)
		// 不中止启动 — rollback/apply 失败不阻塞 supervisor 运行
	}

	workerExit := make(chan error, 1)
	go func() {
		workerExit <- superviseWorker(ctx, h.log, m)
	}()

	// 报告 Running，声明只接受 Stop / Shutdown（不处理 Pause/Continue）。
	status <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	for {
		select {
		case cr := <-req:
			switch cr.Cmd {
			case svc.Interrogate:
				// SCM 周期性 ping 检查服务存活，回显当前状态。
				status <- cr.CurrentStatus
			case svc.Stop, svc.Shutdown:
				h.log.Info("service stop requested", "cmd", cr.Cmd)
				status <- svc.Status{State: svc.StopPending, Accepts: 0}
				cancel()
				if err := <-workerExit; err != nil {
					h.log.Error("worker supervisor exit on stop", "err", err)
				}
				status <- svc.Status{State: svc.Stopped, Accepts: 0}
				return false, 0
			default:
				h.log.Warn("unsupported service command", "cmd", cr.Cmd)
			}
		case err := <-workerExit:
			// superviseWorker 返回 ErrSupervisorRestartSoon = supervisor self-swap 完成
			//。
			// 返回 false, 1 让 SCM 视为 failure → 触发 recovery restart 加载新 supervisor.exe。
			if errors.Is(err, upgrademachine.ErrSupervisorRestartSoon) {
				h.log.Info("supervisor self-swap done (worker path); exiting for SCM restart")
				status <- svc.Status{State: svc.StopPending, Accepts: 0}
				status <- svc.Status{State: svc.Stopped, Accepts: 0}
				return false, 1
			}
			// worker supervisor 异常退出（ctx 未取消）= 真异常。
			if ctx.Err() != nil {
				return false, 0
			}
			h.log.Error("worker supervisor unexpected exit", "err", err)
			status <- svc.Status{State: svc.Stopped, Accepts: 0}
			return false, 1
		}
	}
}
