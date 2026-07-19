// apply_cycle_windows_test.go — issue #20 反馈循环测试（Windows-only）。
//
// 复现 issue #20 核心场景：worker 退出后 plugin 进程 orphaned → .exe 文件锁 →
// ApplyBundle rename 失败（Access denied）。
//
// 本测试不模拟 orphaned 路径（PID 1 reparenting 是内核行为，无法用 Go 模拟），
// 而是直接测试核心时序问题：
//   1. 启动 plugin.exe（持有自身 .exe 文件锁）
//   2. taskkill /F /IM plugin.exe（windowsProcessController.KillByImage）
//   3. 立即 ApplyBundle rename plugin.exe
//   4. 验证 rename 是否成功（若失败则 bug 复现）
//
// 如果 taskkill 返回后文件锁未及时释放（race condition），本测试会 Access denied。
//
// 这是 Phase 1 的 tight feedback loop：秒级、deterministic、agent-runnable。

//go:build windows

package upgrademachine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sleeperOnce 保证 sleeper.exe 只编译一次（所有 subtests 共用）。
var (
	sleeperOnce sync.Once
	sleeperPath string
	sleeperErr  error
)

// buildSleeper 编译 sleeper_main.go → sleeper.exe，所有测试共用。
// 首次调用 ~3-5s，后续调用 0s（cache 在 t.TempDir 外的固定路径）。
func buildSleeper(t *testing.T) string {
	t.Helper()
	sleeperOnce.Do(func() {
		out := filepath.Join(os.TempDir(), "ongrid-sleeper.exe")
		src := filepath.Join("testdata", "sleeper_main.go")
		cmd := exec.Command("go", "build", "-o", out, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			sleeperErr = fmt.Errorf("go build sleeper: %w\noutput: %s", err, out)
			return
		}
		sleeperPath = out
	})
	if sleeperErr != nil {
		t.Fatal(sleeperErr)
	}
	return sleeperPath
}

// realWindowsPC 是真实的 Windows ProcessController（taskkill 实现）。
// 对称 cmd/ongrid-edge-supervisor/upgrade_windows.go 的 windowsProcessController，
// 但放在测试包中避免循环依赖（cmd/ 不能被 import）。
type realWindowsPC struct{}

func (realWindowsPC) KillTree(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

func (realWindowsPC) KillByImage(name string) error {
	return exec.Command("taskkill", "/F", "/IM", name).Run()
}

// copyFileExe / appendMarker / copyStream 已提取到 dummy_helper_test.go（跨平台共用）。

// TestKillByImageReleaseTiming 是 issue #20 的核心反馈循环：
// KillByImage 返回后立即尝试 ApplyBundle rename .exe → 是否 Access denied。
//
// 失败（t.Errorf "BUG REPRODUCED"）= 文件锁未及时释放 = issue #20 根因确认。
// 成功 = 当前实现（KillManifestExes）在测试场景下工作正常。
func TestKillByImageReleaseTiming(t *testing.T) {
	sleeper := buildSleeper(t)

	// 铺地板：dest 目录有 plugin.exe（旧版本）
	destDir := t.TempDir()
	destExe := filepath.Join(destDir, "plugin.exe")
	copyFileExe(t, sleeper, destExe)
	appendMarker(t, destExe, "OLD-VERSION\n")

	// 启动 destExe（持有自身 .exe 文件锁）
	cmd := exec.Command(destExe)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	t.Logf("plugin started pid=%d", cmd.Process.Pid)
	// 测试结束确保清理
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// 短暂等待进程完全启动（加载 image、初始化）
	time.Sleep(200 * time.Millisecond)

	// 铺地板：incoming/ 有新版本 plugin.exe
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		t.Fatalf("mkdir incoming: %v", err)
	}
	srcExe := filepath.Join(incoming, "plugin.exe")
	copyFileExe(t, sleeper, srcExe)
	appendMarker(t, srcExe, "NEW-VERSION\n")

	// 写 MANIFEST.txt
	sha := shaFile(t, srcExe)
	manifestLine := sha + " 0755 plugin.exe " + destExe + "\n"
	writeTestFile(t, filepath.Join(incoming, ManifestFileName), manifestLine)
	writeTestFile(t, filepath.Join(incoming, VersionFileName), "v0.9.1-test\n")

	// 准备 Machine（PID=0 跳过 KillTree；KillManifestExes 会触发）
	m := NewMachine(stageDir, destDir, testLogger(), realWindowsPC{})

	start := time.Now()
	err := m.Apply(context.Background(), 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("BUG REPRODUCED: Machine.Apply failed after KillByImage (elapsed=%v): %v",
			elapsed, err)
		// 尝试找出实际释放延迟
		probeReleaseDelay(t, destExe, srcExe)
		return
	}

	t.Logf("Apply succeeded in %v — KillByImage 释放了文件锁（测试场景无 race）", elapsed)

	// 验证 swap 完成状态
	got, _ := os.ReadFile(destExe)
	if !strings.Contains(string(got), "NEW-VERSION") {
		t.Errorf("dest content = %q, want contains NEW-VERSION", got)
	}
}

