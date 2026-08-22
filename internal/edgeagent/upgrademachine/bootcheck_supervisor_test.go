// bootcheck_supervisor_test.go 测试  BootCheck 的 supervisor 自升级集成。
// 覆盖步骤 3（applied sentinel 清理）/ 4（brick recovery）/ 5（pending →  KillByImage → self-swap）。

package upgrademachine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- 步骤 3：applied sentinel 清理 ---

func TestBootCheck_AppliedSentinel_CleansOldAndSentinel(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 .old 备份 + applied sentinel
	oldPath := filepath.Join(binDir, SupervisorBinaryName+".old")
	writeTestFile(t, oldPath, "old-supervisor-binary")
	WriteSupervisorUpgradeApplied(stageDir, "v0.9.2")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 注入 stub：supervisor 实际在 binDir（标准安装路径）
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}
	err := m.BootCheck(context.Background())
	if err != nil {
		t.Fatalf("BootCheck 不应返回错误（applied 清理是非关键路径）： %v", err)
	}

	// .old 应被清理
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf(".old 应被清理（err=%v）", err)
	}
	// applied sentinel 应被删除
	if IsSupervisorUpgradeApplied(stageDir) {
		t.Errorf("applied sentinel 应被删除")
	}
}

// TestBootCheck_AppliedAndPendingCoexist_CleansBoth 验证  死循环场景 (d)：
// WriteSupervisorUpgradeApplied 成功后 + 删 pending 前断电 → 下次 BootCheck 步骤 3
// 命中（清 .old + 删 applied），但步骤 5 仍命中（pending 残留）→ 触发 SelfSwap。
// 此时 supervisor.exe 已新版（上次 swap 成功），.new 不存在 → relocate 失败 →
// SCM restart 死循环。
// 加固（方案 A）：步骤 3 applied 分支顺带清孤儿 pending，断电窗口的不变量违反
// 在 BootCheck 收尾阶段自动纠正。
// 断言契约：
//   - err == nil（不应触发步骤 5 SelfSwap）
//   - .old 备份被清理
//   - applied sentinel 被删
//   - pending sentinel 被删（步骤 3 收尾时一并清孤儿状态）
//   - supervisor.exe 未被改动（步骤 4 brick / 步骤 5 SelfSwap 都未触发）
func TestBootCheck_AppliedAndPendingCoexist_CleansBoth(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 supervisor.exe（已是新版 — 上次 swap 成功）
	supervisorPath := filepath.Join(binDir, SupervisorBinaryName)
	writeTestFile(t, supervisorPath, "new-supervisor-binary-already-swapped")
	// 铺 .old 备份（swap step 1 的残留）
	oldPath := filepath.Join(binDir, SupervisorBinaryName+".old")
	writeTestFile(t, oldPath, "pre-swap-supervisor-binary")
	// 铺 applied + pending 共存（断电窗口：applied 已写、pending 未删）
	WriteSupervisorUpgradeApplied(stageDir, "v0.9.2")
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")
	// 注意：不铺 .new — 上次 swap 已 rename .new → supervisor.exe，.new 不存在

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}

	err := m.BootCheck(context.Background())
	// 关键断言 1：不应返回 ErrSupervisorRestartSoon（步骤 5 不应触发）
	if errors.Is(err, ErrSupervisorRestartSoon) {
		t.Fatalf("applied+pending 共存时不应触发 SelfSwap（死循环）： %v", err)
	}
	if err != nil {
		t.Fatalf("BootCheck 应在清理后干净返回 nil： %v", err)
	}

	// 关键断言 2：.old 应被清理
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf(".old 应被清理（err=%v）", err)
	}
	// 关键断言 3：applied sentinel 应被删
	if IsSupervisorUpgradeApplied(stageDir) {
		t.Errorf("applied sentinel 应被删除")
	}
	// 关键断言 4：pending sentinel 应被删（步骤 3 收尾时一并清孤儿状态）
	if IsSupervisorUpgradePending(stageDir) {
		t.Errorf("pending sentinel 应被删除（applied 已写，pending 是孤儿状态）")
	}
	// 断言 5：supervisor.exe 内容未被改动（步骤 4/5 都不应触发）
	got, rerr := os.ReadFile(supervisorPath)
	if rerr != nil {
		t.Fatalf("supervisor.exe 应存在： %v", rerr)
	}
	if string(got) != "new-supervisor-binary-already-swapped" {
		t.Errorf("supervisor.exe 内容不应被改动，got %q", got)
	}
}

