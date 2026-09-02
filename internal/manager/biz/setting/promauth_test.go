package setting

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
)

func TestPromResolverTLSSettingOverridesFallback(t *testing.T) {
	ctx := context.Background()
	settings := New(newFakeRepo(), nil)
	resolver := NewPromResolver(settings, "", "", promauth.TLSConfig{Insecure: true})

	cfg, err := resolver.ResolveTLS(ctx)
	if err != nil || !cfg.Insecure {
		t.Fatalf("fallback TLS config = %#v, %v", cfg, err)
	}
	if err := settings.Set(ctx, model.CategoryProm, model.KeyPromTLSInsecure, "false", false); err != nil {
		t.Fatalf("disable TLS skip verify: %v", err)
	}
	cfg, err = resolver.ResolveTLS(ctx)
	if err != nil || cfg.Insecure {
		t.Fatalf("stored TLS config = %#v, %v", cfg, err)
	}
}
