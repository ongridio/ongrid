package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(WebSearch) }

// Provider names — lowercased canonical form. Mirrors
// internal/manager/model/setting.ProviderXxx but kept locally to avoid
// the skill package depending on the manager-side model package
// (layering rule, see TavilyKeyResolver comment).
const (
	providerSearxng = "searxng"
	providerTavily  = "tavily"
	providerBrave   = "brave"
)

// defaultSearxngURL is the docker-internal address the SearXNG service
// listens on inside ongrid's compose stack. Falls back to this when no
// resolver is wired (e.g. unit tests) or when the operator left the
// URL row empty.
const defaultSearxngURL = "http://searxng:8080"

// WebSearch is the manager-side singleton instance — exported so
// cmd/main.go can call SetWebSearchConfigResolver / SetHTTPClient at
// boot to inject the config resolver and (for tests) a stub HTTP
// client. Skills register their Executor with the global registry as a
// value pointer, so external mutations on this var go through internal
// sync primitives.
var WebSearch = &webSearchSkill{
	tavilyEndpoint: "https://api.tavily.com/search",
	braveEndpoint:  "https://api.search.brave.com/res/v1/web/search",
}

// WebSearchConfigResolver returns the runtime config the skill needs to
// dispatch to the right provider. Implementations live in the manager's
// biz/setting package; the skill keeps the dependency as an interface
// so internal/skill never imports internal/manager.
//
// All methods are called per-Execute; resolver impls are expected to be
// cache-backed (Service does this) so the round-trip is cheap.
type WebSearchConfigResolver interface {
	// Provider returns the user-selected backend ("searxng" | "tavily"
	// | "brave"). Unknown / empty values are treated as "searxng".
	Provider(ctx context.Context) string
	// SearxngURL returns the SearXNG base URL (no trailing slash).
	// Empty falls back to defaultSearxngURL.
	SearxngURL(ctx context.Context) string
	// TavilyAPIKey returns the Tavily key, "" when unset.
	TavilyAPIKey(ctx context.Context) string
	// BraveAPIKey returns the Brave Search key, "" when unset.
	BraveAPIKey(ctx context.Context) string
}

// TavilyKeyResolver is the legacy subset of WebSearchConfigResolver
// kept for back-compat — early integrations only knew about Tavily.
// Code that still passes a TavilyKeyResolver gets adapted internally
// to "Tavily-only" behaviour (provider=tavily, no SearXNG fallback).
//
// Deprecated: pass a WebSearchConfigResolver instead.
type TavilyKeyResolver interface {
	TavilyAPIKey(ctx context.Context) string
}

// SetWebSearchConfigResolver wires the full multi-provider config
// resolver. nil disables the skill's resolver path (every invocation
// falls back to the SearXNG default URL with no key).
//
// Idempotent — re-calling from tests overrides the previous resolver.
func SetWebSearchConfigResolver(r WebSearchConfigResolver) {
	WebSearch.mu.Lock()
	WebSearch.cfgResolver = r
	WebSearch.mu.Unlock()
}

// SetWebSearchKeyResolver is the legacy back-compat shim. Wraps a
// Tavily-only resolver into a WebSearchConfigResolver that pins
// provider=tavily.
//
// Deprecated: prefer SetWebSearchConfigResolver. Kept so external
// callers (and historical tests) keep working through the transition.
func SetWebSearchKeyResolver(r TavilyKeyResolver) {
	if r == nil {
		SetWebSearchConfigResolver(nil)
		return
	}
	SetWebSearchConfigResolver(legacyTavilyResolver{inner: r})
}

// legacyTavilyResolver adapts a Tavily-only resolver into the full
// config interface. Provider is pinned to "tavily" so existing setups
// keep their pre-SearXNG behaviour.
type legacyTavilyResolver struct {
	inner TavilyKeyResolver
}

func (l legacyTavilyResolver) Provider(_ context.Context) string { return providerTavily }
func (l legacyTavilyResolver) SearxngURL(_ context.Context) string {
	return defaultSearxngURL
}
func (l legacyTavilyResolver) TavilyAPIKey(ctx context.Context) string {
	if l.inner == nil {
		return ""
	}
	return l.inner.TavilyAPIKey(ctx)
}
func (l legacyTavilyResolver) BraveAPIKey(_ context.Context) string { return "" }

// SetWebSearchHTTPClient is a test seam for injecting an httptest server
// roundtripper or a fake client. nil resets to the default 30s client.
func SetWebSearchHTTPClient(c *http.Client) {
	WebSearch.mu.Lock()
	WebSearch.httpClient = c
	WebSearch.mu.Unlock()
}

// SetWebSearchEndpoint overrides the Tavily search URL. Used by tests
// pointing at httptest.NewServer.
//
// Deprecated: prefer SetWebSearchTavilyEndpoint.
func SetWebSearchEndpoint(u string) { SetWebSearchTavilyEndpoint(u) }

