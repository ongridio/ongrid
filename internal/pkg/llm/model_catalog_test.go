package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeminiNativeCatalogWindows(t *testing.T) {
	cfg := Config{BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", APIKey: "test-key"}
	var pages []string
	models, err := fetchModelCatalog(context.Background(), ProviderGemini, cfg, reasoningRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "generativelanguage.googleapis.com", r.URL.Host)
		require.Equal(t, "/v1beta/models", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-goog-api-key"))
		require.Empty(t, r.Header.Get("Authorization"))
		page := r.URL.Query().Get("pageToken")
		pages = append(pages, page)
		body := `{"models":[{"name":"models/gemini-future","inputTokenLimit":65536,"outputTokenLimit":4096}],"nextPageToken":"next"}`
		if page == "next" {
			body = `{"models":[{"name":"models/gemini-4-argon","inputTokenLimit":1048576,"outputTokenLimit":65536}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"", "next"}, pages)
	require.Len(t, models, 2)
	require.Equal(t, "gemini-future", models[0].ID)
	require.Equal(t, WindowCapability{Input: 65536, Output: 4096, Source: "provider"}, models[0].Window)
	require.Equal(t, 1048576, models[1].Window.Total)
	p := ProviderConfig{ID: ProviderGemini, Model: "models/gemini-future", BaseURL: cfg.BaseURL}
	require.Equal(t, models[0].Window, providerWindows(p, map[string]CatalogModel{models[0].ID: models[0]})[p.Model])
}

func TestCompatibleCatalogWindowLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1beta/openai/models", r.URL.Path)
		require.Empty(t, r.Header.Get("x-goog-api-key"))
		_, _ = io.WriteString(w, `{"data":[
			{"id":"routed","context_length":1000000,"top_provider":{"context_length":64000,"max_completion_tokens":2048}},
			{"id":"self-hosted","max_model_len":32000,"max_input_tokens":99999999},
			{"id":"output-only","max_completion_tokens":4096},{"id":"unknown"}]}`)
	}))
	defer srv.Close()
	models, err := FetchModelCatalog(context.Background(), ProviderGemini, Config{BaseURL: srv.URL + "/v1beta/openai"})
	require.NoError(t, err)
	require.Len(t, models, 4)
	require.Equal(t, WindowCapability{Total: 64000, Output: 2048, Source: "provider"}, models[0].Window)
	require.Equal(t, WindowCapability{Total: 32000, Source: "provider"}, models[1].Window)
	require.Equal(t, WindowCapability{Output: 4096}, models[2].Window)
	require.Equal(t, WindowCapability{}, models[3].Window)
}

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
