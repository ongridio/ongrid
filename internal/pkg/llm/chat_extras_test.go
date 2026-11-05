package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiThoughtSignatureSurvivesToolRoundTrip(t *testing.T) {
	calls := 0
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		calls++
		if calls == 2 {
			require.Contains(t, string(payload["messages"]), `"thought_signature":"signed"`)
		}
		if calls == 3 {
			require.NotContains(t, string(payload["messages"]), "thought_signature")
		}
		require.NotContains(t, string(payload["messages"]), chatExtrasPrefix)
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"probe","arguments":"{}"},"extra_content":{"google":{"thought_signature":"signed"}}}]}}]}`))
		} else {
			_, _ = w.Write(sampleChatResponse("OK", nil))
		}
	})
	cfg.Model = "gemini-3.8-flash"
	client := newTestClient(t, cfg, nil)
	req := ChatReq{ReasoningEffort: "high", Messages: []Message{{Role: "user", Content: "call probe"}}, Tools: []ToolSchema{{Name: "probe", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	first, err := client.Chat(context.Background(), req)
	require.NoError(t, err)
	require.Contains(t, first.Assistant.ReasoningContent, chatExtrasPrefix)
	req.Messages = append(req.Messages, einoMessageToLLM(einoMessageFromChatResp(first)), Message{Role: "tool", ToolCallID: "c1", Content: `{"value":7}`})
	_, err = client.Chat(context.Background(), req)
	require.NoError(t, err)
	req.Model = "gpt-5.2"
	_, err = client.Chat(context.Background(), req)
	require.NoError(t, err)
}
