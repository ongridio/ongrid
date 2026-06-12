package main

import (
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type fakeLLMProviderCatalog struct {
	providers []llm.ProviderInfo
}

func (f fakeLLMProviderCatalog) Providers() []llm.ProviderInfo {
	return f.providers
}

func TestHasConfiguredLLMProviderAcceptsNonOpenAIProvider(t *testing.T) {
	catalog := fakeLLMProviderCatalog{providers: []llm.ProviderInfo{
		{ID: llm.ProviderDeepSeek, Model: "deepseek-v4-flash"},
	}}

	if !hasConfiguredLLMProvider(catalog) {
		t.Fatalf("hasConfiguredLLMProvider() = false, want true for non-OpenAI provider")
	}
}

func TestHasConfiguredLLMProviderReturnsFalseWithoutProviders(t *testing.T) {
	if hasConfiguredLLMProvider(fakeLLMProviderCatalog{}) {
		t.Fatalf("hasConfiguredLLMProvider() = true, want false without providers")
	}
}

func TestPickProviderDefaultFallsBackToConfiguredProvider(t *testing.T) {
	providers := []llm.ProviderConfig{
		{ID: llm.ProviderDeepSeek, APIKey: "sk-test", Model: "deepseek-v4-flash"},
	}

	gotProvider, gotModel := pickProviderDefault(providers, "")
	if gotProvider != llm.ProviderDeepSeek || gotModel != "deepseek-v4-flash" {
		t.Fatalf("pickProviderDefault() = (%q, %q), want (deepseek, deepseek-v4-flash)", gotProvider, gotModel)
	}
}

func TestPickProviderDefaultIgnoresUnavailablePreferred(t *testing.T) {
	providers := []llm.ProviderConfig{
		{ID: llm.ProviderOpenAI, APIKey: "", Model: "gpt-5.4"},
		{ID: llm.ProviderDeepSeek, APIKey: "sk-test", Model: "deepseek-v4-flash"},
	}

	gotProvider, gotModel := pickProviderDefault(providers, llm.ProviderOpenAI)
	if gotProvider != llm.ProviderDeepSeek || gotModel != "deepseek-v4-flash" {
		t.Fatalf("pickProviderDefault() = (%q, %q), want (deepseek, deepseek-v4-flash)", gotProvider, gotModel)
	}
}

func TestPickProviderDefaultReturnsEmptyWithoutConfiguredProvider(t *testing.T) {
	providers := []llm.ProviderConfig{
		{ID: llm.ProviderDeepSeek, APIKey: "", Model: "deepseek-v4-flash"},
	}

	gotProvider, gotModel := pickProviderDefault(providers, llm.ProviderDeepSeek)
	if gotProvider != "" || gotModel != "" {
		t.Fatalf("pickProviderDefault() = (%q, %q), want empty default", gotProvider, gotModel)
	}
}
