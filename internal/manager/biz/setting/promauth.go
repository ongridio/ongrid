package setting

import (
	"context"
	"net"
	"net/url"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
)

// PromResolver implements four resolver interfaces against the
// system_settings table:
//
//   - promauth.Resolver         (Bearer / Basic)
//   - promauth.TLSResolver      (TLS verification / custom CA)
//   - promwrite.EndpointResolver (full remote_write URL with fallback)
//   - promquery.BaseURLResolver  (PromQL API root with fallback)
//
// All three reads route through Service.Get, which has its own internal
// cache; the round-tripper layers a 5s TTL on top of that, so an admin
// edit in the UI propagates within ~5s without restarting the manager.
//
// fallbackQueryURL / fallbackWriteURL are the env-derived bootstrap
// values from cfg.Prom — used when the corresponding system_settings
// row is missing or empty. This way a fresh install with nothing in the
// DB still talks to the embedded Prometheus.
type PromResolver struct {
	svc              *Service
	fallbackQueryURL string
	fallbackWriteURL string
	fallbackTLS      promauth.TLSConfig
}

// NewPromResolver wires the resolver. svc must be non-nil. The fallback
// values come from cfg.Prom and remain available when settings rows are absent.
func NewPromResolver(svc *Service, fallbackQueryURL, fallbackWriteURL string, fallbackTLS promauth.TLSConfig) *PromResolver {
	return &PromResolver{
		svc:              svc,
		fallbackQueryURL: strings.TrimRight(fallbackQueryURL, "/"),
		fallbackWriteURL: fallbackWriteURL,
		fallbackTLS:      fallbackTLS,
	}
}

func (r *PromResolver) get(ctx context.Context, key string) string {
	if r.svc == nil {
		return ""
	}
	v, _, err := r.svc.Get(ctx, model.CategoryProm, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// Resolve implements promauth.Resolver. Missing rows collapse to empty
// strings (= no auth header).
func (r *PromResolver) Resolve(ctx context.Context) (promauth.Config, error) {
	return promauth.Config{
		BearerToken:   r.get(ctx, model.KeyPromBearerToken),
		BasicUser:     r.get(ctx, model.KeyPromBasicUser),
		BasicPassword: r.get(ctx, model.KeyPromBasicPassword),
	}, nil
}

// ResolveTLS implements promauth.TLSResolver. Stored values override the
// env-derived startup defaults and are read through the settings cache, which
// is invalidated immediately after a UI save.
func (r *PromResolver) ResolveTLS(ctx context.Context) (promauth.TLSConfig, error) {
	cfg := r.fallbackTLS
	if r.svc == nil {
		return cfg, nil
	}
	if value, found, err := r.svc.Get(ctx, model.CategoryProm, model.KeyPromTLSInsecure); err != nil {
		return cfg, err
	} else if found {
		cfg.Insecure = strings.EqualFold(strings.TrimSpace(value), "true")
	}
	if value, found, err := r.svc.Get(ctx, model.CategoryProm, model.KeyPromTLSCAPEM); err != nil {
		return cfg, err
	} else if found {
		cfg.CAPEM = strings.TrimSpace(value)
	}
	return cfg, nil
}

// ResolveBaseURL implements promquery.BaseURLResolver. Falls back to
// the env-seeded URL when system_settings has no value.
func (r *PromResolver) ResolveBaseURL(ctx context.Context) (string, error) {
	if v := r.get(ctx, model.KeyPromQueryURL); v != "" {
		return strings.TrimRight(v, "/"), nil
	}
	return r.fallbackQueryURL, nil
}

// ResolveWriteURL implements promwrite.EndpointResolver. Returns the
// admin-configured remote_write_url if set; otherwise derives it from the
// configured query URL before falling back to the env-derived endpoint.
func (r *PromResolver) ResolveWriteURL(ctx context.Context) (string, error) {
	if v := r.get(ctx, model.KeyPromRemoteWriteURL); v != "" {
		return v, nil
	}
	if v := r.get(ctx, model.KeyPromQueryURL); v != "" {
		return promRemoteWriteURL(v), nil
	}
	if r.fallbackWriteURL != "" {
		return r.fallbackWriteURL, nil
	}
	base, err := r.ResolveBaseURL(ctx)
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", nil
	}
	return promRemoteWriteURL(base), nil
}

func promRemoteWriteURL(queryURL string) string {
	base := strings.TrimRight(strings.TrimSpace(queryURL), "/")
	u, err := url.Parse(base)
	if err != nil {
		return base + "/api/v1/write"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "select" || parts[1] == "" || parts[2] != "prometheus" {
		return base + "/api/v1/write"
	}

	u.Path = "/insert/" + parts[1] + "/prometheus/api/v1/write"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	if u.Port() == "8481" {
		u.Host = net.JoinHostPort(u.Hostname(), "8480")
	}
	return u.String()
}
