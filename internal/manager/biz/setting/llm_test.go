package setting

import (
	"context"
	"reflect"
	"testing"

	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
)

func TestDedupeStrings(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		// The out-of-box bug: the OpenAI catalog was seeded with the
		// configured model (defaulting to gpt-4o) plus a base list that
		// already contained gpt-4o → two gpt-4o rows in the picker.
		{"out-of-box openai dup", []string{"gpt-4o", "gpt-4o", "gpt-4-turbo"}, []string{"gpt-4o", "gpt-4-turbo"}},
		{"empty entries dropped", []string{"", "a", "", "b"}, []string{"a", "b"}},
		{"order preserved, later dups dropped", []string{"b", "a", "b", "c", "a"}, []string{"b", "a", "c"}},
		{"nil -> empty", nil, []string{}},
		{"no dups untouched", []string{"x", "y"}, []string{"x", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupeStrings(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("dedupeStrings(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLLMSettingsResolver_EmptyStoredAPIKeyOverridesEnvironment(t *testing.T) {
	t.Parallel()

	svc := New(newFakeRepo(), nil)
	resolver := NewLLMSettingsResolver(svc, map[string]EnvProviderDefaults{
		settingmodel.LLMProviderOpenAI: {
			Label:  "OpenAI",
			APIKey: "env-key",
			Model:  "env-model",
			Models: []string{"env-model"},
		},
	}, settingmodel.LLMProviderOpenAI)

	providers, _, err := resolver.ResolveProviders(context.Background())
	if err != nil {
		t.Fatalf("ResolveProviders before override: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != settingmodel.LLMProviderOpenAI {
		t.Fatalf("providers before override = %+v", providers)
	}

	if err := svc.Set(context.Background(), settingmodel.CategoryLLM, settingmodel.KeyOpenAIAPIKey, "", true); err != nil {
		t.Fatalf("Set empty override: %v", err)
	}
	providers, _, err = resolver.ResolveProviders(context.Background())
	if err != nil {
		t.Fatalf("ResolveProviders after override: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("empty stored key did not disable env provider: %+v", providers)
	}
}
