// Package llm — multi-provider routing.
//
// The MultiClient dispatches Chat requests to one of N pre-built sub-
// clients keyed by ChatReq.Provider. This is the implementation of the
// per-message provider/model selector (Anthropic / 智谱 / Gemini /
// OpenAI). Sub-clients are themselves *openaiClient instances — every
// supported provider exposes an OpenAI-compatible chat completions API
// (Anthropic via its OpenAI-compatible endpoint at
// api.anthropic.com/v1, Zhipu at open.bigmodel.cn/api/paas/v4, Gemini at
// generativelanguage.googleapis.com/v1beta/openai), so we route by
// (apiKey, baseURL) and keep the SDK uniform.
//
// Backwards compat: if a caller passes ChatReq.Provider == "" the router
// falls back to the default provider, which preserves the single-
// provider behaviour (just OpenAI today).
package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// ProviderConfig describes one configured upstream. Models is the
// closed-set of model slugs the operator wants to expose for this
// provider; Label is the human-readable name shown in the UI dropdown.
type ProviderConfig struct {
	ID      string   // stable id: "openai" | "anthropic" | "zhipu" | "gemini"
	Label   string   // display name
	APIKey  string   // empty → provider not configured (skipped at build)
	Model   string   // default model
	BaseURL string   // optional base URL override
	Models  []string // closed-set of allowed models for the UI selector
}

// ProviderInfo is the subset of ProviderConfig safe to leak through the
// HTTP /v1/aiops/models endpoint (no API key).
type ProviderInfo struct {
	ID     string
	Label  string
	Model  string
	Models []string
}

// ProvidersResolver supplies a fresh provider catalog at call time. The
// seam exists so admin-edited DB rows (system_settings.llm.*) flow into
// the router without a manager restart. The returned slice supersedes
// any constructor-time providers when set; a successful empty slice is an
// authoritative "no providers configured" result. Constructor providers are
// used only when no resolver exists or the resolver returns an error.
type ProvidersResolver interface {
	ResolveProviders(ctx context.Context) (providers []ProviderConfig, defaultProvider string, err error)
}

// MultiClient is a Client that fans Chat() out to a sub-client based on
// ChatReq.Provider. Sub-clients are built up-front from ProviderConfigs;
// callers add new providers via NewMultiClient. When a ProvidersResolver
// is wired (SetProvidersResolver), the catalog is refreshed lazily on
// each Chat / Providers / Default call (TTL cache so a slow resolver
// does not block hot paths).
type MultiClient struct {
	// Static provider set — built at construction. Used as the seed and
	// as the fallback when no resolver is wired.
	staticSubs  map[string]Client
	staticInfos []ProviderInfo
	staticDefID string
	fallback    Client

	// Dynamic provider set — repopulated from the resolver every resolveTTL.
	// A successful empty result remains authoritative; nil/error uses the
	// static set for backwards-compatible soft failure.
	resolver   ProvidersResolver
	resolveTTL time.Duration

	mu          sync.RWMutex
	dynSubs     map[string]Client
	dynInfos    []ProviderInfo
	dynDefID    string
	dynLoadedAt time.Time
	dynActive   bool // true after any successful resolver result, including empty
}

// NewMultiClient builds a router. Providers with empty APIKey are
// skipped (they stay invisible to /v1/aiops/models so the UI doesn't
// surface unusable options). The first non-skipped entry, OR the entry
// whose ID matches defaultProvider when set, is used as the default.
//
// fallback is the legacy single-provider client used when ChatReq.
// Provider is empty AND no default is configured. Pass the env-seeded
// OpenAI client here so existing callers (alert investigator, agent
// loop without explicit provider) keep working unchanged.
func NewMultiClient(providers []ProviderConfig, defaultProvider string, fallback Client) *MultiClient {
	mc := &MultiClient{
		staticSubs: make(map[string]Client, len(providers)),
		fallback:   fallback,
		resolveTTL: 60 * time.Second,
	}
	for _, p := range providers {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		sub := New(Config{APIKey: p.APIKey, Model: p.Model, BaseURL: p.BaseURL}, nil, nil)
		mc.staticSubs[p.ID] = sub
		models := p.Models
		if len(models) == 0 && p.Model != "" {
			models = []string{p.Model}
		}
		mc.staticInfos = append(mc.staticInfos, ProviderInfo{ID: p.ID, Label: p.Label, Model: p.Model, Models: models})
	}
	// Sort infos for stable JSON output.
	sort.Slice(mc.staticInfos, func(i, j int) bool { return mc.staticInfos[i].ID < mc.staticInfos[j].ID })

	if defaultProvider != "" {
		if _, ok := mc.staticSubs[defaultProvider]; ok {
			mc.staticDefID = defaultProvider
		}
	}
	if mc.staticDefID == "" && len(mc.staticInfos) > 0 {
		// Prefer the entry sorted first (deterministic) so tests don't flake.
		mc.staticDefID = mc.staticInfos[0].ID
	}
	return mc
}

