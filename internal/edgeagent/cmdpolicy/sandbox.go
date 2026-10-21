//go:build linux

package cmdpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/runner"
)

// sandbox.go is the executor that turns a Decision into actual exec
// behaviour. The Policy alone makes a yes/no decision based on policy
// rules; Sandbox layers on:
//
//   - PathValidator: every argv token starting with "/" is run through
//     the validator (we reuse host_files.SandboxConfig.ValidatePath via
//     the PathValidator interface — keeps a single source of truth).
//
//   - NetworkHostAllowlist: for ClassNetwork binaries the resolved
//     target host (curl URL host, ping <host>, dig <host>, etc.) must
//     match an allowlist entry. Empty list = deny ALL outbound.
//
//   - exec.Command pipeline: when the Decision contains pipe segments
//     we wire them stdout→stdin Linux-style. Each child runs with a
//     scrubbed env (PATH/LANG/LC_ALL only) and a context-bound
//     timeout. On timeout the whole pipeline is cancelled.
//
//   - Output cap: stdout / stderr are truncated at Policy.StdoutCap /
//     StderrCap respectively. The ShellResult flags Truncated when we
//     hit the cap so the LLM can ask for a different command rather
//     than guess what got chopped.

// PathValidator is the narrow interface cmdpolicy borrows from
// host_files.SandboxConfig. We don't redeclare path validation logic —
// the host_files implementation is the single source of truth and
// already handles symlink canonicalisation / lexical-prefix-match.
//
// Implementations MUST treat absolute paths as the only valid input;
// relative paths / "" should error. Empty input is the caller's
// responsibility to filter, but a defensive validator returning an
// error is fine too.
type PathValidator interface {
	ValidatePath(path string) error
}

// Sandbox wires Policy + PathValidator + logger into the executor.
// Construct via &Sandbox{Policy: ..., PathValidator: ..., Logger: ...}
// — there is no NewSandbox helper because the wiring is trivial.
type Sandbox struct {
	Policy        *Policy
	PathValidator PathValidator
	Logger        *slog.Logger
}

// ShellResult is the wire-friendly outcome of one Sandbox.Exec call.
// Allowed=false carries Reason; Allowed=true carries Stdout/Stderr/
// ExitCode/DurationMs/Truncated. Empty stdout + stderr is fine for a
// successful command that emitted nothing — distinguish from "rejected"
// via the Allowed flag, not the byte counts.
type ShellResult struct {
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// Decide is the policy + path + network combined check. Use this for
// dry-run UI / audit; Exec calls it internally before spawning.
func (s *Sandbox) Decide(cmd string) Decision {
	if s == nil || s.Policy == nil {
		return Decision{Allow: false, Reason: "cmdpolicy: sandbox not configured"}
	}
	d := s.Policy.Decide(cmd)
	if !d.Allow {
		return d
	}
	// Path validation across all segments.
	for segIdx, seg := range d.Segments {
		for argIdx, a := range seg {
			if argIdx == 0 {
				continue // argv[0] is the binary; resolved separately
			}
			if !strings.HasPrefix(a, "/") {
				continue
			}
			if s.PathValidator == nil {
				continue
			}
			if err := s.PathValidator.ValidatePath(a); err != nil {
				return Decision{
					Allow:    false,
					Reason:   fmt.Sprintf("segment %d arg %d: %s", segIdx, argIdx, err.Error()),
					Segments: d.Segments,
				}
			}
		}
	}
	// Network host check for ClassNetwork segments.
	for segIdx, seg := range d.Segments {
		if len(seg) == 0 {
			continue
		}
		bp := s.Policy.Lookup(seg[0])
		if bp == nil || bp.Class != ClassNetwork {
			continue
		}
		host := extractNetworkHost(seg)
		if host == "" {
			// No identifiable host (e.g. `dig` with no args). Allow —
			// it'll just print usage info.
			continue
		}
		if !hostAllowed(host, s.Policy.NetworkHostAllowlist) {
			return Decision{
				Allow: false,
				Reason: fmt.Sprintf("segment %d: network target %q not in allowlist (set network_host_allowlist to permit)",
					segIdx, host),
				Segments: d.Segments,
			}
		}
	}
	return d
}

// Exec runs cmd. Returns ShellResult with Allowed=false + Reason on
// any policy / path / network rejection; Allowed=true with the actual
// process result otherwise. Errors returned alongside the result
// indicate INTERNAL failures (failed to start the process, OS error)
// — not policy denials and not non-zero exit codes (those flow through
// ExitCode).
func (s *Sandbox) Exec(ctx context.Context, cmd string) (*ShellResult, error) {
	if s == nil || s.Policy == nil {
		return &ShellResult{Allowed: false, Reason: "cmdpolicy: sandbox not configured"}, nil
	}
	d := s.Decide(cmd)
	if !d.Allow {
		return &ShellResult{Allowed: false, Reason: d.Reason}, nil
	}
	timeout := s.Policy.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()

	stdout, stderr, exitCode, truncated, err := s.runPipeline(cctx, d.Segments)
	dur := time.Since(start)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		// Non-timeout startup error (binary missing post-policy etc.).
		// Surface as a non-zero exit + the underlying message.
		message := err.Error()
		if errors.Is(err, context.Canceled) {
			message = "cmdpolicy: command canceled"
		}
		return &ShellResult{
			Allowed:    true,
			Stdout:     stdout,
			Stderr:     message,
			ExitCode:   -1,
			Truncated:  truncated,
			DurationMs: dur.Milliseconds(),
		}, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ShellResult{
			Allowed:    true,
			Stdout:     stdout,
			Stderr:     "cmdpolicy: command timed out",
			ExitCode:   -1,
			Truncated:  truncated,
			DurationMs: dur.Milliseconds(),
		}, nil
	}
	return &ShellResult{
		Allowed:    true,
		Stdout:     stdout,
		Stderr:     stderr,
		ExitCode:   exitCode,
		Truncated:  truncated,
		DurationMs: dur.Milliseconds(),
	}, nil
}

