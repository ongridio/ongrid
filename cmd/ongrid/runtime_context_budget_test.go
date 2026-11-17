package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/require"
)

type contextBudgetProviders struct{ url string }

func (p contextBudgetProviders) ResolveProviders(context.Context) ([]llm.ProviderConfig, string, error) {
	return []llm.ProviderConfig{{ID: "custom", APIKey: "test", BaseURL: p.url, Model: "alias"}}, "custom", nil
}

func TestRuntimeBudgetUsesCatalogBehindTimeoutWrapper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"alias","context_window":32000,"max_output_tokens":2048}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	router := llm.NewMultiClient(nil, "", nil)
	router.SetProvidersResolver(contextBudgetProviders{url: server.URL})
	router.EnableReasoningDiscovery()
	client := llm.WithDefaultTimeout(router, func(context.Context) time.Duration { return time.Minute })
	chat, err := buildRuntimeChatModel(context.Background(), &config.Config{}, client, router, nil, slog.Default())
	require.NoError(t, err)
	bounded, err := llm.WithContextBudget(chat, llm.ContextBudget{}).WithTools(nil)
	require.NoError(t, err)
	var usage llm.ContextUsage
	_, err = bounded.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")}, llm.WithProvider("custom"), model.WithModel("alias"), llm.WithContextUsageObserver(func(u llm.ContextUsage) { usage = u }))
	require.NoError(t, err)
	require.Equal(t, 32000, usage.Window)
	require.Equal(t, 2048, usage.OutputTokens)
	require.Equal(t, "provider", usage.Source)
}
