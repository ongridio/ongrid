package promauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubResolver is a counter-tracking Resolver for cache and refresh tests.
type stubResolver struct {
	mu    sync.Mutex
	calls int32
	cfg   Config
	tls   TLSConfig
	err   error
}

func (s *stubResolver) Resolve(_ context.Context) (Config, error) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.err
}

func (s *stubResolver) ResolveTLS(_ context.Context) (TLSConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tls, s.err
}

func TestBuildClientInjectsBearerHeader(t *testing.T) {
	t.Parallel()

	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{cfg: Config{BearerToken: "tok-123"}}
	hc, err := BuildClient(TLSConfig{}, res, 5*time.Second)
	if err != nil {
		t.Fatalf("BuildClient: %v", err)
	}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	select {
	case h := <-got:
		if h != "Bearer tok-123" {
			t.Fatalf("Authorization = %q", h)
		}
	case <-time.After(time.Second):
		t.Fatal("server never received request")
	}
}

func TestBuildClientFallsBackToBasicWhenBearerEmpty(t *testing.T) {
	t.Parallel()

	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := NewStaticResolver(Config{BasicUser: "u", BasicPassword: "p"})
	hc, _ := BuildClient(TLSConfig{}, res, 5*time.Second)
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	h := <-got
	// "u:p" base64 = "dTpw"
	if h != "Basic dTpw" {
		t.Fatalf("Authorization = %q", h)
	}
}

func TestRoundTripperCachesResolverWithinTTL(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	res := &stubResolver{cfg: Config{BearerToken: "x"}}
	hc, _ := BuildClient(TLSConfig{}, res, 5*time.Second)
	for i := 0; i < 5; i++ {
		resp, err := hc.Get(srv.URL)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if got := atomic.LoadInt32(&res.calls); got != 1 {
		t.Fatalf("Resolve calls = %d, want 1 (TTL cache)", got)
	}
}

func TestRoundTripperPropagatesResolverError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	res := &stubResolver{err: errors.New("boom")}
	hc, _ := BuildClient(TLSConfig{}, res, 5*time.Second)
	_, err := hc.Get(srv.URL)
	if err == nil {
		t.Fatal("expected resolve error to surface, got nil")
	}
}

func TestNilResolverPassesThrough(t *testing.T) {
	t.Parallel()

	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc, _ := BuildClient(TLSConfig{}, nil, 5*time.Second)
	resp, _ := hc.Get(srv.URL)
	resp.Body.Close()
	if h := <-got; h != "" {
		t.Fatalf("expected no auth header, got %q", h)
	}
}

func TestBuildClientReloadsTLSConfig(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{}
	hc, err := BuildClient(TLSConfig{}, res, 5*time.Second)
	if err != nil {
		t.Fatalf("BuildClient: %v", err)
	}
	if _, err := hc.Get(srv.URL); err == nil {
		t.Fatal("self-signed server unexpectedly passed with TLS verification enabled")
	}

	res.mu.Lock()
	res.tls.Insecure = true
	res.mu.Unlock()
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatalf("get after enabling TLS skip verify: %v", err)
	}
	resp.Body.Close()
}
