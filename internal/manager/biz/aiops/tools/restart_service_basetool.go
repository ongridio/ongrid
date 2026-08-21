package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// restart_service_basetool.go is the manager-side BaseTool that pairs
// with internal/edgeagent/restart_service/handlers.go. It is the FIRST
// mutating-class BaseTool in ongrid (Class="write" —
// blast-radius taxonomy and SOP double-sign), and the proof
// that the ReviewGate decorator (decorators/review_gate.go) intercepts
// mutating tool calls correctly: the LLM emits a restart_service call
// → ReviewGate sees Class=="write" → spawns the reviewer worker
// (agents/reviewer.md) → only on "Decision: approve" does this BaseTool
// dispatch to the edge.
//
// Why Class="write" and not "destructive": "destructive" is reserved
// for irreversible operations (rm, drop table, reboot). A service
// restart is reversible — `systemctl start` brings it back. The
// blast radius is "single device" (matrix). The
// ReviewGate decorator treats both classes the same (intercepts both),
// so the choice is documentation-only here, but it sets a precedent
// for kill_process / drop_silence / other PR-N follow-ups that share
// blast radius.
//
// Why we own a BaseTool counterpart instead of going through
// skill_bridge's generic execute_skill route:
//
//   - skill_bridge wraps every safe-class skill behind one wire
//     method; mutating skills need their own dedicated wire method
//     (MethodRestartService) so the audit log sees a typed call name
//     and the edge handler can re-validate the unit allow-list
//     without parsing a generic params blob.
//   - The reviewer flow needs a stable BaseTool name + Class to gate
//     on. A skill_bridge-wrapped tool would all show up as
//     "execute_skill", which the ReviewGate decorator can't class
//     without parsing args.
//   - Future SPA approval UI will list mutating
//     proposals by tool name; the typed wire makes that listing
//     trivial.
//
// The whitelist of allowed services is duplicated here AND in the edge
// sandbox (defense in depth). When the manager rejects, the LLM gets a
// fast clean error without burning a tunnel round-trip; when the edge
// rejects, the manager's reviewer would have approved a stale config —
// both paths exist on purpose.

// ToolNameRestartService is the stable wire name the LLM sees. Equal
// to the skill key in internal/skill/builtin/restart_service so audit
// logs and catalogs cross-link.
const ToolNameRestartService = "host_restart_service"

// RestartServiceDescription is the one-line "what does this tool do"
// blurb the LLM reads when picking tools.
const RestartServiceDescription = "Restart an allow-listed systemd service on a device. " +
	"MUTATING — calls trigger a reviewer worker for SOP gating before execution."

// restartServiceWhenToUse is the routing hint shown in the system
// prompt under a "When to use" header. kept distinct
// from Description so skill manifests can override one without
// rewriting the other.
//
// The "DO NOT propose restart" rule is the most important bit: a
// runaway agent shouldn't suggest restart-as-a-fix; the user should
// ask first. The reviewer is the second line of defense — explicit
// guidance here is the first.
const restartServiceWhenToUse = "Use ONLY when the user explicitly asks to restart one of the allow-listed " +
	"services (nginx / redis / prometheus / loki / tempo / grafana / mysql / ongrid). " +
	"DO NOT proactively suggest restart — diagnose first with search_logs / get_edge_summary. " +
	"This tool is MUTATING: the call spawns a reviewer worker (SOP gating); " +
	"on reject, do NOT retry — convey the reviewer's reason to the user verbatim and let them decide."

// RestartServiceSchema is the JSON Schema of the tool's argument
// object. The allow-list is enforced both by the schema's `enum` (so
// the LLM gets a structured constraint) AND by an explicit check in
// InvokableRun (so a hijacked tool_call still bounces).
var RestartServiceSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {"type": "integer", "description": "Device id to restart on (same id as the @-mention chip and the Prom device_id label)."},
    "service": {
      "type": "string",
      "enum": ["nginx", "redis", "prometheus", "loki", "tempo", "grafana", "mysql", "ongrid"],
      "description": "Systemd unit short name (no .service suffix)."
    },
    "reason": {"type": "string", "description": "Why the restart is being requested. Written verbatim to the audit row; encouraged for post-mortem trail."}
  },
  "required": ["device_id", "service"]
}`)

// AllowedRestartServices is the canonical allow-list mirrored from
// skills/restart-service/SKILL.md and the edge sandbox. Exported so the
// reviewer / SPA approval UI can render the same list without
// duplicating the literal.
var AllowedRestartServices = []string{
	"nginx", "redis", "prometheus", "loki", "tempo", "grafana", "mysql", "ongrid",
}

// allowedRestartServicesSet is the lookup form, computed once at
// init() so InvokableRun is O(1) per check.
var allowedRestartServicesSet map[string]struct{}

func init() {
	allowedRestartServicesSet = make(map[string]struct{}, len(AllowedRestartServices))
	for _, s := range AllowedRestartServices {
		allowedRestartServicesSet[s] = struct{}{}
	}
}

// restartServiceCallTimeout caps a single tunnel round-trip. The mock
// handler returns instantly; the budget is here for parity with
// host_files (30s) and so the eventual real systemctl shell-out has
// room to breathe.
const restartServiceCallTimeout = 30 * time.Second

// restartServiceArgs is the typed form of RestartServiceSchema.
type restartServiceArgs struct {
	DeviceID uint64 `json:"device_id"`
	Service  string `json:"service"`
	Reason   string `json:"reason"`
}

// restartServiceResultEnvelope wraps the edge response with the
// device_id the call resolved to so the LLM sees the routing confirmed
// in its own input. Mirrors the host_files envelope shape.
type restartServiceResultEnvelope struct {
	DeviceID  uint64    `json:"device_id"`
	Service   string    `json:"service"`
	Restarted bool      `json:"restarted"`
	Mocked    bool      `json:"mocked"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Error     string    `json:"error,omitempty"`
}

