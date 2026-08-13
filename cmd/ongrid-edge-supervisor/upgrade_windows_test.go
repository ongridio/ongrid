// upgrade_windows_test.go 测试 --upgrade 子命令的 sentinel 写入逻辑。
// Seam：测 runUpgrade orchestrator（仿 runInstall 模式），不测 main() ——
// main 含 os.Exit(0) 不可测，且现有 install_windows_test.go 也是测 runInstall 而非 main。
// 端到端流程：
//   supervisor.exe --upgrade
//     → runUpgrade 写 supervisor_upgrade.pending sentinel
//     → os.Exit(0) 让进程退出
//     → SCM 按 recovery action 重启 supervisor
//     → serviceHandler.Execute → BootCheck 检测 sentinel
//     → SupervisorSelfSwap 完成 → ErrSupervisorRestartSoon → exit(1) 让 SCM 再次 restart
//     → 新 supervisor.exe 加载完成

//go:build windows

package main

import (
	"log/slog"
	"os"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
)

// TestRunUpgrade_WritesPendingSentinel 验证 runUpgrade 在 stageDir 下写
// supervisor_upgrade.pending sentinel 文件。
// 这是 --upgrade 子命令的唯一可观测副作用 —— 不验证 log 输出（log 是观察副作用，
// 非行为契约），只验证 sentinel 落盘（这是 BootCheck 后续依赖的唯一信号）。
func TestRunUpgrade_WritesPendingSentinel(t *testing.T) {
	// 用 t.TempDir() 隔离 stageDir —— 不污染 ProgramData。
	// 注意：upgrademachine.WriteSupervisorUpgradePending 内部会 MkdirAll(stageDir)，
	// 所以传入不存在的子目录也能工作（验证幂等的 stage 创建）。
	stageDir := t.TempDir() + `\upgrade`

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := runUpgrade(log, stageDir); err != nil {
		t.Fatalf("runUpgrade failed: %v", err)
	}

	// 断言 1：sentinel 文件存在
	if !upgrademachine.IsSupervisorUpgradePending(stageDir) {
		t.Fatal("supervisor_upgrade.pending sentinel not written")
	}

	// 断言 2：sentinel 路径与 upgrademachine 单一真相源一致（防 drift）
	// （如果将来常量改了但 runUpgrade 用硬编码字符串，此断言会捕获）
	sentinelPath := upgrademachine.SupervisorUpgradePendingPath(stageDir)
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("sentinel path mismatch (drift?): %v", err)
	}
}

// TestRunUpgrade_IdempotentOverExistingSentinel 验证 runUpgrade 在 sentinel
// 已存在时仍能成功（幂等）—— upgrademachine.WriteSupervisorUpgradePending 用
// os.WriteFile 覆盖写，不应返回错误。
// 场景：用户重复跑 supervisor.exe --upgrade（未触发 SCM restart 前），
// 不应失败。
func TestRunUpgrade_IdempotentOverExistingSentinel(t *testing.T) {
	stageDir := t.TempDir() + `\upgrade`
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// 第一次写
	if err := runUpgrade(log, stageDir); err != nil {
		t.Fatalf("first runUpgrade failed: %v", err)
	}
	// 第二次写（sentinel 已存在）
	if err := runUpgrade(log, stageDir); err != nil {
		t.Fatalf("second runUpgrade failed (should be idempotent): %v", err)
	}
	if !upgrademachine.IsSupervisorUpgradePending(stageDir) {
		t.Fatal("sentinel missing after second runUpgrade")
	}
}
