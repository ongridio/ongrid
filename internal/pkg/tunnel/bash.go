package tunnel

// bash is the generic shell-execution wire — see
// internal/edgeagent/bash/handlers.go for the edge implementation and
// internal/manager/biz/aiops/tools/bash.go for the manager-side
// BaseTool. v1 ships a read-only policy enforced via cmdpolicy
// (internal/edgeagent/cmdpolicy); future mutating-bash variants would
// reuse this same wire under a different policy preset.
//
// The wire shape stays minimal: a single Cmd string plus an optional
// timeout. Parsing / validation happens edge-side so there's exactly
// one source of truth for what's allowed to run.
const (
	// MethodBashExec runs an LLM-supplied command string under the edge
	// cmdpolicy sandbox. The edge enforces policy + path allowlist +
	// network host allowlist; the manager-side BaseTool merely shapes
	// the call and surfaces the result to the LLM.
	MethodBashExec = "bash.exec"
)

// BashExecRequest is the wire body for MethodBashExec.
type BashExecRequest struct {
	// Cmd is the shell-like command string. Pipes are supported; every
	// other shell metacharacter (redirects / && / || / $() / backticks)
	// is rejected by the edge tokenizer. See cmdpolicy.SplitPipes for
	// the full grammar.
	Cmd string `json:"cmd"`

	// Timeout overrides the policy default per-call ceiling. 0 → use
	// the sandbox default (300s). Negative is treated as 0.
	Timeout int `json:"timeout_seconds,omitempty"`

	// Unrestricted, when true, tells the edge to BYPASS cmdpolicy entirely
	// and run Cmd through a real shell (/bin/sh -c) — binary allowlist,
	// denied-class list, path allowlist and the shell-metacharacter grammar
	// (redirects, &&, ||, &) are ALL skipped, so the command runs with the
	// edge agent's full privileges. The manager only sets this when the
	// admin "allow Agent write actions" gate is ON (resolved per request via
	// the AgentWriteEnabled setting). Default false = the locked read-only
	// cmdpolicy path. This is a deliberate, admin-gated escape hatch — see
	// internal/manager/biz/aiops/chatruntime/runtime.go for where it's set.
	Unrestricted bool `json:"unrestricted,omitempty"`
}

// BashExecResponse is the wire body returned by the edge. Mirrors
// cmdpolicy.ShellResult byte-for-byte except for snake_case JSON keys
// that match the broader ongrid wire convention.
type BashExecResponse struct {
	// Allowed reports whether the policy / path / network checks
	// passed. False means the command was rejected before any process
	// spawn; Reason carries a single human-readable explanation
	// suitable to feed back to the LLM.
	Allowed bool `json:"allowed"`

	// Reason is the rejection reason when Allowed=false; empty on
	// success. Stable enough to be parsed by the LLM ("binary 'rm' is
	// in denied class") so the model can adjust its next call.
	Reason string `json:"reason,omitempty"`

	// Stdout / Stderr carry the (possibly truncated) process output.
	// Truncated reports whether either stream hit Policy.StdoutCap /
	// StderrCap respectively.
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated,omitempty"`

	// DurationMs is the wall-clock duration of the process pipeline,
	// excluding policy-decision time (which is microseconds).
	DurationMs int64 `json:"duration_ms,omitempty"`
}
