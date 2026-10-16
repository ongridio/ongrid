package llm

import (
	"testing"
)

func TestPickProviderDefaultFallsBackToConfiguredProvider(t *testing.T) {
	providers := []ProviderConfig{
		{ID: ProviderDeepSeek, APIKey: "sk-test", Model: "deepseek-v4-flash"},
	}

	gotProvider, gotModel := PickProviderDefault(providers, "")
	if gotProvider != ProviderDeepSeek || gotModel != "deepseek-v4-flash" {
		t.Fatalf("PickProviderDefault() = (%q, %q), want (deepseek, deepseek-v4-flash)", gotProvider, gotModel)
	}
}

func TestPickProviderDefaultIgnoresUnavailablePreferred(t *testing.T) {
	providers := []ProviderConfig{
		{ID: ProviderOpenAI, APIKey: "", Model: "gpt-5.4"},
		{ID: ProviderDeepSeek, APIKey: "sk-test", Model: "deepseek-v4-flash"},
	}

	gotProvider, gotModel := PickProviderDefault(providers, ProviderOpenAI)
	if gotProvider != ProviderDeepSeek || gotModel != "deepseek-v4-flash" {
		t.Fatalf("PickProviderDefault() = (%q, %q), want (deepseek, deepseek-v4-flash)", gotProvider, gotModel)
	}
}

func TestPickProviderDefaultReturnsEmptyWithoutConfiguredProvider(t *testing.T) {
	providers := []ProviderConfig{
		{ID: ProviderDeepSeek, APIKey: "", Model: "deepseek-v4-flash"},
	}

	gotProvider, gotModel := PickProviderDefault(providers, ProviderDeepSeek)
	if gotProvider != "" || gotModel != "" {
		t.Fatalf("PickProviderDefault() = (%q, %q), want empty default", gotProvider, gotModel)
	}
}
