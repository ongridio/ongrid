package llm

import "strings"

// ModelLimits is optional metadata from the configured provider's model catalog.
type ModelLimits struct {
	Window    int `json:"window"`
	MaxOutput int `json:"max_output,omitempty"`
}

// ResolveContextBudget is shared by request preflight and the model picker.
// The configured output cap is retained, but cannot exceed the model's limit.
func ResolveContextBudget(provider, model string, fallback ContextBudget, discovered *ModelLimits) ContextBudget {
	if fallback.Window == 0 {
		fallback.Window = 131072
	}
	if fallback.Output == 0 {
		fallback.Output = 8192
	}
	fallback.Source = "global"
	limits := builtinModelLimits(provider, model)
	if limits != nil {
		fallback.Source = "builtin"
	}
	if discovered != nil && validModelLimits(*discovered) {
		limits = discovered
		fallback.Source = "provider"
	}
	if limits != nil {
		fallback.Window = limits.Window
		if limits.MaxOutput > 0 {
			fallback.Output = min(fallback.Output, limits.MaxOutput)
		}
	}
	return fallback
}

func validModelLimits(limits ModelLimits) bool {
	return limits.Window > 0 && limits.Window <= 16<<20 && limits.MaxOutput >= 0 && limits.MaxOutput <= limits.Window
}

func builtinModelLimits(provider, model string) *ModelLimits {
	// Exact names and dated snapshots only; unknown variants use global config.
	// Sources and verification date are recorded in docs/install/tool-results.md.
	for _, rule := range []struct {
		provider, models string
		window, output   int
	}{
		{ProviderOpenAI, "gpt-6-astra gpt-6.1-sol gpt-6.1-terra gpt-5.4", 1050000, 128000},
		{ProviderOpenAI, "gpt-4.1", 1047576, 32768},
		{ProviderOpenAI, "gpt-4o gpt-4o-mini", 128000, 16384},
		{ProviderAnthropic, "claude-fable-5-2 claude-opus-5-5 claude-sonnet-5-5 claude-haiku-5-5 claude-opus-4-6 claude-opus-4-7 claude-opus-4-8 claude-sonnet-4-6 claude-sonnet-5", 1000000, 128000},
		{ProviderAnthropic, "claude-opus-4-5 claude-sonnet-4-5 claude-sonnet-4 claude-haiku-4-5", 200000, 64000},
		{ProviderAnthropic, "claude-opus-4 claude-opus-4-1", 200000, 32000},
		{ProviderGemini, "gemini-2.5-pro", 1048576, 65536},
		{ProviderZhipu, "glm-4.7 glm-4.7-flashx", 200000, 128000},
		{ProviderDeepSeek, "deepseek-flash deepseek-v4-pro", 1000000, 384000},
		{ProviderMiniMax, "minimax-m3 minimax-m3.1-flash", 1000000, 0},
		{ProviderMiniMax, "minimax-m2.5", 196608, 0},
		{ProviderKimi, "kimi-k2.5", 262144, 0},
		{ProviderKimi, "kimi-k3 kimi-k3.1-pro kimi-k3.1-pro-highspeed", 1000000, 0},
	} {
		if provider != "" && provider != ProviderCustom && provider != rule.provider {
			continue
		}
		for _, name := range strings.Fields(rule.models) {
			if modelVersion(strings.ToLower(model), name) {
				return &ModelLimits{Window: rule.window, MaxOutput: rule.output}
			}
		}
	}
	return nil
}
