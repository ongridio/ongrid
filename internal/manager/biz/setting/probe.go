package setting

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
)

// LokiURLProbe is a URLProbe implementation that hits the configured
// Loki's /ready endpoint. Used by the Integrations card's "测试连接"
// button. Returns nil iff the URL is reachable, the auth header (if
// supplied) is accepted, and Loki returns 200/2xx.
//
// The 5s deadline is intentionally tight — operators expect the probe
// to either succeed quickly or fail with a clear "timed out" message;
// no point waiting for slow networks here when the real ingest path
// has its own retry budget.
type LokiURLProbe struct {
	resolver *LokiResolver
	timeout  time.Duration
}

type TelemetryProbeInput struct {
	URL           string `json:"url"`
	BasicUser     string `json:"basic_user"`
	BasicPassword string `json:"basic_password"`
	TLSInsecure   bool   `json:"tls_insecure"`
}

// NewLokiURLProbe wires a probe against the resolver. resolver must be
// non-nil; the probe Probe() returns ErrInvalid otherwise.
func NewLokiURLProbe(r *LokiResolver) *LokiURLProbe {
	return &LokiURLProbe{resolver: r, timeout: 5 * time.Second}
}

// Probe runs a GET <url>/ready against the configured Loki.
func (p *LokiURLProbe) Probe(ctx context.Context) error {
	if p == nil || p.resolver == nil {
		return fmt.Errorf("loki probe not wired")
	}
	user, pass := p.resolver.Auth(ctx)
	return p.ProbeConfiguration(ctx, TelemetryProbeInput{
		URL: p.resolver.URL(ctx), BasicUser: user, BasicPassword: pass, TLSInsecure: p.resolver.TLSInsecure(ctx),
	})
}

func (p *LokiURLProbe) ProbeConfiguration(ctx context.Context, in TelemetryProbeInput) error {
	if p == nil {
		return fmt.Errorf("loki probe not wired")
	}
	u := strings.TrimRight(strings.TrimSpace(in.URL), "/")
	if u == "" {
		return fmt.Errorf("loki url is empty")
	}
	if err := validateLLMBaseURL(u); err != nil {
		return fmt.Errorf("loki url: %w", err)
	}
	return probeReadyEndpoint(ctx, u+"/ready", in.BasicUser, in.BasicPassword, in.TLSInsecure, p.timeout)
}

// TempoURLProbe verifies the configured Tempo integration URL. Explicit
// OTLP/HTTP endpoints ending in /v1/traces receive an empty export request;
// the built-in/legacy query URL shape is checked through GET /ready.
type TempoURLProbe struct {
	resolver *TempoResolver
	timeout  time.Duration
}

// NewTempoURLProbe wires a probe against the resolver.
func NewTempoURLProbe(r *TempoResolver) *TempoURLProbe {
	return &TempoURLProbe{resolver: r, timeout: 5 * time.Second}
}

// Probe checks the configured Tempo URL without conflating its two listeners:
// /v1/traces is the OTLP/HTTP receiver, while a URL without that path denotes
// the Tempo query API and exposes /ready.
func (p *TempoURLProbe) Probe(ctx context.Context) error {
	if p == nil || p.resolver == nil {
		return fmt.Errorf("tempo probe not wired")
	}
	user, pass := p.resolver.Auth(ctx)
	return p.ProbeConfiguration(ctx, TelemetryProbeInput{
		URL: p.resolver.URL(ctx), BasicUser: user, BasicPassword: pass, TLSInsecure: p.resolver.TLSInsecure(ctx),
	})
}

func (p *TempoURLProbe) ProbeConfiguration(ctx context.Context, in TelemetryProbeInput) error {
	if p == nil {
		return fmt.Errorf("tempo probe not wired")
	}
	u := strings.TrimRight(strings.TrimSpace(in.URL), "/")
	if u == "" {
		return fmt.Errorf("tempo url is empty")
	}
	if err := validateLLMBaseURL(u); err != nil {
		return fmt.Errorf("tempo url: %w", err)
	}
	if strings.HasSuffix(u, "/v1/traces") {
		return probeOTLPHTTPTraceEndpoint(ctx, u, in.BasicUser, in.BasicPassword, in.TLSInsecure, p.timeout)
	}
	return probeReadyEndpoint(ctx, u+"/ready", in.BasicUser, in.BasicPassword, in.TLSInsecure, p.timeout)
}

