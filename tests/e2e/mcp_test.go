//go:build e2e

// Catalog: HLD-018 MCP client — config CRUD + live test-connection, over HTTP
// on a clean manager, against an IN-PROCESS mock MCP server (JSON-RPC 2.0 over
// HTTP). Covers:
//
//	POST/GET/PUT/DELETE /api/v1/mcp/servers
//	POST /api/v1/mcp/servers/{id}/test  (connects → initialize → tools/list)
//	credential injection: header_template_json {{field}} is filled from the
//	bound vault credential and actually reaches the MCP server's HTTP headers.
//
// Runtime hot registration, discovery/execution consistency and background
// refresh are covered in dynamic_capabilities_test.go. This file protects
// CRUD, protocol round-trips and credential-to-header injection.
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

// mockMCP is a minimal MCP server: it records the Authorization header it saw
// (to prove credential injection) and answers initialize + tools/list.
func mockMCP(authSeen *string, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*authSeen = r.Header.Get("Authorization")
		mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &req)
		switch req.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "initialize":
			mcpWriteRPC(w, req.ID, map[string]any{"protocolVersion": "2024-11-05"})
		case "tools/list":
			mcpWriteRPC(w, req.ID, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description":  "echo back the input",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}},
			}}})
		case "tools/call":
			// Fixed marker so a flow-node test can assert the round-trip.
			mcpWriteRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "echo-ok-T3"}}})
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
}

func mcpWriteRPC(w http.ResponseWriter, id int, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func TestMCP_CRUDTestConnectionAndCredentialInjection_HLD018(t *testing.T) {
	var authSeen string
	var mu sync.Mutex
	srv := mockMCP(&authSeen, &mu)
	defer srv.Close()

	env := testenv.Start(t)
	tok := env.LoginAdmin().AccessToken

	// ─── auth gate ────────────────────────────────────────────────────────
	if status, _, err := env.DoJSON("GET", "/api/v1/mcp/servers", nil, ""); err != nil || status != 401 {
		t.Fatalf("GET /mcp/servers no token: status=%d err=%v want 401", status, err)
	}

	// ─── a credential whose 'token' field will fill the auth header ───────
	if status, _, err := env.DoJSON("POST", "/api/v1/secrets", map[string]any{
		"name": "mcp-token", "type": "custom", "fields": map[string]string{"token": "s3cr3t-mcp"},
	}, tok); err != nil || status != 200 {
		t.Fatalf("create credential: status=%d err=%v", status, err)
	}

	// ─── create MCP server bound to that credential ───────────────────────
	status, body, err := env.DoJSON("POST", "/api/v1/mcp/servers", map[string]any{
		"name":                 "mock",
		"transport":            "http",
		"endpoint":             srv.URL,
		"credential":           "mcp-token",
		"header_template_json": `{"Authorization":"Bearer {{token}}"}`,
		"trusted":              true,
		"enabled":              true,
	}, tok)
	if err != nil || status != 200 {
		t.Fatalf("create mcp server: status=%d err=%v body=%v", status, err, body)
	}

	// ─── list + extract id ────────────────────────────────────────────────
	status, body, _ = env.DoJSON("GET", "/api/v1/mcp/servers", nil, tok)
	if status != 200 {
		t.Fatalf("list mcp servers: status=%d", status)
	}
	items, _ := body["items"].([]any)
	id := mcpFindID(items, "mock")
	if id == 0 {
		t.Fatalf("created mcp server not listed: %v", items)
	}

	// ─── test connection: connects to the mock, lists tools ───────────────
	status, body, _ = env.DoJSON("POST", fmt.Sprintf("/api/v1/mcp/servers/%d/test", id), nil, tok)
	if status != 200 {
		t.Fatalf("test connection: status=%d body=%v", status, body)
	}
	tools, _ := body["tools"].([]any)
	if !mcpHasTool(tools, "echo") {
		t.Fatalf("test connection did not return the mock's 'echo' tool: %v", body)
	}

	// ─── credential injection actually reached the server's headers ───────
	mu.Lock()
	gotAuth := authSeen
	mu.Unlock()
	if gotAuth != "Bearer s3cr3t-mcp" {
		t.Fatalf("credential not injected into MCP request header: got %q want %q", gotAuth, "Bearer s3cr3t-mcp")
	}

	// ─── update (flip trusted) + delete ───────────────────────────────────
	if status, _, _ = env.DoJSON("PUT", fmt.Sprintf("/api/v1/mcp/servers/%d", id), map[string]any{
		"name": "mock", "transport": "http", "endpoint": srv.URL, "trusted": false, "enabled": true,
	}, tok); status != 200 {
		t.Fatalf("update mcp server: status=%d", status)
	}
	if status, _, _ = env.DoJSON("DELETE", fmt.Sprintf("/api/v1/mcp/servers/%d", id), nil, tok); status != 200 && status != 204 {
		t.Fatalf("delete mcp server: status=%d", status)
	}
}

func mcpFindID(items []any, name string) int {
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		// model.Server has no json tags → PascalCase; tolerate snake too.
		n, _ := m["Name"].(string)
		if n == "" {
			n, _ = m["name"].(string)
		}
		if n == name {
			if f, ok := m["ID"].(float64); ok {
				return int(f)
			}
			if f, ok := m["id"].(float64); ok {
				return int(f)
			}
		}
	}
	return 0
}

func mcpHasTool(tools []any, name string) bool {
	for _, tl := range tools {
		if m, ok := tl.(map[string]any); ok {
			if s, _ := m["name"].(string); s == name {
				return true
			}
		}
	}
	return false
}
