// upgrade_windows.go 定义 Windows 专属的进程控制器 + 升级超时常量
//。
// 升级编排逻辑（applyAndSwap、maybeApply/maybeRollback、watchUpgradeHealth、
// rollbackAndMark、checkPendingUpgrade）已移至 upgrademachine.Machine。
// 本文件仅保留 Windows 平台的 taskkill 实现和超时常量。

//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
)

// upgradeWatchTimeout 是 swap 后等待新 worker register_edge 成功的窗口。
// 超过此时间 healthy_marker 仍未匹配 → rollback（对称 Linux 180s watchdog）。
const upgradeWatchTimeout = 180 * time.Second

// upgradePollInterval 是 Machine.HealthPoll 轮询 IsUpgradeHealthy 的间隔
// （HealthPoll redesign：HealthCheck → HealthPoll，仅 polling 不再持 timer）。
const upgradePollInterval = 5 * time.Second

// windowsProcessController 实现 upgrademachine.ProcessController 接口。
// 用 Windows taskkill 终止进程树和按镜像名杀进程。
type windowsProcessController struct{}

// KillTree 用 taskkill /T /F /PID 终止 pid 及其所有子进程
// （windows_exporter / promtail 等），释放 .exe 文件锁。
// 进程已退出时 taskkill 返回非零退出码，调用方应忽略错误（幂等）。
func (windowsProcessController) KillTree(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// KillByImage 用 taskkill /F /IM <name> 按镜像名杀进程。
// 解决场景：worker 干净退出后子进程（windows_exporter.exe 等）被
// orphaned（reparented to PID 1），KillTree 无法触达。
// 幂等：进程不存在时 taskkill 返回非零，调用方忽略。
func (windowsProcessController) KillByImage(name string) error {
	return exec.Command("taskkill", "/F", "/IM", name).Run()
}

// runUpgrade 写 supervisor_upgrade.pending sentinel 触发 SCM 重启升级流程
//。
// 端到端流程（由调用方 os.Exit(0) + SCM recovery action 串联）：
//  1. supervisor.exe --upgrade 调本函数写 sentinel + os.Exit(0)
//  2. SCM 视 supervisor 进程退出，按 recovery action 重启
//  3. 新 supervisor 启动 → serviceHandler.Execute → BootCheck 检测 sentinel
//  4. BootCheck 触发 SupervisorSelfSwap → 完成 rename-aside 后返回
//     ErrSupervisorRestartSoon → supervisor 再 exit(1) 让 SCM 再次 restart
//  5. 最终加载新 supervisor.exe，升级闭环
// sentinel version 参数传空 —— applyOne 已 stage supervisor.exe.new 时
// supervisor 的真实版本由 BootCheck 从 supervisor.exe.new 自身读取，
// sentinel 内容非必要。
// 幂等：WriteSupervisorUpgradePending 用 os.WriteFile 覆盖写，
// 多次调用不报错（用于 --upgrade 重复触发场景）。
func runUpgrade(log *slog.Logger, stageDir string) error {
	log.Info("supervisor upgrade pending, exiting for SCM restart",
		"stage_dir", stageDir)
	if err := upgrademachine.WriteSupervisorUpgradePending(stageDir, ""); err != nil {
		return fmt.Errorf("--upgrade write supervisor pending sentinel: %w", err)
	}
	return nil
}
