// machine_test.go 测试 Machine 深模块的编排逻辑。
// 从 cmd/upgrade_windows_test.go 迁移，适配 Machine API：
//   - applyAndSwap → Machine.Apply
//   - maybeApplyOnBoot / maybeRollbackOnBoot → Machine.BootCheck
//   - checkPendingUpgrade → Machine.CheckPending
//   - watchUpgradeHealth → Machine.HealthCheck
//   - rollbackAndMark → Machine.RollbackAndMark
// 纯 Go（无 Windows 专属依赖），在 Linux CI 可跑。

package upgrademachine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- 测试辅助 ---

// testLogger 返回一个日志级别设为 100（高于所有级别）的 Logger，
// 实际效果是抑制所有日志输出。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(100)}))
}

// mockProcessController 是 ProcessController 的测试 mock。
type mockProcessController struct {
	killTreeCalls   atomic.Int32
	killTreeLastPID atomic.Int32
	killImageCalls  atomic.Int32
	killImageNames  []string
	treeErr         error // 非 nil 时 KillTree 返回此 error
}

func (m *mockProcessController) KillTree(pid int) error {
	m.killTreeCalls.Add(1)
	m.killTreeLastPID.Store(int32(pid))
	if m.treeErr != nil {
		return m.treeErr
	}
	return nil
}

func (m *mockProcessController) KillByImage(name string) error {
	m.killImageCalls.Add(1)
	m.killImageNames = append(m.killImageNames, name)
	return nil
}

// buildMachineBundle 创建完整 fake bundle：stageDir/incoming/ 下有 MANIFEST.txt
// + src 文件 + VERSION。destDir 模拟部署目标（已有旧版本）。
func buildMachineBundle(t *testing.T, stageDir, destDir, version string,
	files []struct{ Src, Dest, Content string }) {

	t.Helper()
	incoming := filepath.Join(stageDir, IncomingDirName)

	var lines []string
	for _, f := range files {
		sha := writeTestFile(t, filepath.Join(incoming, f.Src), f.Content)
		lines = append(lines, sha+" 0755 "+f.Src+" "+filepath.Join(destDir, f.Dest))
	}
	// MANIFEST.txt
	manifestContent := strings.Join(lines, "\n") + "\n"
	writeTestFile(t, filepath.Join(incoming, ManifestFileName), manifestContent)
	// VERSION
	writeTestFile(t, filepath.Join(incoming, VersionFileName), version)
}

// --- Machine.Apply ---

func TestMachine_Apply_KillCalledWhenPIDPositive(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()
	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	buildMachineBundle(t, stageDir, destDir, "v1.0.0", []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new"},
	})

	var pc mockProcessController
	m := NewMachine(stageDir, destDir, testLogger(), &pc)
	if err := m.Apply(context.Background(), 12345); err != nil {
		t.Fatalf("Machine.Apply: %v", err)
	}
	if pc.killTreeCalls.Load() != 1 {
		t.Errorf("KillTree called %d times, want 1", pc.killTreeCalls.Load())
	}
	if pc.killTreeLastPID.Load() != 12345 {
		t.Errorf("KillTree called with pid=%d, want 12345", pc.killTreeLastPID.Load())
	}
}

func TestMachine_Apply_KillSkippedWhenPIDZero(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()
	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	buildMachineBundle(t, stageDir, destDir, "v1.0.0", []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new"},
	})

	var pc mockProcessController
	m := NewMachine(stageDir, destDir, testLogger(), &pc)
	// PID=0 → 应跳过 kill
	if err := m.Apply(context.Background(), 0); err != nil {
		t.Fatalf("Machine.Apply: %v", err)
	}
	if pc.killTreeCalls.Load() != 0 {
		t.Errorf("KillTree should NOT be called when PID=0")
	}
}

