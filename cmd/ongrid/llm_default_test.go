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
