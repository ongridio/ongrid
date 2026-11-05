package setting

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type defaultCatalog []llm.ProviderInfo

func (c defaultCatalog) ProvidersContext(context.Context) []llm.ProviderInfo { return c }

func TestSaveLLMDefaultPersistsAndBindsEffortToModel(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	settings := New(repo, nil)
	defaults := map[string]EnvProviderDefaults{"openai": {APIKey: "test", Model: "gpt-6.1-terra", Models: []string{"gpt-6.1-terra", "gpt-4o"}}}
	svc := NewLLMConfigurationService(defaults, settings)
	svc.SetModelCatalog(defaultCatalog{{ID: "openai", Model: "gpt-6.1-terra", Models: defaults["openai"].Models}})
	selection := LLMDefaultSelection{Provider: "openai", Model: "gpt-6.1-terra", ReasoningEffort: "high"}
	if _, err := svc.SaveDefault(ctx, selection); err != nil {
		t.Fatal(err)
	}
	// A new service/cache sees the stored selection after restart.
	resolver := NewLLMSettingsResolver(New(repo, nil), defaults, "")
	providers, provider, err := resolver.ResolveProviders(ctx)
	if err != nil || provider != "openai" || len(providers) != 1 || providers[0].DefaultReasoningEffort != "high" {
		t.Fatalf("reload: %+v %s %v", providers, provider, err)
	}
	for _, bad := range []LLMDefaultSelection{{Provider: "openai", Model: "gpt-4o", ReasoningEffort: "high"}, {Provider: "openai", Model: "not-configured"}, {Provider: "missing", Model: "m"}} {
		if _, err := svc.SaveDefault(ctx, bad); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	if v, _, _ := settings.Get(ctx, model.CategoryLLM, model.KeyOpenAIDefaultModel); v != "gpt-6.1-terra" {
		t.Fatal("invalid selection partially persisted")
	}
	// Older settings clients may still edit the default model independently.
	if err := settings.Set(ctx, model.CategoryLLM, model.KeyOpenAIDefaultModel, "gpt-4o", false); err != nil {
		t.Fatal(err)
	}
	providers, _, err = NewLLMSettingsResolver(settings, defaults, "").ResolveProviders(ctx)
	if err != nil || providers[0].DefaultReasoningEffort != "" {
		t.Fatalf("effort leaked across models: %+v %v", providers, err)
	}
	if _, err := svc.SaveDefault(ctx, LLMDefaultSelection{Provider: "openai", Model: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if raw, _, _ := settings.Get(ctx, model.CategoryLLM, "openai_reasoning_default"); raw != `{"provider":"openai","model":"gpt-4o","reasoning_effort":""}` {
		t.Fatalf("default not cleared: %s", raw)
	}
}

type failingDefaultRepo struct{ *fakeRepo }

func (f failingDefaultRepo) SetBatch(context.Context, []model.Setting) error {
	return errors.New("database unavailable")
}
func TestSaveLLMDefaultDoesNotFallBackToPartialWrites(t *testing.T) {
	repo := newFakeRepo()
	svc := NewLLMConfigurationService(nil, New(failingDefaultRepo{repo}, nil))
	svc.SetModelCatalog(defaultCatalog{{ID: "openai", Model: "gpt-6.1-terra"}})
	if _, err := svc.SaveDefault(context.Background(), LLMDefaultSelection{Provider: "openai", Model: "gpt-6.1-terra", ReasoningEffort: "high"}); err == nil {
		t.Fatal("save succeeded")
	}
	if len(repo.rows) != 0 {
		t.Fatal("failed save partially changed settings")
	}
}
