//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

// dynamicMCP is controlled through locks/channels, but all Manager interactions
// go through public HTTP APIs. Each list page contains one tool.
type dynamicMCP struct {
	mu       sync.Mutex
	names    []string
	calls    []string
	fail     bool
	notify   chan struct{}
	watching chan struct{}
	once     sync.Once
	server   *httptest.Server
}

func newDynamicMCP(t *testing.T, names ...string) *dynamicMCP {
	t.Helper()
	m := &dynamicMCP{names: names, notify: make(chan struct{}, 1), watching: make(chan struct{})}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}
func (m *dynamicMCP) set(names ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.names = append([]string(nil), names...)
}
func (m *dynamicMCP) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.calls) }
func (m *dynamicMCP) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		m.once.Do(func() { close(m.watching) })
		for {
			select {
			case <-r.Context().Done():
				return
			case <-m.notify:
				if _, err := fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}
	var req struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			Cursor string `json:"cursor"`
			Name   string `json:"name"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch req.Method {
	case "initialize":
		mcpWriteRPC(w, req.ID, map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]bool{"listChanged": true}}})
	case "notifications/initialized":
		w.WriteHeader(202)
	case "tools/list":
		if m.fail {
			http.Error(w, "temporary discovery failure", 503)
			return
		}
		page := 0
		if req.Params.Cursor != "" {
			if _, err := fmt.Sscanf(req.Params.Cursor, "page-%d", &page); err != nil {
				http.Error(w, "bad cursor", 400)
				return
			}
		}
		list := []map[string]any{}
		if page < len(m.names) {
			list = append(list, map[string]any{"name": m.names[page], "description": "dynamic evidence " + m.names[page], "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
		}
		result := map[string]any{"tools": list}
		if page+1 < len(m.names) {
			result["nextCursor"] = fmt.Sprintf("page-%d", page+1)
		}
		mcpWriteRPC(w, req.ID, result)
	case "tools/call":
		m.calls = append(m.calls, req.Params.Name)
		mcpWriteRPC(w, req.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": "dynamic-evidence:" + req.Params.Name}}})
	default:
		http.Error(w, "unsupported method", 400)
	}
}

func dynamicJSON(t *testing.T, env *testenv.Env, token, method, path string, input any) map[string]any {
	t.Helper()
	status, body, err := env.DoJSON(method, path, input, token)
	if err != nil || status < 200 || status >= 300 {
		t.Fatalf("%s %s: status=%d err=%v body=%v", method, path, status, err, body)
	}
	return body
}
func registerDynamicMCP(t *testing.T, env *testenv.Env, token, name string, m *dynamicMCP) string {
	t.Helper()
	dynamicJSON(t, env, token, "POST", "/api/v1/mcp/servers", map[string]any{"name": name, "transport": "http", "endpoint": m.server.URL, "enabled": true, "trusted": true})
	list := dynamicJSON(t, env, token, "GET", "/api/v1/mcp/servers", nil)
	items, _ := list["items"].([]any)
	id := mcpFindID(items, name)
	if id == 0 {
		t.Fatal("new MCP registration missing")
	}
	path := fmt.Sprintf("/api/v1/mcp/servers/%d", id)
	t.Cleanup(func() {
		status, _, err := env.DoJSON("DELETE", path, nil, token)
		if err != nil || (status != 200 && status != 204 && status != 404) {
			t.Errorf("cleanup MCP: %d %v", status, err)
		}
	})
	return path
}
func dynamicToolReply(name, args string) testenv.LLMReply {
	return testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{ID: "dynamic-call", Name: name, Arguments: args}}}
}
func dynamicSearch(t *testing.T, env *testenv.Env, token, session, name string, want bool) {
	t.Helper()
	env.FakeLLM().SetScript(dynamicToolReply("ToolSearch", fmt.Sprintf(`{"query":"select:%s"}`, name)), testenv.LLMReply{Content: "catalog inspected"})
	dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "查询可用工具目录"})
	reqs := env.FakeLLM().Requests()
	if len(reqs) != 2 {
		t.Fatalf("search model requests=%d want 2", len(reqs))
	}
	if got := containsString(reqs[1].ToolNames, name); got != want {
		t.Fatalf("discovered schema %s=%v want %v; schemas=%v", name, got, want, reqs[1].ToolNames)
	}
}
func dynamicExecute(t *testing.T, env *testenv.Env, token, session, name string, m *dynamicMCP) {
	t.Helper()
	before := m.count()
	env.FakeLLM().SetScript(dynamicToolReply("ToolSearch", fmt.Sprintf(`{"query":"select:%s"}`, name)), dynamicToolReply(name, `{}`), testenv.LLMReply{Content: "evidence received"})
	body := dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "调用工具获取证据"})
	if !containsToolCall(body["tool_calls"], name) || m.count() != before+1 {
		t.Fatalf("dynamic execution missing: calls=%d body=%v", m.count()-before, body)
	}
	reqs := env.FakeLLM().Requests()
	if len(reqs) != 3 {
		t.Fatalf("execution requests=%d want 3", len(reqs))
	}
	found := false
	for _, msg := range reqs[2].Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "dynamic-evidence:"+name[strings.LastIndex(name, "__")+2:]) {
			found = true
		}
	}
	if !found {
		t.Fatal("actual MCP evidence did not reach the model")
	}
}
func dynamicRevoked(t *testing.T, env *testenv.Env, token, session, name string, m *dynamicMCP) {
	t.Helper()
	dynamicSearch(t, env, token, session, name, false)
	before := m.count()
	env.FakeLLM().SetScript(dynamicToolReply(name, `{}`), testenv.LLMReply{Content: "stale call rejected"})
	// Even a model that emits a stale tool call must not reach the endpoint.
	_, _, err := env.DoJSON("POST", chatMessagesPath(session), map[string]string{"content": "尝试旧工具"}, token)
	if err != nil {
		t.Fatal(err)
	}
	if m.count() != before {
		t.Fatal("revoked tool reached MCP endpoint")
	}
}

func TestDynamicMCP_HotRegistrationLifecycle(t *testing.T) {
	first := newDynamicMCP(t, "get_first", "get_second")
	replacement := newDynamicMCP(t, "get_replacement")
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	token := env.LoginAdmin().AccessToken
	session := createChatSession(t, env, token, "dynamic MCP lifecycle")
	const name = "e2e_dynamic"
	oldTool := "mcp__" + name + "__get_second"
	dynamicSearch(t, env, token, session, oldTool, false)
	path := registerDynamicMCP(t, env, token, name, first)
	t.Run("register and discover paginated tools in existing chat", func(t *testing.T) {
		body := dynamicJSON(t, env, token, "POST", path+"/test", nil)
		tools, _ := body["tools"].([]any)
		if len(tools) != 2 || !mcpHasTool(tools, "get_second") {
			t.Fatalf("pagination incomplete: %v", body)
		}
		dynamicExecute(t, env, token, session, oldTool, first)
	})
	t.Run("temporary discovery failure retains executable catalog", func(t *testing.T) {
		first.mu.Lock()
		first.fail = true
		first.mu.Unlock()
		status, _, err := env.DoJSON("POST", path+"/test", nil, token)
		first.mu.Lock()
		first.fail = false
		first.mu.Unlock()
		if err != nil || status < 400 {
			t.Fatalf("expected discovery failure: %d %v", status, err)
		}
		dynamicExecute(t, env, token, session, oldTool, first)
	})
	t.Run("schema refresh removes stale tools", func(t *testing.T) {
		first.set("get_new_tool")
		dynamicJSON(t, env, token, "POST", path+"/test", nil)
		dynamicRevoked(t, env, token, session, oldTool, first)
		dynamicExecute(t, env, token, session, "mcp__"+name+"__get_new_tool", first)
	})
	patch := func(t *testing.T, endpoint string, enabled bool) {
		t.Helper()
		dynamicJSON(t, env, token, "PUT", path, map[string]any{"name": name, "transport": "http", "endpoint": endpoint, "enabled": enabled, "trusted": true})
	}
	t.Run("disable and reenable without restart", func(t *testing.T) {
		patch(t, first.server.URL, false)
		dynamicRevoked(t, env, token, session, "mcp__"+name+"__get_new_tool", first)
		patch(t, first.server.URL, true)
		dynamicExecute(t, env, token, session, "mcp__"+name+"__get_new_tool", first)
	})
	t.Run("replace endpoint then delete", func(t *testing.T) {
		patch(t, replacement.server.URL, true)
		dynamicRevoked(t, env, token, session, "mcp__"+name+"__get_new_tool", first)
		dynamicJSON(t, env, token, "POST", path+"/test", nil)
		dynamicExecute(t, env, token, session, "mcp__"+name+"__get_replacement", replacement)
		dynamicJSON(t, env, token, "DELETE", path, nil)
		dynamicRevoked(t, env, token, session, "mcp__"+name+"__get_replacement", replacement)
	})
}

func TestDynamicMCP_BackgroundDiscoveryNotificationAndPolling(t *testing.T) {
	m := newDynamicMCP(t, "get_before_notification")
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	token := env.LoginAdmin().AccessToken
	session := createChatSession(t, env, token, "MCP notification refresh")
	dynamicJSON(t, env, token, "PUT", "/api/v1/system-settings/agent/runtime_version", map[string]any{"value": "v2", "sensitive": false})
	v2 := createVersionedChatSession(t, env, token, "v2 automatic MCP discovery", "v2")
	registerDynamicMCP(t, env, token, "e2e_notify", m)
	// 不调用 /test；注册必须唤醒发现流程，而不是等待一分钟轮询。
	select {
	case <-m.watching:
	case <-time.After(10 * time.Second):
		t.Fatal("registration did not promptly wake MCP discovery")
	}
	t.Log("background supervisor discovered registration and opened SSE")
	for _, sid := range []string{session, v2} {
		dynamicExecute(t, env, token, sid, "mcp__e2e_notify__get_before_notification", m)
	}
	m.set("get_after_notification")
	m.notify <- struct{}{}
	waitDynamicCatalog(t, env, token, "get_after_notification", 12*time.Second)
	t.Log("notification published replacement catalog")

	dynamicRevoked(t, env, token, session, "mcp__e2e_notify__get_before_notification", m)
	dynamicExecute(t, env, token, session, "mcp__e2e_notify__get_after_notification", m)
	// No notification this time: only the production polling fallback can
	// discover this change. Keep its actual one-minute cadence in E2E.
	m.set("get_after_poll")
	waitDynamicCatalog(t, env, token, "get_after_poll", 85*time.Second)
	t.Log("polling discovered change without a notification")
	dynamicRevoked(t, env, token, session, "mcp__e2e_notify__get_after_notification", m)
	dynamicExecute(t, env, token, session, "mcp__e2e_notify__get_after_poll", m)
}

// waitDynamicCatalog polls the public cache to observe background publication.
func waitDynamicCatalog(t *testing.T, env *testenv.Env, token, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		body := dynamicJSON(t, env, token, "GET", "/api/v1/mcp/servers", nil)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background catalog did not publish %s within %s", marker, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
