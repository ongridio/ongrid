package llm

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// An absent override means automatic discovery; an empty Levels array means
// default-only. "*" sets the provider's fallback for unknown models.
type ReasoningOverride struct {
	Levels []string `json:"levels"`
	Mode   string   `json:"mode,omitempty"`
}

// ReasoningCapability is shared by the picker, validation and wire adapter.
type ReasoningCapability struct {
	Levels       []string `json:"levels"`
	Mode         string   `json:"mode"`
	Source       string   `json:"source"`
	DefaultLevel string   `json:"default_level,omitempty"`
	Thinking     string   `json:"-"`
}

const (
	ReasoningEffortMode   = "effort"
	ReasoningThinkingMode = "thinking"
	ReasoningHybridMode   = "thinking_effort"
	ReasoningAdaptiveMode = "adaptive"
	ReasoningClaudeMode   = "anthropic"
)

var reasoningLevelPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func ValidateReasoningOverrides(provider string, overrides map[string]ReasoningOverride) error {
	limit := 32
	if _, ok := overrides["*"]; ok {
		limit++
	}
	if len(overrides) > limit {
		return fmt.Errorf("at most 32 model overrides and one provider fallback are allowed")
	}
	for model, override := range overrides {
		if model == "" || model != strings.TrimSpace(model) || len(model) > 256 {
			return fmt.Errorf("invalid reasoning override model")
		}
		if override.Mode == "" {
			override.Mode = BuiltinReasoning(provider, model).Mode
		}
		switch override.Mode {
		case "", ReasoningEffortMode, ReasoningThinkingMode, ReasoningHybridMode, ReasoningAdaptiveMode, ReasoningClaudeMode:
		default:
			return fmt.Errorf("unsupported reasoning parameter format %q", override.Mode)
		}
		if override.Levels == nil {
			return fmt.Errorf("reasoning levels for %q must be an array", model)
		}
		if err := ValidateReasoningLevels(override.Levels); err != nil {
			return fmt.Errorf("reasoning levels for %q: %w", model, err)
		}
		for _, level := range override.Levels {
			if (override.Mode == ReasoningThinkingMode || override.Mode == ReasoningAdaptiveMode) && level != "none" && level != "enabled" {
				return fmt.Errorf("%s accepts only enabled and none", override.Mode)
			}
			if override.Mode == ReasoningEffortMode && level == "enabled" {
				return fmt.Errorf("enabled requires a thinking parameter format")
			}
		}
	}
	return nil
}

// ValidateReasoningLevels bounds administrator-supplied effort names.
func ValidateReasoningLevels(levels []string) error {
	if len(levels) > 16 {
		return fmt.Errorf("at most 16 reasoning levels are allowed")
	}
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		if !reasoningLevelPattern.MatchString(level) || seen[level] {
			return fmt.Errorf("invalid or duplicate reasoning level %q", level)
		}
		seen[level] = true
	}
	return nil
}

func ResolveReasoning(provider, model string, discovered map[string]ReasoningCapability, overrides map[string]ReasoningOverride) ReasoningCapability {
	capability := BuiltinReasoning(provider, model)
	if upstream, ok := discovered[model]; ok {
		capability = upstream
	}
	override, explicit := overrides[model]
	if !explicit && capability.Source == "fallback" {
		override, explicit = overrides["*"]
	}
	if explicit {
		capability.Levels = slices.Clone(override.Levels)
		capability.DefaultLevel, capability.Source = "", "configuration"
		if override.Mode != "" {
			capability.Mode = override.Mode
			if override.Mode == ReasoningClaudeMode && capability.Thinking == "" {
				capability.Thinking = "adaptive"
			}
		}
	}
	return capability
}

func (c ReasoningCapability) Validate(provider, model, effort string) error {
	if effort == "" || slices.Contains(c.Levels, effort) {
		return nil
	}
	return fmt.Errorf("unsupported reasoning effort %q for %s/%s", effort, provider, model)
}

func ReasoningEfforts(provider, model string) []string {
	return BuiltinReasoning(provider, model).Levels
}

type reasoningRule struct{ provider, models, levels, mode, thinking string }

type reasoningCapabilityKey struct{}

func (c *openaiClient) reasoningCapability(provider, model string) ReasoningCapability {
	if capability, ok := c.cfg.ReasoningCapabilities[model]; ok {
		return capability
	}
	if provider == "" {
		provider = c.cfg.Provider
	}
	return BuiltinReasoning(provider, model)
}

