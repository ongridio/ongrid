package setting

import (
	"context"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
)

// LokiResolver reads loki.url + optional basic auth from system_settings.
// Used by:
//
//   - PluginConfigUC.FetchForEdge to decide where the edge's logs plugin
//     pushes (customer Loki vs manager nginx fall-through to the
//     docker-internal Loki).
//   - HTTP handler for "test connection" probes from the Integrations UI.
//
// fallbackURL is the env-seeded default (cfg.Logs.URL); when the DB row
// is missing or empty we fall back to that so a fresh install with no
// admin edits still resolves to the embedded Loki.
type LokiResolver struct {
	svc         *Service
	fallbackURL string
}

// NewLokiResolver builds the resolver. svc must be non-nil.
func NewLokiResolver(svc *Service, fallbackURL string) *LokiResolver {
	return &LokiResolver{svc: svc, fallbackURL: strings.TrimRight(fallbackURL, "/")}
}

func (r *LokiResolver) get(ctx context.Context, key string) string {
	if r == nil || r.svc == nil {
		return ""
	}
	v, _, err := r.svc.Get(ctx, model.CategoryLoki, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// getRaw returns the stored value untouched. Passwords may legitimately
// start or end with whitespace, so they must not go through get.
func (r *LokiResolver) getRaw(ctx context.Context, key string) string {
	if r == nil || r.svc == nil {
		return ""
	}
	v, _, err := r.svc.Get(ctx, model.CategoryLoki, key)
	if err != nil {
		return ""
	}
	return v
}

// URL returns the resolved Loki base URL with trailing slashes trimmed.
// Falls back to the env-seeded value when the DB row is empty.
func (r *LokiResolver) URL(ctx context.Context) string {
	if r == nil {
		return ""
	}
	if v := r.get(ctx, model.KeyLokiURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	return r.fallbackURL
}

// Auth returns optional basic-auth credentials. Empty user means no auth.
func (r *LokiResolver) Auth(ctx context.Context) (basicUser, basicPassword string) {
	if r == nil {
		return "", ""
	}
	return r.get(ctx, model.KeyLokiBasicUser), r.getRaw(ctx, model.KeyLokiBasicPassword)
}

// TLSInsecure reports whether TLS verification should be skipped. The
// stored value is "true" or "false" (the http handler default-marks it
// non-sensitive). Anything other than "true" returns false.
func (r *LokiResolver) TLSInsecure(ctx context.Context) bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(r.get(ctx, model.KeyLokiTLSInsecure), "true")
}

// TempoResolver mirrors LokiResolver for the trace signal. The URL it
// resolves is the OTLP HTTP push endpoint the edge's traces plugin
// targets (otelcol exporters.otlphttp.endpoint).
type TempoResolver struct {
	svc         *Service
	fallbackURL string
}

// NewTempoResolver builds the resolver. svc must be non-nil.
func NewTempoResolver(svc *Service, fallbackURL string) *TempoResolver {
	return &TempoResolver{svc: svc, fallbackURL: strings.TrimRight(fallbackURL, "/")}
}

func (r *TempoResolver) get(ctx context.Context, key string) string {
	if r == nil || r.svc == nil {
		return ""
	}
	v, _, err := r.svc.Get(ctx, model.CategoryTempo, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// getRaw returns the stored value untouched. Passwords may legitimately
// start or end with whitespace, so they must not go through get.
func (r *TempoResolver) getRaw(ctx context.Context, key string) string {
	if r == nil || r.svc == nil {
		return ""
	}
	v, _, err := r.svc.Get(ctx, model.CategoryTempo, key)
	if err != nil {
		return ""
	}
	return v
}

// URL returns the resolved Tempo OTLP HTTP push URL. Falls back to
// the env-seeded value.
func (r *TempoResolver) URL(ctx context.Context) string {
	if r == nil {
		return ""
	}
	if v := r.get(ctx, model.KeyTempoURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	return r.fallbackURL
}

// Auth returns optional basic-auth credentials. Empty user means no auth.
func (r *TempoResolver) Auth(ctx context.Context) (basicUser, basicPassword string) {
	if r == nil {
		return "", ""
	}
	return r.get(ctx, model.KeyTempoBasicUser), r.getRaw(ctx, model.KeyTempoBasicPassword)
}

// TLSInsecure reports whether TLS verification should be skipped.
func (r *TempoResolver) TLSInsecure(ctx context.Context) bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(r.get(ctx, model.KeyTempoTLSInsecure), "true")
}
