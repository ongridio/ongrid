package setting

import (
	"context"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func (resolver *LLMSettingsResolver) CheckReady(ctx context.Context) error {
	if resolver == nil {
		return fmt.Errorf("%w: LLM provider not configured", errs.ErrNotWiredYet)
	}
	providers, resolvedDefault, err := resolver.ResolveProviders(ctx)
	if err != nil {
		return fmt.Errorf("resolve LLM providers: %w", err)
	}
	if id, _ := llm.PickProviderDefault(providers, resolvedDefault); id != "" {
		return nil
	}
	return fmt.Errorf("%w: LLM provider not configured", errs.ErrNotWiredYet)
}

// DefaultModel follows the live provider selection for background model calls.
func (resolver *LLMSettingsResolver) DefaultModel(rctx context.Context) (string, string) {
	provCfgs, resolvedDefault, rerr := resolver.ResolveProviders(rctx)
	if rerr != nil {
		return "", ""
	}
	return llm.PickProviderDefault(provCfgs, resolvedDefault)

}
