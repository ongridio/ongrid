package telemetry

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"strings"

	managerbizk8s "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	"github.com/ongridio/ongrid/internal/pkg/config"
	pkglogquery "github.com/ongridio/ongrid/internal/pkg/logquery"
)

// PluginEndpointResolver implements edgebiz.EndpointResolver: maps a
// plugin name to the URL the edge subprocess should push to. Two-tier
// resolution:
//
//  1. Admin-supplied URL in system_settings (loki.url / tempo.url) —
//     when set to a browser-/edge-reachable URL (e.g.
//     https://loki.customer.com), the edge pushes there directly.
//  2. Fallback: manager's PublicURL + the per-plugin path. The cloud
//     nginx then auth_request-gates and proxy_pass's into the
//     docker-internal Loki/Tempo. This is the "out of the box" path
//     where loki.url still equals the env-seeded http://loki:3100,
//     which is unreachable from the edge.
//
// We treat any URL whose hostname looks like the docker-internal
// service name (loki, tempo, prometheus, grafana) — i.e. has no dot
// and no port-without-host — as a marker that the admin hasn't
// overridden the seed and we should fall through to PublicURL.
type PluginEndpointResolver struct {
	publicURL string
	loki      telemetryBackendResolver
	tempo     telemetryBackendResolver
}

type telemetryBackendResolver interface {
	URL(ctx context.Context) string
	Auth(ctx context.Context) (basicUser, basicPassword string)
	TLSInsecure(ctx context.Context) bool
}

type LokiQueryEndpointResolver struct {
	resolver telemetryBackendResolver
}

func (r LokiQueryEndpointResolver) ResolveLokiEndpoint(ctx context.Context) (pkglogquery.LokiEndpoint, error) {
	if r.resolver == nil {
		return pkglogquery.LokiEndpoint{}, errors.New("Loki query resolver is unavailable")
	}
	user, password := r.resolver.Auth(ctx)
	return pkglogquery.LokiEndpoint{
		URL:           r.resolver.URL(ctx),
		BasicUser:     user,
		BasicPassword: password,
		TLSInsecure:   r.resolver.TLSInsecure(ctx),
	}, nil
}

func (r PluginEndpointResolver) Endpoint(ctx context.Context, plugin string) string {
	// Keep the Edge wire contract on Loki's legacy push URL. Pre-OTel Edge
	// releases pass it directly to Promtail, while current releases translate
	// it to OTLP locally. Sending OTLP here would break every old Edge as soon
	// as Manager reloads its plugin config.
	if plugin == "logs" {
		if r.loki != nil {
			if u := edgeReachableLokiURL(r.loki.URL(ctx)); u != "" {
				return u + "/loki/api/v1/push"
			}
		}
		if r.publicURL != "" {
			return strings.TrimRight(r.publicURL, "/") + "/loki/api/v1/push"
		}
	}
	target, err := r.ResolveTelemetryTarget(ctx, plugin)
	if err != nil {
		return ""
	}
	return target.Endpoint
}

func (r PluginEndpointResolver) ResolveTelemetryTarget(ctx context.Context, signal string) (managerbizk8s.TelemetryTarget, error) {
	switch signal {
	case "logs":
		if r.loki != nil {
			if u := edgeReachableLokiURL(r.loki.URL(ctx)); u != "" {
				user, password := r.loki.Auth(ctx)
				return managerbizk8s.TelemetryTarget{
					Endpoint:      u + "/otlp/v1/logs",
					BasicUser:     user,
					BasicPassword: password,
					TLSInsecure:   r.loki.TLSInsecure(ctx),
				}, nil
			}
		}
		if r.publicURL == "" {
			return managerbizk8s.TelemetryTarget{}, nil
		}
		return managerbizk8s.TelemetryTarget{
			Endpoint:               strings.TrimRight(r.publicURL, "/") + "/loki/otlp/v1/logs",
			UseTelemetryCredential: true,
		}, nil
	case "traces":
		if r.tempo != nil {
			if u := edgeReachableTempoURL(r.tempo.URL(ctx)); u != "" {
				// If the admin URL already includes /v1/traces (some
				// OTLP endpoints publish the path inline), respect it.
				endpoint := u
				if !strings.HasSuffix(endpoint, "/v1/traces") {
					endpoint += "/v1/traces"
				}
				user, password := r.tempo.Auth(ctx)
				return managerbizk8s.TelemetryTarget{
					Endpoint:      endpoint,
					BasicUser:     user,
					BasicPassword: password,
					TLSInsecure:   r.tempo.TLSInsecure(ctx),
				}, nil
			}
		}
		if r.publicURL == "" {
			return managerbizk8s.TelemetryTarget{}, nil
		}
		return managerbizk8s.TelemetryTarget{
			Endpoint:               strings.TrimRight(r.publicURL, "/") + "/v1/traces",
			UseTelemetryCredential: true,
		}, nil
	case "profiles":
		if r.publicURL == "" {
			return managerbizk8s.TelemetryTarget{}, nil
		}
		return managerbizk8s.TelemetryTarget{
			Endpoint:               strings.TrimRight(r.publicURL, "/") + "/v1development/profiles",
			UseTelemetryCredential: true,
		}, nil
	}
	return managerbizk8s.TelemetryTarget{}, nil
}