// TempoReadinessProbe checks the manager-side Tempo query API. This URL is
// intentionally independent from TempoURLProbe's edge-facing OTLP endpoint:
// a standard Tempo deployment serves /ready on port 3200 and OTLP/HTTP on
// port 4318.
type TempoReadinessProbe struct {
	baseURL string
	timeout time.Duration
}

// NewTempoReadinessProbe builds a readiness probe for the Tempo query API.
func NewTempoReadinessProbe(baseURL string) *TempoReadinessProbe {
	return &TempoReadinessProbe{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		timeout: 5 * time.Second,
	}
}

// Probe runs GET <query-api-url>/ready.
func (p *TempoReadinessProbe) Probe(ctx context.Context) error {
	if p == nil || p.baseURL == "" {
		return fmt.Errorf("tempo query url is empty")
	}
	return probeReadyEndpoint(ctx, p.baseURL+"/ready", "", "", false, p.timeout)
}

// WebSearchProbe is the integration-handler-side probe for the
// manager-scoped web_search skill. It runs a tiny 1-result query
// against whichever provider is currently selected, returning the
// provider name + a sample title so the SPA's 测试连接 button can
// surface tangible confirmation.
//
// Implementation note: we deliberately re-issue the upstream HTTP call
// here rather than going through the skill registry — the registry
// path adds the agent-loop audit pipeline + JSON envelope that we
// don't want for an admin probe. The provider URLs / keys are read
// from the same WebSearchResolver the skill uses, so a successful
// probe means the skill itself will work.
type WebSearchProbe struct {
	resolver *WebSearchResolver
	timeout  time.Duration
}

type WebSearchProbeInput struct {
	Provider     string `json:"provider"`
	SearxngURL   string `json:"searxng_url"`
	TavilyAPIKey string `json:"tavily_api_key"`
	BraveAPIKey  string `json:"brave_api_key"`
}

// NewWebSearchProbe builds the probe. resolver must be non-nil; the
// probe Probe() returns an error otherwise.
func NewWebSearchProbe(r *WebSearchResolver) *WebSearchProbe {
	return &WebSearchProbe{resolver: r, timeout: 8 * time.Second}
}

// Probe runs a 1-result query against the selected provider. Returns
// (provider, sample-title, nil) on success; sample is the first
// result's title if any (empty when the provider returned zero hits).
func (p *WebSearchProbe) Probe(ctx context.Context) (string, string, error) {
	if p == nil || p.resolver == nil {
		return "", "", fmt.Errorf("web_search probe not wired")
	}
	return p.ProbeConfiguration(ctx, WebSearchProbeInput{
		Provider: p.resolver.Provider(ctx), SearxngURL: p.resolver.SearxngURL(ctx),
		TavilyAPIKey: p.resolver.TavilyAPIKey(ctx), BraveAPIKey: p.resolver.BraveAPIKey(ctx),
	})
}