// BuiltinReasoning is the offline baseline. Specific older models precede the
// latest-family fallback. Sources and vendor references are in the design doc.
func BuiltinReasoning(provider, model string) ReasoningCapability {
	m := strings.ToLower(strings.TrimSpace(model))
	p := strings.ToLower(provider)
	if p == "" || p == ProviderCustom {
		for _, family := range []struct{ prefix, provider string }{
			{"gpt-", ProviderOpenAI}, {"o1", ProviderOpenAI}, {"o3", ProviderOpenAI}, {"o4", ProviderOpenAI},
			{"deepseek", ProviderDeepSeek}, {"mimo-", ProviderXiaomi}, {"gemini-", ProviderGemini},
			{"glm-", ProviderZhipu}, {"kimi-", ProviderKimi}, {"minimax-", ProviderMiniMax}, {"claude-", ProviderAnthropic},
		} {
			if strings.HasPrefix(m, family.prefix) {
				p = family.provider
				break
			}
		}
	}
	c := ReasoningCapability{Levels: []string{}, Mode: ReasoningEffortMode, Source: "builtin"}
	if m == "" {
		return c
	}
	for _, marker := range []string{"embedding", "rerank", "moderation", "transcribe", "whisper", "tts", "realtime", "audio", "image", "dall-e", "sora", "-asr"} {
		if strings.Contains(m, marker) {
			return c
		}
	}
	for _, rule := range []reasoningRule{
		{ProviderOpenAI, "gpt-5-pro o1-pro o3-pro", "high", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-5.2-pro gpt-5.4-pro gpt-5.5-pro", "medium high xhigh", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-6.1-terra gpt-6-sol gpt-6-luna gpt-5.6-sol gpt-5.6-terra gpt-5.6-luna", "none low medium high xhigh max", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-6.1-sol gpt-6-astra", "low medium high xhigh max", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-5.2 gpt-5.4 gpt-5.4-mini gpt-5.4-nano gpt-5.5", "none low medium high xhigh", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-5.1", "none low medium high", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-5 gpt-5-mini gpt-5-nano", "minimal low medium high", ReasoningEffortMode, ""},
		{ProviderOpenAI, "o1 o3 o3-mini o4-mini gpt-5-codex gpt-5.1-codex gpt-5.1-codex-mini", "low medium high", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-5.1-codex-max gpt-5.2-codex gpt-5.3-codex gpt-5.3-codex-spark", "low medium high xhigh", ReasoningEffortMode, ""},
		{ProviderOpenAI, "gpt-4* gpt-3* o1-mini* o1-preview* *chat-latest", "", ReasoningEffortMode, ""},
		{ProviderDeepSeek, "deepseek-chat deepseek-reasoner", "", ReasoningEffortMode, ""},
		{ProviderDeepSeek, "deepseek-v4-flash deepseek-v4-pro deepseek-flash", "none low high max", ReasoningHybridMode, ""},
		{ProviderXiaomi, "mimo-v2.6-flash mimo-v2.6-pro mimo-v2.6-pro-ultraspeed", "none enabled", ReasoningThinkingMode, ""},
		{ProviderGemini, "gemini-1* gemini-2.0*", "", ReasoningEffortMode, ""},
		{ProviderGemini, "gemini-2.5-pro*", "minimal low medium high", ReasoningEffortMode, ""},
		{ProviderGemini, "gemini-2.5-*", "none minimal low medium high", ReasoningEffortMode, ""},
		{ProviderGemini, "gemini-3-pro*", "low high", ReasoningEffortMode, ""},
		{ProviderGemini, "gemini-3* gemini-4-argon*", "minimal low medium high", ReasoningEffortMode, ""},
		{ProviderKimi, "moonshot-* kimi-k2-thinking* kimi-k2 kimi-k2-0*", "", ReasoningEffortMode, ""},
		{ProviderKimi, "kimi-k2.5* kimi-k2.6* kimi-k2.7*", "none enabled", ReasoningThinkingMode, ""},
		{ProviderKimi, "kimi-k3* kimi-k2.8*", "low high max", ReasoningEffortMode, ""},
		{ProviderZhipu, "glm-5.2*", "none minimal low medium high xhigh max", ReasoningHybridMode, ""},
		{ProviderZhipu, "glm-5.3*", "low high max", ReasoningHybridMode, ""},
		{ProviderZhipu, "glm-4.5* glm-4.6* glm-4.7* glm-5 glm-5.1*", "none enabled", ReasoningThinkingMode, ""},
		{ProviderZhipu, "glm-4* glm-3*", "", ReasoningEffortMode, ""},
		{ProviderMiniMax, "minimax-m1* minimax-m2*", "", ReasoningEffortMode, ""},
		{ProviderMiniMax, "minimax-m3", "none enabled", ReasoningAdaptiveMode, ""},
		{ProviderMiniMax, "minimax-m3.1-flash*", "low medium high xhigh max", ReasoningEffortMode, ""},
		{ProviderAnthropic, "claude-3-7-sonnet* claude-sonnet-4 claude-opus-4 claude-opus-4-1* claude-sonnet-4-5* claude-haiku-4-5*", "none enabled", ReasoningClaudeMode, "enabled"},
		{ProviderAnthropic, "claude-opus-4-5*", "low medium high", ReasoningClaudeMode, "enabled"},
		{ProviderAnthropic, "claude-opus-4-6* claude-sonnet-4-6* claude-mythos-preview*", "low medium high max", ReasoningClaudeMode, "adaptive"},
		{ProviderAnthropic, "claude-3* claude-2* claude-instant*", "", ReasoningClaudeMode, ""},
		{ProviderAnthropic, "claude-opus-4-7* claude-opus-4-8* claude-opus-5* claude-sonnet-5* claude-haiku-5-5* claude-fable-5* claude-mythos-5*", "low medium high xhigh max", ReasoningClaudeMode, "adaptive"},
	} {
		if rule.provider != p {
			continue
		}
		for _, pattern := range strings.Fields(rule.models) {
			if (strings.HasSuffix(pattern, "*") && strings.HasPrefix(m, strings.TrimSuffix(pattern, "*"))) || (strings.HasPrefix(pattern, "*") && strings.HasSuffix(m, strings.TrimPrefix(pattern, "*"))) || modelVersion(m, pattern) {
				c.Levels, c.Mode, c.Thinking = strings.Fields(rule.levels), rule.mode, rule.thinking
				// Custom entries promise an OpenAI-compatible endpoint; native
				// Claude routing must be selected explicitly for such gateways.
				if c.Mode == ReasoningClaudeMode && provider != ProviderAnthropic {
					c.Mode, c.Thinking = ReasoningEffortMode, ""
					if slices.Contains(c.Levels, "enabled") {
						c.Levels = []string{}
					}
				}
				if c.Levels == nil {
					c.Levels = []string{}
				}
				return c
			}
		}
	}
	// shortcut: future models inherit the current provider profile; upstream
	// metadata or an administrator override supersedes it when contracts differ.
	c.Source = "fallback"
	switch p {
	case ProviderOpenAI, ProviderMiniMax:
		c.Levels = strings.Fields("low medium high xhigh max")
	case ProviderDeepSeek, ProviderZhipu:
		c.Levels, c.Mode = strings.Fields("low high max"), ReasoningHybridMode
	case ProviderKimi:
		c.Levels = strings.Fields("low high max")
	case ProviderGemini:
		c.Levels = strings.Fields("low medium high")
	case ProviderXiaomi:
		c.Levels, c.Mode = []string{"enabled"}, ReasoningThinkingMode
	case ProviderAnthropic:
		c.Levels, c.Mode, c.Thinking = strings.Fields("low medium high xhigh max"), ReasoningClaudeMode, "adaptive"
		if provider != ProviderAnthropic {
			c.Mode, c.Thinking = ReasoningEffortMode, ""
		}
	}
	return c
}

func modelVersion(model, name string) bool {
	if model == name {
		return true
	}
	// Accept dated snapshots, but not a different variant such as -pro.
	suffix := strings.TrimPrefix(model, name+"-")
	if suffix == model {
		return false
	}
	for _, format := range []string{"2006-01-02", "20060102"} {
		if _, err := time.Parse(format, suffix); err == nil {
			return true
		}
	}
	return false
}

func ValidateReasoningEffort(provider, model, effort string) error {
	return BuiltinReasoning(provider, model).Validate(provider, model, effort)
}

// DefaultEffort applies only to the bound model and its current capabilities.
func (p ProviderInfo) DefaultEffort(model string) string {
	if model != p.Model || p.DefaultReasoningEffort == "none" || p.Reasoning(model).Validate(p.ID, model, p.DefaultReasoningEffort) != nil {
		return ""
	}
	return p.DefaultReasoningEffort
}
