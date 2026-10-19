package tunnel

import "time"

// restart_service is the first **mutating** edge skill — SOP
// double-sign verification end-to-end (PR-7 of +). The
// handler restarts an allow-listed systemd service via `systemctl
// restart <unit>`. Manager dispatches via the manager-side BaseTool in
// internal/manager/biz/aiops/tools/restart_service.go, which is
// gated through the new ReviewGate decorator (decorators/review_gate.go)
// so the call is intercepted before reaching the tunnel: the coordinator
// LLM emits a restart_service tool_call, the decorator spawns a
// reviewer worker, the worker reads SOP + edge state, and only on
// "Decision: approve" do we ever marshal a wire request through here.
//
// First version intentionally keeps the wire shape thin (unit name,
// optional reason for the audit row) and mirrors host_files.go: one
// method constant + Request/Response struct pair. Real systemctl
// shell-out is deferred — see internal/edgeagent/restart_service for
// the mock + sandbox implementation.
const (
	// MethodRestartService restarts a systemd service on the edge. Only
	// units whose short name appears in the edge sandbox allow-list are
	// accepted; the manager-side BaseTool also pre-filters the same set
	// so the LLM gets a clear error before the call leaves the cloud.
	MethodRestartService = "restart_service.restart"
)

// RestartServiceRequest is the wire body for MethodRestartService. The
// manager-side BaseTool fills these from the LLM's argsJSON after the
// reviewer worker approved the proposal; the edge re-validates the
// service name against its own sandbox before shelling out.
type RestartServiceRequest struct {
	// Service is the short systemd unit name (e.g. "nginx", "redis").
	// No `.service` suffix; no full path. The edge sandbox declares the
	// allow-list authoritatively — see
	// internal/edgeagent/restart_service/handlers.go::DefaultSandbox.
	Service string `json:"service"`

	// Reason is the operator-supplied justification, copied verbatim
	// into the edge audit log entry. Optional but encouraged so the
	// post-mortem trail records WHY the restart fired, not just WHAT.
	Reason string `json:"reason,omitempty"`
}

// RestartServiceResponse is the wire body returned by the edge.
//
// First version: the edge mocks the actual restart (no real systemctl
// call yet — same posture as host_files PR-8 mocks). Started/Ended
// reflect when the mock pretended to run; Mocked is true to make the
// posture obvious in audit logs and the SPA when we wire UI later.
type RestartServiceResponse struct {
	// Service echoes the requested unit so the LLM/audit row sees the
	// resolved value (and any future canonicalization on the edge).
	Service string `json:"service"`

	// Restarted is true when the (mock or real) restart completed
	// without error; false on any failure — see Error.
	Restarted bool `json:"restarted"`

	// Mocked = true while the edge handler is the PR-7 stub. Once real
	// systemctl shell-out lands this flips to false. Tests assert on
	// this so the audit posture is unambiguous in both modes.
	Mocked bool `json:"mocked"`

	// StartedAt / EndedAt bracket the (mock) restart on the edge clock.
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`

	// Error is the (mock) systemctl failure message, populated only when
	// Restarted=false. Empty on success.
	Error string `json:"error,omitempty"`
}
