// worker_test.go 测试 superviseWorker 循环顶部 rollback 决策逻辑（HealthPoll redesign）。
// superviseWorker 本身需要起 worker.exe 子进程，无法在 unit test 跑；但 HealthPoll redesign
// 的核心决策（watchDeadline 到期 + worker 退出 + 未健康 → RollbackAndMark）是 4 条件
// AND 纯函数，抽出为 shouldRollbackAfterDeadline 在 worker.go。这里覆盖它的全部
// 真值分支。
// 重点场景：
//   - worker crash loop：worker 在 watch 窗口内反复崩溃 → deadline 未到 → 不 rollback
//     （避免过早回滚；deadline 到期后才触发）
//   - 空窗 rollback（file lock fix）：deadline 到期 + worker 已退出 + 未健康 → rollback true

//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
)

// writeStageFile 写 path 下单个文件（自动 mkdir 父目录）。
func writeStageFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeStageFiles 写 stageDir 下的 last_upgrade_ver 和 healthy_marker。
// lastVer != marker 模拟升级未确认；两者相等模拟健康。marker="" 表示不写
// healthy_marker 文件（模拟新 worker 还未 register）。
func writeStageFiles(t *testing.T, stageDir, lastVer, marker string) {
	t.Helper()
	writeStageFile(t, filepath.Join(stageDir, upgrademachine.LastUpgradeVerFile), lastVer)
	if marker == "" {
		return
	}
	writeStageFile(t, filepath.Join(stageDir, upgrademachine.HealthyMarkerFile), marker)
}

// TestShouldRollbackAfterDeadline_ExceededAndUnhealthy 验证 HealthPoll redesign
// 根治的核心断言：watchDeadline 已到 + worker 已退出（runWorkerOnce 已返回）+
// 升级未确认健康 → shouldRollbackAfterDeadline 返回 true（触发 RollbackAndMark 在
// superviseWorker 循环顶部空窗执行，worker.exe 文件锁已释放）。
func TestShouldRollbackAfterDeadline_ExceededAndUnhealthy(t *testing.T) {
	stageDir := t.TempDir()
	// 未健康：lastVer != marker（worker 启动了但未 register 或 register 了但版本不匹配）
	writeStageFiles(t, stageDir, "v9.9.9", "v9.9.8")

	deadline := time.Now().Add(-1 * time.Second) // 1 秒前已到期
	now := time.Now()

	if !shouldRollbackAfterDeadline(stageDir, true, deadline, now) {
		t.Error("expected rollback when watch active + deadline exceeded + unhealthy")
	}
}

// TestShouldRollbackAfterDeadline_NotExceededInWatchWindow 验证 worker crash loop
// 场景：worker 在 watch 窗口内反复崩溃（runWorkerOnce 多次返回），但 deadline 还没到 →
// 不应过早 rollback。HealthPoll redesign 的关键不变式：watchDeadline 是 180s 总窗口，
// 不因 worker 单次崩溃触发回滚。
func TestShouldRollbackAfterDeadline_NotExceededInWatchWindow(t *testing.T) {
	stageDir := t.TempDir()
	// 未健康（worker 在 crash loop 中，从未 register）
	writeStageFiles(t, stageDir, "v9.9.9", "")

	deadline := time.Now().Add(120 * time.Second) // 还有 120s
	now := time.Now()

	if shouldRollbackAfterDeadline(stageDir, true, deadline, now) {
		t.Error("rollback should NOT fire within watch window (worker crash loop scenario)")
	}
}

// TestShouldRollbackAfterDeadline_HealthyAfterDeadline 验证 deadline 到期但升级
// 已确认健康（HealthPoll 返回 nil 路径）→ 不 rollback，superviseWorker 走 "disarm
// watch" 分支。
func TestShouldRollbackAfterDeadline_HealthyAfterDeadline(t *testing.T) {
	stageDir := t.TempDir()
	// 健康：lastVer == marker
	writeStageFiles(t, stageDir, "v9.9.9", "v9.9.9")

	deadline := time.Now().Add(-1 * time.Second) // 已到期
	now := time.Now()

	if shouldRollbackAfterDeadline(stageDir, true, deadline, now) {
		t.Error("rollback should NOT fire when upgrade healthy (even after deadline)")
	}
}

// TestShouldRollbackAfterDeadline_WatchInactive 验证 watchUpgrade=false（普通路径
// 或 rollback.done 已存在）→ 不 rollback，即使 deadline 似乎已到。
func TestShouldRollbackAfterDeadline_WatchInactive(t *testing.T) {
	stageDir := t.TempDir()
	writeStageFiles(t, stageDir, "v9.9.9", "v9.9.8") // 不健康，但 watch 未激活

	deadline := time.Now().Add(-1 * time.Second)
	now := time.Now()

	if shouldRollbackAfterDeadline(stageDir, false, deadline, now) {
		t.Error("rollback should NOT fire when watchUpgrade=false")
	}
}

// TestShouldRollbackAfterDeadline_DeadlineZero 验证 watchDeadline 零值（未武装）
// → 不 rollback。防止 superviseWorker 初始化时的零值误触发。
func TestShouldRollbackAfterDeadline_DeadlineZero(t *testing.T) {
	stageDir := t.TempDir()
	writeStageFiles(t, stageDir, "v9.9.9", "")

	if shouldRollbackAfterDeadline(stageDir, true, time.Time{}, time.Now()) {
		t.Error("rollback should NOT fire when watchDeadline is zero (not armed)")
	}
}