// --- 步骤 4：brick recovery 边界 — resolver 失败时显式 log warn ---

// TestIsSupervisorBrickState_ResolverFail_LogsWarnAndReturnsFalse 验证：
// selfPathResolver 失败时，isSupervisorBrickState 必须显式 log warn（不静默返回 false），
// 符合 CLAUDE.md §10 显式失败原则。
// 不 fail-fast：brick check 是启动期防御，resolver 失败时安全 fallback 是"不触发恢复"，
// 避免误恢复破坏正常状态。
// 断言三项行为契约（不耦合具体 message 文案）：
//   - level=WARN（不是 INFO/ERROR）
//   - message 含 "resolve self path failed" 子串（运维侧日志搜索 anchor）
//   - err 字段透传 resolver 返回的原始错误（错误链完整，%w wrapping）
func TestIsSupervisorBrickState_ResolverFail_LogsWarnAndReturnsFalse(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 注入唯一可识别的错误，验证 err chain 透传
	resolverErr := errors.New("synthetic resolver failure sentinel-7f3a")

	var buf bytes.Buffer
	m := NewMachine(stageDir, binDir, testLoggerCapturing(&buf), nil)
	m.selfPathResolver = func() (string, error) {
		return "", resolverErr
	}

	got := m.isSupervisorBrickState()
	if got {
		t.Errorf("resolver 失败时应返回 false（不触发恢复），got true")
	}

	logOut := buf.String()
	// 契约 1：level=WARN（testLoggerCapturing 已过滤 < WARN，但仍验证确实有输出且不是更高 level）
	if !strings.Contains(logOut, "level=WARN") {
		t.Errorf("期望 level=WARN，got:\n%s", logOut)
	}
	// 契约 2：message 含失败上下文 anchor（运维搜索用）
	if !strings.Contains(logOut, "resolve self path failed") {
		t.Errorf("期望 warn message 含 'resolve self path failed' anchor，got:\n%s", logOut)
	}
	// 契约 3：err 字段透传原始 resolver 错误（验证错误链完整，不是被吞或包装丢失）
	if !strings.Contains(logOut, "synthetic resolver failure sentinel-7f3a") {
		t.Errorf("期望 err 字段透传 resolver 错误，got:\n%s", logOut)
	}
}

// --- 步骤 4：brick recovery 边界 — resolver 成功 + 文件状态 ---

// TestBootCheck_BrickState_RestoresOldToSupervisor 验证 brick recovery 成功路径。
func TestBootCheck_BrickState_RestoresOldToSupervisor(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// brick 状态：supervisor.exe 缺失 + .old 存在
	oldPath := filepath.Join(binDir, SupervisorBinaryName+".old")
	writeTestFile(t, oldPath, "rescued-supervisor-binary")
	// supervisor.exe 不存在

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 注入 stub：supervisor 实际在 binDir（标准安装路径）
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}
	err := m.BootCheck(context.Background())
	if err != nil {
		t.Fatalf("BootCheck brick recovery 不应失败： %v", err)
	}

	// supervisor.exe 应被恢复（.old rename 回来）
	supervisorPath := filepath.Join(binDir, SupervisorBinaryName)
	got, rerr := os.ReadFile(supervisorPath)
	if rerr != nil {
		t.Fatalf("supervisor.exe 应存在： %v", rerr)
	}
	if string(got) != "rescued-supervisor-binary" {
		t.Errorf("supervisor.exe 内容 = %q, want rescued-supervisor-binary", got)
	}
	// .old 应不存在（已被 rename 走）
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf(".old 应不存在（err=%v）", err)
	}
}

