package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/httpserver"
	"github.com/prometheus/client_golang/prometheus"
)

func TestEdgeMetricsOccupiedPort(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			occupied, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err != nil {
				if host == "::1" {
					t.Skipf("IPv6 unavailable: %v", err)
				}
				t.Fatal(err)
			}
			t.Cleanup(func() { occupied.Close() })
			addr := occupied.Addr().String()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ln, err := listenEdgeMetrics(ctx, addr, true, log)
			if err != nil {
				t.Fatal(err)
			}
			actual := ln.Addr().String()
			if actual == addr {
				t.Fatal("fallback reused occupied address")
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
			mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
			done := make(chan error, 1)
			go func() { done <- httpserver.New(addr, mux, log).StartListener(ctx, ln) }()
			client := &http.Client{Timeout: 2 * time.Second}
			defer client.CloseIdleConnections()
			for _, path := range []string{"/healthz", "/metrics"} {
				res, err := client.Get("http://" + actual + path)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					t.Fatalf("%s status=%d", path, res.StatusCode)
				}
			}
			// Fixed mode must not hide a conflict, including the untouched K8s data plane.
			t.Setenv("ONGRID_EDGE_METRICS_ADDR", addr)
			if err := runEdgeMetrics(ctx, mux, log); err == nil {
				t.Fatal("fixed edge listener accepted occupied address")
			}
			if err := runDataPlaneDiagnostics(ctx, prometheus.NewRegistry(), nil, log); err == nil {
				t.Fatal("K8s data plane silently changed its fixed port")
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("listener did not shut down")
			}
			// The fallback port is released; the original listener remains ours.
			rebound, err := net.Listen("tcp", actual)
			if err != nil {
				t.Fatal(err)
			}
			rebound.Close()
			if err := occupied.(*net.TCPListener).SetDeadline(time.Now()); err != nil {
				t.Fatalf("original listener was closed: %v", err)
			}
		})
	}
}

func TestEdgeMetricsValidationAndNonConflictErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, addr := range []string{"bad", ":0", ":-1", ":65536", ":http", "::1:9101"} {
		if ln, err := listenEdgeMetrics(t.Context(), addr, true, log); err == nil {
			ln.Close()
			t.Fatalf("accepted invalid address %q", addr)
		}
	}
	// An invalid local IP fails before binding. Auto mode must preserve that error.
	if ln, err := listenEdgeMetrics(t.Context(), "256.256.256.256:9101", true, log); err == nil {
		ln.Close()
		t.Fatal("invalid host accepted")
	} else if strings.Contains(err.Error(), "automatic allocation failed") {
		t.Fatalf("non-conflict error triggered fallback: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if ln, err := listenEdgeMetrics(ctx, "127.0.0.1:9101", true, log); err == nil {
		ln.Close()
		t.Fatal("cancelled listen succeeded")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestEdgeMetricsAvailablePort(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	ln, err := listenEdgeMetrics(t.Context(), addr, true, log)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().String() != addr {
		t.Fatalf("available preferred port changed: %s", ln.Addr())
	}
}

func TestEdgeMetricsConcurrentFallback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	listeners := make([]net.Listener, 8)
	errors := make([]error, len(listeners))
	var wg sync.WaitGroup
	for i := range listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			listeners[i], errors[i] = listenEdgeMetrics(t.Context(), occupied.Addr().String(), true, log)
		}()
	}
	wg.Wait()
	seen := map[string]bool{occupied.Addr().String(): true}
	for i, ln := range listeners {
		if errors[i] != nil {
			t.Error(errors[i])
			continue
		}
		defer ln.Close()
		addr := ln.Addr().String()
		if seen[addr] {
			t.Errorf("concurrent listeners reused %s", addr)
		}
		seen[addr] = true
	}
}