func (p *WebSearchProbe) ProbeConfiguration(ctx context.Context, in WebSearchProbeInput) (string, string, error) {
	if p == nil {
		return "", "", fmt.Errorf("web_search probe not wired")
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	switch provider {
	case model.ProviderSearxng:
		return p.probeSearxng(cctx, in.SearxngURL)
	case model.ProviderTavily:
		return p.probeTavily(cctx, in.TavilyAPIKey)
	case model.ProviderBrave:
		return p.probeBrave(cctx, in.BraveAPIKey)
	default:
		return "", "", fmt.Errorf("unsupported web search provider %q", in.Provider)
	}
}

func (p *WebSearchProbe) probeSearxng(ctx context.Context, rawURL string) (string, string, error) {
	base := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if base == "" {
		base = model.DefaultSearxngURL
	}
	if err := validateLLMBaseURL(base); err != nil {
		return model.ProviderSearxng, "", fmt.Errorf("searxng url: %w", err)
	}
	q := url.Values{}
	q.Set("q", "ongrid web search probe")
	q.Set("format", "json")
	q.Set("safesearch", "1")
	q.Set("pageno", "1")
	full := base + "/search?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return model.ProviderSearxng, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ongrid-web-search-probe/1.0")
	resp, err := newHTTPClient(p.timeout, false).Do(req)
	if err != nil {
		return model.ProviderSearxng, "", fmt.Errorf("dial %s: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return model.ProviderSearxng, "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &out); err != nil {
		return model.ProviderSearxng, "", fmt.Errorf("decode: %w", err)
	}
	sample := ""
	if len(out.Results) > 0 {
		sample = out.Results[0].Title
	}
	return model.ProviderSearxng, sample, nil
}

func (p *WebSearchProbe) probeTavily(ctx context.Context, rawAPIKey string) (string, string, error) {
	apiKey := strings.TrimSpace(rawAPIKey)
	if apiKey == "" {
		return model.ProviderTavily, "", fmt.Errorf("tavily api key not configured")
	}
	body, _ := json.Marshal(map[string]any{
		"api_key":     apiKey,
		"query":       "ongrid web search probe",
		"max_results": 1,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.tavily.com/search", strings.NewReader(string(body)))
	if err != nil {
		return model.ProviderTavily, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := newHTTPClient(p.timeout, false).Do(req)
	if err != nil {
		return model.ProviderTavily, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return model.ProviderTavily, "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	var out struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(buf, &out); err != nil {
		return model.ProviderTavily, "", err
	}
	sample := ""
	if len(out.Results) > 0 {
		sample = out.Results[0].Title
	}
	return model.ProviderTavily, sample, nil
}

func (p *WebSearchProbe) probeBrave(ctx context.Context, rawAPIKey string) (string, string, error) {
	apiKey := strings.TrimSpace(rawAPIKey)
	if apiKey == "" {
		return model.ProviderBrave, "", fmt.Errorf("brave api key not configured")
	}
	q := url.Values{}
	q.Set("q", "ongrid web search probe")
	q.Set("count", "1")
	full := "https://api.search.brave.com/res/v1/web/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return model.ProviderBrave, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)
	resp, err := newHTTPClient(p.timeout, false).Do(req)
	if err != nil {
		return model.ProviderBrave, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return model.ProviderBrave, "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	var out struct {
		Web struct {
			Results []struct {
				Title string `json:"title"`
			} `json:"results"`
		} `json:"web"`
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(buf, &out); err != nil {
		return model.ProviderBrave, "", err
	}
	sample := ""
	if len(out.Web.Results) > 0 {
		sample = out.Web.Results[0].Title
	}
	return model.ProviderBrave, sample, nil
}

// newHTTPClient is a small helper for the probe's outbound calls. Kept
// local so probeReadyEndpoint's tighter shape isn't disturbed.
func newHTTPClient(timeout time.Duration, insecure bool) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // operator opt-in
		},
	}
}

// probeReadyEndpoint is the shared GET-/ready helper. We return a
// concise error string surfaceable in the UI: the body is at most
// 200 bytes so a 401 / 403 from a misconfigured auth gets shown
// verbatim, but a multi-MB Tempo dump never reaches the operator.
func probeReadyEndpoint(ctx context.Context, fullURL, user, pass string, insecure bool, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // operator opt-in
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("dial %s: %w", fullURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// probeOTLPHTTPTraceEndpoint verifies both transport reachability and the
// configured OTLP path. An empty ExportTraceServiceRequest is a valid protobuf
// message and Tempo accepts it without storing a trace.
func probeOTLPHTTPTraceEndpoint(ctx context.Context, fullURL, user, pass string, insecure bool, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, fullURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := newHTTPClient(timeout, insecure).Do(req)
	if err != nil {
		return fmt.Errorf("dial %s: %w", fullURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
