package externalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/stretchr/testify/require"
)

func apiFixture(t *testing.T, router chi.Router, writes bool) map[string]*biz.Tool {
	t.Helper()
	t.Setenv("ONGRID_SECRET_KEY", "test-only-external-api-encryption-key")
	tools, err := APITools(router, writes)
	require.NoError(t, err)
	catalog, err := biz.NewCatalog(tools)
	require.NoError(t, err)
	return catalog
}

func TestAPIReusesIdentityAndFiltersDiscovery(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/v1/apm/services", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := tenantctx.From(r.Context())
		require.True(t, ok)
		require.EqualValues(t, 42, owner.UserID)
		require.Empty(t, r.Header.Get("Authorization"))
		require.Equal(t, "50", r.URL.Query().Get("limit"))
		_, _ = w.Write([]byte(`{"items":[{"name":"api","credential":"do-not-return","private_key":"do-not-return"}]}`))
	})
	r.Get("/v1/users", func(http.ResponseWriter, *http.Request) { t.Fatal("unscoped API called") })
	r.Post("/v1/flows", func(http.ResponseWriter, *http.Request) { t.Fatal("write called") })
	catalog := apiFixture(t, r, true)
	i := integrationauth.Identity{Owner: tenantctx.Tenant{UserID: 42, Role: "admin"}, Scopes: []string{"apm:read"}}
	ctx := integrationauth.With(context.Background(), i)
	data, err := catalog["list_api_operations"].Run(ctx, biz.Arguments{})
	require.NoError(t, err)
	require.Equal(t, 1, data.(map[string]any)["total"])
	require.NotContains(t, string(catalog["call_api_read"].InputSchema(i)), "get_users")
	require.False(t, catalog["call_api_write"].Visible(i))
	args, err := catalog["call_api_read"].Parse([]byte(`{"action":"get_apm_services"}`))
	require.NoError(t, err)
	require.True(t, catalog["call_api_read"].Authorize(i, args))
	data, err = catalog["call_api_read"].Run(ctx, args)
	require.NoError(t, err)
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"name":"api"`)
	require.NotContains(t, string(encoded), "do-not-return")
	args["action"] = "get_users"
	require.False(t, catalog["call_api_read"].Authorize(i, args))
	_, err = catalog["call_api_read"].Run(ctx, args)
	require.ErrorIs(t, err, errs.ErrForbidden)
	readOnly := apiFixture(t, r, false)
	require.NotContains(t, readOnly, "call_api_write")
	require.NotContains(t, readOnly["describe_api_operation"].ActionScopes, "post_flows")
}

func TestAPIRejectsPathsHeadersUnknownArgumentsAndOversizedResponses(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/v1/knowledge/docs/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + chi.URLParam(r, "id") + `"}`))
	})
	r.Get("/v1/knowledge/docs/private", func(http.ResponseWriter, *http.Request) { t.Fatal("static route bypass") })
	r.Get("/v1/users", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 129*1024))) })
	catalog := apiFixture(t, r, true)
	tool := catalog["call_api_read"]
	for _, value := range []string{"../secrets", "%2e%2e", "x/y", "x?key=y", "x#fragment", "..", "x\\y"} {
		raw, err := json.Marshal(map[string]any{"action": "get_knowledge_docs_id", "path": map[string]string{"id": value}})
		require.NoError(t, err)
		_, err = tool.Parse(raw)
		require.Error(t, err, value)
	}
	for _, raw := range []string{`{"action":"get_users","headers":{"Authorization":"x"}}`, `{"action":"get_users","url":"https://example.test"}`, `{"action":"get_users","query":{"not_a_parameter":"x"}}`, `{"action":"get_knowledge_docs_id","path":{"id":"1","other":"x"}}`} {
		_, err := tool.Parse([]byte(raw))
		require.Error(t, err, raw)
	}
	i := integrationauth.Identity{Owner: tenantctx.Tenant{UserID: 42, Role: "admin"}, Scopes: []string{"knowledge:read", "iam:read"}}
	ctx := integrationauth.With(context.Background(), i)
	args, err := tool.Parse([]byte(`{"action":"get_knowledge_docs_id","path":{"id":"private"}}`))
	require.NoError(t, err)
	_, err = tool.Run(ctx, args)
	require.ErrorIs(t, err, errs.ErrForbidden)
	args, err = tool.Parse([]byte(`{"action":"get_knowledge_docs_id","path":{"id":"123"}}`))
	require.NoError(t, err)
	data, err := tool.Run(ctx, args)
	require.NoError(t, err)
	require.Equal(t, "123", data.(map[string]any)["result"].(map[string]any)["id"])
	_, err = tool.Run(ctx, biz.Arguments{"action": "get_users"})
	require.ErrorIs(t, err, biz.ErrResultTooLarge)
}

func TestAPIWritePreviewNeverInvokesHandlerAndDoesNotExposeBody(t *testing.T) {
	r := chi.NewRouter()
	called := false
	r.Post("/v1/flows", func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(400) })
	catalog := apiFixture(t, r, true)
	tool := catalog["call_api_write"]
	args, err := tool.Parse([]byte(`{"action":"post_flows","body":{"name":"fixture","graph":{"credential":"never-in-approval"}},"idempotency_key":"test-key-001","reason":"test","dry_run":true}`))
	require.NoError(t, err)
	i := integrationauth.Identity{Owner: tenantctx.Tenant{UserID: 42, Role: "admin"}, Scopes: []string{"flows:read", "flows:write"}}
	ctx := integrationauth.With(context.Background(), i)
	policy, err := tool.Policy(args)
	require.NoError(t, err)
	require.True(t, policy.Remote)
	require.True(t, tool.Sensitive)
	data, err := tool.Run(ctx, args)
	require.NoError(t, err)
	require.False(t, called)
	require.Equal(t, true, data.(map[string]any)["approval_required"])
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "never-in-approval")
	args["dry_run"] = false
	_, err = tool.Run(ctx, args)
	require.Error(t, err)
	require.NotErrorIs(t, err, errs.ErrInvalid, "failed writes may have side effects and must become unknown")
	require.True(t, called)
}

