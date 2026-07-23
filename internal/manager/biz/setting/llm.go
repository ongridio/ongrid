package setting

import (
	"context"
	"encoding/json"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// LLMSettingsResolver shapes per-provider rows in system_settings.llm.*
// into the llm.ProvidersResolver contract (multi-provider routing).
//
// Layering: env-seeded defaults (passed in at construction) form the fallback;
// DB rows override per field. A present empty API-key row is authoritative and
// disables that provider, while an absent row still inherits the environment.
//
// Caching is the responsibility of the underlying setting.Service (60s
// TTL) and the llm.MultiClient (60s TTL on top); LLMSettingsResolver
// itself is stateless beyond the env defaults.
type LLMSettingsResolver struct {
	svc *Service

	// Env defaults — used when the matching DB row is absent. Non-credential
	// fields also keep their legacy empty-value fallback.
	defaults map[string]EnvProviderDefaults

	// envDefaultProvider is the env-seeded default provider id (e.g.
	// from ONGRID_LLM_DEFAULT_PROVIDER). Used when the DB has no
	// default_provider row.
	envDefaultProvider string
}

// EnvProviderDefaults is the env-seeded fallback for one provider. The
// router reads from these when the DB row is absent so existing
// deployments survive without an admin filling in the UI first.
type EnvProviderDefaults struct {
	Label   string   // "OpenAI" / "Anthropic" / "智谱 GLM" / "Gemini"
	APIKey  string   // env-seeded key (e.g. ONGRID_OPENAI_API_KEY)
	Model   string   // env-seeded default model
	BaseURL string   // env-seeded base URL
	Models  []string // env-seeded model list
}

// NewLLMSettingsResolver builds a resolver bound to the given setting
// service. defaults is the per-provider env-seeded fallback (keyed by
// provider id: "openai" / "anthropic" / "zhipu" / "gemini").
// envDefaultProvider is the env-seeded default provider id (empty =
// pick the first sorted provider, matching legacy behaviour).
func NewLLMSettingsResolver(svc *Service, defaults map[string]EnvProviderDefaults, envDefaultProvider string) *LLMSettingsResolver {
	return &LLMSettingsResolver{svc: svc, defaults: defaults, envDefaultProvider: envDefaultProvider}
}

// providerKeys bundles the per-provider settings keys into one struct so
// ResolveProviders can iterate uniformly.
type providerKeys struct {
	id           string
	label        string // display fallback when env defaults carry none (e.g. custom)
	apiKey       string
	baseURL      string
	models       string
	defaultModel string
	// legacyModelKey is the pre-2026-05 single-model key for OpenAI; only
	// non-empty for openai. Falls back to legacy when default_model row
	// is empty so old deployments still work.
	legacyModelKey string
}

func allProviderKeys() []providerKeys {
	return []providerKeys{
		{
			id:             model.LLMProviderOpenAI,
			apiKey:         model.KeyOpenAIAPIKey,
			baseURL:        model.KeyOpenAIBaseURL,
			models:         model.KeyOpenAIModels,
			defaultModel:   model.KeyOpenAIDefaultModel,
			legacyModelKey: model.KeyOpenAIModel,
		},
		{
			id:           model.LLMProviderAnthropic,
			apiKey:       model.KeyAnthropicAPIKey,
			baseURL:      model.KeyAnthropicBaseURL,
			models:       model.KeyAnthropicModels,
			defaultModel: model.KeyAnthropicDefaultModel,
		},
		{
			id:           model.LLMProviderZhipu,
			apiKey:       model.KeyZhipuAPIKey,
			baseURL:      model.KeyZhipuBaseURL,
			models:       model.KeyZhipuModels,
			defaultModel: model.KeyZhipuDefaultModel,
		},
		{
			id:           model.LLMProviderGemini,
			apiKey:       model.KeyGeminiAPIKey,
			baseURL:      model.KeyGeminiBaseURL,
			models:       model.KeyGeminiModels,
			defaultModel: model.KeyGeminiDefaultModel,
		},
		{
			id:           model.LLMProviderDeepSeek,
			apiKey:       model.KeyDeepSeekAPIKey,
			baseURL:      model.KeyDeepSeekBaseURL,
			models:       model.KeyDeepSeekModels,
			defaultModel: model.KeyDeepSeekDefaultModel,
		},
		{
			id:           model.LLMProviderKimi,
			apiKey:       model.KeyKimiAPIKey,
			baseURL:      model.KeyKimiBaseURL,
			models:       model.KeyKimiModels,
			defaultModel: model.KeyKimiDefaultModel,
		},
		{
			id:           model.LLMProviderCustom,
			label:        "Custom",
			apiKey:       model.KeyCustomAPIKey,
			baseURL:      model.KeyCustomBaseURL,
			models:       model.KeyCustomModels,
			defaultModel: model.KeyCustomDefaultModel,
		},
	}
}

// ResolveProviders implements llm.ProvidersResolver. Returns a fresh
// catalog every call (the router caches the result for 60s; the
// underlying setting.Service caches for 60s as well). On a transient DB
// error any single provider may fall back to its env defaults, but a
// global error is rare — Get returns (val, found, err) and treats
// "row absent" as found=false rather than err. An empty providers slice is an
// authoritative no-provider catalog so explicit disable overrides are honored.
func (r *LLMSettingsResolver) ResolveProviders(ctx context.Context) ([]llm.ProviderConfig, string, error) {
	if r == nil || r.svc == nil {
		return nil, "", nil
	}
	out := make([]llm.ProviderConfig, 0, 4)
	for _, pk := range allProviderKeys() {
		def := r.defaults[pk.id]
		apiKey, apiKeyFound, apiKeyErr := r.svc.Get(ctx, model.CategoryLLM, pk.apiKey)
		if apiKeyErr != nil || !apiKeyFound {
			apiKey = def.APIKey
		}
		if strings.TrimSpace(apiKey) == "" {
			// Skip — provider not configured anywhere.
			continue
		}
		baseURL, _, _ := r.svc.Get(ctx, model.CategoryLLM, pk.baseURL)
		if strings.TrimSpace(baseURL) == "" {
			baseURL = def.BaseURL
		}
		// A custom provider has no default endpoint — without a base URL the
		// SDK would silently fall back to OpenAI's, sending the operator's key
		// to the wrong host. Skip until a base URL is supplied.
		if pk.id == model.LLMProviderCustom && strings.TrimSpace(baseURL) == "" {
			continue
		}

		// Model list: DB JSON wins; fall back to env defaults; fall back
		// to single legacy model row (openai only) so the openai legacy
		// path keeps working.
		models := []string{}
		if raw, _, _ := r.svc.Get(ctx, model.CategoryLLM, pk.models); strings.TrimSpace(raw) != "" {
			parsed, err := decodeModelsList(raw)
			if err == nil {
				models = parsed
			}
		}
		if len(models) == 0 {
			models = append(models, def.Models...)
		}
		// Drop duplicates so the SPA model picker never shows the same
		// model twice. Out-of-box the OpenAI list was seeded
		// [gpt-4o, gpt-4o, gpt-4-turbo] (the configured-model slot defaults
		// to gpt-4o, which the base list already carries); deduping here
		// heals existing installs at read time and guards any operator list
		// with accidental repeats.
		models = dedupeStrings(models)

		// Default model: DB row > env default > first model in list.
		defaultModel, _, _ := r.svc.Get(ctx, model.CategoryLLM, pk.defaultModel)
		defaultModel = strings.TrimSpace(defaultModel)
		if defaultModel == "" && pk.legacyModelKey != "" {
			// Honour the legacy openai_model row that pre-dates the
			// per-provider expansion.
			legacy, _, _ := r.svc.Get(ctx, model.CategoryLLM, pk.legacyModelKey)
			defaultModel = strings.TrimSpace(legacy)
		}
		if defaultModel == "" {
			defaultModel = strings.TrimSpace(def.Model)
		}
		if defaultModel == "" && len(models) > 0 {
			defaultModel = models[0]
		}
		if defaultModel != "" && !containsString(models, defaultModel) {
			// Make sure the default appears in the catalog so the SPA
			// dropdown can highlight it.
			models = append([]string{defaultModel}, models...)
		}
		// Dedup while preserving order.
		models = dedupStrings(models)

		label := def.Label
		if strings.TrimSpace(label) == "" {
			label = pk.label // env defaults carry no label for custom
		}
		out = append(out, llm.ProviderConfig{
			ID:      pk.id,
			Label:   label,
			APIKey:  apiKey,
			Model:   defaultModel,
			BaseURL: baseURL,
			Models:  models,
		})
	}

	// Default provider: DB > env > "" (router picks first sorted).
	dbDefault, _, _ := r.svc.Get(ctx, model.CategoryLLM, model.KeyLLMDefaultProvider)
	def := strings.TrimSpace(dbDefault)
	if def == "" {
		def = strings.TrimSpace(r.envDefaultProvider)
	}
	return out, def, nil
}

// EncodeModelsList serialises a closed-set of model slugs into the JSON
// shape stored in system_settings.llm.<provider>_models. Order is
// preserved verbatim so the SPA's "default" pin doesn't get reshuffled
// across saves.
func EncodeModelsList(models []string) (string, error) {
	if models == nil {
		models = []string{}
	}
	b, err := json.Marshal(models)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// dedupeStrings drops empty + duplicate entries, preserving first-seen
// order. Used so the resolved model list never carries the same model id
// twice (the SPA picker renders one row per entry).
func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func decodeModelsList(raw string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	cleaned := make([]string, 0, len(out))
	for _, m := range out {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		cleaned = append(cleaned, m)
	}
	return cleaned, nil
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