// ExecRaw runs cmd through /bin/sh -c, BYPASSING every cmdpolicy check —
// the binary allowlist, the denied-class list, the path/network allowlists
// and the shell-metacharacter grammar are ALL skipped, so redirects, &&, |,
// $(), backticks etc. work and the command runs with the edge agent's full
// privileges. This is the admin write-gate escape hatch
// (BashExecRequest.Unrestricted): the manager only asks for it when the
// operator has turned on "allow Agent write actions". The output caps + the
// per-call timeout still apply so a runaway command can't park a tunnel slot
// or flood the LLM. Path/allowlist safety is intentionally NOT enforced here
// — bypassing it is the whole point of the gate.
func (s *Sandbox) ExecRaw(ctx context.Context, cmd string) (*ShellResult, error) {
	if s == nil || s.Policy == nil {
		return &ShellResult{Allowed: false, Reason: "cmdpolicy: sandbox not configured"}, nil
	}
	if strings.TrimSpace(cmd) == "" {
		return &ShellResult{Allowed: false, Reason: "cmdpolicy: empty command"}, nil
	}
	timeout := s.Policy.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()

	c := exec.CommandContext(cctx, "/bin/sh", "-c", cmd)
	c.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	outW := &capWriter{buf: &bytes.Buffer{}, cap: s.Policy.StdoutCap}
	errW := &capWriter{buf: &bytes.Buffer{}, cap: s.Policy.StderrCap}
	c.Stdout = outW
	c.Stderr = errW

	wait, runErr := runner.StartCommand(cctx, c)
	if runErr == nil {
		runErr = wait()
	}
	dur := time.Since(start)
	trunc := capHit(outW) || capHit(errW)

	if err := cctx.Err(); err != nil {
		message := "cmdpolicy: command canceled"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "cmdpolicy: command timed out"
		}
		return &ShellResult{
			Allowed: true, Stdout: outW.buf.String(), Stderr: message,
			ExitCode: -1, Truncated: trunc, DurationMs: dur.Milliseconds(),
		}, nil
	}
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			// OS-level startup failure (e.g. /bin/sh missing).
			return &ShellResult{
				Allowed: true, Stdout: outW.buf.String(), Stderr: runErr.Error(),
				ExitCode: -1, Truncated: trunc, DurationMs: dur.Milliseconds(),
			}, nil
		}
	}
	return &ShellResult{
		Allowed: true, Stdout: outW.buf.String(), Stderr: errW.buf.String(),
		ExitCode: exitCode, Truncated: trunc, DurationMs: dur.Milliseconds(),
	}, nil
}

