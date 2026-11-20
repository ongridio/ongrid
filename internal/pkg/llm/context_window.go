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
const UnknownWindow = 32000

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
	maximum := c.Maximum()
	window = UnknownWindow
	if maximum > 0 {
		window = min(maximum, DefaultWindow)
		if selected > 0 {
			window = min(selected, maximum)
		}
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
	windows := map[string]WindowCapability{}
	for _, id := range append(slices.Clone(p.Models), p.Model) {
		if id == "" {
			continue
		}
		c := catalog[id].Window
		if c.Maximum() == 0 {
			official := map[string][]string{
				ProviderOpenAI: {"api.openai.com"}, ProviderDeepSeek: {"api.deepseek.com"}, ProviderAnthropic: {"api.anthropic.com"},
				ProviderGemini: {"generativelanguage.googleapis.com"}, ProviderKimi: {"api.moonshot.cn", "api.moonshot.ai"},
				ProviderMiniMax: {"api.minimax.io", "api.minimaxi.com"}, ProviderZhipu: {"open.bigmodel.cn", "api.z.ai"},
			}
			if (p.ID == ProviderOpenAI && host == "" && p.BaseURL == "") || slices.Contains(official[p.ID], host) {
				c = builtinWindow(p.ID, id)
			}
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
	for _, rule := range []struct {
		provider, models string
		window, output   int
	}{
		{ProviderOpenAI, "gpt-6-astra gpt-6.1-sol gpt-6.1-terra gpt-5.4", 1050000, 128000},
		{ProviderOpenAI, "gpt-4.1 gpt-4.1-mini gpt-4.1-nano", 1047576, 32768},
		{ProviderOpenAI, "gpt-4o gpt-4o-mini", 128000, 16384},
		{ProviderAnthropic, "claude-fable-5-2 claude-opus-5-5 claude-sonnet-5-5 claude-haiku-5-5 claude-opus-4-6 claude-opus-4-7 claude-opus-4-8 claude-sonnet-4-6 claude-sonnet-5", 1000000, 128000},
		{ProviderAnthropic, "claude-opus-4-5 claude-sonnet-4-5 claude-sonnet-4 claude-haiku-4-5", 200000, 64000},
		{ProviderAnthropic, "claude-opus-4 claude-opus-4-1", 200000, 32000},
		{ProviderGemini, "gemini-2.5-pro", 1048576, 65536},
		{ProviderZhipu, "glm-4.7 glm-4.7-flashx", 200000, 128000},
		{ProviderDeepSeek, "deepseek-flash deepseek-v4-flash deepseek-v4-pro", 1000000, 384000},
		{ProviderMiniMax, "minimax-m3 minimax-m3.1-flash", 1000000, 0},
		{ProviderMiniMax, "minimax-m2.5", 196608, 0},
		{ProviderKimi, "kimi-k2.5", 262144, 0},
		{ProviderKimi, "kimi-k3 kimi-k3.1-pro kimi-k3.1-pro-highspeed", 1000000, 0},
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
