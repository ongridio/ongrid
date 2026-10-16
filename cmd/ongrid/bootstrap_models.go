package main

import (
	"context"
	"log/slog"

	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/prometheus/client_golang/prometheus"
)

type modelsModule struct {
	llmRouter           *llm.MultiClient
	llmEnvDefaults      map[string]managerbizsetting.EnvProviderDefaults
	llmSettingsResolver *managerbizsetting.LLMSettingsResolver
	llmClient           llm.Client
}

func bootstrapModels(rootCtx context.Context, cfg *config.Config, log *slog.Logger, reg *prometheus.Registry, settings *settingsModule) *modelsModule {
	// LLM client. Resolver lets admin edits to system_settings take effect
	// on the next Chat call (cache TTL = 60s) without a manager restart.
	// Empty resolver fields fall back to cfg.OpenAI.
	llmResolver := managerbizsetting.NewLLMResolver(settings.settingSvc)
	openaiClient := llm.NewWithResolver(
		llm.Config{APIKey: cfg.OpenAI.APIKey, Model: cfg.OpenAI.Model, BaseURL: cfg.OpenAI.BaseURL},
		llmResolver,
		nil, // BudgetChecker wired in Phase 2
		reg,
	)

	// Multi-provider router (ChatInput model selector). The OpenAI
	// sub-client uses the resolver-aware path so admin edits keep taking
	// effect; the other providers (Anthropic / Zhipu / Gemini /
	// DeepSeek / Kimi) seed from env here and then read live values via
	// the LLMSettingsResolver wired below, so /settings/llm edits
	// propagate within ~60s. A provider with empty APIKey is silently
	// dropped from the catalog so it never appears in the SPA selector.
	providerCfgs := []llm.ProviderConfig{}
	if cfg.OpenAI.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "openai", Label: "OpenAI",
			APIKey:  cfg.OpenAI.APIKey,
			Model:   firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"),
			BaseURL: cfg.OpenAI.BaseURL,
			Models:  dedupeModels(firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"), "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"),
		})
	}
	if cfg.LLM.Anthropic.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "anthropic", Label: "Anthropic",
			APIKey:  cfg.LLM.Anthropic.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Anthropic.Model, "claude-sonnet-4-6"),
			BaseURL: firstNonEmpty(cfg.LLM.Anthropic.BaseURL, "https://api.anthropic.com/v1"),
			Models:  cfg.LLM.Anthropic.Models,
		})
	}
	if cfg.LLM.Zhipu.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "zhipu", Label: "智谱 GLM",
			APIKey:  cfg.LLM.Zhipu.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Zhipu.Model, "glm-4.7"),
			BaseURL: firstNonEmpty(cfg.LLM.Zhipu.BaseURL, "https://open.bigmodel.cn/api/paas/v4"),
			Models:  cfg.LLM.Zhipu.Models,
		})
	}
	if cfg.LLM.Gemini.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "gemini", Label: "Gemini",
			APIKey:  cfg.LLM.Gemini.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Gemini.Model, "gemini-2.5-pro"),
			BaseURL: firstNonEmpty(cfg.LLM.Gemini.BaseURL, "https://generativelanguage.googleapis.com/v1beta/openai"),
			Models:  cfg.LLM.Gemini.Models,
		})
	}
	if cfg.LLM.DeepSeek.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "deepseek", Label: "DeepSeek",
			APIKey:  cfg.LLM.DeepSeek.APIKey,
			Model:   firstNonEmpty(cfg.LLM.DeepSeek.Model, "deepseek-v4-flash"),
			BaseURL: firstNonEmpty(cfg.LLM.DeepSeek.BaseURL, "https://api.deepseek.com/v1"),
			Models:  cfg.LLM.DeepSeek.Models,
		})
	}
	if cfg.LLM.Kimi.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "kimi", Label: "Kimi",
			APIKey:  cfg.LLM.Kimi.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Kimi.Model, "kimi-k2.6"),
			BaseURL: firstNonEmpty(cfg.LLM.Kimi.BaseURL, "https://api.moonshot.cn/v1"),
			Models:  cfg.LLM.Kimi.Models,
		})
	}
	if cfg.LLM.MiniMax.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "minimax", Label: "MiniMax",
			APIKey:  cfg.LLM.MiniMax.APIKey,
			Model:   firstNonEmpty(cfg.LLM.MiniMax.Model, "MiniMax-M2.7"),
			BaseURL: firstNonEmpty(cfg.LLM.MiniMax.BaseURL, "https://api.minimaxi.com/v1"),
			Models:  cfg.LLM.MiniMax.Models,
		})
	}
	if cfg.LLM.Xiaomi.APIKey != "" {
		providerCfgs = append(providerCfgs, llm.ProviderConfig{
			ID: "xiaomi", Label: "Xiaomi MiMo",
			APIKey:  cfg.LLM.Xiaomi.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Xiaomi.Model, "mimo-v2.5-pro"),
			BaseURL: firstNonEmpty(cfg.LLM.Xiaomi.BaseURL, "https://api.xiaomimimo.com/v1"),
			Models:  cfg.LLM.Xiaomi.Models,
		})
	}
	llmRouter := llm.NewMultiClient(providerCfgs, cfg.LLM.Default, openaiClient)

	// Seed per-provider LLM settings rows from env on first boot so the
	// 设置 → 集成 → LLM 模型 page has something to show out of the box.
	// SetIfAbsent honours prior admin edits across restarts. Models lists
	// are stored as JSON arrays (matches the on-the-wire contract used
	// by the integration handler).
	for _, seed := range []struct {
		key       string
		val       string
		sensitive bool
	}{
		// Anthropic
		{settingmodel.KeyAnthropicAPIKey, cfg.LLM.Anthropic.APIKey, true},
		{settingmodel.KeyAnthropicBaseURL, cfg.LLM.Anthropic.BaseURL, false},
		{settingmodel.KeyAnthropicDefaultModel, cfg.LLM.Anthropic.Model, false},
		// Zhipu
		{settingmodel.KeyZhipuAPIKey, cfg.LLM.Zhipu.APIKey, true},
		{settingmodel.KeyZhipuBaseURL, cfg.LLM.Zhipu.BaseURL, false},
		{settingmodel.KeyZhipuDefaultModel, cfg.LLM.Zhipu.Model, false},
		// Gemini
		{settingmodel.KeyGeminiAPIKey, cfg.LLM.Gemini.APIKey, true},
		{settingmodel.KeyGeminiBaseURL, cfg.LLM.Gemini.BaseURL, false},
		{settingmodel.KeyGeminiDefaultModel, cfg.LLM.Gemini.Model, false},
		// DeepSeek
		{settingmodel.KeyDeepSeekAPIKey, cfg.LLM.DeepSeek.APIKey, true},
		{settingmodel.KeyDeepSeekBaseURL, cfg.LLM.DeepSeek.BaseURL, false},
		{settingmodel.KeyDeepSeekDefaultModel, cfg.LLM.DeepSeek.Model, false},
		// Kimi (Moonshot)
		{settingmodel.KeyKimiAPIKey, cfg.LLM.Kimi.APIKey, true},
		{settingmodel.KeyKimiBaseURL, cfg.LLM.Kimi.BaseURL, false},
		{settingmodel.KeyKimiDefaultModel, cfg.LLM.Kimi.Model, false},
		// MiniMax
		{settingmodel.KeyMiniMaxAPIKey, cfg.LLM.MiniMax.APIKey, true},
		{settingmodel.KeyMiniMaxBaseURL, cfg.LLM.MiniMax.BaseURL, false},
		{settingmodel.KeyMiniMaxDefaultModel, cfg.LLM.MiniMax.Model, false},
		// Xiaomi MiMo
		{settingmodel.KeyXiaomiAPIKey, cfg.LLM.Xiaomi.APIKey, true},
		{settingmodel.KeyXiaomiBaseURL, cfg.LLM.Xiaomi.BaseURL, false},
		{settingmodel.KeyXiaomiDefaultModel, cfg.LLM.Xiaomi.Model, false},
		// OpenAI's _default_model expansion (the legacy
		// openai_api_key / openai_model / openai_base_url rows are
		// already seeded above).
		{settingmodel.KeyOpenAIDefaultModel, firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"), false},
		// Cluster-wide default provider hint.
		{settingmodel.KeyLLMDefaultProvider, cfg.LLM.Default, false},
	} {
		if err := settings.settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryLLM, seed.key, seed.val, seed.sensitive); err != nil {
			log.Warn("seed llm setting", slog.String("key", seed.key), slog.Any("err", err))
		}
	}
	for _, seed := range []struct {
		key  string
		list []string
	}{
		{settingmodel.KeyOpenAIModels, dedupeModels(firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"), "gpt-5.5", "gpt-5.4", "gpt-5.4-mini")},
		{settingmodel.KeyAnthropicModels, cfg.LLM.Anthropic.Models},
		{settingmodel.KeyZhipuModels, cfg.LLM.Zhipu.Models},
		{settingmodel.KeyGeminiModels, cfg.LLM.Gemini.Models},
		{settingmodel.KeyDeepSeekModels, cfg.LLM.DeepSeek.Models},
		{settingmodel.KeyKimiModels, cfg.LLM.Kimi.Models},
		{settingmodel.KeyMiniMaxModels, cfg.LLM.MiniMax.Models},
		{settingmodel.KeyXiaomiModels, cfg.LLM.Xiaomi.Models},
	} {
		if len(seed.list) == 0 {
			continue
		}
		raw, err := managerbizsetting.EncodeModelsList(seed.list)
		if err != nil {
			log.Warn("encode llm models list", slog.String("key", seed.key), slog.Any("err", err))
			continue
		}
		if err := settings.settingSvc.SetIfAbsent(rootCtx, settingmodel.CategoryLLM, seed.key, raw, false); err != nil {
			log.Warn("seed llm models list", slog.String("key", seed.key), slog.Any("err", err))
		}
	}

	// Wire the dynamic provider catalog. The resolver reads from the
	// same system_settings.llm.* rows the integration UI edits, so an
	// admin save propagates to the chat surface within ~60s without a
	// manager restart. Empty rows fall back to the env defaults below.
	llmEnvDefaults := map[string]managerbizsetting.EnvProviderDefaults{
		settingmodel.LLMProviderOpenAI: {
			Label:   "OpenAI",
			APIKey:  cfg.OpenAI.APIKey,
			Model:   firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"),
			BaseURL: cfg.OpenAI.BaseURL,
			Models:  dedupeModels(firstNonEmpty(cfg.OpenAI.Model, "gpt-5.4"), "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"),
		},
		settingmodel.LLMProviderAnthropic: {
			Label:   "Anthropic",
			APIKey:  cfg.LLM.Anthropic.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Anthropic.Model, "claude-sonnet-4-6"),
			BaseURL: firstNonEmpty(cfg.LLM.Anthropic.BaseURL, "https://api.anthropic.com/v1"),
			Models:  cfg.LLM.Anthropic.Models,
		},
		settingmodel.LLMProviderZhipu: {
			Label:   "智谱 GLM",
			APIKey:  cfg.LLM.Zhipu.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Zhipu.Model, "glm-4.7"),
			BaseURL: firstNonEmpty(cfg.LLM.Zhipu.BaseURL, "https://open.bigmodel.cn/api/paas/v4"),
			Models:  cfg.LLM.Zhipu.Models,
		},
		settingmodel.LLMProviderGemini: {
			Label:   "Gemini",
			APIKey:  cfg.LLM.Gemini.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Gemini.Model, "gemini-2.5-pro"),
			BaseURL: firstNonEmpty(cfg.LLM.Gemini.BaseURL, "https://generativelanguage.googleapis.com/v1beta/openai"),
			Models:  cfg.LLM.Gemini.Models,
		},
		settingmodel.LLMProviderDeepSeek: {
			Label:   "DeepSeek",
			APIKey:  cfg.LLM.DeepSeek.APIKey,
			Model:   firstNonEmpty(cfg.LLM.DeepSeek.Model, "deepseek-v4-flash"),
			BaseURL: firstNonEmpty(cfg.LLM.DeepSeek.BaseURL, "https://api.deepseek.com/v1"),
			Models:  cfg.LLM.DeepSeek.Models,
		},
		settingmodel.LLMProviderKimi: {
			Label:   "Kimi",
			APIKey:  cfg.LLM.Kimi.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Kimi.Model, "kimi-k2.6"),
			BaseURL: firstNonEmpty(cfg.LLM.Kimi.BaseURL, "https://api.moonshot.cn/v1"),
			Models:  cfg.LLM.Kimi.Models,
		},
		settingmodel.LLMProviderMiniMax: {
			Label:   "MiniMax",
			APIKey:  cfg.LLM.MiniMax.APIKey,
			Model:   firstNonEmpty(cfg.LLM.MiniMax.Model, "MiniMax-M2.7"),
			BaseURL: firstNonEmpty(cfg.LLM.MiniMax.BaseURL, "https://api.minimaxi.com/v1"),
			Models:  cfg.LLM.MiniMax.Models,
		},
		settingmodel.LLMProviderXiaomi: {
			Label:   "Xiaomi MiMo",
			APIKey:  cfg.LLM.Xiaomi.APIKey,
			Model:   firstNonEmpty(cfg.LLM.Xiaomi.Model, "mimo-v2.5-pro"),
			BaseURL: firstNonEmpty(cfg.LLM.Xiaomi.BaseURL, "https://api.xiaomimimo.com/v1"),
			Models:  cfg.LLM.Xiaomi.Models,
		},
	}
	llmSettingsResolver := managerbizsetting.NewLLMSettingsResolver(settings.settingSvc, llmEnvDefaults, cfg.LLM.Default)
	llmRouter.SetProvidersResolver(llmSettingsResolver)

	// All downstream agent/investigator wiring takes the router so a
	// per-request Provider override flows through; absent that, behaviour
	// matches the legacy single-provider path (router falls back to
	// openaiClient when no providers are configured).
	// Apply the live assistant timeout outside provider routing so every
	// configured provider gets the same default deadline. Explicit callers
	// (workflows/tools) keep their narrower or specialised deadlines.
	llmClient := llm.WithDefaultTimeout(llmRouter, settings.settingSvc.AgentLLMTimeout)

	return &modelsModule{
		llmRouter:           llmRouter,
		llmEnvDefaults:      llmEnvDefaults,
		llmSettingsResolver: llmSettingsResolver,
		llmClient:           llmClient,
	}
}

// firstNonEmpty returns the first non-empty string from its arguments,
// falling back to "" if all are empty. Used at the LLM provider wiring
// site to layer "config → env default → hard-coded default" without
// nesting ternaries.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func knownLLMProviderIDs() []string {
	return []string{
		llm.ProviderOpenAI,
		llm.ProviderAnthropic,
		llm.ProviderZhipu,
		llm.ProviderGemini,
		llm.ProviderDeepSeek,
		llm.ProviderKimi,
		llm.ProviderMiniMax,
		llm.ProviderXiaomi,
		llm.ProviderCustom,
	}
}

type llmProviderCatalog interface {
	Providers() []llm.ProviderInfo
}

func hasConfiguredLLMProvider(catalog llmProviderCatalog) bool {
	return catalog != nil && len(catalog.Providers()) > 0
}

// dedupeModels returns vals with empty strings dropped and duplicates
// removed, preserving first-seen order. The OpenAI model catalog is built
// as [configuredModel, "gpt-4o", "gpt-4-turbo"]; out-of-box the configured
// model defaults to "gpt-4o", which would otherwise list "gpt-4o" twice in
// the SPA model picker.
func dedupeModels(vals ...string) []string {
	seen := make(map[string]struct{}, len(vals))
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
