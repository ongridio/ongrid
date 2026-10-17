//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestDynamicSkills_InstallLoadAndUninstall(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_SKILLS_ROOT", t.TempDir()))
	token := env.LoginAdmin().AccessToken
	session := createChatSession(t, env, token, "skill hot registration")
	const packID = "e2e-dynamic-skill"
	const skillName = "e2e_on_demand"
	const marker = "PRIVATE_SKILL_BODY_7e93"
	source := t.TempDir()
	files := map[string]string{
		".claude-plugin/plugin.json":    `{"id":"` + packID + `","name":"` + packID + `","version":"1.0.0","description":"dynamic E2E fixture"}`,
		"skills/e2e_on_demand/SKILL.md": "---\nname: " + skillName + "\ndescription: E2E on-demand instructions\nversion: 1.0.0\nactivation:\n  mode: on_demand\n---\n" + marker + "\nUse the bundled resource.\n",
		"bin/resource.txt":              "shared-plugin-asset",
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
	assertCatalog := func(want bool) {
		t.Helper()
		env.FakeLLM().SetScript(testenv.LLMReply{Content: "catalog checked"})
		dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "请说明当前可用能力"})
		reqs := env.FakeLLM().Requests()
		if len(reqs) != 1 {
			t.Fatalf("requests=%d", len(reqs))
		}
		system := ""
		for _, msg := range reqs[0].Messages {
			if msg.Role == "system" {
				system += msg.Content
			}
		}
		if strings.Contains(system, skillName) != want {
			t.Fatalf("skill catalog membership=%v want %v", strings.Contains(system, skillName), want)
		}
		if strings.Contains(system, marker) {
			t.Fatal("on-demand skill body was eagerly injected")
		}
	}
	assertCatalog(false)
	dynamicJSON(t, env, token, "POST", "/api/v1/marketplace/install", map[string]string{"type": "local", "path": source})
	installed := true
	t.Cleanup(func() {
		if installed {
			dynamicJSON(t, env, token, "DELETE", "/api/v1/marketplace/installed/"+packID, nil)
		}
	})
	assertCatalog(true)

	t.Run("load body and persist versioned tool result", func(t *testing.T) {
		env.FakeLLM().SetScript(dynamicToolReply("load_skill", `{"name":"`+skillName+`"}`), testenv.LLMReply{Content: "skill loaded"})
		body := dynamicJSON(t, env, token, "POST", chatMessagesPath(session), map[string]string{"content": "加载按需操作说明"})
		if !containsToolCall(body["tool_calls"], "load_skill") {
			t.Fatalf("load_skill not executed: %v", body)
		}
		reqs := env.FakeLLM().Requests()
		if len(reqs) != 2 {
			t.Fatalf("requests=%d", len(reqs))
		}
		result := ""
		for _, msg := range reqs[1].Messages {
			if msg.Role == "tool" && strings.Contains(msg.Content, marker) {
				result = msg.Content
			}
		}
		if result == "" {
			t.Fatal("loaded skill instructions never reached model")
		}
		var loaded struct{ Name, Version, Revision, Directory, Body string }
		if err := json.Unmarshal([]byte(result), &loaded); err != nil {
			t.Fatalf("skill result: %v; %s", err, result)
		}
		if loaded.Name != skillName || loaded.Version != "1.0.0" || loaded.Revision == "" || loaded.Directory == "" {
			t.Fatalf("missing versioned resource identity: %+v", loaded)
		}
		history := dynamicJSON(t, env, token, "GET", chatMessagesPath(session), nil)
		items, _ := history["items"].([]any)
		persisted := false
		for _, raw := range items {
			row, _ := raw.(map[string]any)
			content, _ := row["content"].(string)
			if row["role"] == "tool" && strings.Contains(content, loaded.Revision) && strings.Contains(content, marker) {
				persisted = true
			}
		}
		if !persisted {
			t.Fatal("skill body and revision were not persisted as tool evidence")
		}
	})

	t.Run("uninstall revokes an already constructed turn catalog", func(t *testing.T) {
		// A fresh session avoids confusing historical tool evidence with a new load.
		blockedSession := createChatSession(t, env, token, "skill removed during inference")
		env.FakeLLM().SetScript(dynamicToolReply("load_skill", `{"name":"`+skillName+`"}`), testenv.LLMReply{Content: "removed skill rejected"})
		block := env.FakeLLM().BlockNext()
		defer block.Release()
		type response struct {
			status int
			body   map[string]any
			err    error
		}
		done := make(chan response, 1)
		go func() {
			status, body, err := env.DoJSON("POST", chatMessagesPath(blockedSession), map[string]string{"content": "加载按需操作说明"}, token)
			done <- response{status, body, err}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := block.WaitStarted(ctx); err != nil {
			t.Fatal(err)
		}
		dynamicJSON(t, env, token, "DELETE", "/api/v1/marketplace/installed/"+packID, nil)
		installed = false
		block.Release()
		select {
		case res := <-done:
			if res.err != nil {
				t.Fatal(res.err)
			}
			if res.status != 200 || !containsToolCall(res.body["tool_calls"], "load_skill") {
				t.Fatalf("load attempt missing: status=%d body=%v", res.status, res.body)
			}
			// Graph reports recoverable tool failures as JSON observations so
			// the model can explain them; inspect that result, not SSE status.
			rejected := false
			for _, req := range env.FakeLLM().Requests() {
				for _, msg := range req.Messages {
					if msg.Role != "tool" {
						continue
					}
					var result struct{ Status, Error string }
					if json.Unmarshal([]byte(msg.Content), &result) == nil && result.Status == "failed" && strings.Contains(result.Error, "was removed") {
						rejected = true
					}
				}
			}
			if !rejected {
				t.Fatal("removed skill did not produce a failed observation")
			}

		case <-ctx.Done():
			t.Fatal("chat did not finish after removing skill")
		}
		for _, req := range env.FakeLLM().Requests() {
			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, marker) {
					t.Fatal("removed skill body leaked into model context")
				}
			}
		}
	})
	assertCatalog(false)
}
