package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicNativeThinkingAndToolContinuation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "key", r.Header.Get("x-api-key"))
		var payload map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotContains(t, payload, "reasoning_effort")
		require.NotContains(t, payload, "temperature")
		require.JSONEq(t, `{"type":"adaptive"}`, string(payload["thinking"]))
		require.JSONEq(t, `{"effort":"high"}`, string(payload["output_config"]))
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			require.Contains(t, string(payload["tools"]), `"input_schema"`)
			_, _ = w.Write([]byte(`{"id":"m1","model":"claude-sonnet-4-6","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"private","signature":"signed"},{"type":"tool_use","id":"call1","name":"probe","input":{"id":1}}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2}}`))
		} else {
			require.Contains(t, string(payload["messages"]), `"signature":"signed"`)
			require.Contains(t, string(payload["messages"]), `"tool_use_id":"call1"`)
			_, _ = w.Write([]byte(`{"id":"m2","model":"claude-sonnet-4-6","stop_reason":"end_turn","content":[{"type":"text","text":"7"}]}`))
		}
	}))
	defer server.Close()
	client := newTestClient(t, Config{Provider: "anthropic", BaseURL: server.URL, APIKey: "key", Model: "claude-sonnet-4-6"}, nil)
	req := ChatReq{ReasoningEffort: "high", Messages: []Message{{Role: "system", Content: "test"}, {Role: "user", Content: "call probe"}}, Tools: []ToolSchema{{Name: "probe", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	first, err := client.Chat(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, first.Assistant.Content)
	require.Len(t, first.Assistant.ToolCalls, 1)
	require.Equal(t, 16, first.Usage.TotalTokens)
	req.Messages = append(req.Messages, einoMessageToLLM(einoMessageFromChatResp(first)), Message{Role: "tool", ToolCallID: "call1", Content: `{"value":7}`})
	second, err := client.Chat(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "7", second.Assistant.Content)
}

func TestAllProviderReasoningWireModes(t *testing.T) {
	for _, tc := range []struct{ provider, model, effort, wire, thinking string }{
		{"gemini", "gemini-3.1-pro", "high", "high", ""},
		{"kimi", "kimi-k2.6", "enabled", "", "enabled"},
		{"kimi", "kimi-k3", "max", "max", ""},
		{"zhipu", "glm-5.3", "low", "low", "enabled"},
		{"zhipu", "glm-5.2", "minimal", "", "disabled"},
		{"zhipu", "glm-4.7", "enabled", "", "enabled"},
		{"minimax", "MiniMax-M3", "enabled", "", "adaptive"},
		{"minimax", "MiniMax-M3.1-Flash", "xhigh", "xhigh", ""},
		{"custom", "local-alias", "ultra", "ultra", ""},
	} {
		t.Run(tc.provider+tc.model, func(t *testing.T) {
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Effort   string `json:"reasoning_effort"`
					Thinking struct {
						Type string `json:"type"`
					} `json:"thinking"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				require.Equal(t, tc.wire, payload.Effort)
				require.Equal(t, tc.thinking, payload.Thinking.Type)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(sampleChatResponse("OK", nil))
			})
			cfg.Provider, cfg.Model = tc.provider, tc.model
			if tc.provider == "custom" {
				cfg.ReasoningCapabilities = map[string]ReasoningCapability{tc.model: {Levels: []string{"ultra"}, Mode: ReasoningEffortMode}}
			}
			_, err := newTestClient(t, cfg, nil).Chat(context.Background(), ChatReq{ReasoningEffort: tc.effort, Messages: []Message{{Role: "user", Content: "test"}}})
			require.NoError(t, err)
		})
	}
}
