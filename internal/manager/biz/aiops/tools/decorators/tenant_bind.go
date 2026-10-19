package decorators

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// TenantBoundTool wraps inner so the tenant identity from ctx (via
// internal/pkg/tenantctx) is injected into the args JSON when the tool's
// declared schema asks for `tenant_id`. ASCII —
// TenantBoundTool (从 ctx 注入 tenant_id 进 args).
//
// Why we mutate args rather than only setting an InvokeOption: tools
// authored as plain JSON-Schema-driven structs already declare a
// tenant_id property (— tools should self-describe);
// rewriting args makes tool_call.arguments self-contained for replay/audit.
//
// The tenantctx.Tenant.UserID is also threaded onto the InvokeOption
// via WithUserID + WithTenant so the audit + ratelimit decorators can
// see it without parsing args themselves.
type TenantBoundTool struct {
	inner basetool.BaseTool
}

// WithTenantBind returns inner wrapped so InvokableRun:
//
//  1. Reads tenantctx.Tenant from ctx (no-op if absent — matches public
//     endpoints / unit tests with bare ctx).
//  2. Appends WithUserID(tenant.UserID) + WithTenant(strconv UserID) to
//     opts so downstream decorators see the resolved identity.
//  3. If the tool's schema declares a tenant_id property AND the
//     incoming argsJSON does not set it, injects tenant_id=UserID into
//     the JSON object before calling inner. Tools that don't declare
//     tenant_id receive args unchanged.
func WithTenantBind(inner basetool.BaseTool) basetool.BaseTool {
	return &TenantBoundTool{inner: inner}
}

// Info passes through — tenant binding is invocation-only.
func (t *TenantBoundTool) Info(ctx context.Context) (*basetool.ToolInfo, error) {
	return t.inner.Info(ctx)
}

// InvokableRun does the tenant resolution + args injection.
func (t *TenantBoundTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	tenant, ok := tenantctx.From(ctx)
	if !ok {
		// No tenant on ctx — pass through unchanged. This is the
		// canonical "public endpoint / test" path; rejecting here
		// would force every test to set up tenantctx.With().
		return t.inner.InvokableRun(ctx, argsJSON, opts...)
	}

	// Decorate opts with the resolved tenant + user id so audit /
	// ratelimit don't have to re-derive them.
	uidStr := strconv.FormatUint(tenant.UserID, 10)
	opts = append(opts,
		basetool.WithTenant(uidStr),
		basetool.WithUserID(tenant.UserID),
	)

	// Decide whether the tool wants tenant_id in its args. Cheap path:
	// inspect the schema. If the schema doesn't declare it, skip
	// injection — keeps this decorator safe to apply to every tool.
	info, err := t.inner.Info(ctx)
	if err != nil || info == nil || len(info.Parameters) == 0 {
		return t.inner.InvokableRun(ctx, argsJSON, opts...)
	}
	if !schemaHasTenantID(info.Parameters) {
		return t.inner.InvokableRun(ctx, argsJSON, opts...)
	}

	// Inject tenant_id only when the model didn't already supply one.
	merged, err := injectTenantID(argsJSON, tenant.UserID)
	if err != nil {
		return "", fmt.Errorf("tenant_bind: rewrite args: %w", err)
	}
	return t.inner.InvokableRun(ctx, merged, opts...)
}

// schemaHasTenantID returns true when the JSON Schema declares a
// `tenant_id` property at the top level. We deliberately keep this
// shallow — nested schemas aren't a use case in PR-3 (one tool only).
func schemaHasTenantID(schema json.RawMessage) bool {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return false
	}
	_, ok := s.Properties["tenant_id"]
	return ok
}

// injectTenantID adds tenant_id=uid to the JSON object in argsJSON when
// not already present. Returns the rewritten JSON. Empty / nil input
// becomes a fresh object {"tenant_id": uid}.
func injectTenantID(argsJSON string, uid uint64) (string, error) {
	if argsJSON == "" {
		argsJSON = "{}"
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return "", err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	if _, exists := m["tenant_id"]; exists {
		return argsJSON, nil
	}
	enc, err := json.Marshal(uid)
	if err != nil {
		return "", err
	}
	m["tenant_id"] = enc
	out, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
