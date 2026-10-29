package externalmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/stretchr/testify/require"
)

type catalogTool struct {
	info  basetool.ToolInfo
	calls int
}

func (t *catalogTool) Info(context.Context) (*basetool.ToolInfo, error) { return &t.info, nil }
func (t *catalogTool) InvokableRun(context.Context, string, ...basetool.InvokeOption) (string, error) {
	t.calls++
	return `{"sent":true}`, nil
}

func TestBuiltinActionsRequireScopesAndDryRunDoesNotSend(t *testing.T) {
	base := &catalogTool{info: basetool.ToolInfo{Name: "send_notification", Class: "write", Parameters: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string"},"text":{"type":"string"}},"required":["channel","text"]}`)}}
	list, err := BuiltinWriteTools([]basetool.BaseTool{base})
	require.NoError(t, err)
	catalog, err := biz.NewCatalog(list)
	require.NoError(t, err)
	tool := catalog["send_notification"]
	args, err := tool.Parse([]byte(`{"channel":"fixture","text":"private text","reason":"requested","dry_run":true,"idempotency_key":"fixture-key-01"}`))
	require.NoError(t, err)
	require.False(t, tool.Visible(integrationauth.Identity{Scopes: []string{"notifications:read"}}))
	require.True(t, tool.Visible(integrationauth.Identity{Scopes: []string{"notifications:read", "notifications:execute"}}))
	policy, err := tool.Policy(args)
	require.NoError(t, err)
	require.True(t, policy.Remote)
	require.True(t, tool.Sensitive)
	data, err := tool.Run(context.Background(), args)
	require.NoError(t, err)
	require.Zero(t, base.calls)
	require.Equal(t, true, data.(map[string]any)["approval_required"])
	base.info.Origin = "mcp"
	_, err = BuiltinWriteTools([]basetool.BaseTool{base})
	require.Error(t, err)
}

func TestQueryAdapterDoesNotShadowResourceContractsOrExportDynamicTools(t *testing.T) {
	resource := &catalogTool{info: basetool.ToolInfo{Name: "get_incident_detail", Class: "read"}}
	list, err := QueryTools([]basetool.BaseTool{resource})
	require.NoError(t, err)
	require.Empty(t, list, "revision-aware incident detail has its own contract")
	query := &catalogTool{info: basetool.ToolInfo{Name: "query_knowledge", Class: "read", Origin: "mcp"}}
	_, err = QueryTools([]basetool.BaseTool{query})
	require.Error(t, err)
}

func TestReviewArgumentsRedactsCredentialsRecursively(t *testing.T) {
	body := map[string]any{"name": "reviewable change", "enabled": true, "fields": map[string]any{"arbitrary": "private-fixture"}, "value": "private-fixture", "config": "private-fixture"}
	for _, key := range []string{"api_key", "apiKey", "api-key", "password", "passwd", "token", "accessToken", "refresh_token", "dsn", "credential", "privateKey", "private_key", "clientSecret", "Authorization", "Cookie", "accessKey", "header_template_json"} {
		body[key] = "private-fixture"
	}
	body["nested"] = []any{map[string]any{"api_key": "private-fixture", "text": "Authorization: Bearer private-fixture", "url": "https://user:private-fixture@example.test/path", "pem": "-----BEGIN PRIVATE KEY-----\nprivate-fixture\n-----END PRIVATE KEY-----"}}
	data := ReviewArguments(biz.Arguments{"action": "post_flows", "body": body})
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-fixture")
	require.Contains(t, string(raw), "reviewable change")
	require.Contains(t, string(raw), `"enabled":true`)
	require.Equal(t, "private-fixture", body["password"], "redaction must not alter execution arguments")
}
