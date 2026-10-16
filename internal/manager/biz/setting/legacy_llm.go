package setting

import (
	"context"

	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
)

// LLMResolver resolves legacy OpenAI settings for llm.Resolver.
type LLMResolver struct {
	svc *Service
}

func NewLLMResolver(svc *Service) *LLMResolver {
	return &LLMResolver{svc: svc}
}

// Resolve implements llm.Resolver. Empty fields tell the LLM client to
// fall back to its env-seeded cfg.OpenAI values.
func (r *LLMResolver) Resolve(ctx context.Context) (string, string, string, error) {
	if r == nil || r.svc == nil {
		return "", "", "", nil
	}
	apiKey, _, err := r.svc.Get(ctx, settingmodel.CategoryLLM, settingmodel.KeyOpenAIAPIKey)
	if err != nil {
		return "", "", "", err
	}
	model, _, err := r.svc.Get(ctx, settingmodel.CategoryLLM, settingmodel.KeyOpenAIModel)
	if err != nil {
		return "", "", "", err
	}
	baseURL, _, err := r.svc.Get(ctx, settingmodel.CategoryLLM, settingmodel.KeyOpenAIBaseURL)
	if err != nil {
		return "", "", "", err
	}
	return apiKey, model, baseURL, nil
}
