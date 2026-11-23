package llm

import (
	"context"
	"net/url"
	"slices"
	"strings"
)

// WindowCapability describes the limits advertised by this deployment.
type WindowCapability struct {
	Total  int    `json:"total,omitempty"`
	Input  int    `json:"input,omitempty"`
	Output int    `json:"output,omitempty"`
	Source string `json:"source,omitempty"`
}

const DefaultWindow = 256000

func (c WindowCapability) Maximum() int {
	if c.Total > 0 {
		return c.Total
	}
	return c.Input
}

func (c WindowCapability) Options() []int {
	maximum := c.Maximum()
	var options []int
	for _, n := range []int{32000, 64000, 128000, 256000, 512000} {
		if n < maximum {
			options = append(options, n)
		}
	}
	if maximum > 0 {
		options = append(options, maximum)
	}
	return options
}

func (c WindowCapability) Supports(n int) bool { return n == 0 || slices.Contains(c.Options(), n) }

func (c WindowCapability) Budget(selected int) (window, input, output int) {
	window = DefaultWindow
	if maximum := c.Maximum(); maximum > 0 {
		if selected > 0 {
			window = selected
		}
		window = min(window, maximum)
	}
	output = min(8192, max(1, window/8))
	if c.Output > 0 {
		output = min(output, c.Output)
	}
	input = window
	if c.Total > 0 || c.Input == 0 {
		input -= output
	}
	if c.Input > 0 {
		input = min(input, c.Input)
	}
	input -= min(4000, input/16)
	return
}

// Built-in limits apply only to a matching first-party endpoint and model.
func providerWindows(p ProviderConfig, catalog map[string]CatalogModel) map[string]WindowCapability {
	u, _ := url.Parse(p.BaseURL)
	host := ""
	if u != nil {
		host = strings.ToLower(u.Hostname())
	}
	official := map[string][]string{
		ProviderOpenAI: {"api.openai.com"}, ProviderDeepSeek: {"api.deepseek.com"}, ProviderAnthropic: {"api.anthropic.com"},
		ProviderGemini: {"generativelanguage.googleapis.com"}, ProviderKimi: {"api.moonshot.cn", "api.moonshot.ai"},
		ProviderMiniMax: {"api.minimax.io", "api.minimaxi.com", "api.minimax.cn"}, ProviderZhipu: {"open.bigmodel.cn", "api.z.ai"},
		ProviderXiaomi: {"api.xiaomimimo.com", "token-plan-cn.xiaomimimo.com", "token-plan-sgp.xiaomimimo.com", "token-plan-ams.xiaomimimo.com"},
	}
	firstParty := (p.ID == ProviderOpenAI && p.BaseURL == "") || slices.Contains(official[p.ID], host)
	windows := map[string]WindowCapability{}
	for _, id := range append(slices.Clone(p.Models), p.Model) {
		if id == "" {
			continue
		}
		name := id
		if firstParty && p.ID == ProviderGemini {
			name = strings.TrimPrefix(id, "models/")
		}
		if firstParty && p.ID == ProviderDeepSeek && (id == "deepseek-v4-flash" || id == "deepseek-v4-flash-vision-exp") {
			name = "deepseek-flash"
		}
		c := catalog[id].Window
		if c.Maximum() == 0 && firstParty {
			fallback := catalog[name].Window
			output := smallestModelLimit(c.Output, fallback.Output)
			if fallback.Maximum() == 0 {
				fallback = builtinWindow(p.ID, name)
			}
			fallback.Output = smallestModelLimit(output, fallback.Output)
			c = fallback
		}
		if limit := p.WindowLimits[id]; limit >= 1024 && limit <= 16<<20 && (c.Maximum() == 0 || limit < c.Maximum()) {
			if c.Total == 0 && c.Input > 0 {
				c.Input = limit
			} else {
				c.Total = limit
			}
			c.Source = "configuration"
		}
		windows[id] = c
	}
	return windows
}

