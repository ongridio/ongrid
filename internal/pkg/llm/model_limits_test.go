package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestModelContextLimitsAndFallback(t *testing.T) {
	fallback := ContextBudget{Window: 64000, Output: 20000}
	for _, tc := range []struct {
		provider, name string
		window, output int
		source         string
	}{
		{"openai", "gpt-4o", 128000, 16384, "builtin"},
		{"openai", "gpt-4.1-2025-04-14", 1047576, 20000, "builtin"},
		{"custom", "gpt-6.1-sol", 1050000, 20000, "builtin"},
		{"anthropic", "claude-sonnet-4-5", 200000, 20000, "builtin"},
		{"openai", "gpt-4o-unknown", 64000, 20000, "global"},
		{"custom", "private-alias", 64000, 20000, "global"},
		{"anthropic", "gpt-4o", 64000, 20000, "global"},
	} {
		t.Run(tc.provider+"/"+tc.name, func(t *testing.T) {
			b := ResolveContextBudget(tc.provider, tc.name, fallback, nil)
			require.Equal(t, ContextBudget{Window: tc.window, Output: tc.output, Source: tc.source}, b)
		})
	}
	require.Equal(t, ContextBudget{Window: 32768, Output: 4096, Source: "provider"}, ResolveContextBudget("openai", "gpt-4o", fallback, &ModelLimits{32768, 4096}))
	require.Equal(t, 128000, ResolveContextBudget("openai", "gpt-4o", fallback, &ModelLimits{1, -1}).Window)
}

func TestCatalogContextLimitsReachPickerAndRouter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"alias","context_length":100000,"top_provider":{"context_length":32000,"max_completion_tokens":4096}}]}`))
	}))
	defer server.Close()
	client := NewMultiClient(nil, "", nil)
	client.SetProvidersResolver(stubProvidersResolver{providers: []ProviderConfig{{ID: "custom", APIKey: "test", BaseURL: server.URL, Model: "alias"}}})
	client.EnableReasoningDiscovery()
	providers := client.ProvidersContext(context.Background())
	require.Len(t, providers, 1)
	fallback := ContextBudget{Window: 80000, Output: 8192}
	expected := ContextBudget{Window: 32000, Output: 4096, Source: "provider"}
	require.Equal(t, expected, providers[0].ContextBudget("alias", fallback))
	require.Equal(t, expected, client.ContextBudget(context.Background(), "custom", "alias", fallback))
	providers[0].ContextLimits["alias"] = ModelLimits{Window: 99999}
	require.Equal(t, expected, client.ContextBudget(context.Background(), "custom", "alias", fallback))
	for _, body := range []string{`{"max_input_tokens":200000,"max_tokens":64000}`, `{"context_window":200000,"max_output_tokens":64000}`} {
		var item catalogModelJSON
		require.NoError(t, json.Unmarshal([]byte(body), &item))
		require.Equal(t, &ModelLimits{200000, 64000}, item.limits())
	}
}

func TestContextBudgetTracksRoutingAndToolBinding(t *testing.T) {
	stub := &einoStubClient{}
	inner, err := NewClientChatModel(ClientChatModelConfig{Client: stub, Model: "gpt-4o"})
	require.NoError(t, err)
	defaultModel := "gpt-4o"
	router, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{"openai": inner}, DefaultProvider: "openai",
		DefaultResolver: func(context.Context) (string, string) { return "openai", defaultModel },
		ContextBudgetResolver: func(_ context.Context, p, m string, fallback ContextBudget) ContextBudget {
			if m == "alias" {
				return ResolveContextBudget(p, m, fallback, &ModelLimits{32000, 1024})
			}
			return ResolveContextBudget(p, m, fallback, nil)
		},
	})
	require.NoError(t, err)
	bounded, err := WithContextBudget(router, ContextBudget{Window: 16000, Output: 8192}).WithTools([]*schema.ToolInfo{{Name: "test", Desc: "test"}})
	require.NoError(t, err)
	var usage ContextUsage
	observer := WithContextUsageObserver(func(u ContextUsage) { usage = u })
	input := []*schema.Message{schema.UserMessage(strings.Repeat("x", 20000))}
	_, err = bounded.Generate(context.Background(), input, observer)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o", stub.lastReq.Model)
	require.Equal(t, 128000, usage.Window)
	require.Greater(t, usage.InputTokens, 20000)
	defaultModel = "gpt-4.1"
	stream, err := bounded.Stream(context.Background(), input, observer)
	require.NoError(t, err)
	stream.Close()
	require.Equal(t, 1047576, usage.Window)
	_, err = bounded.Generate(context.Background(), input, observer, WithProvider("openai"), model.WithModel("alias"))
	require.NoError(t, err)
	require.Equal(t, 32000, usage.Window)
	require.Equal(t, 1024, stub.lastReq.MaxTokens)
	// An explicit provider with no model still uses its inner default after WithTools.
	_, err = bounded.Generate(context.Background(), input, observer, WithProvider("openai"))
	require.NoError(t, err)
	require.Equal(t, "gpt-4o", usage.Model)
}
