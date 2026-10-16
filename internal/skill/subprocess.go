package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxSubprocessStdout caps the bytes the runtime will read from a
// subprocess skill's stdout. 16 MiB is enough headroom for tool output
// while bounding RAM in the manager when a misbehaving binary spews.
const MaxSubprocessStdout = 16 * 1024 * 1024

// MaxSubprocessStderrTail is how many trailing bytes of stderr we keep
// for diagnostics in error envelopes / audit rows.
const MaxSubprocessStderrTail = 4 * 1024

// DefaultSubprocessTimeout is the deadline applied when a manifest does
// not specify timeout_seconds. Allow longer diagnostic subprocesses while
// retaining explicit manifest deadlines and caller cancellation.
const DefaultSubprocessTimeout = 5 * time.Minute

// SubprocessSkill runs an external executable per invocation. The skill
// body lives in a separate process: ongrid pipes a JSON args blob to its
// stdin, captures stdout (the JSON result), and surfaces non-zero exits
// as errors. This is how skills.sh / openclaw-style external skill
// packs plug into the framework without dragging in a Python or Node
// runtime.
//
// Security: SubprocessSkill is always ScopeManager (we can't sandbox
// arbitrary binaries on edges; that's a future hardening). The Loader
// enforces an allowlist directory so callers can't point Entry at
// /bin/sh; SubprocessSkill itself only checks Entry is an absolute path.
// The env passed to the child is filtered to EnvAllow (never the full
// manager env), so leaking secrets requires explicit opt-in.
type SubprocessSkill struct {
	// Meta is the framework-visible spec. Scope is forced to
	// ScopeManager by Validate; Class defaults to ClassSafe so the
	// LLM tool registry picks the skill up. The full ParamSchema is
	// optional — when empty, Schema (the raw JSON Schema from the
	// manifest) takes over.
	Meta Metadata

	// Schema is the raw JSON Schema for the args object, mirroring
	// what the manifest carries. When non-empty it overrides whatever
	// Metadata.Params would generate. Built into the LLM tool
	// description; passed unmodified to the model.
	Schema json.RawMessage

	// Entry is the absolute path to the executable. Loader enforces
	// "must live under one of the configured ExternalDirs"; this struct
	// only checks the path is absolute (defence-in-depth — a hand-built
	// SubprocessSkill that lands in the registry should still refuse
	// "/etc/shadow"-style relative-path tricks).
	Entry string

	// EnvAllow is the list of env var names from the manager process
	// that we forward to the child. Anything outside this list is
	// stripped (no PATH inheritance unless EnvAllow includes "PATH").
	// In practice manifests only opt into the API key env var(s) the
	// skill needs.
	EnvAllow []string

	// Timeout caps the subprocess runtime. Zero falls back to
	// DefaultSubprocessTimeout.
	Timeout time.Duration

	// runner exists for tests — production paths use exec.CommandContext.
	// nil = use the real implementation.
	runner subprocessRunner
}

// subprocessRunner is the seam tests inject through. The default
// implementation uses os/exec; the test double captures the request and
// returns canned stdout / stderr / exit code.
type subprocessRunner interface {
	Run(ctx context.Context, entry string, env []string, stdin []byte) (stdout, stderr []byte, exitCode int, err error)
}

// Metadata implements Executor. The returned metadata reports
// ScopeManager — we override whatever the author put on Meta so a
// hand-rolled SubprocessSkill can't accidentally claim edge scope.
func (s *SubprocessSkill) Metadata() Metadata {
	m := s.Meta
	m.Scope = ScopeManager
	if m.Class == "" {
		m.Class = ClassSafe
	}
	return m
}