// capHit reports whether a capWriter reached its cap (output truncated).
func capHit(w *capWriter) bool {
	return w != nil && w.cap > 0 && w.buf.Len() >= w.cap
}

// runPipeline wires the segments through stdin/stdout pipes and runs
// them. Returns (stdout, stderr, exitCode, truncated, err). err is
// non-nil for startup, cancellation, or I/O failures.
func (s *Sandbox) runPipeline(ctx context.Context, segments [][]string) (string, string, int, bool, error) {
	if len(segments) == 0 {
		return "", "", -1, false, errors.New("cmdpolicy: no segments to run")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmds := make([]*exec.Cmd, 0, len(segments))
	for _, seg := range segments {
		bp := s.Policy.Lookup(seg[0])
		if bp == nil || bp.AbsPath == "" {
			return "", "", -1, false, fmt.Errorf("cmdpolicy: binary %q not resolvable at exec time", seg[0])
		}
		cmd := exec.CommandContext(ctx, bp.AbsPath, seg[1:]...)
		cmd.Env = []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/usr/bin:/bin",
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
		}
		cmds = append(cmds, cmd)
	}

	// Own the pipeline ends so an upstream Wait cannot close a downstream
	// reader before it has drained the remaining output.
	var pipes []*os.File
	closePipes := func() {
		for _, p := range pipes {
			// These private ends may already be closed after startup.
			_ = p.Close()
		}
	}
	defer closePipes()
	for i := 0; i < len(cmds)-1; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			return "", "", -1, false, fmt.Errorf("cmdpolicy: stdout pipe seg %d: %w", i, err)
		}
		pipes = append(pipes, r, w)
		cmds[i].Stdout = w
		cmds[i+1].Stdin = r
	}
	// Capture last segment's stdout + per-segment stderr.
	var lastStdout bytes.Buffer
	last := cmds[len(cmds)-1]
	last.Stdout = &capWriter{buf: &lastStdout, cap: s.Policy.StdoutCap}
	stderrs := make([]*capWriter, len(cmds))
	for i, c := range cmds {
		w := &capWriter{buf: &bytes.Buffer{}, cap: s.Policy.StderrCap}
		c.Stderr = w
		stderrs[i] = w
	}

	var waits []func() error
	for i, c := range cmds {
		wait, err := runner.StartCommand(ctx, c)
		if err != nil {
			cancel()
			closePipes()
			for _, wait := range waits {
				// Cancellation is intentional; still reap every started child.
				_ = wait()
			}
			return "", "", -1, false, fmt.Errorf("cmdpolicy: start seg %d (%s): %w",
				i, c.Path, err)
		}
		waits = append(waits, wait)
	}
	closePipes()
	// Wait. Pipeline exit code = last segment's. Errors from earlier
	// segments are surfaced via stderr.
	var lastExit int
	var pipelineErr error
	for i, wait := range waits {
		err := wait()
		if err != nil {
			// Closed pipe (broken pipe when downstream exits early)
			// is benign for non-last segments.
			if i != len(cmds)-1 && isBrokenPipe(err) {
				continue
			}
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				if i == len(cmds)-1 {
					lastExit = ee.ExitCode()
				}
				continue
			}
			pipelineErr = errors.Join(pipelineErr, err)
		}
	}
	if err := ctx.Err(); err != nil {
		lastExit, pipelineErr = -1, err
	}
	stdout := lastStdout.String()
	stderr := joinStderrs(stderrs)
	truncated := anyTruncated(lastStdout, stderrs, s.Policy)
	return stdout, stderr, lastExit, truncated, pipelineErr
}

// joinStderrs concatenates per-segment stderr into one stream, in order.
func joinStderrs(ws []*capWriter) string {
	var b strings.Builder
	for _, w := range ws {
		if w == nil || w.buf == nil {
			continue
		}
		s := w.buf.String()
		if s == "" {
			continue
		}
		b.WriteString(s)
	}
	return b.String()
}

