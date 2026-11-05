package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/zhipuauth"
)

const anthropicAPIVersion = "2026-10-21"

type CatalogModel struct {
	ID        string
	Reasoning *ReasoningCapability
}

// FetchModelCatalog keeps optional metadata that OpenAI-compatible SDK structs
// discard. Credentials stay on the configured origin; redirects are rejected.
func FetchModelCatalog(ctx context.Context, provider string, cfg Config) ([]CatalogModel, error) {
	baseURL := normalizeOpenAIBaseURL(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid model catalog URL")
	}
	transport := NewHTTPTransport(cfg.TLSInsecure)
	defer transport.CloseIdleConnections()
	var roundTripper http.RoundTripper = transport
	if zhipuauth.LooksLikeZhipuURL(baseURL) && zhipuauth.LooksLikeZhipuKey(cfg.APIKey) {
		roundTripper = &zhipuJWTTransport{apiKey: cfg.APIKey, base: roundTripper}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Transport: roundTripper, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("model catalog redirects are not allowed")
	}}
	models := make([]CatalogModel, 0)
	after := ""
	for page := 0; page < 4; page++ {
		if provider == ProviderAnthropic {
			query := u.Query()
			query.Set("limit", "1000")
			if after != "" {
				query.Set("after_id", after)
			}
			u.RawQuery = query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("build model catalog request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		req.Header.Set("Accept", "application/json")
		if provider == ProviderAnthropic {
			req.Header.Set("x-api-key", cfg.APIKey)
			req.Header.Set("anthropic-version", anthropicAPIVersion)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("model catalog request: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read model catalog: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close model catalog: %w", closeErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("model catalog returned HTTP %d", resp.StatusCode)
		}
		if len(raw) > 2<<20 {
			return nil, errors.New("model catalog exceeds 2 MiB")
		}
		var payload struct {
			Data    []catalogModelJSON `json:"data"`
			HasMore bool               `json:"has_more"`
			LastID  string             `json:"last_id"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, errors.New("invalid model catalog JSON")
		}
		if payload.Data == nil {
			return nil, errors.New("model catalog is missing data")
		}
		for _, item := range payload.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" || len(id) > 256 {
				continue
			}
			capability, err := item.reasoning(provider)
			if err != nil {
				return nil, fmt.Errorf("invalid model reasoning metadata: %w", err)
			}
			models = append(models, CatalogModel{ID: id, Reasoning: capability})
		}
		if !payload.HasMore {
			return models, nil
		}
		if provider != ProviderAnthropic || payload.LastID == "" || payload.LastID == after {
			return nil, errors.New("invalid model catalog pagination")
		}
		after = payload.LastID
	}
	return nil, errors.New("model catalog exceeds four pages")
}

type capabilitySupport struct {
	Supported bool `json:"supported"`
}
type catalogModelJSON struct {
	ID     string `json:"id"`
	Effort *struct {
		Levels       []string `json:"supported_levels"`
		DefaultLevel string   `json:"default_level"`
	} `json:"effort"`
	// Some OpenAI-compatible gateways publish this explicit extension.
	ReasoningEfforts *[]string `json:"supported_reasoning_efforts"`
	Capabilities     *struct {
		Effort *struct {
			Supported bool              `json:"supported"`
			Low       capabilitySupport `json:"low"`
			Medium    capabilitySupport `json:"medium"`
			High      capabilitySupport `json:"high"`
			Xhigh     capabilitySupport `json:"xhigh"`
			Max       capabilitySupport `json:"max"`
		} `json:"effort"`
		Thinking *struct {
			Supported bool `json:"supported"`
			Types     struct {
				Adaptive capabilitySupport `json:"adaptive"`
				Enabled  capabilitySupport `json:"enabled"`
				Disabled capabilitySupport `json:"disabled"`
			} `json:"types"`
		} `json:"thinking"`
	} `json:"capabilities"`
}

func (m catalogModelJSON) reasoning(provider string) (*ReasoningCapability, error) {
	c := BuiltinReasoning(provider, m.ID)
	c.Source, c.Levels = "provider", []string{}
	switch {
	case m.Effort != nil:
		if m.Effort.Levels == nil {
			return nil, errors.New("effort.supported_levels is missing")
		}
		c.Levels, c.DefaultLevel = m.Effort.Levels, m.Effort.DefaultLevel
		// DeepSeek documents this list as thinking-on levels, excluding none.
		if provider == ProviderDeepSeek && !slices.Contains(c.Levels, "none") {
			c.Levels = append([]string{"none"}, c.Levels...)
		}
	case m.ReasoningEfforts != nil:
		c.Levels = *m.ReasoningEfforts
	case m.Capabilities != nil && (m.Capabilities.Effort != nil || m.Capabilities.Thinking != nil):
		c.Mode = ReasoningClaudeMode
		if effort := m.Capabilities.Effort; effort != nil && effort.Supported {
			for _, level := range []struct {
				name      string
				supported bool
			}{
				{"low", effort.Low.Supported}, {"medium", effort.Medium.Supported}, {"high", effort.High.Supported}, {"xhigh", effort.Xhigh.Supported}, {"max", effort.Max.Supported},
			} {
				if level.supported {
					c.Levels = append(c.Levels, level.name)
				}
			}
		}
		if thinking := m.Capabilities.Thinking; thinking != nil {
			c.Thinking = ""
			if thinking.Supported {
				if thinking.Types.Adaptive.Supported {
					c.Thinking = "adaptive"
				} else if thinking.Types.Enabled.Supported {
					c.Thinking = "enabled"
				}
				if len(c.Levels) == 0 && c.Thinking != "" {
					c.Levels = append(c.Levels, "enabled")
				}
			}
			if thinking.Types.Disabled.Supported {
				c.Levels = append([]string{"none"}, c.Levels...)
			}
		}
	default:
		return nil, nil
	}
	if err := ValidateReasoningLevels(c.Levels); err != nil {
		return nil, err
	}
	if c.DefaultLevel != "" && !slices.Contains(c.Levels, c.DefaultLevel) {
		return nil, errors.New("default effort is not supported")
	}
	return &c, nil
}

type reasoningDiscoveryEntry struct {
	fingerprint  [32]byte
	capabilities map[string]ReasoningCapability
	nextFetch    time.Time
	fetching     chan struct{}
}

// ReasoningDiscovery keeps one bounded cache entry per configured provider.
// Failed refreshes preserve successful metadata and are retried after 5 minutes.
type ReasoningDiscovery struct {
	mu      sync.Mutex
	entries map[string]*reasoningDiscoveryEntry
}

func (d *ReasoningDiscovery) capabilities(ctx context.Context, p ProviderConfig) map[string]ReasoningCapability {
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%t", p.BaseURL, p.APIKey, p.TLSInsecure)))
	d.mu.Lock()
	if d.entries == nil {
		d.entries = make(map[string]*reasoningDiscoveryEntry)
	}
	entry := d.entries[p.ID]
	if entry == nil || entry.fingerprint != fingerprint {
		entry = &reasoningDiscoveryEntry{fingerprint: fingerprint}
		d.entries[p.ID] = entry
	}
	if time.Now().Before(entry.nextFetch) || (entry.fetching != nil && entry.capabilities != nil) {
		cached := entry.capabilities
		d.mu.Unlock()
		return cached
	}
	if entry.fetching != nil {
		done := entry.fetching
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-done:
		}
		d.mu.Lock()
		cached := entry.capabilities
		d.mu.Unlock()
		return cached
	}
	entry.fetching = make(chan struct{})
	d.mu.Unlock()
	models, err := FetchModelCatalog(ctx, p.ID, Config{APIKey: p.APIKey, BaseURL: p.BaseURL, TLSInsecure: p.TLSInsecure, Timeout: 5 * time.Second})
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		entry.capabilities = make(map[string]ReasoningCapability)
		for _, model := range models {
			if model.Reasoning != nil {
				entry.capabilities[model.ID] = *model.Reasoning
			}
		}
		entry.nextFetch = time.Now().Add(time.Hour + time.Duration(rand.IntN(300))*time.Second)
	} else {
		entry.nextFetch = time.Now().Add(5*time.Minute + time.Duration(rand.IntN(60))*time.Second)
		// Do not log upstream bodies, URLs or credentials.
		slog.Warn("reasoning catalog refresh failed; using cached or builtin capabilities", "provider", p.ID)
	}
	close(entry.fetching)
	entry.fetching = nil
	return entry.capabilities
}

func providerReasoning(p ProviderConfig, discovered map[string]ReasoningCapability) map[string]ReasoningCapability {
	capabilities := make(map[string]ReasoningCapability, len(p.Models)+1)
	for _, model := range append(slices.Clone(p.Models), p.Model) {
		if model != "" {
			capabilities[model] = ResolveReasoning(p.ID, model, discovered, p.ReasoningOverrides)
		}
	}
	return capabilities
}
