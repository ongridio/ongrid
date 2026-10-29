// Package integrationauth defines the request identity shared by IAM and
// external integrations. It carries no credential secret.
package integrationauth

import (
	"context"
	"slices"

	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const Audience = "external-mcp"

type Identity struct {
	TokenID string
	Owner   tenantctx.Tenant
	Scopes  []string
}

func (i Identity) Allows(scopes ...string) bool {
	for _, scope := range scopes {
		if !slices.Contains(i.Scopes, scope) {
			return false
		}
	}
	return true
}

// Scopes is returned as a new slice so callers cannot mutate the contract.
func Scopes() []string {
	return []string{
		"agent-traces:read",
		"agents:delete",
		"agents:execute",
		"agents:read",
		"agents:write",
		"alert-rules:delete",
		"alert-rules:read",
		"alert-rules:write",
		"alerts:execute",
		"alerts:read",
		"alerts:write",
		"apm:delete",
		"apm:read",
		"apm:write",
		"approvals:read",
		"audit:read",
		"credentials:delete",
		"credentials:read",
		"credentials:write",
		"databases:read",
		"devices:delete",
		"devices:read",
		"devices:write",
		"edges:delete",
		"edges:execute",
		"edges:read",
		"edges:write",
		"flows:delete",
		"flows:execute",
		"flows:read",
		"flows:write",
		"host-files:read",
		"host:execute",
		"iam:delete",
		"iam:read",
		"iam:write",
		"im:delete",
		"im:execute",
		"im:read",
		"im:write",
		"integrations:execute",
		"integrations:read",
		"integrations:write",
		"k8s:delete",
		"k8s:execute",
		"k8s:read",
		"k8s:write",
		"knowledge:delete",
		"knowledge:execute",
		"knowledge:read",
		"knowledge:write",
		"logs:execute",
		"logs:read",
		"logs:write",
		"marketplace:delete",
		"marketplace:read",
		"marketplace:write",
		"mcp-servers:delete",
		"mcp-servers:execute",
		"mcp-servers:read",
		"mcp-servers:write",
		"metrics:read",
		"monitor:delete",
		"monitor:read",
		"monitor:write",
		"network:execute",
		"network:read",
		"network:write",
		"notifications:delete",
		"notifications:execute",
		"notifications:read",
		"notifications:write",
		"operator-runs:execute",
		"operator-runs:read",
		"packet-captures:execute",
		"packet-captures:read",
		"pages:delete",
		"pages:read",
		"pages:write",
		"profiles:read",
		"reports:delete",
		"reports:execute",
		"reports:read",
		"reports:write",
		"settings:delete",
		"settings:execute",
		"settings:read",
		"settings:write",
		"skills:execute",
		"skills:read",
		"system:read",
		"topology:delete",
		"topology:read",
		"topology:write",
		"traces:read",
		"webshell:delete",
		"webshell:read",
	}
}

type contextKey struct{}

func With(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(tenantctx.With(ctx, identity.Owner), contextKey{}, identity)
}

func From(ctx context.Context) (Identity, bool) {
	i, ok := ctx.Value(contextKey{}).(Identity)
	return i, ok
}
