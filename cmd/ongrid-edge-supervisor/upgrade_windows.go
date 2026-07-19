// upgrade_windows.go 定义 Windows 专属的进程控制器 + 升级超时常量
//。
// 升级编排逻辑（applyAndSwap、maybeApply/maybeRollback、watchUpgradeHealth、
// rollbackAndMark、checkPendingUpgrade）已移至 upgrademachine.Machine。
// 本文件仅保留 Windows 平台的 taskkill 实现和超时常量。

//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"time"
)

// upgradeWatchTimeout 是 swap 后等待新 worker register_edge 成功的窗口。
// 超过此时间 healthy_marker 仍未匹配 → rollback（对称 Linux 180s watchdog）。
const upgradeWatchTimeout = 180 * time.Second

// upgradePollInterval 是 Machine.HealthCheck 轮询 IsUpgradeHealthy 的间隔。
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
