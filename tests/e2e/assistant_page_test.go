//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

// Inspect the provider request and the transcript separately. A plausible
// model answer alone cannot prove the right snapshot reached either turn.
func TestAssistantPageContext(t *testing.T) {
	env := testenv.Start(t)
	token := env.LoginAdmin().AccessToken
	for _, version := range []string{"v3", "v2"} {
		t.Run(version, func(t *testing.T) {
			dynamicJSON(t, env, token, "PUT", "/api/v1/system-settings/agent/runtime_version", map[string]any{"value": version, "sensitive": false})
			page := map[string]any{"page": "Dashboard", "fields": map[string]string{"open_incidents": "173"}}
			session := dynamicJSON(t, env, token, "POST", "/api/v1/chat/sessions", map[string]any{"title": "Check dashboard", "page_context": page})
			if session["page_context"] == nil {
				t.Fatal("missing accepted page reference")
			}
			path := chatMessagesPath(session["id"].(string))
			env.FakeLLM().SetLLMReply("acknowledged")
			for _, question := range []string{"Explain the dashboard.", "And the open incidents?"} {
				before := len(env.FakeLLM().Requests())
				dynamicJSON(t, env, token, "POST", path, map[string]string{"content": question})
				found := false
				for _, request := range env.FakeLLM().Requests()[before:] {
					for _, msg := range request.Messages {
						if msg.Role == "user" && strings.Contains(msg.Content, question) && strings.Contains(msg.Content, `"open_incidents":"173"`) {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("page reference absent from current model turn: %s", question)
				}
			}
			history := dynamicJSON(t, env, token, "GET", path, nil)
			for _, item := range history["items"].([]any) {
				msg := item.(map[string]any)
				if msg["role"] == "user" && strings.Contains(msg["content"].(string), "open_incidents") {
					t.Fatal("snapshot overwrote user message")
				}
			}
		})
	}
}
