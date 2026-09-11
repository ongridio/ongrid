package tracequery

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

func TestClientReusesParallelTraceConnections(t *testing.T) {
	var connections, requests atomic.Int32
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(requests.Add(1)) - 1
		gate := gates[n/8]
		if n%8 == 7 {
			close(gate)
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"resourceSpans":[]}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	c := New(srv.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for range 2 {
		workers, ctx := errgroup.WithContext(ctx)
		for range 8 {
			workers.Go(func() error { _, err := c.GetTrace(ctx, "abc123"); return err })
		}
		if err := workers.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 8 {
		t.Fatalf("parallel reads redialed connections: %d", connections.Load())
	}
}

func TestClient_SearchTraces_Query(t *testing.T) {
	var gotPath string
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"traces":[{"traceID":"abc","rootServiceName":"web"}],"metrics":{"inspectedTraces":1}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, slog.Default())
	res, err := c.SearchTraces(context.Background(), SearchOptions{
		Query: `{ resource.service.name = "web" }`,
		Limit: 50,
		Start: time.Unix(1700000000, 0),
		End:   time.Unix(1700001000, 0),
	})
	if err != nil {
		t.Fatalf("SearchTraces: %v", err)
	}
	if gotPath != "/api/search" {
		t.Errorf("path = %q, want /api/search", gotPath)
	}
	if !strings.Contains(gotQuery, "q=") {
		t.Errorf("query string missing q=: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "limit=50") {
		t.Errorf("query string missing limit=50: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "start=1700000000") {
		t.Errorf("query string missing start: %q", gotQuery)
	}
	if len(res.Traces) == 0 {
		t.Errorf("expected non-empty traces blob")
	}
}

func TestClient_SearchTraces_DefaultLimit(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"traces":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, slog.Default())
	if _, err := c.SearchTraces(context.Background(), SearchOptions{}); err != nil {
		t.Fatalf("SearchTraces: %v", err)
	}
	if !strings.Contains(gotQuery, "limit=100") {
		t.Errorf("expected default limit=100, got %q", gotQuery)
	}
}

func TestClient_GetTrace(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"batches":[{"resource":{"attributes":[]},"scopeSpans":[]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, slog.Default())
	res, err := c.GetTrace(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if gotPath != "/api/traces/abc123" {
		t.Errorf("path = %q, want /api/traces/abc123", gotPath)
	}
	if len(res.Body) == 0 {
		t.Errorf("expected trace body to be passed through")
	}
}

func TestClient_GetTrace_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"trace not found"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, slog.Default())
	if _, err := c.GetTrace(context.Background(), "deadbeef"); err == nil {
		t.Errorf("expected not-found error")
	}
}

func TestClient_GetTrace_EmptyID(t *testing.T) {
	c := New("http://example", slog.Default())
	if _, err := c.GetTrace(context.Background(), ""); err == nil {
		t.Errorf("expected error for empty traceID")
	}
}
