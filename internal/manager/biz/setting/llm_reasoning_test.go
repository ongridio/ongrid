package setting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/require"
)

func TestReasoningConfigurationPersistsResolvesAndClears(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"alias","supported_reasoning_efforts":["high"]}]}`))
			return
		}
		var request struct {
			Effort string `json:"reasoning_effort"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Contains(t, []string{"", "ultra", "high"}, request.Effort)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer server.Close()
	settings := New(newFakeRepo(), nil)
	svc := NewLLMConfigurationService(nil, settings)
	in := LLMProbeInput{Provider: "custom", APIKey: "test", BaseURL: server.URL, DefaultModel: "alias", Models: []string{"alias"}, ReasoningOverrides: map[string]llm.ReasoningOverride{"alias": {Levels: []string{"ultra"}, Mode: "effort"}}}
	result, err := svc.Save(context.Background(), in)
	require.NoError(t, err)
	require.True(t, result.Saved)
	resolver := NewLLMSettingsResolver(settings, nil, "custom")
	client := llm.NewMultiClient(nil, "", nil)
	client.SetProvidersResolver(resolver)
	client.EnableReasoningDiscovery()
	require.Equal(t, "configuration", client.Providers()[0].Reasoning("alias").Source)
	_, err = client.Chat(context.Background(), llm.ChatReq{ReasoningEffort: "ultra", Messages: []llm.Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
	in.ReasoningOverrides = nil
	result, err = svc.Save(context.Background(), in)
	require.NoError(t, err)
	require.True(t, result.Saved)
	client.Invalidate()
	require.Equal(t, []string{"ultra"}, client.Providers()[0].Reasoning("alias").Levels)
	in.ReasoningOverrides = map[string]llm.ReasoningOverride{}
	result, err = svc.Save(context.Background(), in)
	require.NoError(t, err)
	require.True(t, result.Saved)
	client.Invalidate()
	require.Equal(t, "provider", client.Providers()[0].Reasoning("alias").Source)
	require.Equal(t, []string{"high"}, client.Providers()[0].Reasoning("alias").Levels)
	_, err = client.Chat(context.Background(), llm.ChatReq{ReasoningEffort: "ultra", Messages: []llm.Message{{Role: "user", Content: "test"}}})
	require.Error(t, err)
}

func TestReasoningSettingsRejectMalformedOverrides(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"m":{"levels":["high"],"typo":true}}`, `{"m":{"levels":null}}`} {
		_, err := decodeReasoningOverrides("custom", raw)
		require.Error(t, err)
	}
	_, err := decodeReasoningOverrides("xiaomi", `{"mimo-new":{"levels":["high"]}}`)
	require.Error(t, err)
}