// Execute spawns the configured executable with stdin = the args blob
// and returns its stdout. Errors fall into three buckets:
//
//   - validation (entry is empty / not absolute / args invalid JSON):
//     returned as a Go error, no audit envelope
//   - timeout: returned as a Go error wrapping context.DeadlineExceeded
//   - non-zero exit: returned as a Go error with the stderr tail so the
//     audit log + LLM see what went wrong
//
// On success the raw stdout is returned as-is (the framework expects
// json.RawMessage). Callers that need to merge a skipped_reason or an
// error envelope should do that one layer up; this keeps the contract
// "stdout IS the result" symmetric with native Go skills.
func (s *SubprocessSkill) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("subprocess skill: nil receiver")
	}
	if s.Entry == "" {
		return nil, errors.New("subprocess skill: entry path required")
	}
	if !filepath.IsAbs(s.Entry) {
		return nil, fmt.Errorf("subprocess skill: entry %q must be absolute", s.Entry)
	}
	// stat the entry so we fail fast when the operator deleted the
	// binary out from under us. We deliberately do NOT follow symlinks
	// for the "must live under allowlist" check (that's the loader's
	// job) — here we only assert the file exists and is executable.
	info, statErr := os.Stat(s.Entry)
	if statErr != nil {
		return nil, fmt.Errorf("subprocess skill: stat %s: %w", s.Entry, statErr)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("subprocess skill: entry %s is a directory", s.Entry)
	}
	if info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("subprocess skill: entry %s not executable", s.Entry)
	}

	// Default an empty params blob to "{}" so stdin is always valid JSON
	// — many shell-based skills can't deal with EOF before any byte.
	if len(bytes.TrimSpace(params)) == 0 {
		params = json.RawMessage(`{}`)
	} else if !json.Valid(params) {
		return nil, fmt.Errorf("subprocess skill: invalid params JSON")
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultSubprocessTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env := buildSubprocessEnv(s.EnvAllow)
	runner := s.runner
	if runner == nil {
		runner = realSubprocessRunner{}
	}
	stdout, stderr, code, err := runner.Run(cctx, s.Entry, env, params)
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("subprocess skill %s: timed out after %s", s.Meta.Key, timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("subprocess skill %s: %w (stderr: %s)", s.Meta.Key, err, tailString(stderr, MaxSubprocessStderrTail))
	}
	if code != 0 {
		return nil, fmt.Errorf("subprocess skill %s: exit %d (stderr: %s)", s.Meta.Key, code, tailString(stderr, MaxSubprocessStderrTail))
	}
	if !json.Valid(stdout) {
		return nil, fmt.Errorf("subprocess skill %s: stdout is not valid JSON (first 200B: %s)",
			s.Meta.Key, tailString(stdout, 200))
	}
	return json.RawMessage(stdout), nil
}

// buildSubprocessEnv assembles the env slice for the child. We start
// from a clean slate (no inherited env) and then opt in to each name
// in allow. Returns nil when allow is empty (Cmd.Env=nil makes the
// child inherit os.Environ, but our contract is "deny by default" — so
// we return an explicit empty slice instead of nil).
func buildSubprocessEnv(allow []string) []string {
	env := make([]string, 0, len(allow))
	for _, name := range allow {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		env = append(env, name+"="+v)
	}
	return env
}

// tailString returns at most n trailing bytes of b as a string. Used
// for stderr previews in error messages.
func tailString(b []byte, n int) string {
	if n <= 0 || len(b) == 0 {
		return ""
	}
	if len(b) <= n {
		return strings.TrimSpace(string(b))
	}
	return "..." + strings.TrimSpace(string(b[len(b)-n:]))
}

// realSubprocessRunner is the production implementation. It uses
// exec.CommandContext to honour the parent context's cancellation, caps
// stdout at MaxSubprocessStdout, and clamps stderr to MaxSubprocessStderrTail
// in memory.
type realSubprocessRunner struct{}

func (realSubprocessRunner) Run(ctx context.Context, entry string, env []string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, entry)
	configureSubprocessCommand(cmd)
	cmd.WaitDelay = time.Second
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)

	stdoutBuf := &bytes.Buffer{}
	stderrBuf := &bytes.Buffer{}
	cmd.Stdout = &cappedWriter{w: stdoutBuf, max: MaxSubprocessStdout}
	cmd.Stderr = &cappedWriter{w: stderrBuf, max: MaxSubprocessStderrTail * 4}

	if err := cmd.Start(); err != nil {
		return nil, stderrBuf.Bytes(), 0, fmt.Errorf("start: %w", err)
	}
	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
			waitErr = nil
		}
	}
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), exitCode, waitErr
}

// cappedWriter is an io.Writer that drops bytes once the cap is reached.
// We write into an in-memory bytes.Buffer; the cap exists to bound RAM
// when a misbehaving skill spews. Beyond the cap we silently discard —
// returning ErrShortWrite would surface as a non-fatal exec error and
// confuse "did the skill succeed".
type cappedWriter struct {
	w   io.Writer
	max int
	n   int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n >= c.max {
		return len(p), nil
	}
	remaining := c.max - c.n
	if len(p) <= remaining {
		written, err := c.w.Write(p)
		c.n += written
		return written, err
	}
	written, err := c.w.Write(p[:remaining])
	c.n += written
	if err != nil {
		return written, err
	}
	return len(p), nil
}