// SetWebSearchTavilyEndpoint overrides the Tavily search URL.
func SetWebSearchTavilyEndpoint(u string) {
	WebSearch.mu.Lock()
	WebSearch.tavilyEndpoint = u
	WebSearch.mu.Unlock()
}

// SetWebSearchBraveEndpoint overrides the Brave Search URL.
func SetWebSearchBraveEndpoint(u string) {
	WebSearch.mu.Lock()
	WebSearch.braveEndpoint = u
	WebSearch.mu.Unlock()
}

// webSearchSkill dispatches to one of three providers (SearXNG / Tavily
// / Brave). Manager-scoped (no edge involved); ClassSafe (read-only
// HTTP — the providers themselves are search APIs, no side effects).
type webSearchSkill struct {
	mu             sync.RWMutex
	tavilyEndpoint string
	braveEndpoint  string
	cfgResolver    WebSearchConfigResolver
	httpClient     *http.Client
	health         map[string]searchProviderHealth
}

// Metadata returns the framework-visible spec.
func (s *webSearchSkill) Metadata() skill.Metadata {
	return skill.Metadata{
		Key:         "web_search",
		Name:        "联网搜索",
		Description: "Search the public web and return top results with title + url + snippet. Check status and engine_failures: failed is not no_results; partial results remain usable. Cite returned URLs, prefer official sources, and use context to disambiguate short acronyms. Do not claim no public information exists after a failed search. Default provider is SearXNG (self-hosted, zero-key); operators may switch to Tavily or Brave Search via Settings → Integrations. When the provider supports it (Tavily) an auto-generated `answer` field is also returned.",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeManager,
		Category:    "web",
		Params: skill.ParamSchema{
			{Name: "query", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "搜索关键词（自然语言或精确字符串均可）",
			}},
			{Name: "max_results", Param: skill.Param{
				Type: "int", Default: 5,
				Desc: "返回结果上限，1~10",
			}},
			{Name: "include_domains", Param: skill.Param{
				Type: "string",
				Desc: "限定搜索域，逗号分隔，留空 = 不限（仅 Tavily 支持，其他 provider 忽略）",
			}},
			{Name: "exclude_domains", Param: skill.Param{
				Type: "string",
				Desc: "排除域，逗号分隔（仅 Tavily 支持）",
			}},
			{Name: "provider", Param: skill.Param{
				Type: "enum", Default: "",
				Enum: []string{"", providerSearxng, providerTavily, providerBrave},
				Desc: "强制使用哪个 provider；留空 = 走系统设置选择",
			}},
		},
		ResultPreview: "{provider,status:success|partial|no_results|failed,results:[{title,url,snippet,published_date}],engine_failures?,attempts?,skipped_reason?}",
	}
}

type webSearchParams struct {
	Query          string `json:"query"`
	MaxResults     int    `json:"max_results"`
	IncludeDomains string `json:"include_domains"`
	ExcludeDomains string `json:"exclude_domains"`
	Provider       string `json:"provider"`
}

// WebSearchResult is the shape we return to the LLM. We keep keys
// stable (snake_case) so prompt templates can refer to them.
type WebSearchResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	PublishedDate string `json:"published_date,omitempty"`
}

// webSearchResponse is the normalised wire shape. `provider` is always
// populated (= which backend actually ran); `answer` is only filled by
// providers that support it (Tavily). `skipped_reason` is set when the
// skill cannot run — empty results, no error.
type webSearchResponse struct {
	Provider       string            `json:"provider"`
	Results        []WebSearchResult `json:"results"`
	Answer         string            `json:"answer,omitempty"`
	SkippedReason  string            `json:"skipped_reason,omitempty"`
	Status         string            `json:"status"`
	EngineFailures [][]string        `json:"engine_failures,omitempty"`
	Attempts       []searchAttempt   `json:"attempts,omitempty"`
}

// Execute dispatches to the chosen provider and packages the result.
// On "not configured" / "unreachable" we return a skipped_reason
// envelope (NOT an error) so the LLM can tell the user how to fix it.
func (s *webSearchSkill) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	s.mu.RLock()
	tavilyURL := s.tavilyEndpoint
	braveURL := s.braveEndpoint
	resolver := s.cfgResolver
	client := s.httpClient
	s.mu.RUnlock()

	if tavilyURL == "" {
		tavilyURL = "https://api.tavily.com/search"
	}
	if braveURL == "" {
		braveURL = "https://api.search.brave.com/res/v1/web/search"
	}
	if client == nil {
		client = s.defaultSearchHTTPClient()
	}

	var p webSearchParams
	if len(bytes.TrimSpace(params)) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("web_search: decode params: %w", err)
		}
	}
	if p.Query == "" {
		return nil, errors.New("web_search: query required")
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 5
	}
	if p.MaxResults > 10 {
		p.MaxResults = 10
	}

	// Provider resolution: explicit param > resolver default > "searxng".
	provider := strings.ToLower(strings.TrimSpace(p.Provider))
	if provider == "" {
		if resolver != nil {
			provider = strings.ToLower(strings.TrimSpace(resolver.Provider(ctx)))
		}
	}
	if provider == "" {
		provider = providerSearxng
	}

	return s.searchWithPolicy(ctx, client, resolver, provider, tavilyURL, braveURL, p)
}

