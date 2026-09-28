package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	model "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
)

type flowMCPFake struct {
	servers []*model.Server
	build   func(context.Context, *model.Server) (*mcpclient.Client, error)
}

func (f *flowMCPFake) ListEnabled(context.Context) ([]*model.Server, error) { return f.servers, nil }
func (f *flowMCPFake) BuildClient(ctx context.Context, s *model.Server) (*mcpclient.Client, error) {
	if f.build != nil {
		return f.build(ctx, s)
	}
	return mcpclient.NewHTTP(s.Endpoint, nil, 0), nil
}
func (f *flowMCPFake) CallTool(context.Context, string, string, map[string]any) (string, error) {
	return "", fmt.Errorf("unexpected call")
}

func mcpTestServer(t *testing.T, probes *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		if req.Method == "tools/list" {
			probes.Add(1)
			result = map[string]any{"tools": []mcpclient.Tool{{Name: "list", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFlowMCPDiscoveryCachesTools(t *testing.T) {
	var probes atomic.Int32
	srv := mcpTestServer(t, &probes)
	source := &flowMCPSource{uc: &flowMCPFake{servers: []*model.Server{{Name: "test", Endpoint: srv.URL}}}}
	for i := 0; i < 2; i++ {
		if got := source.enumerate(context.Background()); len(got) != 1 {
			t.Fatalf("tools: %+v", got)
		}
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probes=%d, want cached single probe", got)
	}
}

func TestFlowMCPDiscoveryParallel(t *testing.T) {
	var probes atomic.Int32
	srv := mcpTestServer(t, &probes)
	entered := make(chan struct{}, 2)
	backend := &flowMCPFake{servers: []*model.Server{{Name: "one", Endpoint: srv.URL}, {Name: "two", Endpoint: srv.URL}}}
	backend.build = func(ctx context.Context, s *model.Server) (*mcpclient.Client, error) {
		entered <- struct{}{}
		for len(entered) < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				time.Sleep(time.Millisecond)
			}
		}
		return mcpclient.NewHTTP(s.Endpoint, nil, 0), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := (&flowMCPSource{uc: backend}).enumerate(ctx)
	if len(got) != 2 {
		t.Fatalf("parallel discovery returned %d tools, want 2", len(got))
	}
}

func TestFlowMCPCacheInvalidationAndIsolation(t *testing.T) {
	var probes atomic.Int32
	srv := mcpTestServer(t, &probes)
	backend := &flowMCPFake{servers: []*model.Server{{Name: "old", Endpoint: srv.URL}}}
	source := &flowMCPSource{uc: backend}
	ctx := context.Background()
	first := source.enumerate(ctx)
	first[0].schema[0] = '!'
	if got := source.enumerate(ctx); !json.Valid(got[0].schema) {
		t.Fatal("caller mutated cached schema")
	}
	backend.servers[0].Name = "new"
	if got := source.enumerate(ctx); len(got) != 1 || got[0].server != "new" {
		t.Fatalf("stale config: %+v", got)
	}
	source.cache.mu.Lock()
	source.cache.expires = time.Time{}
	source.cache.mu.Unlock()
	source.enumerate(ctx)
	if got := probes.Load(); got != 3 {
		t.Fatalf("probes=%d, want 3", got)
	}
	backend.servers = nil
	if got := source.enumerate(ctx); len(got) != 0 {
		t.Fatalf("removed server still visible: %+v", got)
	}
}

func TestFlowMCPFailureCacheAndCancellation(t *testing.T) {
	var builds atomic.Int32
	backend := &flowMCPFake{servers: []*model.Server{{Name: "offline"}}}
	backend.build = func(ctx context.Context, _ *model.Server) (*mcpclient.Client, error) {
		builds.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > flowMCPProbeTimeout {
			t.Error("missing discovery timeout")
		}
		return nil, fmt.Errorf("offline")
	}
	source := &flowMCPSource{uc: backend}
	source.enumerate(context.Background())
	source.enumerate(context.Background())
	if builds.Load() != 1 {
		t.Fatal("failed discovery was not cached")
	}
	source.cache.mu.Lock()
	source.cache.expires = time.Time{}
	source.cache.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source.enumerate(ctx)
	source.enumerate(context.Background())
	if builds.Load() != 2 {
		t.Fatal("canceled discovery polluted the cache")
	}
}

func TestFlowMCPConcurrentReaders(t *testing.T) {
	var probes, builds atomic.Int32
	srv := mcpTestServer(t, &probes)
	backend := &flowMCPFake{servers: []*model.Server{{Name: "shared", Endpoint: srv.URL}}}
	entered, release := make(chan struct{}), make(chan struct{})
	backend.build = func(ctx context.Context, s *model.Server) (*mcpclient.Client, error) {
		if builds.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		return mcpclient.NewHTTP(s.Endpoint, nil, 0), nil
	}
	source := &flowMCPSource{uc: backend}
	var group errgroup.Group
	for i := 0; i < 16; i++ {
		group.Go(func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panic: %v", p)
				}
			}()
			if got := source.enumerate(context.Background()); len(got) != 1 {
				return fmt.Errorf("tools=%d", len(got))
			}
			return nil
		})
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	// 刷新进行中，取消的等待者应立即返回，不取消共享探测。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := source.enumerate(ctx); len(got) != 0 {
		t.Error("canceled waiter returned tools")
	}
	close(release)
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("duplicate discovery: builds=%d probes=%d", builds.Load(), probes.Load())
	}
}

func TestFlowMCPConcurrencyLimitAndPartialResults(t *testing.T) {
	var probes, active, maximum atomic.Int32
	srv := mcpTestServer(t, &probes)
	backend := &flowMCPFake{}
	for i := 0; i < 12; i++ {
		backend.servers = append(backend.servers, &model.Server{Name: fmt.Sprintf("server-%d", i), Endpoint: srv.URL})
	}
	backend.build = func(ctx context.Context, s *model.Server) (*mcpclient.Client, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		if s.Name == "server-0" {
			return nil, fmt.Errorf("offline")
		}
		return mcpclient.NewHTTP(s.Endpoint, nil, 0), nil
	}
	got := (&flowMCPSource{uc: backend}).enumerate(context.Background())
	if len(got) != 11 {
		t.Fatalf("healthy tools=%d, want 11", len(got))
	}
	if maximum.Load() > flowMCPConcurrency {
		t.Fatalf("concurrency=%d", maximum.Load())
	}
	for i, e := range got {
		if want := fmt.Sprintf("server-%d", i+1); e.server != want {
			t.Fatalf("unstable order: %s, want %s", e.server, want)
		}
	}
}
