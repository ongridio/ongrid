package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Opt-in: synthetic data only; providers may omit visible reasoning content.
func TestToolCompatibilityLive(t *testing.T) {
	if os.Getenv("ONGRID_TOOL_COMPAT_SMOKE") != "1" {
		t.Skip("opt-in live provider smoke")
	}
	cfg := Config{Provider: os.Getenv("SMOKE_PROVIDER"), APIKey: os.Getenv("SMOKE_API_KEY"), BaseURL: os.Getenv("SMOKE_BASE_URL"), Model: os.Getenv("SMOKE_MODEL")}
	if raw := os.Getenv("SMOKE_REASONING_CAPABILITY"); raw != "" {
		var capability ReasoningCapability
		require.NoError(t, json.Unmarshal([]byte(raw), &capability))
		cfg.ReasoningCapabilities = map[string]ReasoningCapability{cfg.Model: capability}
	}
	client := New(cfg, nil, prometheus.NewRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req := ChatReq{ReasoningEffort: os.Getenv("SMOKE_REASONING_EFFORT"), Messages: []Message{{Role: "user", Content: "Hello"}, {Role: "assistant", Content: "Hello."}, {Role: "user", Content: "Call test_value once with id 1, then report the returned value briefly. This is a synthetic compatibility test."}}, Tools: []ToolSchema{{Name: "test_value", Description: "Returns a synthetic value by id or label.", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"},"label":{"type":"string"}},"oneOf":[{"required":["id"],"not":{"required":["label"]}},{"required":["label"],"not":{"required":["id"]}}]}`)}}}
	if effort := os.Getenv("SMOKE_WARMUP_EFFORT"); effort != "" {
		warmup := req
		warmup.ReasoningEffort = effort
		_, err := client.Chat(ctx, warmup)
		require.NoError(t, err, "protocol warm-up")
	}
	first, err := client.Chat(ctx, req)
	require.NoError(t, err, "initial tool request")
	require.Len(t, first.Assistant.ToolCalls, 1)
	call := first.Assistant.ToolCalls[0]
	require.Equal(t, "test_value", call.Name)
	require.JSONEq(t, `{"id":1}`, string(call.Args))
	t.Logf("stage=tool_request total_tokens=%d", first.Usage.TotalTokens)
	req.Messages = append(req.Messages, einoMessageToLLM(einoMessageFromChatResp(first)), Message{Role: "tool", ToolCallID: call.ID, Content: `{"value":7}`})
	second, err := client.Chat(ctx, req)
	require.NoError(t, err, "tool result continuation")
	require.Empty(t, second.Assistant.ToolCalls)
	require.Contains(t, second.Assistant.Content, "7")
	t.Logf("stage=tool_result total_tokens=%d", second.Usage.TotalTokens)
	req.Messages = append(req.Messages, second.Assistant, Message{Role: "user", Content: "What was the returned value? Answer briefly from history."})
	req.DisableTools = true
	third, err := client.Chat(ctx, req)
	require.NoError(t, err, "disabled-tool follow-up")
	require.Empty(t, third.Assistant.ToolCalls)
	require.Contains(t, third.Assistant.Content, "7")
	t.Logf("stage=history_followup total_tokens=%d", third.Usage.TotalTokens)
	t.Log("schema, legacy history, tool continuation, and disabled-tool follow-up succeeded")
}
