package setting

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
)

// TestLokiResolverFallsBackToEnv verifies the resolver returns the
// env-seeded URL when no DB row exists. This is the "fresh install"
// path where built-in deployments must still resolve to the embedded
// loki:3100.
func TestLokiResolverFallsBackToEnv(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	r := NewLokiResolver(svc, "http://loki:3100")
	if got := r.URL(context.Background()); got != "http://loki:3100" {
		t.Fatalf("URL fallback = %q, want http://loki:3100", got)
	}
}

// TestLokiResolverPrefersDB verifies an admin edit in system_settings
// wins over the env fallback.
func TestLokiResolverPrefersDB(t *testing.T) {
	repo := newFakeRepo()
	svc := New(repo, nil)
	ctx := context.Background()
	if err := svc.Set(ctx, model.CategoryLoki, model.KeyLokiURL, "https://loki.customer.com/", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	r := NewLokiResolver(svc, "http://loki:3100")
	if got := r.URL(ctx); got != "https://loki.customer.com" {
		t.Fatalf("URL = %q, want https://loki.customer.com (trailing slash trimmed)", got)
	}
}

// TestLokiResolverAuth covers the basic-auth pair and the TLS toggle.
func TestLokiResolverAuth(t *testing.T) {
	repo := newFakeRepo()
	svc := New(repo, nil)
	ctx := context.Background()
	if err := svc.Set(ctx, model.CategoryLoki, model.KeyLokiBasicUser, "alice", false); err != nil {
		t.Fatalf("Set user: %v", err)
	}
	if err := svc.Set(ctx, model.CategoryLoki, model.KeyLokiBasicPassword, "s3cret", true); err != nil {
		t.Fatalf("Set password: %v", err)
	}
	if err := svc.Set(ctx, model.CategoryLoki, model.KeyLokiTLSInsecure, "true", false); err != nil {
		t.Fatalf("Set tls: %v", err)
	}
	r := NewLokiResolver(svc, "")
	user, pass := r.Auth(ctx)
	if user != "alice" || pass != "s3cret" {
		t.Fatalf("Auth = (%q,%q), want (alice,s3cret)", user, pass)
	}
	if !r.TLSInsecure(ctx) {
		t.Fatalf("TLSInsecure = false, want true")
	}
}

// TestLokiResolverEmptyServiceReturnsEmpty — a resolver wrapping a
// zero service returns the empty fallback. Tightens the contract:
// callers shouldn't have to handle nil-service cases at every site.
func TestLokiResolverEmptyServiceReturnsEmpty(t *testing.T) {
	r := NewLokiResolver(New(newFakeRepo(), nil), "")
	if got := r.URL(context.Background()); got != "" {
		t.Fatalf("URL = %q, want empty", got)
	}
	user, pass := r.Auth(context.Background())
	if user != "" || pass != "" {
		t.Fatalf("Auth = (%q,%q), want both empty", user, pass)
	}
}

// TestTempoResolverFallsBackToEnv mirrors the Loki fallback test for
// the trace signal — same wiring shape, different category/key.
func TestTempoResolverFallsBackToEnv(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	r := NewTempoResolver(svc, "http://tempo:4318/v1/traces")
	if got := r.URL(context.Background()); got != "http://tempo:4318/v1/traces" {
		t.Fatalf("URL fallback = %q", got)
	}
}

func TestTempoResolverPrefersDB(t *testing.T) {
	repo := newFakeRepo()
	svc := New(repo, nil)
	ctx := context.Background()
	if err := svc.Set(ctx, model.CategoryTempo, model.KeyTempoURL, "https://tempo.customer.com/v1/traces", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	r := NewTempoResolver(svc, "http://tempo:4318")
	if got := r.URL(ctx); got != "https://tempo.customer.com/v1/traces" {
		t.Fatalf("URL = %q", got)
	}
}

// TestTelemetryResolverAuthKeepsPasswordWhitespace verifies a saved basic-auth
// password is returned exactly as stored, so it matches what the connection
// test used.
func TestTelemetryResolverAuthKeepsPasswordWhitespace(t *testing.T) {
	const pw = "  pa ss\t"
	ctx := context.Background()
	svc := New(newFakeRepo(), nil)
	if err := svc.Set(ctx, model.CategoryLoki, model.KeyLokiBasicUser, " alice ", false); err != nil {
		t.Fatalf("Set loki user: %v", err)
	}
	if err := svc.Set(ctx, model.CategoryLoki, model.KeyLokiBasicPassword, pw, true); err != nil {
		t.Fatalf("Set loki password: %v", err)
	}
	if err := svc.Set(ctx, model.CategoryTempo, model.KeyTempoBasicPassword, pw, true); err != nil {
		t.Fatalf("Set tempo password: %v", err)
	}
	user, pass := NewLokiResolver(svc, "").Auth(ctx)
	if user != "alice" || pass != pw {
		t.Fatalf("loki Auth = (%q,%q), want (alice,%q)", user, pass, pw)
	}
	if _, pass := NewTempoResolver(svc, "").Auth(ctx); pass != pw {
		t.Fatalf("tempo password = %q, want %q", pass, pw)
	}
}