// SetProvidersResolver wires a dynamic catalog source. Pass nil to clear.
// The resolver is queried lazily (TTL = 60s) on each Chat / Providers /
// Default call. A successful empty result disables the static set; only a
// resolver error falls back to constructor-time providers.
func (m *MultiClient) SetProvidersResolver(r ProvidersResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolver = r
	// Invalidate dynamic cache so the next call rebuilds.
	m.dynSubs = nil
	m.dynInfos = nil
	m.dynDefID = ""
	m.dynLoadedAt = time.Time{}
	m.dynActive = false
}

// SetResolveTTL overrides the dynamic-resolve cache TTL. Mainly used by
// tests; production code should leave the default.
func (m *MultiClient) SetResolveTTL(d time.Duration) {
	m.mu.Lock()
	m.resolveTTL = d
	m.mu.Unlock()
}

// activeSubs returns the in-effect catalog and whether the legacy fallback
// client may be used. A successful resolver result, including empty, is
// authoritative and disables the constructor-time fallback.
func (m *MultiClient) activeSubs(ctx context.Context) (map[string]Client, []ProviderInfo, string, bool) {
	m.mu.RLock()
	resolver := m.resolver
	ttl := m.resolveTTL
	loadedAt := m.dynLoadedAt
	dynActive := m.dynActive
	subs := m.dynSubs
	infos := m.dynInfos
	defID := m.dynDefID
	m.mu.RUnlock()

	if resolver == nil {
		return m.staticSubs, m.staticInfos, m.staticDefID, true
	}
	if dynActive && time.Since(loadedAt) < ttl {
		return subs, infos, defID, false
	}

	cfgs, def, err := resolver.ResolveProviders(ctx)
	if err != nil {
		// Soft-fail: fall back to static set, refresh the timestamp so a
		// flaky resolver doesn't hammer the DB.
		m.mu.Lock()
		m.dynLoadedAt = time.Now()
		m.dynActive = false
		m.dynSubs = nil
		m.dynInfos = nil
		m.dynDefID = ""
		m.mu.Unlock()
		return m.staticSubs, m.staticInfos, m.staticDefID, true
	}

	newSubs := make(map[string]Client, len(cfgs))
	newInfos := make([]ProviderInfo, 0, len(cfgs))
	for _, p := range cfgs {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		sub := New(Config{APIKey: p.APIKey, Model: p.Model, BaseURL: p.BaseURL}, nil, nil)
		newSubs[p.ID] = sub
		models := p.Models
		if len(models) == 0 && p.Model != "" {
			models = []string{p.Model}
		}
		newInfos = append(newInfos, ProviderInfo{ID: p.ID, Label: p.Label, Model: p.Model, Models: models})
	}
	sort.Slice(newInfos, func(i, j int) bool { return newInfos[i].ID < newInfos[j].ID })

	resolvedDef := def
	if resolvedDef != "" {
		if _, ok := newSubs[resolvedDef]; !ok {
			resolvedDef = ""
		}
	}
	if resolvedDef == "" && len(newInfos) > 0 {
		resolvedDef = newInfos[0].ID
	}

	m.mu.Lock()
	m.dynSubs = newSubs
	m.dynInfos = newInfos
	m.dynDefID = resolvedDef
	m.dynLoadedAt = time.Now()
	m.dynActive = true
	m.mu.Unlock()

	return newSubs, newInfos, resolvedDef, false
}

