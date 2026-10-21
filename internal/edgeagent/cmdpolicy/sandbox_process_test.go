//go:build linux

package cmdpolicy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSandboxProcessGroupCancellation(t *testing.T) {
	for _, path := range []string{"raw", "exec", "pipeline"} {
		for _, mode := range []string{"timeout", "parent_deadline", "cancel"} {
			for _, waitForChild := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/wait=%t", path, mode, waitForChild), func(t *testing.T) {
					pidFile := filepath.Join(t.TempDir(), "pids")
					t.Cleanup(func() { checkSandboxProcesses(t, pidFile) })
					script := fmt.Sprintf("sleep 3 & echo $$ $! > %q; echo ready; echo warning >&2", pidFile)
					if waitForChild {
						script += "; wait"
					}
					p := DefaultReadOnly()
					p.Timeout = 200 * time.Millisecond
					ctx := context.Background()
					wantMessage := "cmdpolicy: command timed out"
					if mode == "parent_deadline" {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, p.Timeout)
						defer cancel()
						p.Timeout = 5 * time.Second
					} else if mode == "cancel" {
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						defer cancel()
						timer := time.AfterFunc(p.Timeout, cancel)
						defer timer.Stop()
						p.Timeout = 5 * time.Second
						wantMessage = "cmdpolicy: command canceled"
					}
					s := &Sandbox{Policy: p}
					run, command := s.ExecRaw, script
					if path != "raw" {
						fixture := filepath.Join(t.TempDir(), "spawn539")
						if err := os.WriteFile(fixture, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
							t.Fatal(err)
						}
						p.bins["spawn539"] = &BinaryPolicy{Bin: "spawn539", AbsPath: fixture, Class: ClassReadSystem}
						run, command = s.Exec, "spawn539"
						if path == "pipeline" {
							command += " | cat"
						}
					}
					start := time.Now()
					res, err := run(ctx, command)
					if err != nil || !res.Allowed || res.ExitCode != -1 || res.Stderr != wantMessage {
						t.Fatalf("result=%+v err=%v, want allowed timeout/cancellation", res, err)
					}
					if res.Stdout != "ready\n" {
						t.Fatalf("partial stdout lost: %+v", res)
					}
					if elapsed := time.Since(start); elapsed > 2*time.Second {
						t.Fatalf("cancellation at 200ms returned after %s", elapsed)
					}
				})
			}
		}
	}
}

func TestSandboxReadOnlyTimeoutStatus(t *testing.T) {
	p := DefaultReadOnly()
	p.Timeout = 100 * time.Millisecond
	file := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	res, err := (&Sandbox{Policy: p}).Exec(context.Background(), "tail -f "+file)
	if err != nil || !res.Allowed || res.ExitCode != -1 || res.Stderr != "cmdpolicy: command timed out" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestSandboxPipelineDrainsOutput(t *testing.T) {
	res, err := (&Sandbox{Policy: DefaultReadOnly()}).Exec(context.Background(), "head -c 262144 /dev/zero | wc -c")
	if err != nil || !res.Allowed || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "262144" {
		t.Fatalf("pipeline output lost: %+v err=%v", res, err)
	}
}

func TestSandboxRawOutputAndExit(t *testing.T) {
	p := DefaultReadOnly()
	p.StdoutCap, p.StderrCap = 4, 4
	res, err := (&Sandbox{Policy: p}).ExecRaw(context.Background(), "printf abcdef; printf uvwxyz >&2; exit 7")
	if err != nil || res.ExitCode != 7 || res.Stdout != "abcd" || res.Stderr != "uvwx" || !res.Truncated {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestSandboxCanceledBeforeStart(t *testing.T) {
	for _, raw := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := &Sandbox{Policy: DefaultReadOnly()}
		run, command := s.Exec, "ls"
		if raw {
			run, command = s.ExecRaw, "echo should-not-run"
		}
		res, err := run(ctx, command)
		if err != nil || !res.Allowed || res.ExitCode != -1 || res.Stdout != "" || res.Stderr != "cmdpolicy: command canceled" {
			t.Fatalf("raw=%t result=%+v err=%v", raw, res, err)
		}
	}
}

func TestSandboxPipelineStartFailureCleansResources(t *testing.T) {
	s := &Sandbox{Policy: DefaultReadOnly()}
	if res, err := s.Exec(context.Background(), "cat"); err != nil || res.ExitCode != 0 {
		t.Fatalf("warmup failed: %+v %v", res, err)
	}
	children := sandboxChildren(t)
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	p := s.Policy
	p.bins["missing539"] = &BinaryPolicy{Bin: "missing539", AbsPath: filepath.Join(t.TempDir(), "missing"), Class: ClassReadSystem}
	for range 5 {
		_, _, _, _, err := s.runPipeline(context.Background(), [][]string{{"cat"}, {"missing539"}})
		if err == nil || !strings.Contains(err.Error(), "start seg 1") {
			t.Fatalf("want second segment startup failure, got %v", err)
		}
	}
	for pid := range sandboxChildren(t) {
		if children[pid] {
			continue
		}
		t.Errorf("started child %d was not reaped", pid)
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			t.Errorf("cleanup child: %v", err)
		}
		if _, err := syscall.Wait4(pid, nil, 0, nil); err != nil && err != syscall.ECHILD {
			t.Errorf("reap child: %v", err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("pipeline descriptors leaked: before=%d after=%d", len(before), len(after))
	}
}

func sandboxChildren(t *testing.T) map[int]bool {
	t.Helper()
	paths, err := filepath.Glob("/proc/self/task/*/children")
	if err != nil {
		t.Fatal(err)
	}
	children := make(map[int]bool)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue // A runtime thread may exit while enumerating it.
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				t.Fatal(err)
			}
			children[pid] = true
		}
	}
	return children
}

func checkSandboxProcesses(t *testing.T, pidFile string) {
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
				t.Errorf("process %d still alive after Exec returned", pid)
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
					t.Errorf("cleanup process: %v", err)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