// RestartServiceTool is the BaseTool-shape implementation of
// restart_service. Holds dependencies on the struct (
// 改进点 #1) so it can be unit-tested without standing up the registry.
//
// Composition note: this tool does NOT itself spawn the reviewer
// worker. The ReviewGate decorator (decorators/review_gate.go) wraps
// this tool and is responsible for the spawn. RestartServiceTool only
// runs on the **approved** path; if you reach InvokableRun, the
// reviewer has already said yes (or the wrap is misconfigured —
// production wiring in Registry.BuildBaseTools makes the wrap
// mandatory for Class="write"|"destructive" tools).
type RestartServiceTool struct {
	caller   Caller
	resolver hostFilesDeviceResolver // reuse the same resolver interface as host_files
	log      *slog.Logger
}

// NewRestartServiceTool builds a new BaseTool. Pass nil log to default
// to slog.Default(). Same dependency triple shape as host_files.
func NewRestartServiceTool(c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) *RestartServiceTool {
	if log == nil {
		log = slog.Default()
	}
	return &RestartServiceTool{
		caller:   c,
		resolver: deviceResolverAdapter{inner: NewDeviceResolver(d, e)},
		log:      log,
	}
}

// Info returns the tool metadata. Class="write" — the call mutates
// edge state (a service restarts). The ReviewGate decorator switches
// on this value to gate the call behind a reviewer worker.
func (t *RestartServiceTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameRestartService,
		Description: RestartServiceDescription,
		WhenToUse:   restartServiceWhenToUse,
		Parameters:  RestartServiceSchema,
		Class:       "write",
	}, nil
}

// InvokableRun parses argsJSON, validates the unit name against the
// allow-list, resolves device_id → edge_id, dispatches the tunnel RPC,
// and re-emits the response wrapped in restartServiceResultEnvelope.
//
// IMPORTANT: by the time we reach this method the ReviewGate decorator
// has already approved the call. Production wiring must wrap this tool
// in ReviewGate (chain.go's Wrap does so by default). A test that
// invokes this method directly bypasses the review — fine for unit
// tests, must NOT be the production path.
func (t *RestartServiceTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameRestartService)
	}
	var in restartServiceArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameRestartService, err)
	}
	if in.DeviceID == 0 {
		return "", fmt.Errorf("%s: device_id required", ToolNameRestartService)
	}
	canonical := strings.TrimSpace(strings.ToLower(in.Service))
	canonical = strings.TrimSuffix(canonical, ".service")
	if canonical == "" {
		return "", fmt.Errorf("%s: service required", ToolNameRestartService)
	}
	if _, ok := allowedRestartServicesSet[canonical]; !ok {
		return "", fmt.Errorf("%s: service %q not in allow-list (%s)",
			ToolNameRestartService, in.Service, strings.Join(AllowedRestartServices, " "))
	}

	edgeID, err := t.resolver.LookupHostEdge(ctx, in.DeviceID)
	if err != nil {
		return "", fmt.Errorf("%s: resolve device %d: %w", ToolNameRestartService, in.DeviceID, err)
	}
	if edgeID == 0 {
		return "", fmt.Errorf("%s: device_id=%d has no host-edge link (try query_devices to list available device ids)",
			ToolNameRestartService, in.DeviceID)
	}

	req := tunnel.RestartServiceRequest{
		Service: canonical,
		Reason:  in.Reason,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: marshal req: %w", ToolNameRestartService, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, restartServiceCallTimeout)
	defer cancel()
	respBody, err := t.caller.Call(callCtx, edgeID, tunnel.MethodRestartService, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameRestartService, err)
	}
	var resp tunnel.RestartServiceResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode resp: %w", ToolNameRestartService, err)
	}
	out, err := json.Marshal(restartServiceResultEnvelope{
		DeviceID:  in.DeviceID,
		Service:   resp.Service,
		Restarted: resp.Restarted,
		Mocked:    resp.Mocked,
		StartedAt: resp.StartedAt,
		EndedAt:   resp.EndedAt,
		Error:     resp.Error,
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameRestartService, err)
	}
	return string(out), nil
}

// AppendRestartServiceTool registers the restart_service BaseTool onto
// the provided slice when the dependency triple is wired. Returns the
// slice unchanged when any dep is nil (graceful degradation — a
// deployment without the tunnel can't restart anything anyway).
//
// The caller is responsible for wrapping the returned tool in the
// decorator chain (chain.go's Wrap), which automatically applies the
// ReviewGate decorator when Class="write"|"destructive".
func AppendRestartServiceTool(out []basetool.BaseTool, c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) []basetool.BaseTool {
	if c == nil || e == nil || d == nil {
		return out
	}
	if log == nil {
		log = slog.Default()
	}
	return append(out, NewRestartServiceTool(c, e, d, log))
}
