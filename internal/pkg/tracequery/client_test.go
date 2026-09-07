package tracequery

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestClient_GetTraceOriginalSkyWalkingID(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"java", "00000000000000000000000000000000.1.1000", "0000000001000000e803000000000000"},
		{"uuid", "de5980b8-fce3-4a37-aab9-b4ac3af7eedd", "de5980b8fce34a37aab9b4ac3af7eedd"},
		{"otlp", "abc123", "abc123"},
		{"otlp prefixed", "0xabc123", "0xabc123"},
		{"bad thread", "00000000000000000000000000000000.bad.1000", "00000000000000000000000000000000.bad.1000"},
		{"bad uuid", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz.1.1000", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz.1.1000"},
		{"overflow", "00000000000000000000000000000000.1.9223372036854775808", "00000000000000000000000000000000.1.9223372036854775808"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/traces/"+tc.want || r.URL.RawQuery != "" {
					t.Errorf("lookup = %s, want direct ID lookup %s", r.URL, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"batches":[]}`)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			if _, err := New(srv.URL, nil).GetTrace(context.Background(), tc.input); err != nil {
				t.Fatal(err)
			}
		})
	}
}
