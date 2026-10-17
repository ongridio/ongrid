package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Optional interface keeps existing integrations compatible. A configured key
// alone never authorizes fallback: the administrator must select the provider.
type webSearchPolicyResolver interface {
	FallbackProvider(context.Context) string
	SearxngEngines(context.Context) string
	SearchTimeouts(context.Context) (time.Duration, time.Duration)
}

type searchAttempt struct {
	Provider       string     `json:"provider"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	EngineFailures [][]string `json:"engine_failures,omitempty"`
}

type searchProviderHealth struct {
	fingerprint [32]byte
	failures    int
	retryAt     time.Time
}

func safeSearchError(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return u.Err
	}
	return err
}

func (s *webSearchSkill) searchWithPolicy(ctx context.Context, client *http.Client, resolver WebSearchConfigResolver, primary, tavilyURL, braveURL string, p webSearchParams) (json.RawMessage, error) {
	providers := []string{primary}
	perAttempt, total := 20*time.Second, 45*time.Second
	engines := ""
	if policy, ok := resolver.(webSearchPolicyResolver); ok {
		fallback := policy.FallbackProvider(ctx)
		// An explicit override to a different provider stays pinned. Explicitly
		// naming the administrator-selected primary still uses its backup.
		if (p.Provider == "" || primary == resolver.Provider(ctx)) && fallback != "" && fallback != primary {
			providers = append(providers, fallback)
		}
		engines = policy.SearxngEngines(ctx)
		perAttempt, total = policy.SearchTimeouts(ctx)
	}
	if perAttempt <= 0 || total <= 0 {
		return nil, errors.New("web_search: invalid timeout policy")
	}
	for _, provider := range providers {
		if provider != providerSearxng && provider != providerTavily && provider != providerBrave {
			return nil, fmt.Errorf("web_search: unknown provider %q", provider)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	attempts := make([]searchAttempt, 0, len(providers))
	var last webSearchResponse
	var lastErr error
	var empty *webSearchResponse
	for _, provider := range providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		config := provider + "|" + engines + "|" + tavilyURL + "|" + braveURL
		if resolver != nil {
			config += "|" + resolver.SearxngURL(ctx) + "|" + resolver.TavilyAPIKey(ctx) + "|" + resolver.BraveAPIKey(ctx)
		}
		fingerprint := sha256.Sum256([]byte(config))
		if !s.allowSearch(provider, fingerprint, time.Now()) {
			attempts = append(attempts, searchAttempt{Provider: provider, Status: "failed", Reason: "cooldown"})
			last = webSearchResponse{Provider: provider, Status: "failed", Results: []WebSearchResult{}, SkippedReason: "Search provider temporarily unavailable; retry after cooldown."}
			continue
		}
		cctx, stop := context.WithTimeout(ctx, perAttempt)
		var raw json.RawMessage
		var err error
		switch provider {
		case providerSearxng:
			raw, err = s.searchSearxng(cctx, client, resolver, p)
		case providerTavily:
			raw, err = s.searchTavily(cctx, client, tavilyURL, resolver, p)
		case providerBrave:
			raw, err = s.searchBrave(cctx, client, braveURL, resolver, p)
		}
		stop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		last = webSearchResponse{Provider: provider, Results: []WebSearchResult{}}
		if err == nil {
			err = json.Unmarshal(raw, &last)
		}
		lastErr = err
		reason := ""
		if err != nil {
			last.Status = "failed"
			reason = "upstream_error"
		} else {
			last.Results = uniqueSearchResults(last.Results, p.MaxResults)
			switch {
			case len(last.Results) > 0:
				last.Status = "success"
				if len(last.EngineFailures) > 0 {
					last.Status = "partial"
				}
			case last.SkippedReason != "" || len(last.EngineFailures) > 0:
				last.Status = "failed"
				reason = "upstream_unavailable"
				if last.SkippedReason == "" {
					last.SkippedReason = "Search engines failed; empty results do not mean no matching information exists."
				}
			default:
				last.Status = "no_results"
			}
		}
		s.recordSearch(provider, fingerprint, last.Status == "failed", time.Now())
		attempts = append(attempts, searchAttempt{Provider: provider, Status: last.Status, Reason: reason, EngineFailures: last.EngineFailures})
		if last.Status == "success" || last.Status == "partial" {
			if len(attempts) > 1 {
				last.Status = "partial"
			}
			last.Attempts = attempts
			return json.Marshal(last)
		}
		if last.Status == "no_results" {
			cp := last
			empty = &cp
		}
	}
	// A successful empty response is distinct from a total outage, even if the
	// optional cross-check failed. Attempts retain that limitation for the model.
	if empty != nil {
		last = *empty
		lastErr = nil
	}
	last.Attempts = attempts
	if lastErr != nil {
		return nil, fmt.Errorf("web_search: configured providers failed: %w", lastErr)
	}
	return json.Marshal(last)
}

func (s *webSearchSkill) allowSearch(provider string, fingerprint [32]byte, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health == nil {
		s.health = make(map[string]searchProviderHealth)
	}
	h := s.health[provider]
	if h.fingerprint != fingerprint {
		s.health[provider] = searchProviderHealth{fingerprint: fingerprint}
		return true
	}
	if !h.retryAt.IsZero() && now.Before(h.retryAt) {
		return false
	}
	// After cooldown admit one probe; concurrent callers use the backup.
	if h.failures >= 3 {
		h.retryAt = now.Add(time.Minute)
		s.health[provider] = h
	}
	return true
}

func (s *webSearchSkill) recordSearch(provider string, fingerprint [32]byte, failed bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.health[provider]
	if h.fingerprint != fingerprint {
		return
	}
	if failed {
		h.failures++
		if h.failures >= 3 {
			h.retryAt = now.Add(time.Minute)
		}
	} else {
		h.failures = 0
		h.retryAt = time.Time{}
	}
	s.health[provider] = h
}

func uniqueSearchResults(in []WebSearchResult, limit int) []WebSearchResult {
	out := make([]WebSearchResult, 0, len(in))
	seen := make(map[string]bool)
	for _, result := range in {
		u, err := url.Parse(result.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.TrimSpace(result.Title) == "" {
			continue
		}
		u.Fragment = ""
		key := u.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, result)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Shared per-skill transport separates connection establishment from the
// configurable request and total budgets, and reuses idle connections.
func (s *webSearchSkill) defaultSearchHTTPClient() *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		transport.TLSHandshakeTimeout = 5 * time.Second
		s.httpClient = &http.Client{Transport: transport}
	}
	return s.httpClient
}
