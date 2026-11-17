// Package runner is the unified execution sandbox abstraction (HLD-017).
// It is the single seam every "run a command/script with injected env"
// path goes through — installed skills, the cloud-shell tool, future
// external-MCP-server launches. One interface, pluggable isolation
// backends: a plain in-process shell today; a container / microVM later,
// WITHOUT changing any caller. This is the manager-side home for the
// sandbox concept that was previously parked on the edge (the edge single-
// tarball constraint doesn't apply to a cloud shell backend).
//
// Credentials are NOT this package's concern: the caller resolves a bound
// credential (biz/secret.ResolveInjection) into Spec.Env and hands it in.
// runner just executes — so it stays dependency-free and unit-testable.
package runner

import (
	"context"
	"io"
	"time"
)

// Isolation is the strength of the execution boundary a backend provides.
// Callers / policy use it to decide whether a given command class is
// allowed in a given runner (e.g. a destructive `terraform apply` may
// require >= IsolationContainer, or human review on IsolationNone).
type Isolation int

const (
	// IsolationNone is an in-process subprocess (shell) — env-scoped and
	// resource-capped, but shares the manager's kernel/filesystem. Trust
	// the command (cmdpolicy / review) accordingly.
	IsolationNone Isolation = iota
	// IsolationContainer runs in a separate container (future backend).
	IsolationContainer
	// IsolationMicroVM runs in a microVM — firecracker/gVisor (future).
	IsolationMicroVM
)

func (i Isolation) String() string {
	switch i {
	case IsolationContainer:
		return "container"
	case IsolationMicroVM:
		return "microvm"
	default:
		return "shell"
	}
}

// Spec is one execution request.
type Spec struct {
	// Argv is the command + args (argv[0] is the binary). Mutually
	// exclusive with Script.
	Argv []string
	// Script, when set, is run via Shell (default /bin/sh -c). Convenient
	// for skills that emit a shell snippet ("terraform plan && ...").
	Script string
	Shell  []string // override the shell for Script (default sh -c)

	// Env is the FULL set of env vars for the child — the runner does NOT
	// inherit the manager's environment (so manager secrets never leak to
	// the child). The caller injects PATH/HOME + the resolved credential
	// env here. nil → a minimal PATH-only env.
	Env map[string]string

	// Workdir is the working directory (must exist). Empty → a transient
	// temp dir created + removed per run.
	Workdir string

	Stdin []byte
	// StdinReader streams large inputs. Mutually exclusive with Stdin.
	StdinReader io.Reader

	// Timeout caps wall-clock; zero → DefaultTimeout.
	Timeout time.Duration

	// MaxOutputBytes caps captured stdout+stderr each; zero → DefaultMaxOutput.
	MaxOutputBytes int
}

// Result is the captured outcome.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool // output hit MaxOutputBytes
	Duration  time.Duration
}

// Runner executes a Spec under some isolation level.
type Runner interface {
	Run(ctx context.Context, spec Spec) (Result, error)
	Isolation() Isolation
}

// Defaults.
const (
	DefaultTimeout   = 2 * time.Minute
	DefaultMaxOutput = 1 << 20 // 1 MiB per stream
)