// Invalidate forces the next Chat / Providers / Default call to refresh from
// the resolver. Called after an atomic LLM configuration save so admin edits
// apply immediately rather than on the next 60s tick.
func (m *MultiClient) Invalidate() {
	m.mu.Lock()
	m.dynLoadedAt = time.Time{}
	m.dynActive = false
	m.dynSubs = nil
	m.dynInfos = nil
	m.dynDefID = ""
	m.mu.Unlock()
}

// Providers returns the currently-configured provider catalog.
// Read-only; safe to share.
func (m *MultiClient) Providers() []ProviderInfo {
	_, infos, _, _ := m.activeSubs(context.Background())
	out := make([]ProviderInfo, len(infos))
	copy(out, infos)
	return out
}

// Default returns the default (provider, model) pair. Empty strings
// when nothing is configured (caller should hide the model selector).
func (m *MultiClient) Default() (string, string) {
	_, infos, defID, _ := m.activeSubs(context.Background())
	if defID == "" {
		return "", ""
	}
	for _, p := range infos {
		if p.ID == defID {
			return p.ID, p.Model
		}
	}
	return defID, ""
}

// HasProvider reports whether id is wired.
func (m *MultiClient) HasProvider(id string) bool {
	if id == "" {
		return false
	}
	subs, _, _, _ := m.activeSubs(context.Background())
	_, ok := subs[id]
	return ok
}

// Chat routes the request to the sub-client matching req.Provider; an
// empty provider falls back to the default sub-client, then to the
// constructor-supplied fallback. Returns an error if neither is
// configured.
//
// Self-obs: records prom.ObserveLLMCall on every path (success, error,
// timeout, missing-provider) so the ADR-026 dashboards reflect even
// configuration errors (the LLM resolver provider/model mismatch bug
// 2026-05-16 was invisible because the legacy metrics inside the sub
// client tagged everything as "model=..." with no provider label).
func (m *MultiClient) Chat(ctx context.Context, req ChatReq) (*ChatResp, error) {
	subs, _, defID, allowFallback := m.activeSubs(ctx)
	id := strings.TrimSpace(req.Provider)
	if id == "" {
		id = defID
	}

	start := time.Now()
	var (
		resp *ChatResp
		err  error
	)

	switch {
	case id == "":
		if !allowFallback || m.fallback == nil {
			err = errors.New("llm: no providers configured")
		} else {
			resp, err = m.fallback.Chat(ctx, req)
		}
	default:
		sub, ok := subs[id]
		if !ok {
			err = fmt.Errorf("llm: provider %q not configured", id)
		} else {
			resp, err = sub.Chat(ctx, req)
		}
	}

	providerLabel := id
	if providerLabel == "" {
		providerLabel = "fallback"
	}
	modelLabel := strings.TrimSpace(req.Model)
	if modelLabel == "" {
		modelLabel = "(default)"
	}
	status := llmStatusFor(err)
	var inp, out int
	if resp != nil {
		inp = resp.Usage.PromptTokens
		out = resp.Usage.CompletionTokens
	}
	prom.ObserveLLMCall(providerLabel, modelLabel, status, time.Since(start).Seconds(), inp, out)
	return resp, err
}

// llmStatusFor maps an error into one of the bounded status labels the
// ADR-026 dashboards group by. timeout / rate_limited stay separate so
// operators can tell "provider slow" from "provider broken".
func llmStatusFor(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "429") {
		return "rate_limited"
	}
	return "error"
}

// ProviderInfoToWire is a small DTO helper used by the HTTP layer to
// shape the /v1/aiops/models response. Lives here so the wire shape is
// co-located with the router definition.
type ProviderInfoToWire struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Models []string `json:"models"`
	Model  string   `json:"model,omitempty"`
}

// AsWire renders the router's provider catalog into the JSON DTO the
// SPA expects.
func (m *MultiClient) AsWire() []ProviderInfoToWire {
	infos := m.Providers()
	out := make([]ProviderInfoToWire, 0, len(infos))
	for _, p := range infos {
		out = append(out, ProviderInfoToWire{
			ID:     p.ID,
			Label:  p.Label,
			Models: p.Models,
			Model:  p.Model,
		})
	}
	return out
}