// probeReleaseDelay 在 bug 复现时探测 taskkill 后的实际文件锁释放延迟。
// 不是测试断言，而是诊断输出。
func probeReleaseDelay(t *testing.T, destExe, srcExe string) {
	t.Helper()
	t.Logf("--- probing file lock release delay ---")
	newPath := destExe + ".new"
	for _, delay := range []time.Duration{
		10 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
	} {
		time.Sleep(delay)
		// 重新 stage
		copyFileExe(t, srcExe, newPath)
		err := os.Rename(newPath, destExe)
		if err == nil {
			t.Logf("rename succeeded after cumulative delay ~%v: %v", delay, err)
			return
		}
		t.Logf("  after %v: %v", delay, err)
	}
	t.Logf("rename never succeeded within probe window — plugin.exe 仍被持有")
}

// shaFile 计算文件 sha256 hex（用于 MANIFEST.txt 行）。
func shaFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestKillByImageRelease_MultiCycle 是 issue #20 的核心验收测试：
// 连续 10 次升级循环，每次都启动 plugin.exe → KillByImage → rename。
// 验收标准：所有循环成功，无残留进程。
//
// 若任一循环失败（Access denied）= bug 复现，记录循环号 + 错误。
func TestKillByImageRelease_MultiCycle(t *testing.T) {
	sleeper := buildSleeper(t)
	const cycles = 10

	// 复用同一个 destDir 模拟生产场景（文件锁可能累积）
	destDir := t.TempDir()
	destExe := filepath.Join(destDir, "plugin.exe")
	copyFileExe(t, sleeper, destExe)
	appendMarker(t, destExe, "BASE\n")

	var runningPIDs []int
	defer func() {
		// 测试结束清理所有可能残留的进程
		for _, pid := range runningPIDs {
			_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
		}
	}()

	for i := 0; i < cycles; i++ {
		// 启动当前 destExe（持有 .exe 锁）
		cmd := exec.Command(destExe)
		if err := cmd.Start(); err != nil {
			t.Fatalf("cycle %d: start plugin: %v", i, err)
		}
		runningPIDs = append(runningPIDs, cmd.Process.Pid)
		time.Sleep(100 * time.Millisecond) // 等待进程完全加载

		// 准备 incoming（每轮内容不同 — version marker）
		stageDir := t.TempDir()
		incoming := filepath.Join(stageDir, IncomingDirName)
		if err := os.MkdirAll(incoming, 0o755); err != nil {
			t.Fatalf("cycle %d: mkdir incoming: %v", i, err)
		}
		srcExe := filepath.Join(incoming, "plugin.exe")
		copyFileExe(t, sleeper, srcExe)
		appendMarker(t, srcExe, fmt.Sprintf("CYCLE-%d\n", i))

		sha := shaFile(t, srcExe)
		writeTestFile(t, filepath.Join(incoming, ManifestFileName),
			sha+" 0755 plugin.exe "+destExe+"\n")
		writeTestFile(t, filepath.Join(incoming, VersionFileName),
			fmt.Sprintf("v0.9.%d\n", i+1))

		m := NewMachine(stageDir, destDir, testLogger(), realWindowsPC{})
		start := time.Now()
		err := m.Apply(context.Background(), 0)
		elapsed := time.Since(start)
		t.Logf("cycle %d: Apply elapsed=%v err=%v", i, elapsed, err)
		if err != nil {
			t.Errorf("BUG REPRODUCED at cycle %d (elapsed=%v): %v", i, elapsed, err)
			return
		}

		// 等待 cmd 完全退出（taskkill 后 Process.Wait 应立即返回）
		_ = cmd.Wait()
		runningPIDs = runningPIDs[:len(runningPIDs)-1] // pop

		// 验证内容已替换
		got, _ := os.ReadFile(destExe)
		want := fmt.Sprintf("CYCLE-%d", i)
		if !strings.Contains(string(got), want) {
			t.Errorf("cycle %d: dest content = %q, want contains %q", i, got, want)
		}
	}
	t.Logf("All %d cycles succeeded", cycles)
}

