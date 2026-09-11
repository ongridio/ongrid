package tracequery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	oteltracing "github.com/ongridio/ongrid/internal/pkg/tracing"
)

// SearchResult is the unmarshalled `data` field from a Tempo /api/search
// response. We expose `Traces` as raw JSON so the AI tool can hand it
// straight back to the model without losing any field shape (Tempo evolves
// its trace summary schema across versions).
type SearchResult struct {
	Traces  json.RawMessage `json:"traces"`
	Metrics json.RawMessage `json:"metrics,omitempty"`
}

// TraceResult is the unmarshalled response from /api/traces/<id>. Tempo
// returns OTLP-shaped JSON (resourceSpans / scopeSpans / spans). We pass
// it through as raw JSON for the same reason as SearchResult.
type TraceResult struct {
	Body json.RawMessage
}

// BaseURLResolver returns the current Tempo API root. It's invoked once
// per call so admin UI edits propagate without restart. Mirrors
// promquery.BaseURLResolver — same wiring layer (biz/setting) is reused.
type BaseURLResolver interface {
	ResolveBaseURL(ctx context.Context) (string, error)
}

// Client wraps Tempo's /api/search + /api/traces/<id>. Safe for concurrent
// use.
type Client struct {
	base       BaseURLResolver
	httpClient *http.Client
	log        *slog.Logger
}

// defaultTimeout caps a single Tempo round-trip. Tempo's search can be slow
// on cold blocks (filesystem backend); 30s matches what we use for promquery.
const defaultTimeout = 30 * time.Second

type staticBase struct{ url string }

func (s staticBase) ResolveBaseURL(_ context.Context) (string, error) {
	if s.url == "" {
		return "", errors.New("tracequery: baseURL is empty")
	}
	return s.url, nil
}

// New builds a Client with the default http.Client and a derived logger.
// baseURL is the Tempo HTTP listener root (e.g. "http://tempo:3200" — the
// default of $ONGRID_TRACE_QUERY_URL); the /api/* suffix is appended on
// each call.
func New(baseURL string, log *slog.Logger) *Client {
	hc := &http.Client{Timeout: defaultTimeout}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport := base.Clone()
		// Error grouping reads many trace IDs in parallel. The default idle
		// pool of two connections otherwise discards most reusable sockets.
		transport.MaxIdleConnsPerHost = 16
		hc.Transport = transport
	}
	return NewWithHTTPClient(baseURL, hc, log)
}

// NewWithHTTPClient is the test seam for the static-baseURL form.
func NewWithHTTPClient(baseURL string, hc *http.Client, log *slog.Logger) *Client {
	return newClient(staticBase{url: strings.TrimRight(baseURL, "/")}, hc, log)
}

// NewWithResolverAndHTTPClient is the dynamic form. The resolver is asked
// for the current base URL on each call.
func NewWithResolverAndHTTPClient(r BaseURLResolver, hc *http.Client, log *slog.Logger) *Client {
	return newClient(r, hc, log)
}

func newClient(r BaseURLResolver, hc *http.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	hc = oteltracing.InstrumentHTTPClient(hc, "tempo")
	return &Client{base: r, httpClient: hc, log: log}
}

// SearchOptions narrows a /api/search call. All fields are optional; if
// Query is set it takes precedence and is forwarded as `q=` (TraceQL); if
// Tags is set we forward each as `tags=` repeated. Limit + lookback are
// always sent.
type SearchOptions struct {
	// Query is a TraceQL expression (e.g. `{ resource.service.name = "web" && duration > 200ms }`).
	Query string
	// Tags is a flat key/value match for legacy non-TraceQL search. Joined
	// as `key1=v1 key2=v2`. Ignored if Query is set.
	Tags map[string]string
	// Limit caps result count. Tempo defaults to 20 when omitted; we send
	// 100 to give the AI more material to reason over.
	Limit int
	// Start / End define the search window in absolute time. When both are
	// zero the call falls back to Tempo's default lookback.
	Start time.Time
	End   time.Time
	// MinDuration / MaxDuration filter by span duration. Zero = unset.
	MinDuration time.Duration
	MaxDuration time.Duration
}

