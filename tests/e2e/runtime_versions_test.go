//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestRuntimeVersions_SwitchAndDynamicMCP(t *testing.T) {
	m := newDynamicMCP(t, "get_evidence")
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	token := env.LoginAdmin().AccessToken
	setVersion := func(version string) {
		dynamicJSON(t, env, token, "PUT", "/api/v1/system-settings/agent/runtime_version", map[string]any{"value": version, "sensitive": false})
	}
	v3 := createChatSession(t, env, token, "release remains v3")
	setVersion("v2")
	v2 := createVersionedChatSession(t, env, token, "v2 fallback", "v2")
	// Selecting a fallback changes only new sessions. Both original sessions remain usable.
	setVersion("release")
	createChatSession(t, env, token, "return to release")
	for _, session := range []struct{ id, version string }{{v2, "v2"}, {v3, "v3"}} {
		list := dynamicJSON(t, env, token, "GET", "/api/v1/chat/sessions", nil)
		var row map[string]any
		for _, item := range list["items"].([]any) {
			candidate := item.(map[string]any)
			if candidate["id"] == session.id {
				row = candidate
				break
			}
		}
		if row["runtime_version"] != session.version {
			t.Fatalf("session changed version: %v", row)
		}
		env.FakeLLM().SetScript(testenv.LLMReply{Content: "versioned response"})
		reply := dynamicJSON(t, env, token, "POST", chatMessagesPath(session.id), map[string]string{"content": "你好"})
		if nestedString(reply, "assistant_message", "content") != "versioned response" {
			t.Fatalf("versioned reply: %v", reply)
		}
	}
	path := registerDynamicMCP(t, env, token, "versioned", m)
	dynamicJSON(t, env, token, "POST", path+"/test", nil)
	const tool = "mcp__versioned__get_evidence"
	for _, session := range []string{v2, v3} {
		dynamicExecute(t, env, token, session, tool, m)
	}
	dynamicJSON(t, env, token, "PUT", path, map[string]any{"name": "versioned", "transport": "http", "endpoint": m.server.URL, "enabled": false, "trusted": true})
	for _, session := range []string{v2, v3} {
		dynamicRevoked(t, env, token, session, tool, m)
	}
	dynamicJSON(t, env, token, "PUT", path, map[string]any{"name": "versioned", "transport": "http", "endpoint": m.server.URL, "enabled": true, "trusted": false})
	dynamicJSON(t, env, token, "POST", path+"/test", nil)
	dynamicJSON(t, env, token, "PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{"value": "true", "sensitive": false})
	// A pending v2 approval must execute through v2 after the default becomes v3.
	env.FakeLLM().SetScript(dynamicToolReply("ToolSearch", `{"query":"select:mcp__versioned__get_evidence"}`), dynamicToolReply(tool, `{}`), testenv.LLMReply{Content: "approved evidence received"})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", env.BaseURL()+chatMessagesPath(v2)+"/stream", bytes.NewBufferString(`{"content":"调用 MCP 工具获取证据"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	before := m.count()
	scanner := bufio.NewScanner(resp.Body)
	var event string
	var approved, done bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if event == "approval_pending" {
			var proposal struct {
				ApprovalID string `json:"approval_id"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &proposal); err != nil {
				t.Fatal(err)
			}
			row := dynamicJSON(t, env, token, "GET", "/api/v1/approvals/"+proposal.ApprovalID, nil)
			if row["runtime_version"] != "v2" {
				t.Fatalf("approval lost v2 ownership: %v", row)
			}
			setVersion("v3")
			dynamicJSON(t, env, token, "POST", "/api/v1/approvals/"+proposal.ApprovalID+"/approve", map[string]any{})
			approved = true
		}
		if event == "done" {
			done = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !approved || !done || m.count() != before+1 {
		t.Fatalf("approval=%t done=%t actual calls=%d", approved, done, m.count()-before)
	}
	reqs := env.FakeLLM().Requests()
	found := false
	for _, request := range reqs {
		for _, msg := range request.Messages {
			if msg.Role == "tool" && strings.Contains(msg.Content, "dynamic-evidence:get_evidence") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("approved result did not reach v2 model")
	}
	env.FakeLLM().SetScript(
		dynamicToolReply("AgentTool", `{"description":"证据复核","subagent_type":"specialist-sre","prompt":"只读复核已有证据"}`),
		testenv.LLMReply{Content: "v2 worker evidence"}, testenv.LLMReply{Content: "v2 synthesized worker evidence"})
	reply := dynamicJSON(t, env, token, "POST", chatMessagesPath(v2), map[string]string{"content": "请 SRE 专家复核证据"})
	if nestedString(reply, "assistant_message", "content") != "v2 synthesized worker evidence" || !containsToolCall(reply["tool_calls"], "AgentTool") {
		t.Fatalf("v2 delegation failed: %v", reply)
	}
	workerSeen := false
	for _, req := range env.FakeLLM().Requests() {
		for _, msg := range req.Messages {
			if msg.Role == "tool" && strings.Contains(msg.Content, "v2 worker evidence") {
				workerSeen = true
			}
		}
	}
	if !workerSeen {
		t.Fatal("v2 worker did not return actual evidence")
	}
}

// Registry fanout must preserve the native skill strategy in each runtime and
// make newly installed personas callable without restarting either engine.
func TestRuntimeVersions_SkillAndPersonaReload(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_SKILLS_ROOT", t.TempDir()))
	token := env.LoginAdmin().AccessToken
	v3 := createChatSession(t, env, token, "v3 dynamic registry")
	dynamicJSON(t, env, token, "PUT", "/api/v1/system-settings/agent/runtime_version", map[string]any{"value": "v2", "sensitive": false})
	v2 := createVersionedChatSession(t, env, token, "v2 dynamic registry", "v2")
	dynamicJSON(t, env, token, "PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{"value": "true", "sensitive": false})
	source := t.TempDir()
	const pack = "e2e-versioned-registry"
	const marker = "VERSIONED_SKILL_BODY_37d92"
	files := map[string]string{
		".claude-plugin/plugin.json":   `{"id":"e2e-versioned-registry","name":"e2e-versioned-registry","version":"1.0.0"}`,
		"skills/versioned/SKILL.md":    "---\nname: versioned-skill\ndescription: test instructions\nactivation:\n  mode: keyword\n  keywords: [versioned-probe]\n---\n" + marker,
		"agents/versioned-reviewer.md": "---\nname: versioned-reviewer\ndescription: Evidence reviewer\nwhen_to_use: Review evidence\ntools: [query_devices]\nmax_turns: 4\n---\nVERSIONED_PERSONA_BODY_37d92\n",
	}
	for name, content := range files {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dynamicJSON(t, env, token, "POST", "/api/v1/marketplace/install", map[string]string{"type": "local", "path": source})
	installed := true
	t.Cleanup(func() {
		if installed {
			dynamicJSON(t, env, token, "DELETE", "/api/v1/marketplace/installed/"+pack, nil)
		}
	})
	assertSkill := func(session string, want bool) {
		t.Helper()
		env.FakeLLM().SetScript(testenv.LLMReply{Content: "skill probe"})
		dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "versioned-probe"})
		found := false
		for _, req := range env.FakeLLM().Requests() {
			for _, msg := range req.Messages {
				if msg.Role == "system" && strings.Contains(msg.Content, marker) {
					found = true
				}
			}
		}
		if found != want {
			t.Fatalf("dynamic skill body=%t want=%t", found, want)
		}
	}
	for _, session := range []string{v2, v3} {
		assertSkill(session, true)
		// A registered skill exposes v3 native loading only in v3; v2 keeps its own strategy.
		if got := containsString(env.FakeLLM().Requests()[0].ToolNames, "load_skill"); got != (session == v3) {
			t.Fatalf("native skill loader present=%t for session %s", got, session)
		}
		env.FakeLLM().SetScript(dynamicToolReply("AgentTool", `{"description":"review","subagent_type":"versioned-reviewer","prompt":"review the evidence"}`), testenv.LLMReply{Content: "registered worker evidence"}, testenv.LLMReply{Content: "integrated evidence"})
		reply := dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "请专家复核证据"})
		if nestedString(reply, "assistant_message", "content") != "integrated evidence" {
			for _, req := range env.FakeLLM().Requests() {
				for _, msg := range req.Messages {
					if msg.Role == "tool" {
						t.Logf("worker tool result: %s", msg.Content)
					}
				}
			}
			t.Fatalf("dynamic worker reply: %v", reply)
		}
		found := false
		for _, req := range env.FakeLLM().Requests() {
			for _, msg := range req.Messages {
				if msg.Role == "system" && strings.Contains(msg.Content, "VERSIONED_PERSONA_BODY_37d92") {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("new persona did not execute")
		}
	}
	dynamicJSON(t, env, token, "DELETE", "/api/v1/marketplace/installed/"+pack, nil)
	installed = false
	for _, session := range []string{v2, v3} {
		assertSkill(session, false)
		env.FakeLLM().SetScript(dynamicToolReply("AgentTool", `{"description":"review","subagent_type":"versioned-reviewer","prompt":"must not execute"}`), testenv.LLMReply{Content: "removed persona rejected"})
		dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "尝试旧专家"})
		if len(env.FakeLLM().Requests()) != 2 {
			t.Fatal("removed persona spawned a worker")
		}
	}
}