// ---- SearXNG sub-fn -------------------------------------------------

// searchSearxng calls the configured SearXNG instance's
// /search?q=...&format=json endpoint. No API key needed.
//
// Reachability failures (DNS / dial / timeout) become a skipped_reason
// envelope — operators usually hit this when the docker-compose stack
// hasn't been brought up with `up -d searxng`, and the LLM should
// nudge them through that rather than crashing the agent loop.
func (s *webSearchSkill) searchSearxng(
	ctx context.Context,
	client *http.Client,
	resolver WebSearchConfigResolver,
	p webSearchParams,
) (json.RawMessage, error) {
	base := defaultSearxngURL
	if resolver != nil {
		if u := strings.TrimRight(resolver.SearxngURL(ctx), "/"); u != "" {
			base = u
		}
	}

	// SearXNG accepts either GET or POST on /search. We use GET because
	// the upstream docker image's default settings.yml only enables GET
	// (method: "GET"), and a request-line query string survives
	// readonly bind mounts cleanly.
	q := url.Values{}
	q.Set("q", p.Query)
	q.Set("format", "json")
	q.Set("safesearch", "1")
	q.Set("pageno", "1")
	if policy, ok := resolver.(webSearchPolicyResolver); ok {
		if engines := policy.SearxngEngines(ctx); engines != "" {
			q.Set("engines", engines)
		}
	}
	full := base + "/search?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("web_search: build searxng request: %w", err)
	}
	// SearXNG checks Accept and rejects bot-like UAs. Set both.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ongrid-web-search/1.0 (+https://github.com/ongridio/ongrid)")

	resp, err := client.Do(req)
	if err != nil {
		// Reachability failure → skipped_reason, NOT a Go error. The
		// most common cause is the operator hasn't run `docker compose
		// up -d searxng` yet; surface it bluntly.
		return json.Marshal(webSearchResponse{
			Provider: providerSearxng,
			Results:  []WebSearchResult{},
			SkippedReason: fmt.Sprintf(
				"SearXNG 不可达 (%s) — 检查 docker-compose 是否启动 searxng 服务，或在设置 → 集成 → 联网搜索 改为 Tavily / Brave。原始错误: %v",
				"configured endpoint", safeSearchError(err),
			),
		})
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if readErr != nil {
		return nil, fmt.Errorf("web_search: read response: %w", readErr)
	}
	if len(respBody) > 4*1024*1024 {
		return nil, errors.New("web_search: response exceeds 4 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return json.Marshal(webSearchResponse{
			Provider: providerSearxng,
			Results:  []WebSearchResult{},
			SkippedReason: fmt.Sprintf(
				"SearXNG 返回 HTTP %d",
				resp.StatusCode,
			),
		})
	}

	var sr searxngResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return nil, fmt.Errorf("web_search: decode searxng response: %w", err)
	}
	out := webSearchResponse{
		Provider:       providerSearxng,
		Results:        make([]WebSearchResult, 0, len(sr.Results)),
		EngineFailures: sr.UnresponsiveEngines,
	}
	limit := p.MaxResults
	for i, r := range sr.Results {
		if i >= limit {
			break
		}
		out.Results = append(out.Results, WebSearchResult{
			Title:         r.Title,
			URL:           r.URL,
			Snippet:       r.Content,
			PublishedDate: r.PublishedDate,
		})
	}
	return json.Marshal(out)
}