// SearchTraces runs a Tempo trace summary search.
func (c *Client) SearchTraces(ctx context.Context, opts SearchOptions) (*SearchResult, error) {
	q := url.Values{}
	if opts.Query != "" {
		q.Set("q", opts.Query)
	} else if len(opts.Tags) > 0 {
		// Tempo legacy `tags=key1=v1 key2=v2` form. Stable ordering not
		// required by the API but cleaner for tests.
		parts := make([]string, 0, len(opts.Tags))
		for k, v := range opts.Tags {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
		q.Set("tags", strings.Join(parts, " "))
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	q.Set("limit", fmt.Sprintf("%d", limit))
	if !opts.Start.IsZero() {
		q.Set("start", fmt.Sprintf("%d", opts.Start.Unix()))
	}
	if !opts.End.IsZero() {
		q.Set("end", fmt.Sprintf("%d", opts.End.Unix()))
	}
	if opts.MinDuration > 0 {
		q.Set("minDuration", opts.MinDuration.String())
	}
	if opts.MaxDuration > 0 {
		q.Set("maxDuration", opts.MaxDuration.String())
	}

	body, err := c.do(ctx, "/api/search", q)
	if err != nil {
		return nil, err
	}
	var sr SearchResult
	// Tempo returns {"traces":[...], "metrics":{...}}. Tolerate absence of
	// metrics block (older versions).
	if err := json.Unmarshal(body, &sr); err != nil {
		// Some Tempo versions return a bare array or a wrapped form when
		// nothing matches; preserve the raw body in that case so the
		// caller can still inspect it.
		sr.Traces = body
	}
	return &sr, nil
}

// TagValues lists known values for a single Tempo tag (e.g. service.name,
// name). Used by the SPA's Traces page to populate the service /
// operation dropdowns. Mirrors logquery.LabelValues — same role for
// the trace signal.
//
// Tempo's response shape is `{"tagValues":[...]}` for the v1 endpoint
// (`/api/search/tag/<tag>/values`); newer versions also expose
// `/api/v2/search/tag/<tag>/values` with a richer payload — we stick to
// v1 because that's what Tempo 2.x stable serves and the value list is
// all the UI needs.
func (c *Client) TagValues(ctx context.Context, tag string) ([]string, error) {
	if tag == "" {
		return nil, errors.New("tracequery: tag is empty")
	}
	path := "/api/search/tag/" + url.PathEscape(tag) + "/values"
	body, err := c.do(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		TagValues []string `json:"tagValues"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("tracequery: decode: %w", err)
	}
	return env.TagValues, nil
}

// GetTrace fetches a single trace by ID.
func (c *Client) GetTrace(ctx context.Context, traceID string) (*TraceResult, error) {
	if traceID == "" {
		return nil, errors.New("tracequery: traceID is empty")
	}
	// /api/traces/<id> — id is path-encoded; Tempo accepts hex or
	// 0x-prefixed forms. We don't validate format here so callers can
	// surface Tempo's own 4xx error message verbatim.
	path := "/api/traces/" + url.PathEscape(traceID)
	body, err := c.do(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	return &TraceResult{Body: body}, nil
}

// do builds the GET, returns the raw response body. Tempo doesn't wrap its
// responses in a status envelope (unlike Prometheus), so we return the
// JSON body straight to the caller and surface non-2xx as an error.
func (c *Client) do(ctx context.Context, path string, q url.Values) ([]byte, error) {
	base, rerr := c.base.ResolveBaseURL(ctx)
	if rerr != nil {
		return nil, fmt.Errorf("tracequery: resolve baseURL: %w", rerr)
	}
	full := base + path
	if q != nil && len(q) > 0 {
		full += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("tracequery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ongrid-tracequery/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tracequery: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024)) // 16 MiB cap (single trace can be big)
	if err != nil {
		return nil, fmt.Errorf("tracequery: read body: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("tracequery: %s: not found", path)
	}
	if resp.StatusCode != http.StatusOK {
		c.log.WarnContext(ctx, "tracequery: non-200",
			slog.Int("status", resp.StatusCode),
			slog.String("path", path),
		)
		return nil, fmt.Errorf("tracequery: %s returned %d: %s", path, resp.StatusCode, truncate(string(body), 512))
	}
	return body, nil
}

// truncate keeps error messages bounded so they don't bloat logs / chat
// context with multi-MB Tempo error pages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