func TestBootCheck_NoBrickState_SupervisorExists(t *testing.T) {
	// supervisor.exe 存在 + .old 不存在 → 非 brick 状态，不应触发恢复
	stageDir := t.TempDir()
	binDir := t.TempDir()
	writeTestFile(t, filepath.Join(binDir, SupervisorBinaryName), "current")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 注入 stub：supervisor 实际在 binDir（标准安装路径）
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}
	// 不应 panic 或误恢复
	_ = m.BootCheck(context.Background())

	got, _ := os.ReadFile(filepath.Join(binDir, SupervisorBinaryName))
	if string(got) != "current" {
		t.Errorf("supervisor.exe 内容不应被改动，got %q", got)
	}
}

// --- 步骤 5：pending sentinel →  KillByImage → self-swap ---

func TestBootCheck_PendingSentinel_KillsWorkerBeforeSelfSwap(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 supervisor.exe + supervisor.exe.new（dummy binary）
	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName))
	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName+".new"))
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	var pc mockProcessController
	m := NewMachine(stageDir, binDir, testLogger(), &pc)
	// 注入 stub：supervisor 实际在 binDir（标准安装路径）
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}

	err := m.BootCheck(context.Background())

	// 应返回 ErrSupervisorRestartSoon（self-swap 成功）
	if !errors.Is(err, ErrSupervisorRestartSoon) {
		t.Fatalf("期望 ErrSupervisorRestartSoon，got %v", err)
	}

	// : KillByImage 应被调用，参数是 WorkerBinaryName
	if pc.killImageCalls.Load() != 1 {
		t.Errorf("KillByImage 应被调用 1 次，got %d", pc.killImageCalls.Load())
	}
	if len(pc.killImageNames) != 1 || pc.killImageNames[0] != WorkerBinaryName {
		t.Errorf("KillByImage 参数应是 %q，got %v", WorkerBinaryName, pc.killImageNames)
	}
}

func TestBootCheck_PendingSentinel_NilPC_SkipsKillButStillSelfSwap(t *testing.T) {
	dummy := buildDummy(t)
	stageDir := t.TempDir()
	binDir := t.TempDir()

	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName))
	copyFileExe(t, dummy, filepath.Join(binDir, SupervisorBinaryName+".new"))
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	// pc == nil（测试场景 / 无 worker 启动场景）
	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 注入 stub：supervisor 实际在 binDir（标准安装路径）
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}

	err := m.BootCheck(context.Background())
	if !errors.Is(err, ErrSupervisorRestartSoon) {
		t.Fatalf("nil pc 也应完成 self-swap，got %v", err)
	}
}

// --- 步骤 5：self-swap 失败记录 lastErr 但不阻断 ---

func TestBootCheck_SelfSwapFails_RecordsLastError(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()

	// 铺 supervisor.exe（旧版本）+ supervisor.exe.new 是坏 binary（冒烟失败）
	writeTestFile(t, filepath.Join(binDir, SupervisorBinaryName), "old-supervisor")
	writeTestFile(t, filepath.Join(binDir, SupervisorBinaryName+".new"), "bad-binary")
	WriteSupervisorUpgradePending(stageDir, "v0.9.2")

	var pc mockProcessController
	m := NewMachine(stageDir, binDir, testLogger(), &pc)
	// 注入 stub：supervisor 实际在 binDir（标准安装路径）
	m.selfPathResolver = func() (string, error) {
		return filepath.Join(binDir, SupervisorBinaryName), nil
	}

	err := m.BootCheck(context.Background())
	// 冒烟失败不是 ErrSupervisorRestartSoon，应是普通 error
	if errors.Is(err, ErrSupervisorRestartSoon) {
		t.Errorf("冒烟失败不应返回 ErrSupervisorRestartSoon")
	}
	if err == nil {
		t.Errorf("冒烟失败应返回错误（lastErr）")
	}

	// : KillByImage 仍被调用（在 self-swap 之前）
	if pc.killImageCalls.Load() != 1 {
		t.Errorf("KillByImage 应被调用，got %d", pc.killImageCalls.Load())
	}

	// supervisor.exe 应未被改动
	got, _ := os.ReadFile(filepath.Join(binDir, SupervisorBinaryName))
	if string(got) != "old-supervisor" {
		t.Errorf("supervisor.exe 应保持旧版本，got %q", got)
	}
}
