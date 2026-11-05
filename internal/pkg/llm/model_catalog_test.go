package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCatalogMetadataAndFallbacks(t *testing.T) {
	for _, tc := range []struct {
		provider, body, mode string
		levels               []string
	}{
		{"deepseek", `{"id":"deepseek-new","effort":{"supported_levels":["low","high","max"],"default_level":"high"}}`, ReasoningHybridMode, []string{"none", "low", "high", "max"}},
		{"anthropic", `{"id":"claude-new","capabilities":{"effort":{"supported":true,"low":{"supported":true},"xhigh":{"supported":true}},"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}}`, ReasoningClaudeMode, []string{"low", "xhigh"}},
		{"custom", `{"id":"my-model","supported_reasoning_efforts":["low","ultra"]}`, ReasoningEffortMode, []string{"low", "ultra"}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/models", r.URL.Path)
				require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				if tc.provider == "anthropic" {
					require.Equal(t, anthropicAPIVersion, r.Header.Get("anthropic-version"))
				}
				_, _ = w.Write([]byte(`{"data":[` + tc.body + `]}`))
			}))
			defer srv.Close()
			models, err := FetchModelCatalog(context.Background(), tc.provider, Config{BaseURL: srv.URL, APIKey: "test-key"})
			require.NoError(t, err)
			require.Len(t, models, 1)
			require.Equal(t, tc.levels, models[0].Reasoning.Levels)
			require.Equal(t, tc.mode, models[0].Reasoning.Mode)
			require.Equal(t, "provider", models[0].Reasoning.Source)
		})
	}
	var m catalogModelJSON
	require.NoError(t, json.Unmarshal([]byte(`{"id":"gpt-next"}`), &m))
	c, err := m.reasoning("openai")
	require.NoError(t, err)
	require.Nil(t, c)
	overrides := map[string]ReasoningOverride{"*": {Levels: []string{"low"}}, "gpt-next": {Levels: []string{}}}
	require.Empty(t, ResolveReasoning("openai", "gpt-next", nil, overrides).Levels)
	require.Equal(t, []string{"low"}, ResolveReasoning("openai", "gpt-future", nil, overrides).Levels)
	require.Equal(t, "builtin", ResolveReasoning("openai", "gpt-4o", nil, overrides).Source)
	upstream := map[string]ReasoningCapability{"gpt-future": {Levels: []string{"high"}, Source: "provider", Mode: ReasoningEffortMode}}
	require.Equal(t, "provider", ResolveReasoning("openai", "gpt-future", upstream, overrides).Source)
	require.Equal(t, []string{"high"}, ResolveReasoning("openai", "gpt-future", upstream, overrides).Levels)
	require.Equal(t, ReasoningEffortMode, BuiltinReasoning("custom", "claude-sonnet-4-6").Mode)
	require.Equal(t, ReasoningClaudeMode, ResolveReasoning("custom", "claude-sonnet-4-6", nil, map[string]ReasoningOverride{"claude-sonnet-4-6": {Levels: []string{"high"}, Mode: ReasoningClaudeMode}}).Mode)
}

func TestCatalogCacheCoalescesRetainsAndRotates(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"future","effort":{"supported_levels":["low","max"]}}]}`))
	}))
	defer srv.Close()
	d := &ReasoningDiscovery{}
	p := ProviderConfig{ID: "deepseek", APIKey: "test", BaseURL: srv.URL}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); d.capabilities(context.Background(), p) }()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
	fail.Store(true)
	d.entries[p.ID].nextFetch = time.Time{}
	require.Contains(t, d.capabilities(context.Background(), p), "future")
	require.Contains(t, d.capabilities(context.Background(), p), "future")
	require.EqualValues(t, 2, calls.Load())
	p.APIKey = "rotated"
	require.Nil(t, d.capabilities(context.Background(), p))
	require.EqualValues(t, 3, calls.Load())
}

func TestCatalogRejectsRedirectsAndMalformedMetadata(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":[{"id":"x","effort":{}}]}`, `{"data":[{"id":"x","supported_reasoning_efforts":["high","high"]}]}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := FetchModelCatalog(context.Background(), "custom", Config{APIKey: "test", BaseURL: srv.URL})
		srv.Close()
		require.Error(t, err)
	}
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	_, err := FetchModelCatalog(context.Background(), "custom", Config{APIKey: "secret", BaseURL: redirect.URL})
	require.Error(t, err)
	require.False(t, leaked.Load())
}

func TestReasoningOverrideValidation(t *testing.T) {
	for _, o := range []ReasoningOverride{
		{Levels: nil}, {Levels: []string{"bad value"}}, {Levels: []string{"low", "low"}},
		{Levels: []string{"high"}, Mode: ReasoningThinkingMode}, {Levels: []string{"enabled"}, Mode: ReasoningEffortMode},
	} {
		require.Error(t, ValidateReasoningOverrides("custom", map[string]ReasoningOverride{"model": o}))
	}
	require.NoError(t, ValidateReasoningOverrides("custom", map[string]ReasoningOverride{"*": {Levels: []string{}}, "alias": {Levels: []string{"ultra"}, Mode: ReasoningEffortMode}}))
}

func TestCatalogRefreshCannotUndoConfigurationInvalidation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client := NewMultiClient(nil, "", nil)
	client.SetProvidersResolver(stubProvidersResolver{providers: []ProviderConfig{{ID: "custom", APIKey: "test", BaseURL: server.URL, Model: "old"}}})
	client.EnableReasoningDiscovery()
	done := make(chan struct{})
	go func() { defer close(done); client.Providers() }()
	<-started
	client.SetProvidersResolver(stubProvidersResolver{})
	client.Invalidate()
	require.Empty(t, client.Providers())
	close(release)
	<-done
	require.Empty(t, client.Providers())
}
