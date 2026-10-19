// Package basetool defines the shared tool interface and value types used by
// AIOps tools, runtime adapters and decorators.
package basetool

import (
	"context"
	"encoding/json"
)

// BaseTool is ongrid's eino-aligned tool surface. The signature mirrors
// cloudwego/eino's `tool.BaseTool` + `tool.InvokableTool` (combined here
// for ergonomic reasons; eino splits them so a tool can advertise its
// schema without being executable, but every ongrid tool is invokable so
// we collapse). When PR-N of swaps in cloudwego/eino, this
// interface becomes a thin alias; callsites do not change.
//
// 改进点 #2: 装饰器自由组合 — every decorator returns a
// BaseTool, so chains compose without leaking concrete types.
type BaseTool interface {
	// Info returns the tool's metadata: name, description, when-to-use,
	// JSON Schema of the argument object, and read/write classification.
	// Pure (read-only); MUST NOT touch external systems.
	Info(ctx context.Context) (*ToolInfo, error)

	// InvokableRun executes the tool. argsJSON is the raw JSON object
	// produced by the LLM as tool_call.arguments; the tool parses it
	// per its own schema. The return value is a JSON string fed back
	// to the LLM as a role=tool message. Errors propagate up to the
	// agent loop which classifies them into chat_tool_calls.status
	// (success / error / timeout).
	//
	// opts carry per-call context not modeled in argsJSON (tenant id,
	// user id, optional device id) — see InvokeOption / WithTenant /
	// WithUserID / WithDeviceID below.
	InvokableRun(ctx context.Context, argsJSON string, opts ...InvokeOption) (string, error)
}

// ToolInfo is the metadata block returned by BaseTool.Info. Mirrors
// eino's `schema.ToolInfo` with one extension: WhenToUse, separated
// from Description ("whenToUse 跟 description 分离")
// and — Description is the LLM-visible blurb summarising what the
// tool does; WhenToUse is the routing hint summarising when to pick it
// over its siblings. Decorators that mutate args (e.g. tenant_bind)
// inspect Parameters to decide whether to inject.
type ToolInfo struct {
	// Name is the stable wire name the LLM sees. Must match the tool
	// name in tool_call.function.name. Snake_case, no spaces.
	Name string

	// Description is the one-sentence "what does this tool do" blurb.
	// Phrased so the LLM can decide whether the user's question matches.
	Description string

	// WhenToUse is the disambiguation hint among sibling tools. ★
	// kept separate from Description so that the system
	// prompt can render it in a different position (e.g. behind a
	// "When to use" header) and skill manifests can override it without
	// rewriting the description.
	WhenToUse string

	// Parameters is the JSON Schema of the argument object. Decorators
	// (tenant_bind) may parse this to decide whether to inject fields
	// like tenant_id; tool implementations parse it implicitly via
	// json.Unmarshal into a typed args struct.
	Parameters json.RawMessage

	// Class records the tool's effect on the world. Used by the agent
	// loop / future SOP gating to decide whether double-sign is required.
	//   - "read" — pure read; default for query_* tools
	//   - "write" — mutates ongrid state (e.g. silence an alert)
	//   - "destructive" — mutates external state (e.g. restart a service)
	Class string

	// Confirmation controls the shared human-confirmation gate.
	// Empty uses the class default (write/destructive require approval),
	// "required" also gates read-class sensitive actions, "not-required"
	// explicitly exempts benign internal writes, and
	// "self-managed" is reserved for tools already backed by the same durable
	// approval protocol internally (for example conditional host_bash writes).
	Confirmation string

	// Origin records where the tool came from, so policy can treat
	// runtime-discovered tools differently from compiled-in builtins
	// WITHOUT string-matching wire names (which doesn't scale as dynamic
	// sources — MCP servers, installed skills, extensions — grow). Empty
	// ("") = builtin, compiled in. Runtime sources set it explicitly
	// (OriginMCP / OriginSkill). The agent loop uses it to keep dynamic
	// tools out of the coordinator's hands — it delegates them to a
	// specialist instead of wielding a sprawling, ever-growing set itself.
	Origin string
}

const (
	ConfirmationRequired    = "required"
	ConfirmationNotRequired = "not-required"
	ConfirmationSelfManaged = "self-managed"
)

// Tool origin codes. Empty (OriginBuiltin) is the default for compiled-in
// BaseTools; every runtime-discovered source stamps its own code.
const (
	OriginBuiltin = ""      // compiled-in BaseTool
	OriginMCP     = "mcp"   // exposed by a registered MCP server (HLD-018)
	OriginSkill   = "skill" // contributed by an installed skill / extension
)

// IsDynamic reports whether the tool was discovered at runtime (MCP, skill,
// …) rather than compiled in. Dynamic tools can't be pre-listed in a persona's
// static whitelist, so callers route them to specialists rather than the
// coordinator. New dynamic sources are covered automatically by stamping a
// non-empty Origin — no name-prefix table to maintain.
func (i ToolInfo) IsDynamic() bool { return i.Origin != OriginBuiltin }