// searxngResponse is the subset of SearXNG's JSON output we consume.
// The full upstream shape carries a lot more (suggestions, infoboxes,
// engine timings); preserve engine failures as well as results.
type searxngResponse struct {
	Query               string     `json:"query"`
	UnresponsiveEngines [][]string `json:"unresponsive_engines"`
	Results             []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"publishedDate,omitempty"`
	} `json:"results"`
}

// ---- Tavily sub-fn (existing logic, kept) --------------------------

func (s *webSearchSkill) searchTavily(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	resolver WebSearchConfigResolver,
	p webSearchParams,
) (json.RawMessage, error) {
	apiKey := ""
	if resolver != nil {
		apiKey = resolver.TavilyAPIKey(ctx)
	}
	if apiKey == "" {
		return json.Marshal(webSearchResponse{
			Provider:      providerTavily,
			SkippedReason: "Tavily API key 未配置。前往 设置 → 集成 → 联网搜索 选择 Tavily provider 并填入 key，或改用默认的 SearXNG。",
			Results:       []WebSearchResult{},
		})
	}

	body, _ := json.Marshal(tavilyRequest{
		APIKey:         apiKey,
		Query:          p.Query,
		MaxResults:     p.MaxResults,
		IncludeAnswer:  true,
		SearchDepth:    "basic",
		IncludeDomains: splitCSV(p.IncludeDomains),
		ExcludeDomains: splitCSV(p.ExcludeDomains),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("web_search: build tavily request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search: Tavily transport: %w", safeSearchError(err))
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if readErr != nil {
		return nil, fmt.Errorf("web_search: read response: %w", readErr)
	}
	if len(respBody) > 4*1024*1024 {
		return nil, errors.New("web_search: response exceeds 4 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("web_search: Tavily returned HTTP %d", resp.StatusCode)
	}

	var tr tavilyResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return nil, fmt.Errorf("web_search: decode Tavily response: %w", err)
	}
	out := webSearchResponse{
		Provider: providerTavily,
		Answer:   tr.Answer,
		Results:  make([]WebSearchResult, 0, len(tr.Results)),
	}
	for _, r := range tr.Results {
		out.Results = append(out.Results, WebSearchResult{
			Title:         r.Title,
			URL:           r.URL,
			Snippet:       r.Content,
			PublishedDate: r.PublishedDate,
		})
	}
	return json.Marshal(out)
}

// tavilyRequest is the JSON shape Tavily's /search expects.
type tavilyRequest struct {
	APIKey         string   `json:"api_key"`
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	IncludeAnswer  bool     `json:"include_answer,omitempty"`
	SearchDepth    string   `json:"search_depth,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

// tavilyResponse is the shape Tavily returns. We map a subset to
// WebSearchResult so swap providers without breaking downstream consumers.
type tavilyResponse struct {
	Answer  string `json:"answer"`
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
}

// ---- Brave sub-fn ---------------------------------------------------

// searchBrave calls Brave Search's web/search REST endpoint. The
// X-Subscription-Token header carries the API key (Brave's idiom; it's
// not a Bearer). No `answer` field — Brave is pure result links.
func (s *webSearchSkill) searchBrave(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	resolver WebSearchConfigResolver,
	p webSearchParams,
) (json.RawMessage, error) {
	apiKey := ""
	if resolver != nil {
		apiKey = resolver.BraveAPIKey(ctx)
	}
	if apiKey == "" {
		return json.Marshal(webSearchResponse{
			Provider:      providerBrave,
			SkippedReason: "Brave Search API key 未配置。前往 设置 → 集成 → 联网搜索 选择 Brave provider 并填入 key，或改用默认的 SearXNG。",
			Results:       []WebSearchResult{},
		})
	}

	q := url.Values{}
	q.Set("q", p.Query)
	q.Set("count", fmt.Sprintf("%d", p.MaxResults))
	full := endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("web_search: build brave request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search: Brave transport: %w", safeSearchError(err))
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if readErr != nil {
		return nil, fmt.Errorf("web_search: read response: %w", readErr)
	}
	if len(respBody) > 4*1024*1024 {
		return nil, errors.New("web_search: response exceeds 4 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("web_search: Brave returned HTTP %d", resp.StatusCode)
	}

	var br braveResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return nil, fmt.Errorf("web_search: decode Brave response: %w", err)
	}
	out := webSearchResponse{
		Provider: providerBrave,
		Results:  make([]WebSearchResult, 0, len(br.Web.Results)),
	}
	for _, r := range br.Web.Results {
		out.Results = append(out.Results, WebSearchResult{
			Title:         r.Title,
			URL:           r.URL,
			Snippet:       r.Description,
			PublishedDate: r.Age,
		})
	}
	return json.Marshal(out)
}

// braveResponse is the subset of Brave's web search response we consume.
// Top-level `web.results[]`; per-result `age` is a freeform string ("3
// days ago" / "2024-09-01") which we surface as published_date for
// LLM-visible parity with the other providers.
type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Age         string `json:"age"`
		} `json:"results"`
	} `json:"web"`
}

// ---- shared helpers ------------------------------------------------

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			seg := s[start:i]
			// trim ASCII spaces
			for len(seg) > 0 && (seg[0] == ' ' || seg[0] == '\t') {
				seg = seg[1:]
			}
			for len(seg) > 0 && (seg[len(seg)-1] == ' ' || seg[len(seg)-1] == '\t') {
				seg = seg[:len(seg)-1]
			}
			if seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	return out
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
