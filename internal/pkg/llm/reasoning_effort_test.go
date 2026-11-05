package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestReasoningEffortCapabilities(t *testing.T) {
	for _, tc := range []struct {
		provider, model, effort string
		valid                   bool
	}{
		{"openai", "gpt-6.1-terra", "none", true},
		{"openai", "gpt-6.1-terra-2026-10-26", "max", true},
		{"openai", "gpt-6.1-terra-pro", "max", true},
		{"openai", "gpt-6.1-sol", "none", false},
		{"openai", "gpt-5-mini", "minimal", true},
		{"openai", "gpt-5-mini", "none", false},
		{"deepseek", "deepseek-v4-flash", "max", true},
		{"deepseek", "deepseek-v4-flash", "medium", false},
		{"custom", "deepseek-v4-flash", "none", true},
		{"anthropic", "claude-future", "high", true},
		{"custom", "unknown", "high", false},
		{"custom", "unknown", "", true},
		{"xiaomi", "mimo-v2.6-flash", "enabled", true},
		{"xiaomi", "mimo-v2.6-flash", "high", false},
	} {
		t.Run(tc.provider+"/"+tc.model+"/"+tc.effort, func(t *testing.T) {
			err := ValidateReasoningEffort(tc.provider, tc.model, tc.effort)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestReasoningEffortWireFormat(t *testing.T) {
	for _, tc := range []struct{ model, effort, wireEffort, thinking string }{
		{"gpt-6.1-terra", "low", "low", ""},
		{"gpt-6.1-terra", "none", "none", ""},
		{"deepseek-v4-flash", "high", "high", "enabled"},
		{"deepseek-v4-flash", "none", "", "disabled"},
		{"deepseek-v4-flash", "", "", ""},
		{"mimo-v2.6-flash", "enabled", "", "enabled"},
		{"mimo-v2.6-flash", "none", "", "disabled"},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Effort   string `json:"reasoning_effort"`
					Thinking *struct {
						Type string `json:"type"`
					} `json:"thinking"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				require.Equal(t, tc.wireEffort, payload.Effort)
				if tc.thinking == "" {
					require.Nil(t, payload.Thinking)
				} else {
					require.NotNil(t, payload.Thinking)
					require.Equal(t, tc.thinking, payload.Thinking.Type)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(sampleChatResponse("ok", nil))
			})
			cfg.Model = tc.model
			client := newTestClient(t, cfg, nil)
			adapter, err := NewClientChatModel(ClientChatModelConfig{Client: client})
			require.NoError(t, err)
			_, err = adapter.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "test"}}, WithReasoningEffort(tc.effort))
			require.NoError(t, err)
		})
	}
}

func TestReasoningEffortResponsesFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"not supported; use /responses"}}`))
			return
		}
		var payload map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.JSONEq(t, `{"effort":"high"}`, string(payload["reasoning"]))
		require.NotContains(t, payload, "reasoning_effort")
		require.NotContains(t, payload, "temperature")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, Config{APIKey: "test", BaseURL: server.URL, Model: "gpt-6.1-terra"}, nil)
	_, err := client.Chat(context.Background(), ChatReq{ReasoningEffort: "high", Messages: []Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
}

func TestDisabledToolsRejectsInferredCalls(t *testing.T) {
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotContains(t, payload, "tools")
		require.NotContains(t, payload, "tool_choice")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("", []map[string]any{{"id": "call-1", "type": "function", "function": map[string]string{"name": "probe", "arguments": "{}"}}}))
	})
	client := newTestClient(t, cfg, nil)
	_, err := client.Chat(context.Background(), ChatReq{DisableTools: true, Messages: []Message{{Role: "user", Content: "Summarize"}}})
	require.ErrorContains(t, err, "tools were disabled")
}