// InvokeOption is a functional option threaded through InvokableRun.
// Decorators set these before calling the inner tool; tools may read
// them via the unexported invokeConfig — but tools that don't care
// simply ignore the options (matching eino's conventions).
type InvokeOption func(*invokeConfig)

// invokeConfig is the resolved per-call context populated by the
// InvokeOption setters. Unexported because tools should read it via
// helper accessors (or, for now, only the decorator chain reads it).
type invokeConfig struct {
	// Tenant is the caller's tenant identifier. Sourced from
	// internal/pkg/tenantctx.Tenant. Used by the audit decorator to
	// scope chat_tool_calls rows and (later) by the per-tenant skill
	// override layer.
	Tenant string

	// UserID is the authenticated caller's user id. Used by the
	// ratelimit decorator (per-user-per-tool limiter) and the audit
	// decorator (chat_tool_calls.user_id when we add that column).
	UserID uint64

	// DeviceID is set on edge-scope tools so the chat_tool_calls audit
	// row can record the device the call hit. Pointer so "no device"
	// (cluster-wide tools like query_promql) is distinguishable from
	// "device 0".
	DeviceID *uint64

	// UserText is the current end-user turn. Tools use this only for
	// validation/normalization that must be grounded in the user's actual
	// request instead of the model's reconstructed arguments.
	UserText string

	// HostWriteAllowed is the per-request admin execution gate. It is carried
	// as an explicit invoke option because nested Eino ToolsNode contexts do
	// not reliably preserve manager context values.
	HostWriteAllowed bool

	// ConfirmedDeviceIDs are explicit @device selections from the current
	// user turn. They are execution evidence, unlike a device discovered by
	// a preceding tool call or inferred by the model.
	ConfirmedDeviceIDs []uint64
	HumanApproved      bool
}

// WithTenant sets the tenant identifier on the invoke config.
// — feeds the tenant_bind decorator.
func WithTenant(tenant string) InvokeOption {
	return func(c *invokeConfig) { c.Tenant = tenant }
}

// WithUserID sets the user id on the invoke config.
// — feeds the ratelimit + audit decorators.
func WithUserID(uid uint64) InvokeOption {
	return func(c *invokeConfig) { c.UserID = uid }
}

// WithDeviceID sets the optional device id on the invoke config.
// — feeds the audit decorator (chat_tool_calls.device_id).
// Pass nil to leave unset.
func WithDeviceID(deviceID *uint64) InvokeOption {
	return func(c *invokeConfig) { c.DeviceID = deviceID }
}

// WithUserText sets the current end-user turn on the invoke config.
func WithUserText(text string) InvokeOption {
	return func(c *invokeConfig) { c.UserText = text }
}

// WithConfirmedDeviceIDs carries structured UI selections into tools that
// require an unambiguous host target.
func WithConfirmedDeviceIDs(ids []uint64) InvokeOption {
	return func(c *invokeConfig) { c.ConfirmedDeviceIDs = append([]uint64(nil), ids...) }
}

// WithHostWritePermission carries the resolved admin write gate to host-side
// tools. When enabled, host_bash may turn a mutating command into an approval
// proposal; it never changes how a command is dispatched. Commands that do not
// reach the approval flow still go through the edge's read-only cmdpolicy path.
func WithHostWritePermission(allowed bool) InvokeOption {
	return func(c *invokeConfig) { c.HostWriteAllowed = allowed }
}

// WithHumanApproval is an internal execution capability stamped only by the
// shared approval gate after the durable proposal has been approved.
func WithHumanApproval(approved bool) InvokeOption {
	return func(c *invokeConfig) { c.HumanApproved = approved }
}

// ResolveOptions applies opts to a fresh invokeConfig and returns it.
// Exposed for the decorator package — tool implementations don't need
// it (they receive the resolved values via decorators or skip them).
//
// This indirection (rather than inlining `for _, o := range opts`
// inside every decorator) keeps the unexported invokeConfig
// truly unexported while still letting cross-package decorators read
// the resolved values.
func ResolveOptions(opts []InvokeOption) Resolved {
	c := invokeConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	return Resolved{
		Tenant:             c.Tenant,
		UserID:             c.UserID,
		DeviceID:           c.DeviceID,
		UserText:           c.UserText,
		HostWriteAllowed:   c.HostWriteAllowed,
		ConfirmedDeviceIDs: append([]uint64(nil), c.ConfirmedDeviceIDs...),
		HumanApproved:      c.HumanApproved,
	}
}

// Resolved is the public, exported form of invokeConfig — the value
// returned by ResolveOptions. Decorators read fields directly.
// — the resolved per-call context.
type Resolved struct {
	Tenant             string
	UserID             uint64
	DeviceID           *uint64
	UserText           string
	HostWriteAllowed   bool
	ConfirmedDeviceIDs []uint64
	HumanApproved      bool
}
