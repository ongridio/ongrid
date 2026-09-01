// Package promauth builds *http.Client values used by the promwrite +
// promquery clients to talk to a Prometheus-compatible TSDB. It centralises
// two concerns:
//
//   - TLS: optional skip-verify and / or a custom root CA. A Resolver that
//     also implements TLSResolver can update these settings at runtime.
//
//   - Auth: Bearer / Basic credentials. These are resolved per-request via
//     a Resolver so admin edits to system_settings take effect within the
//     internal TTL window (default 5s) without a restart.
//
// The split mirrors how Prom's own client_golang treats them: hostname /
// TLS belongs to the dialer, headers belong to the round trip.
package promauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Config carries the per-request credentials. Empty values mean "no auth
// header for that mechanism"; precedence is Bearer > Basic. If both are
// populated only Bearer is sent — matching curl's -H precedence over -u.
//
// File-mounted bearer tokens (the prometheus.yml `bearer_token_file`
// pattern) intentionally aren't supported here: ongrid runs as a docker-
// managed monolith, and the only way to put a file inside the manager
// container is to ssh in and bind-mount it, which defeats the point of a
// UI-driven configuration. If a real K8s deployment ever needs it, push
// the token into system_settings.prom.bearer_token via the same UI.
type Config struct {
	BearerToken   string
	BasicUser     string
	BasicPassword string
}

// TLSConfig carries the static dialer-level TLS settings. Empty values
// mean "use Go defaults". CAPEM and CAPath are merged: both sources are
// added to the same root pool when set.
type TLSConfig struct {
	Insecure bool   // skip cert verification
	CAPath   string // path to PEM file
	CAPEM    string // raw PEM string (overrides any system roots when used alone)
}

// Resolver returns the current credentials. Implementations are expected
// to be cheap (the round-tripper calls them on every HTTP request, so a
// short TTL cache is highly recommended).
type Resolver interface {
	Resolve(ctx context.Context) (Config, error)
}

// TLSResolver returns the current dialer-level TLS settings. BuildClient
// detects this optional interface so existing static resolvers keep their
// original behaviour.
type TLSResolver interface {
	ResolveTLS(ctx context.Context) (TLSConfig, error)
}

// staticResolver is the trivial Resolver — always returns the same config.
// Useful for tests and as a fallback when system_settings is empty.
type staticResolver struct{ cfg Config }

func (s staticResolver) Resolve(_ context.Context) (Config, error) { return s.cfg, nil }

// NewStaticResolver returns a Resolver that always emits cfg.
func NewStaticResolver(cfg Config) Resolver { return staticResolver{cfg: cfg} }

// authTTL is how long the round-tripper caches a resolved Config before
// asking the resolver again. UI saves to system_settings invalidate the
// underlying biz/setting cache anyway, so 5s is enough headroom for two
// pieces of code to converge without re-reading every request.
const authTTL = 5 * time.Second

// BuildClient builds an http.Client whose Transport applies tlsCfg at the
// dialer layer and (if resolver != nil) injects auth headers per request.
//
// timeout caps the total round-trip. resolver may be nil — pass-through.
func BuildClient(tlsCfg TLSConfig, resolver Resolver, timeout time.Duration) (*http.Client, error) {
	transport, err := buildTransport(tlsCfg)
	if err != nil {
		return nil, err
	}

	var rt http.RoundTripper = transport
	if tlsResolver, ok := resolver.(TLSResolver); ok {
		rt = &tlsRoundTripper{resolver: tlsResolver, cachedConfig: tlsCfg, cached: transport}
	}
	if resolver != nil {
		rt = &authRoundTripper{base: rt, resolver: resolver}
	}
	return &http.Client{Transport: rt, Timeout: timeout}, nil
}

type tlsRoundTripper struct {
	resolver TLSResolver

	mu           sync.Mutex
	cachedConfig TLSConfig
	cached       *http.Transport
}

func (t *tlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg, err := t.resolver.ResolveTLS(req.Context())
	if err != nil {
		return nil, fmt.Errorf("promauth: resolve TLS config: %w", err)
	}
	transport, err := t.transport(cfg)
	if err != nil {
		return nil, err
	}
	return transport.RoundTrip(req)
}

func (t *tlsRoundTripper) transport(cfg TLSConfig) (*http.Transport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cfg == t.cachedConfig {
		return t.cached, nil
	}
	next, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}
	t.cached.CloseIdleConnections()
	t.cachedConfig = cfg
	t.cached = next
	return next, nil
}

// authRoundTripper decorates an http.RoundTripper with auth headers
// resolved on each call (within authTTL).
type authRoundTripper struct {
	base     http.RoundTripper
	resolver Resolver

	mu       sync.Mutex
	cached   Config
	cachedAt time.Time
}

// RoundTrip resolves credentials, clones the request to avoid mutating the
// caller's Header (RoundTripper contract), and invokes the underlying
// transport. Resolver errors are surfaced — the assumption is "no auth =
// fail closed" because a silent auth-less request to a real TSDB looks
// identical to a network glitch and is much harder to diagnose.
func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg, err := a.fetch(req.Context())
	if err != nil {
		return nil, fmt.Errorf("promauth: resolve creds: %w", err)
	}

	cloned := req.Clone(req.Context())
	if cfg.BearerToken != "" {
		cloned.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	} else if cfg.BasicUser != "" {
		cloned.SetBasicAuth(cfg.BasicUser, cfg.BasicPassword)
	}
	return a.base.RoundTrip(cloned)
}

func (a *authRoundTripper) fetch(ctx context.Context) (Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cachedAt.IsZero() && time.Since(a.cachedAt) < authTTL {
		return a.cached, nil
	}
	cfg, err := a.resolver.Resolve(ctx)
	if err != nil {
		return Config{}, err
	}
	a.cached = cfg
	a.cachedAt = time.Now()
	return cfg, nil
}

func buildPool(tlsCfg TLSConfig) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if tlsCfg.CAPEM != "" {
		if !pool.AppendCertsFromPEM([]byte(tlsCfg.CAPEM)) {
			return nil, errors.New("promauth: TLSConfig.CAPEM contained no valid certificates")
		}
	}
	if tlsCfg.CAPath != "" {
		b, err := os.ReadFile(tlsCfg.CAPath)
		if err != nil {
			return nil, fmt.Errorf("promauth: read TLSConfig.CAPath: %w", err)
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("promauth: TLSConfig.CAPath contained no valid certificates")
		}
	}
	return pool, nil
}

func buildTransport(tlsCfg TLSConfig) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !tlsCfg.Insecure && tlsCfg.CAPath == "" && tlsCfg.CAPEM == "" {
		return transport, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: tlsCfg.Insecure}
	if tlsCfg.CAPath != "" || tlsCfg.CAPEM != "" {
		pool, err := buildPool(tlsCfg)
		if err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}
