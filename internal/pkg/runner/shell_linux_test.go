package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestShellRunnerProcessGroupCancellation(t *testing.T) {
	for _, mode := range []string{"timeout", "parent_deadline", "cancel"} {
		for _, waitForChild := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wait=%t", mode, waitForChild), func(t *testing.T) {
				pidFile := filepath.Join(t.TempDir(), "pids")
				t.Cleanup(func() { checkCommandProcesses(t, pidFile) })
				script := fmt.Sprintf("sleep 3 & echo $$ $! > %q; echo ready; echo warning >&2", pidFile)
				if waitForChild {
					script += "; wait"
				}
				ctx := context.Background()
				timeout := 200 * time.Millisecond
				wantErr := error(context.DeadlineExceeded)
				if mode == "parent_deadline" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
					timeout = 5 * time.Second
				} else if mode == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					timer := time.AfterFunc(timeout, cancel)
					defer timer.Stop()
					timeout, wantErr = 5*time.Second, context.Canceled
				}
				start := time.Now()
				res, err := NewShellRunner().Run(ctx, Spec{Script: script, Timeout: timeout})
				if !errors.Is(err, wantErr) || res.ExitCode != -1 {
					t.Fatalf("result=%+v err=%v, want %v and exit=-1", res, err, wantErr)
				}
				if res.Stdout != "ready\n" || res.Stderr != "warning\n" {
					t.Fatalf("partial output lost: %+v", res)
				}
				if elapsed := time.Since(start); elapsed > 2*time.Second {
					t.Fatalf("cancellation at 200ms returned after %s", elapsed)
				}
			})
		}
	}
}

func TestShellRunnerDrainsBackgroundOutputBeforeDeadline(t *testing.T) {
	res, err := NewShellRunner().Run(context.Background(), Spec{
		Script: "(sleep 1.2; echo delayed; echo warning >&2) & echo ready", Timeout: 3 * time.Second,
	})
	if err != nil || res.ExitCode != 0 || res.Stdout != "ready\ndelayed\n" || res.Stderr != "warning\n" {
		t.Fatalf("normal shell exit lost background output: %+v, %v", res, err)
	}
}

func TestShellRunnerCleansRedirectedBackgroundProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	t.Cleanup(func() { checkCommandProcesses(t, pidFile) })
	res, err := NewShellRunner().Run(context.Background(), Spec{
		Script: fmt.Sprintf("sleep 3 >/dev/null 2>&1 & echo $$ $! > %q; echo ready", pidFile),
	})
	if err != nil || res.ExitCode != 0 || res.Stdout != "ready\n" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestShellRunnerCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := NewShellRunner().Run(ctx, Spec{Script: "echo should-not-run"})
	if !errors.Is(err, context.Canceled) || res.ExitCode != -1 || res.Stdout != "" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestShellRunnerBoundsEscapedChildPipeWait(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	pidFile := filepath.Join(t.TempDir(), "escaped-pid")
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Errorf("read escaped PID: %v", err)
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 1 {
			t.Fatalf("invalid escaped PID %q", data)
		}
		// This fixture deliberately leaves the command's process group.
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			t.Errorf("cleanup escaped group: %v", err)
		}
	})
	start := time.Now()
	res, err := NewShellRunner().Run(context.Background(), Spec{
		Script:  fmt.Sprintf("setsid sh -c 'echo $$ > %q; sleep 10' & wait", pidFile),
		Timeout: 200 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) || res.ExitCode != -1 {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("escaped descendant kept output pipes open for %s", elapsed)
	}
}

func TestShellRunnerCancellationReleasesPipes(t *testing.T) {
	if _, err := NewShellRunner().Run(context.Background(), Spec{Script: "true"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		res, err := NewShellRunner().Run(context.Background(), Spec{Script: "sleep 3 & echo ready", Timeout: 50 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) || res.ExitCode != -1 {
			t.Fatalf("result=%+v err=%v", res, err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("file descriptors leaked: before=%d after=%d", len(before), len(after))
	}
}

// Check only this invocation's PIDs; zombies are dead and await the host reaper.
func checkCommandProcesses(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Errorf("read process IDs: %v", err)
		return
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 1 {
			t.Fatalf("invalid test PID %q", field)
		}
		deadline := time.Now().Add(time.Second)
		for {
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				break
			}
			if err != nil {
				t.Fatalf("read process state: %v", err)
			}
			state := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])[0]
			if state == "Z" || state == "X" {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("process %d still alive after Run returned", pid)
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
					t.Errorf("cleanup process: %v", err)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