// TestKillManifestExes_SkipsSupervisorBinary 验证 KillManifestExes 不 kill
// supervisor 自己。MANIFEST 包含 supervisor.exe 用于 rename-aside 自升级，
// 但 kill supervisor 进程会导致 SCM restart 死循环。
//  2026-07-16 发现此 bug。
func TestKillManifestExes_SkipsSupervisorBinary(t *testing.T) {
	entries := []ManifestEntry{
		{Dest: `C:\bin\` + WorkerBinaryName},
		{Dest: `C:\bin\windows_exporter.exe`},
		{Dest: `C:\bin\` + SupervisorBinaryName},
	}
	var pc mockProcessController
	m := NewMachine(t.TempDir(), t.TempDir(), testLogger(), &pc)

	m.KillManifestExes(entries)

	if len(pc.killImageNames) != 2 {
		t.Fatalf("KillByImage should be called 2 times (worker + exporter), got %d: %v",
			len(pc.killImageNames), pc.killImageNames)
	}
	for _, name := range pc.killImageNames {
		if name == SupervisorBinaryName {
			t.Errorf("KillByImage must NOT be called for %q (supervisor self-kill)", SupervisorBinaryName)
		}
	}
}

func TestMachine_Apply_KillErrorIgnored(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()
	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	buildMachineBundle(t, stageDir, destDir, "v1.0.0", []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new"},
	})

	pc := &mockProcessController{treeErr: errors.New("ERROR: not found")}
	m := NewMachine(stageDir, destDir, testLogger(), pc)
	// KillTree 报错但 Apply 应继续（幂等语义）
	if err := m.Apply(context.Background(), 12345); err != nil {
		t.Fatalf("Machine.Apply should ignore kill error: %v", err)
	}
}

func TestMachine_Apply_FullOrdering(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()
	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old-worker")
	writeTestFile(t, filepath.Join(destDir, "exporter.exe"), "old-exporter")
	buildMachineBundle(t, stageDir, destDir, "v2.0.0", []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new-worker"},
		{"exporter.exe", "exporter.exe", "new-exporter"},
	})

	m := NewMachine(stageDir, destDir, testLogger(), nil)
	if err := m.Apply(context.Background(), 0); err != nil {
		t.Fatalf("Machine.Apply: %v", err)
	}

	// 验证 swap：dest 内容是新版本
	got, _ := os.ReadFile(filepath.Join(destDir, "worker.exe"))
	if string(got) != "new-worker" {
		t.Errorf("worker.exe = %q, want %q", got, "new-worker")
	}
	got, _ = os.ReadFile(filepath.Join(destDir, "exporter.exe"))
	if string(got) != "new-exporter" {
		t.Errorf("exporter.exe = %q, want %q", got, "new-exporter")
	}

	// 验证 .previous 备份存在（旧版本）
	got, _ = os.ReadFile(filepath.Join(destDir, "worker.exe.previous"))
	if string(got) != "old-worker" {
		t.Errorf("worker.exe.previous = %q, want %q", got, "old-worker")
	}

	// 验证 incoming/ 已删除
	if _, err := os.Stat(filepath.Join(stageDir, IncomingDirName)); !os.IsNotExist(err) {
		t.Error("incoming/ should be removed after Apply")
	}

	// 验证 last_upgrade_ver 已写
	ver, _ := os.ReadFile(filepath.Join(stageDir, LastUpgradeVerFile))
	if string(ver)[:len("v2.0.0")] != "v2.0.0" {
		t.Errorf("last_upgrade_ver = %q, want v2.0.0", ver)
	}

	// 验证 healthy_marker 已删（重新武装 watchdog）
	if _, err := os.Stat(filepath.Join(stageDir, HealthyMarkerFile)); !os.IsNotExist(err) {
		t.Error("healthy_marker should be removed after Apply")
	}
}

func TestMachine_Apply_BadManifest(t *testing.T) {
	stageDir := t.TempDir()
	writeTestFile(t, filepath.Join(stageDir, IncomingDirName, ManifestFileName), "bad line")

	m := NewMachine(stageDir, t.TempDir(), testLogger(), nil)
	err := m.Apply(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error for malformed manifest")
	}
}

// --- Machine.BootCheck ---

func TestMachine_BootCheck_NoPending(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	m := NewMachine(stageDir, binDir, testLogger(), nil)
	if err := m.BootCheck(context.Background()); err != nil {
		t.Fatalf("should be no-op when no pending: %v", err)
	}
}

func TestMachine_BootCheck_PendingApplied(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()
	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	buildMachineBundle(t, stageDir, destDir, "v1.0.0", []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new"},
	})

	m := NewMachine(stageDir, destDir, testLogger(), nil)
	if err := m.BootCheck(context.Background()); err != nil {
		t.Fatalf("BootCheck: %v", err)
	}
	if IsPending(stageDir) {
		t.Error("pending should be cleared after boot apply")
	}
}

func TestMachine_BootCheck_NeverUpgraded(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	m := NewMachine(stageDir, binDir, testLogger(), nil)
	if err := m.BootCheck(context.Background()); err != nil {
		t.Fatalf("should be no-op when never upgraded: %v", err)
	}
}

func TestMachine_BootCheck_Healthy(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	writeTestFile(t, filepath.Join(stageDir, LastUpgradeVerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(stageDir, HealthyMarkerFile), "v1.0.0")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	if err := m.BootCheck(context.Background()); err != nil {
		t.Fatalf("should be no-op when healthy: %v", err)
	}
}

func TestMachine_BootCheck_UnhealthyRollsBack(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	writeTestFile(t, filepath.Join(stageDir, LastUpgradeVerFile), "v2.0.0")
	writeTestFile(t, filepath.Join(stageDir, HealthyMarkerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(binDir, "worker.exe"), "new-broken")
	writeTestFile(t, filepath.Join(binDir, "worker.exe.previous"), "old-good")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	if err := m.BootCheck(context.Background()); err != nil {
		t.Fatalf("BootCheck: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(binDir, "worker.exe"))
	if string(got) != "old-good" {
		t.Errorf("worker.exe = %q, want %q (rolled back)", got, "old-good")
	}
	if _, err := os.Stat(filepath.Join(stageDir, RollbackDoneFile)); err != nil {
		t.Errorf("rollback.done sentinel should exist: %v", err)
	}
}

func TestMachine_BootCheck_RollbackDoneSkips(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	writeTestFile(t, filepath.Join(stageDir, RollbackDoneFile), "done")
	writeTestFile(t, filepath.Join(stageDir, LastUpgradeVerFile), "v2.0.0")
	writeTestFile(t, filepath.Join(stageDir, HealthyMarkerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(binDir, "worker.exe"), "new-broken")
	writeTestFile(t, filepath.Join(binDir, "worker.exe.previous"), "old-good")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	if err := m.BootCheck(context.Background()); err != nil {
		t.Fatalf("should be no-op when rollback.done present: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(binDir, "worker.exe"))
	if string(got) != "new-broken" {
		t.Errorf("worker.exe = %q, should remain unchanged", got)
	}
}

// --- Machine.CheckPending ---

// TestMachine_BootCheck_ExtractsPendingBundle 验证 BootCheck 在 incoming/ 为空但
// pending tar.gz 存在时自动解压再 apply。
func TestMachine_BootCheck_ExtractsPendingBundle(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()

	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	// 写 pending tar.gz（不含 incoming/，模拟 Windows 无 ExecStartPre 的场景）
	h := sha256.Sum256([]byte("new"))
	sha := hex.EncodeToString(h[:])
	manifestContent := sha + " 0755 worker.exe " + filepath.Join(destDir, "worker.exe") + "\n"
	writePendingBundle(t, stageDir, map[string]string{
		"MANIFEST.txt": manifestContent,
		"VERSION":      "v9.9.9",
		"worker.exe":   "new",
	})

	m := NewMachine(stageDir, destDir, testLogger(), &mockProcessController{})
	_ = m.BootCheck(context.Background())

	// pending tar.gz 应被删除（解压后清理）
	if _, err := os.Stat(filepath.Join(stageDir, PendingFileName)); !os.IsNotExist(err) {
		t.Errorf("pending should be deleted after BootCheck extraction, got err: %v", err)
	}
	// worker.exe 应被更新（apply 成功）
	data, err := os.ReadFile(filepath.Join(destDir, "worker.exe"))
	if err != nil {
		t.Fatalf("read worker.exe after BootCheck: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("worker.exe not updated after BootCheck pending extraction, got %q", string(data))
	}
}

// TestMachine_CheckPending_ExtractsPendingBundle 验证 CheckPending 在 incoming/ 为空但
// pending tar.gz 存在时自动解压再 apply。
func TestMachine_CheckPending_ExtractsPendingBundle(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()

	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	h := sha256.Sum256([]byte("new"))
	sha := hex.EncodeToString(h[:])
	manifestContent := sha + " 0755 worker.exe " + filepath.Join(destDir, "worker.exe") + "\n"
	writePendingBundle(t, stageDir, map[string]string{
		"MANIFEST.txt": manifestContent,
		"VERSION":      "v9.9.9",
		"worker.exe":   "new",
	})

	m := NewMachine(stageDir, destDir, testLogger(), &mockProcessController{})
	err := m.CheckPending(context.Background(), 123)
	if !errors.Is(err, ErrApplied) {
		t.Errorf("expected ErrApplied after pending extraction, got %v", err)
	}
	// pending tar.gz 应被删除
	if _, err := os.Stat(filepath.Join(stageDir, PendingFileName)); !os.IsNotExist(err) {
		t.Errorf("pending should be deleted after CheckPending extraction, got err: %v", err)
	}
}

func TestMachine_CheckPending_NoPending(t *testing.T) {
	stageDir := t.TempDir()
	m := NewMachine(stageDir, t.TempDir(), testLogger(), &mockProcessController{})
	if err := m.CheckPending(context.Background(), 123); err != nil {
		t.Fatalf("should return nil when no pending: %v", err)
	}
}

func TestMachine_CheckPending_PendingReturnsSentinel(t *testing.T) {
	stageDir := t.TempDir()
	destDir := t.TempDir()

	writeTestFile(t, filepath.Join(destDir, "worker.exe"), "old")
	buildMachineBundle(t, stageDir, destDir, "v1.0.0", []struct{ Src, Dest, Content string }{
		{"worker.exe", "worker.exe", "new"},
	})

	m := NewMachine(stageDir, destDir, testLogger(), &mockProcessController{})
	err := m.CheckPending(context.Background(), 123)
	if !errors.Is(err, ErrApplied) {
		t.Errorf("expected ErrApplied, got %v", err)
	}
}

// --- Machine.HealthCheck ---

func TestMachine_HealthCheck_HealthyCleanup(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeTestFile(t, filepath.Join(stageDir, LastUpgradeVerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(stageDir, HealthyMarkerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(binDir, "worker.exe.previous"), "old")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 短 timeout（2s）+ 短 poll（50ms）确保轮询先于 timeout 发现健康状态
	err := m.HealthCheck(ctx, 2*time.Second, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("expected nil (healthy), got %v", err)
	}

	if _, err := os.Stat(filepath.Join(binDir, "worker.exe.previous")); !os.IsNotExist(err) {
		t.Error(".previous should be cleaned up after healthy confirmation")
	}
}

func TestMachine_HealthCheck_TimeoutRollback(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeTestFile(t, filepath.Join(stageDir, LastUpgradeVerFile), "v2.0.0")
	writeTestFile(t, filepath.Join(stageDir, HealthyMarkerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(binDir, "worker.exe"), "new-broken")
	writeTestFile(t, filepath.Join(binDir, "worker.exe.previous"), "old-good")

	m := NewMachine(stageDir, binDir, testLogger(), nil)
	// 极短 timeout（800ms）触发 rollback
	err := m.HealthCheck(ctx, 800*time.Millisecond, 50*time.Millisecond)
	if !errors.Is(err, ErrRolledBack) {
		t.Errorf("expected ErrRolledBack, got %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(binDir, "worker.exe"))
	if string(got) != "old-good" {
		t.Errorf("worker.exe = %q, want %q (rolled back)", got, "old-good")
	}
	if _, err := os.Stat(filepath.Join(stageDir, RollbackDoneFile)); err != nil {
		t.Errorf("rollback.done sentinel should exist: %v", err)
	}
}

func TestMachine_HealthCheck_ContextCancelled(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	writeTestFile(t, filepath.Join(stageDir, LastUpgradeVerFile), "v2.0.0")
	writeTestFile(t, filepath.Join(stageDir, HealthyMarkerFile), "v1.0.0")

	cancel()
	m := NewMachine(stageDir, binDir, testLogger(), nil)
	err := m.HealthCheck(ctx, 10*time.Second, 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
