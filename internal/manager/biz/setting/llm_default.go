package setting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type LLMDefaultSelection struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type llmModelCatalog interface {
	ProvidersContext(context.Context) []llm.ProviderInfo
}

func (s *LLMConfigurationService) SetModelCatalog(catalog llmModelCatalog) { s.catalog = catalog }

// SaveDefault changes only the configured selection; it does not call inference.
func (s *LLMConfigurationService) SaveDefault(ctx context.Context, in LLMDefaultSelection) (LLMDefaultSelection, error) {
	if s.catalog == nil || s.settings == nil {
		return in, errs.ErrNotWiredYet
	}
	in.Provider, in.Model, in.ReasoningEffort = strings.TrimSpace(in.Provider), strings.TrimSpace(in.Model), strings.TrimSpace(in.ReasoningEffort)
	raw, err := json.Marshal(in)
	if err != nil {
		return in, fmt.Errorf("encode default selection: %w", err)
	}
	if _, err := decodeLLMDefault(in.Provider, string(raw)); err != nil {
		return in, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	for _, provider := range s.catalog.ProvidersContext(ctx) {
		if provider.ID != in.Provider {
			continue
		}
		if in.Model != provider.Model && !slices.Contains(provider.Models, in.Model) {
			break
		}
		if err := provider.Reasoning(in.Model).Validate(in.Provider, in.Model, in.ReasoningEffort); err != nil {
			return in, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
		}
		if err := s.settings.SetBatch(ctx, []model.Setting{
			{Category: model.CategoryLLM, Key: model.KeyLLMDefaultProvider, Value: in.Provider},
			{Category: model.CategoryLLM, Key: in.Provider + "_default_model", Value: in.Model},
			{Category: model.CategoryLLM, Key: in.Provider + "_reasoning_default", Value: string(raw)},
		}); err != nil {
			return in, fmt.Errorf("save default model: %w", err)
		}
		return in, nil
	}
	return in, fmt.Errorf("%w: model is not configured", errs.ErrInvalid)
}

func decodeLLMDefault(provider, raw string) (LLMDefaultSelection, error) {
	var value LLMDefaultSelection
	if len(raw) > 4096 {
		return value, fmt.Errorf("default selection exceeds 4 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode default selection: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return value, fmt.Errorf("default selection must contain one JSON object")
	}
	if !isKnownLLMProvider(provider) || value.Provider != provider || value.Model == "" || len(value.Model) > maxLLMModelBytes || value.Model != strings.TrimSpace(value.Model) {
		return value, fmt.Errorf("invalid default provider or model")
	}
	if value.ReasoningEffort == "none" || (value.ReasoningEffort != "" && llm.ValidateReasoningLevels([]string{value.ReasoningEffort}) != nil) {
		return value, fmt.Errorf("invalid default reasoning effort")
	}
	return value, nil
}

func (r *LLMSettingsResolver) defaultReasoningEffort(ctx context.Context, provider, selectedModel string) string {
	raw, found, err := r.svc.Get(ctx, model.CategoryLLM, provider+"_reasoning_default")
	if err != nil {
		r.svc.log.Warn("read default reasoning effort failed", "provider", provider, "err", err)
		return ""
	}
	if !found {
		return ""
	}
	value, err := decodeLLMDefault(provider, raw)
	if err != nil {
		r.svc.log.Warn("invalid default reasoning effort", "provider", provider, "err", err)
		return ""
	}
	if value.Model != selectedModel {
		return ""
	}
	return value.ReasoningEffort
}
