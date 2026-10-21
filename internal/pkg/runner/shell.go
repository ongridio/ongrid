package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

// ShellRunner is the IsolationNone backend: an in-process subprocess. Env
// is fully replaced (no manager-env inheritance), output + time are capped.
// Mirrors the proven knobs of internal/skill/subprocess.go, generalized to
// arbitrary argv/script.
type ShellRunner struct{}

// NewShellRunner builds the shell backend.
func NewShellRunner() *ShellRunner { return &ShellRunner{} }

func (*ShellRunner) Isolation() Isolation { return IsolationNone }

func (r *ShellRunner) Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 && spec.Script == "" {
		return Result{}, fmt.Errorf("runner: empty spec (need Argv or Script)")
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOut := spec.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if spec.Script != "" {
		shell := spec.Shell
		if len(shell) == 0 {
			shell = []string{"/bin/sh", "-c"}
		}
		args := append(append([]string{}, shell[1:]...), spec.Script)
		cmd = exec.CommandContext(cctx, shell[0], args...)
	} else {
		cmd = exec.CommandContext(cctx, spec.Argv[0], spec.Argv[1:]...)
	}

	// Working dir: caller-supplied (must exist) or a transient temp dir.
	workdir := spec.Workdir
	var tmp string
	if workdir == "" {
		var err error
		if tmp, err = os.MkdirTemp("", "runner-"); err != nil {
			return Result{}, fmt.Errorf("runner: mkdtemp: %w", err)
		}
		defer os.RemoveAll(tmp)
		workdir = tmp
	}
	cmd.Dir = workdir

	// Full env replacement — NEVER inherit the manager process env, so its
	// secrets (ONGRID_SECRET_KEY, DB DSN, …) can't leak to the child. The
	// caller supplies everything, including a PATH.
	cmd.Env = buildEnv(spec.Env)

	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	outBuf := &capBuffer{max: maxOut}
	errBuf := &capBuffer{max: maxOut}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf

	start := time.Now()
	wait, runErr := StartCommand(cctx, cmd)
	if runErr == nil {
		runErr = wait()
	}
	dur := time.Since(start)

	res := Result{
		Stdout:    outBuf.String(),
		Stderr:    errBuf.String(),
		Truncated: outBuf.truncated || errBuf.truncated,
		Duration:  dur,
	}
	if err := cctx.Err(); err != nil {
		res.ExitCode = -1
		if errors.Is(err, context.DeadlineExceeded) {
			return res, fmt.Errorf("runner: timed out after %s: %w", timeout, err)
		}
		return res, fmt.Errorf("runner: canceled: %w", err)
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil // non-zero exit is a result, not a runner error
		}
		res.ExitCode = -1
		return res, fmt.Errorf("runner: exec: %w", runErr)
	}
	return res, nil
}

// buildEnv turns the env map into a sorted KEY=VALUE slice. A nil/empty map
// still gets a minimal PATH so common binaries resolve.
func buildEnv(env map[string]string) []string {
	if _, ok := env["PATH"]; !ok {
		if env == nil {
			env = map[string]string{}
		} else {
			cp := make(map[string]string, len(env)+1)
			for k, v := range env {
				cp[k] = v
			}
			env = cp
		}
		env["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// capBuffer is a bytes.Buffer that stops growing past max and records that
// it truncated. Keeps a runaway command from OOMing the manager.
type capBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() >= c.max {
		c.truncated = true
		return len(p), nil // pretend-consume so the pipe doesn't block
	}
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capBuffer) String() string { return c.buf.String() }