// TestKillByImageRelease_ParallelChildren 模拟更接近生产的环境：
// 主进程启动多个子进程（模拟 windows_exporter + 其他 plugin），
// 验证 KillManifestExes 能否一次性杀光所有 .exe 持有者。
func TestKillByImageRelease_ParallelChildren(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping parallel test in short mode")
	}
	sleeper := buildSleeper(t)

	// 准备两个不同的 .exe（plugin-a.exe + plugin-b.exe）
	destDir := t.TempDir()
	destA := filepath.Join(destDir, "plugin-a.exe")
	destB := filepath.Join(destDir, "plugin-b.exe")
	copyFileExe(t, sleeper, destA)
	copyFileExe(t, sleeper, destB)
	appendMarker(t, destA, "OLD-A\n")
	appendMarker(t, destB, "OLD-B\n")

	// 同时启动两个进程（都持有各自的 .exe 锁）
	cmdA := exec.Command(destA)
	cmdB := exec.Command(destB)
	if err := cmdA.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	if err := cmdB.Start(); err != nil {
		t.Fatalf("start B: %v", err)
	}
	defer func() {
		_ = cmdA.Process.Kill()
		_ = cmdB.Process.Kill()
		_, _ = cmdA.Process.Wait()
		_, _ = cmdB.Process.Wait()
	}()
	time.Sleep(200 * time.Millisecond)

	// 准备 incoming（含两条 MANIFEST 条目）
	stageDir := t.TempDir()
	incoming := filepath.Join(stageDir, IncomingDirName)
	os.MkdirAll(incoming, 0o755)

	srcA := filepath.Join(incoming, "plugin-a.exe")
	srcB := filepath.Join(incoming, "plugin-b.exe")
	copyFileExe(t, sleeper, srcA)
	copyFileExe(t, sleeper, srcB)
	appendMarker(t, srcA, "NEW-A\n")
	appendMarker(t, srcB, "NEW-B\n")

	shaA := shaFile(t, srcA)
	shaB := shaFile(t, srcB)
	manifest := shaA + " 0755 plugin-a.exe " + destA + "\n" +
		shaB + " 0755 plugin-b.exe " + destB + "\n"
	writeTestFile(t, filepath.Join(incoming, ManifestFileName), manifest)
	writeTestFile(t, filepath.Join(incoming, VersionFileName), "v1.0.0-parallel\n")

	m := NewMachine(stageDir, destDir, testLogger(), realWindowsPC{})
	start := time.Now()
	err := m.Apply(context.Background(), 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Errorf("BUG REPRODUCED in parallel scenario (elapsed=%v): %v", elapsed, err)
		return
	}
	t.Logf("Parallel Apply succeeded in %v", elapsed)

	gotA, _ := os.ReadFile(destA)
	gotB, _ := os.ReadFile(destB)
	if !strings.Contains(string(gotA), "NEW-A") {
		t.Errorf("destA = %q, want NEW-A", gotA)
	}
	if !strings.Contains(string(gotB), "NEW-B") {
		t.Errorf("destB = %q, want NEW-B", gotB)
	}
}