func builtinWindow(provider, model string) WindowCapability {
	// Match exact names and dated snapshots. Unknown aliases use the local fallback.
	// Deployment metadata takes priority over the public model specification.
	if provider == ProviderOpenAI && (modelVersion(model, "gpt-6.2-astra") || modelVersion(model, "gpt-6.2-sol")) {
		return WindowCapability{Total: 1050000, Input: 922000, Output: 128000, Source: "model_spec"}
	}
	if provider == ProviderGemini && (modelVersion(model, "gemini-4-argon") || modelVersion(model, "gemini-4-carbon")) {
		return WindowCapability{Total: 1048576, Output: 65536, Source: "model_spec"}
	}
	for _, rule := range []struct {
		provider, models string
		window, output   int
	}{
		{ProviderOpenAI, "gpt-6-astra gpt-6-sol gpt-6-luna gpt-6.1-sol gpt-6.1-terra gpt-5.6-sol gpt-5.6-terra gpt-5.6-luna gpt-5.5 gpt-5.5-pro gpt-5.4 gpt-5.4-pro", 1050000, 128000},
		{ProviderOpenAI, "gpt-5.4-mini gpt-5.4-nano gpt-5.2 gpt-5.2-pro gpt-5.1 gpt-5 gpt-5-mini gpt-5-nano", 400000, 128000},
		{ProviderOpenAI, "gpt-5-codex gpt-5.1-codex gpt-5.1-codex-mini gpt-5.1-codex-max gpt-5.2-codex gpt-5.3-codex", 400000, 128000},
		{ProviderOpenAI, "gpt-5-pro", 400000, 272000},
		{ProviderOpenAI, "o1 o1-pro o3 o3-pro o3-mini o4-mini", 200000, 100000},
		{ProviderOpenAI, "gpt-4", 8192, 8192},
		{ProviderOpenAI, "gpt-4-turbo", 128000, 4096},
		{ProviderOpenAI, "gpt-4.1 gpt-4.1-mini gpt-4.1-nano", 1047576, 32768},
		{ProviderOpenAI, "gpt-4o gpt-4o-mini", 128000, 16384},
		{ProviderAnthropic, "claude-fable-5-2 claude-fable-5-1 claude-fable-5 claude-mythos-5-2 claude-mythos-5-1 claude-mythos-5 claude-mythos-preview claude-opus-5 claude-opus-5-5 claude-sonnet-5-5 claude-haiku-5-5 claude-opus-4-6 claude-opus-4-7 claude-opus-4-8 claude-sonnet-4-6 claude-sonnet-5", 1000000, 128000},
		{ProviderAnthropic, "claude-opus-4-5 claude-sonnet-4-5 claude-sonnet-4 claude-haiku-4-5", 200000, 64000},
		{ProviderAnthropic, "claude-opus-4 claude-opus-4-1", 200000, 32000},
		{ProviderGemini, "gemini-2.5-pro gemini-2.5-flash gemini-2.5-flash-lite gemini-3.1-pro-preview gemini-3.5-flash gemini-3.8-flash", 1048576, 65536},
		{ProviderZhipu, "glm-4.7 glm-4.7-flash glm-4.7-flashx glm-5 glm-5.1", 200000, 128000},
		{ProviderZhipu, "glm-5.2", 1000000, 128000},
		{ProviderZhipu, "glm-5.3-flash glm-5.3-flashx", 1048576, 131072},
		{ProviderXiaomi, "mimo-v2.6-flash mimo-v2.6-pro mimo-v2.6-pro-ultraspeed mimo-v2.5 mimo-v2.5-pro", 1048576, 131072},
		{ProviderDeepSeek, "deepseek-flash deepseek-v4-flash deepseek-v4-pro", 1000000, 384000},
		{ProviderMiniMax, "minimax-m3 minimax-m3.1-flash", 1000000, 0},
		{ProviderMiniMax, "minimax-m2.5", 196608, 0},
		{ProviderMiniMax, "minimax-m2.7 minimax-m2.7-highspeed", 204800, 0},
		{ProviderKimi, "kimi-k2.5 kimi-k2.6 kimi-k2.7-code kimi-k2.7-code-highspeed", 262144, 0},
		{ProviderKimi, "kimi-k3 kimi-k3.1-pro kimi-k3.1-pro-highspeed", 1048576, 0},
	} {
		if provider != rule.provider {
			continue
		}
		for _, name := range strings.Fields(rule.models) {
			if modelVersion(strings.ToLower(model), name) {
				if provider == ProviderGemini {
					return WindowCapability{Input: rule.window, Output: rule.output, Source: "model_spec"}
				}
				return WindowCapability{Total: rule.window, Output: rule.output, Source: "model_spec"}
			}
		}
	}
	return WindowCapability{}
}

func (m *MultiClient) ContextWindow(ctx context.Context, provider, name string) (string, string, WindowCapability) {
	if m == nil {
		return provider, name, WindowCapability{}
	}
	if provider == "" {
		p, n := m.Default()
		provider = p
		if name == "" {
			name = n
		}
	}
	for _, p := range m.ProvidersContext(ctx) {
		if p.ID == provider {
			if name == "" {
				name = p.Model
			}
			return provider, name, p.ContextWindows[name]
		}
	}
	return provider, name, WindowCapability{}
}