func anyTruncated(stdout bytes.Buffer, stderrs []*capWriter, p *Policy) bool {
	if p == nil {
		return false
	}
	if p.StdoutCap > 0 && stdout.Len() >= p.StdoutCap {
		return true
	}
	for _, w := range stderrs {
		if w == nil {
			continue
		}
		if w.cap > 0 && w.buf.Len() >= w.cap {
			return true
		}
	}
	return false
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	// exec.ExitError wraps the underlying signal exit; treat broken
	// pipe SIGPIPE as benign mid-pipeline.
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() && ws.Signal() == syscall.SIGPIPE {
				return true
			}
		}
	}
	return false
}

// capWriter is a bytes.Buffer with a hard byte cap. Writes past the cap
// are dropped silently; callers detect via cap>0 && len(buf)>=cap.
type capWriter struct {
	buf *bytes.Buffer
	cap int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.cap <= 0 {
		return w.buf.Write(p)
	}
	remaining := w.cap - w.buf.Len()
	if remaining <= 0 {
		// Pretend success so the upstream process keeps streaming
		// without erroring out — we'll truncate in the result envelope.
		return len(p), nil
	}
	if len(p) <= remaining {
		return w.buf.Write(p)
	}
	w.buf.Write(p[:remaining])
	return len(p), nil
}

var _ io.Writer = (*capWriter)(nil)

// =====================================================================
// network host extraction + allowlist match
// =====================================================================

// extractNetworkHost picks the most likely target host from a network
// binary's argv. Heuristics by binary; on no-match returns "". The
// caller treats "" as "no host to validate" and lets the call through.
func extractNetworkHost(seg []string) string {
	if len(seg) < 2 {
		return ""
	}
	bin := seg[0]
	rest := seg[1:]
	switch bin {
	case "curl", "wget":
		// First non-flag token that looks like a URL or hostname.
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				continue
			}
			return hostFromURL(a)
		}
	case "dig", "host", "nslookup":
		// dig has lots of @server / +flag options; the first non-flag
		// non-@ argument is the name being looked up.
		for _, a := range rest {
			if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") || strings.HasPrefix(a, "@") {
				continue
			}
			return a
		}
	case "ping", "traceroute":
		// Last non-flag positional is the target.
		for i := len(rest) - 1; i >= 0; i-- {
			a := rest[i]
			if strings.HasPrefix(a, "-") {
				continue
			}
			return a
		}
	case "nc":
		// `nc -z host port` — second-to-last positional is host.
		positionals := []string{}
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				continue
			}
			positionals = append(positionals, a)
		}
		if len(positionals) >= 1 {
			return positionals[0]
		}
	}
	return ""
}

// hostFromURL extracts the host out of "http://example.com/path" or
// "example.com:8080" — falls back to the input string when neither
// scheme nor port is present.
func hostFromURL(s string) string {
	// Strip scheme.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Take up to the first /, ?, # — the authority section.
	for _, c := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(s, c); i >= 0 {
			s = s[:i]
		}
	}
	// Strip user@ prefix.
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	// Strip port.
	if i := strings.LastIndex(s, ":"); i >= 0 {
		// Bracketed IPv6 [::1]:8080 — host is between [].
		if strings.HasPrefix(s, "[") {
			if end := strings.IndexByte(s, ']'); end > 0 {
				return s[1:end]
			}
		}
		s = s[:i]
	}
	return s
}

// hostAllowed checks host against an allowlist. Each entry is either a
// CIDR (10.0.0.0/8) or a hostname suffix (e.g. ".internal"); a literal
// IP / hostname can also appear. Empty list = deny ALL.
func hostAllowed(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return false
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// CIDR?
		if strings.Contains(entry, "/") {
			_, ipnet, err := net.ParseCIDR(entry)
			if err == nil && ip != nil && ipnet.Contains(ip) {
				return true
			}
			continue
		}
		// Hostname suffix? (entries beginning with "." match suffix.)
		if strings.HasPrefix(entry, ".") {
			if strings.HasSuffix(host, entry) {
				return true
			}
			continue
		}
		// Exact match (IP or hostname).
		if entry == host {
			return true
		}
	}
	return false
}
