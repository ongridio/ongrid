// Package basetool defines ongrid's eino-aligned tool surface.
//
// (Tool 层) lays out the migration target: each tool becomes
// an object implementing tool.BaseTool — `Info(ctx) -> ToolInfo` plus
// `InvokableRun(ctx, argsJSON) -> string` — and the standard decorator
// chain (tenant_bind / timeout / audit / ratelimit / metric,
// ASCII diagram and 主参考图 Tool 执行后端区块) wraps each tool
// uniformly. This package owns the interface + value types so the
// decorator package and individual tool impls can depend on it without
// reaching into the closure-style Tool/Registry from registry.go.
//
// Why a local interface that mirrors cloudwego/eino:
//
//   - PR-1 of owns the cloudwego/eino dep. PR-3 (this PR) MAY
//     merge first, so we cannot import eino yet.
//   - Field/method names match eino's tool.BaseTool + InvokableTool so
//     the later "swap to eino" PR is a one-line type alias change in
//     this file. Any callsite that consumes basetool.BaseTool keeps
//     compiling.
//
// The closure-style Tool from registry.go stays — both paths coexist
// for this PR (改进点 #5: 可单测，单文件可测).
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
}

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
		Tenant:   c.Tenant,
		UserID:   c.UserID,
		DeviceID: c.DeviceID,
		UserText: c.UserText,
	}
}

// Resolved is the public, exported form of invokeConfig — the value
// returned by ResolveOptions. Decorators read fields directly.
// — the resolved per-call context.
type Resolved struct {
	Tenant   string
	UserID   uint64
	DeviceID *uint64
	UserText string
}
