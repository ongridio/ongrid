package imbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// fakeDefaults stubs the LLMSettingsResolver surface the adapter needs.
type fakeDefaults struct {
	providers []llm.ProviderConfig
	defID     string
	err       error
}

func (f *fakeDefaults) ResolveProviders(_ context.Context) ([]llm.ProviderConfig, string, error) {
	return f.providers, f.defID, f.err
}

// TestAdapter_runOptions_picksResolverDefault covers the v0.7.169 fix:
// IM bridge must thread the cluster default_provider + <provider>_default_model
// into agent.RunOptions instead of leaving them empty (which made agent.New
// fall back to the hard-coded "gpt-5.4" model and end users saw
// "助手执行失败" in Lark/Slack/Telegram).
func TestAdapter_runOptions_picksResolverDefault(t *testing.T) {
	tests := []struct {
		name         string
		defaults     LLMDefaultProvider
		wantProvider string
		wantModel    string
	}{
		{
			name: "configured default points at a registered provider",
			defaults: &fakeDefaults{
				providers: []llm.ProviderConfig{
					{ID: "custom", Model: "claude-opus-4-7"},
					{ID: "zhipu", Model: "glm-4.7-flash"},
				},
				defID: "zhipu",
			},
			wantProvider: "zhipu",
			wantModel:    "glm-4.7-flash",
		},
		{
			name: "empty default falls back to the first provider in catalog",
			defaults: &fakeDefaults{
				providers: []llm.ProviderConfig{
					{ID: "custom", Model: "claude-opus-4-7"},
					{ID: "zhipu", Model: "glm-4.7-flash"},
				},
				defID: "",
			},
			wantProvider: "custom",
			wantModel:    "claude-opus-4-7",
		},
		{
			name: "default points at a provider that's no longer in the catalog falls back to first",
			defaults: &fakeDefaults{
				providers: []llm.ProviderConfig{
					{ID: "zhipu", Model: "glm-4.7-flash"},
				},
				defID: "openai",
			},
			wantProvider: "zhipu",
			wantModel:    "glm-4.7-flash",
		},
		{
			name: "resolver error → zero RunOptions (agent fallback applies)",
			defaults: &fakeDefaults{
				err: errors.New("transient DB error"),
			},
			wantProvider: "",
			wantModel:    "",
		},
		{
			name: "empty catalog → zero RunOptions (no provider configured at all)",
			defaults: &fakeDefaults{
				providers: nil,
			},
			wantProvider: "",
			wantModel:    "",
		},
		{
			name:         "nil resolver → zero RunOptions (caller didn't wire it)",
			defaults:     nil,
			wantProvider: "",
			wantModel:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAiopsAdapter(nil, 1, tt.defaults, nil)
			opts := a.runOptions(context.Background())
			if opts.Provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", opts.Provider, tt.wantProvider)
			}
			if opts.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", opts.Model, tt.wantModel)
			}
		})
	}
}
