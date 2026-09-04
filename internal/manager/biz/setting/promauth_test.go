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

func TestPromResolverDerivesVictoriaMetricsClusterWriteURL(t *testing.T) {
	ctx := context.Background()
	settings := New(newFakeRepo(), nil)
	resolver := NewPromResolver(settings, "", "http://prometheus:9090/api/v1/write", promauth.TLSConfig{})

	if err := settings.Set(ctx, model.CategoryProm, model.KeyPromQueryURL, "http://host.docker.internal:8481/select/0/prometheus", false); err != nil {
		t.Fatalf("set query URL: %v", err)
	}
	got, err := resolver.ResolveWriteURL(ctx)
	if err != nil {
		t.Fatalf("resolve write URL: %v", err)
	}
	if want := "http://host.docker.internal:8480/insert/0/prometheus/api/v1/write"; got != want {
		t.Fatalf("write URL = %q, want %q", got, want)
	}
}