func TestAPICatalogAccountsForEveryStaticRoute(t *testing.T) {
	var ops []apiOperation
	require.NoError(t, json.Unmarshal(apiCatalogJSON, &ops))
	excludedJSON, err := os.ReadFile("api_exclusions.json")
	require.NoError(t, err)
	var excluded []struct{ Method, Path, Reason string }
	require.NoError(t, json.Unmarshal(excludedJSON, &excluded))
	accounted := map[string]bool{}
	router := chi.NewRouter()
	for _, op := range ops {
		key := op.Method + " " + op.Path
		require.False(t, accounted[key], key)
		accounted[key] = true
		require.Contains(t, integrationauth.Scopes(), op.Scope)
		for _, forbidden := range []string{"/auth/", "/iam/mcp-tokens", "/approve", "/reject", "/reveal", "/internal/", "/rotate-secret", "/rotate-token", "/share"} {
			require.NotContains(t, op.Path, forbidden)
		}
		router.MethodFunc(op.Method, op.Path, func(http.ResponseWriter, *http.Request) {})
	}
	for _, entry := range excluded {
		key := entry.Method + " " + entry.Path
		require.False(t, accounted[key], key)
		require.NotEmpty(t, entry.Reason)
		accounted[key] = true
	}
	for _, bc := range []string{"iam", "manager"} {
		base := filepath.Join("../../../..", "internal", bc, "server")
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !slices.Contains([]string{"Get", "Post", "Put", "Patch", "Delete", "Handle"}, selector.Sel.Name) {
					return true
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				route, _ := strconv.Unquote(literal.Value)
				if strings.HasPrefix(route, "/") {
					key := strings.ToUpper(selector.Sel.Name) + " " + route
					require.True(t, accounted[key], "classify new route %s (%s)", key, path)
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	catalog := apiFixture(t, router, true)
	require.Len(t, catalog, 4)
	for _, op := range ops {
		t.Run(op.ID, func(t *testing.T) {
			tool := catalog["call_api_read"]
			if op.mutation() {
				tool = catalog["call_api_write"]
			}
			require.Contains(t, tool.ActionScopes, op.ID)
		})
	}
	t.Log(fmt.Sprintf("%d API operations and %d explicit exclusions", len(ops), len(excluded)))
}

func TestAPIRiskPolicyKeepsDestructiveAndMixedChangesGated(t *testing.T) {
	var ops []apiOperation
	require.NoError(t, json.Unmarshal(apiCatalogJSON, &ops))
	byID := map[string]apiOperation{}
	for _, op := range ops {
		byID[op.ID] = op
		if op.Method == "DELETE" || strings.HasSuffix(op.Scope, ":execute") {
			require.False(t, op.lowRisk(biz.Arguments{}), op.ID)
		}
	}
	for _, tc := range []struct {
		id   string
		body map[string]any
		low  bool
	}{
		{"post_topology_nodes", nil, true},
		{"post_topology_relations", nil, true},
		{"post_topology_node_types", nil, false},
		{"post_topology_relation_types", nil, false},
		{"post_chat_sessions", nil, true},
		{"patch_topology_nodes_id", map[string]any{"name": "new"}, true},
		{"patch_topology_nodes_id", map[string]any{"name": "new", "props": map[string]any{"environment": "prod"}}, false},
		{"patch_chat_sessions_id", map[string]any{"title": "new"}, true},
		{"patch_chat_sessions_id", map[string]any{"title": "new", "provider": "other"}, false},
		{"post_flows", map[string]any{"name": "new"}, false},
		{"patch_knowledge_docs_id", map[string]any{"title": "new"}, false},
		{"post_edge_upgrade_jobs", nil, false},
		{"patch_users_id_role", map[string]any{"role": "admin"}, false},
		{"post_mcp_servers", nil, false},
	} {
		t.Run(tc.id+fmt.Sprint(tc.body), func(t *testing.T) {
			op, ok := byID[tc.id]
			require.True(t, ok)
			require.Equal(t, tc.low, op.lowRisk(biz.Arguments{"body": tc.body}))
		})
	}
	require.False(t, (apiOperation{ID: "future_unreviewed_create"}).lowRisk(biz.Arguments{}))
	r := chi.NewRouter()
	r.Post("/v1/topology/nodes", func(http.ResponseWriter, *http.Request) { t.Fatal("preview executed") })
	r.Patch("/v1/topology/nodes/{id}", func(http.ResponseWriter, *http.Request) { t.Fatal("preview executed") })
	catalog := apiFixture(t, r, true)
	tool := catalog["call_api_write"]
	args, err := tool.Parse([]byte(`{"action":"patch_topology_nodes_id","path":{"id":"1"},"body":{"name":"new"},"idempotency_key":"risk-preview-001","reason":"rename","dry_run":true}`))
	require.NoError(t, err)
	identity := integrationauth.Identity{Owner: tenantctx.Tenant{UserID: 42, Role: "admin"}, Scopes: []string{"topology:read", "topology:write"}}
	result, err := tool.Run(integrationauth.With(context.Background(), identity), args)
	require.NoError(t, err)
	require.Equal(t, false, result.(map[string]any)["approval_required"])
	policy, err := tool.Policy(args)
	require.NoError(t, err)
	require.True(t, policy.LowRisk)
}