// edgeReachableLokiURL returns the URL when it looks like an
// admin-configured external endpoint (a public hostname or IP), and ""
// when it's the docker-internal seed which the edge can't reach. The
// caller falls back to the manager's PublicURL in the latter case.
func edgeReachableLokiURL(u string) string {
	if !isEdgeReachableURL(u) {
		return ""
	}
	return strings.TrimRight(u, "/")
}

func edgeReachableTempoURL(u string) string {
	if !isEdgeReachableURL(u) {
		return ""
	}
	return strings.TrimRight(u, "/")
}

// isEdgeReachableURL reports whether the URL looks reachable from an
// edge across the public internet. The seed values from cfg.Logs.URL /
// cfg.Traces.URL point at docker-service hostnames (loki, tempo) which
// resolve only on the manager's compose network.
func isEdgeReachableURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return false
	}
	// Docker compose service names have no dot and no IP form.
	if !strings.Contains(host, ".") {
		return false
	}
	return true
}

type RemoteWriteResolver struct {
	resolver  *managerbizsetting.PromResolver
	prom      config.PromConfig
	publicURL string
}

func (r RemoteWriteResolver) ResolveRemoteWrite(ctx context.Context) (managerbizk8s.RemoteWriteTarget, error) {
	if !r.prom.Enabled || r.resolver == nil {
		return managerbizk8s.RemoteWriteTarget{}, nil
	}
	writeURL, err := r.resolver.ResolveWriteURL(ctx)
	if err != nil {
		return managerbizk8s.RemoteWriteTarget{}, err
	}
	if isEmbeddedPrometheusURL(writeURL) {
		base := strings.TrimRight(strings.TrimSpace(r.publicURL), "/")
		if base == "" {
			return managerbizk8s.RemoteWriteTarget{}, nil
		}
		return managerbizk8s.RemoteWriteTarget{
			Endpoint:               base + "/prometheus/api/v1/write",
			UseTelemetryCredential: true,
		}, nil
	}
	authConfig, err := r.resolver.Resolve(ctx)
	if err != nil {
		return managerbizk8s.RemoteWriteTarget{}, err
	}
	tlsConfig, err := r.resolver.ResolveTLS(ctx)
	if err != nil {
		return managerbizk8s.RemoteWriteTarget{}, err
	}
	caPEM := strings.TrimSpace(tlsConfig.CAPEM)
	if path := strings.TrimSpace(tlsConfig.CAPath); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return managerbizk8s.RemoteWriteTarget{}, fmt.Errorf("read prometheus TLS CA: %w", err)
		}
		if caPEM != "" {
			caPEM += "\n"
		}
		caPEM += string(raw)
	}
	return managerbizk8s.RemoteWriteTarget{
		Endpoint:      writeURL,
		BearerToken:   authConfig.BearerToken,
		BasicUser:     authConfig.BasicUser,
		BasicPassword: authConfig.BasicPassword,
		TLSInsecure:   tlsConfig.Insecure,
		TLSCAPEM:      caPEM,
	}, nil
}

func isEmbeddedPrometheusURL(raw string) bool {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "prometheus")
}

func NewPluginEndpointResolver(publicURL string, loki, tempo telemetryBackendResolver) PluginEndpointResolver {
	return PluginEndpointResolver{publicURL: publicURL, loki: loki, tempo: tempo}
}

func NewLokiQueryEndpointResolver(resolver telemetryBackendResolver) LokiQueryEndpointResolver {
	return LokiQueryEndpointResolver{resolver: resolver}
}

func NewRemoteWriteResolver(resolver *managerbizsetting.PromResolver, prom config.PromConfig, publicURL string) RemoteWriteResolver {
	return RemoteWriteResolver{resolver: resolver, prom: prom, publicURL: publicURL}
}
