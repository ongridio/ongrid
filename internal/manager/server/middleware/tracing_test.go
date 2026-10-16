package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	chi "github.com/go-chi/chi/v5"
	bizagenttrace "github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	serveragenttrace "github.com/ongridio/ongrid/internal/manager/server/agenttrace"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestShouldTraceHTTPRequest(t *testing.T) {
	tests := map[string]bool{
		"/healthz":                               false,
		"/readyz":                                false,
		"/api/v1/prometheus/auth":                false,
		"/api/v1/traces":                         false,
		"/api/v1/traces/search":                  false,
		"/api/v1/agent-observability":            false,
		"/api/v1/agent-observability/runs":       false,
		"/api/v1/agent-observability/traces/abc": false,
		"/internal/auth/dataPlane-verify":        true,
		"/api/v1/aiops/sessions":                 true,
	}
	for path, want := range tests {
		if got := ShouldTraceHTTPRequest(httptest.NewRequest("GET", path, nil)); got != want {
			t.Errorf("ShouldTraceHTTPRequest(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestAgentObservationReadsDoNotTraceThemselves(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	var queries atomic.Int32
	tempo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		if r.Header.Get("traceparent") != "" {
			t.Error("observation query propagated tracing context")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"traces":[]}`))
	}))
	defer tempo.Close()
	router := chi.NewRouter()
	router.Use(otelhttp.NewMiddleware("manager", otelhttp.WithFilter(ShouldTraceHTTPRequest)))
	router.Route("/api", func(r chi.Router) {
		serveragenttrace.NewHandler(bizagenttrace.New(tracequery.New(tempo.URL, nil)), nil).Register(r)
	})
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-observability/runs?start=2026-10-14T00:00:00Z&end=2026-10-14T01:00:00Z", nil)
		req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 1}))
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("query status = %d: %s", out.Code, out.Body.String())
		}
	}
	if queries.Load() != 3 || len(recorder.Ended()) != 0 {
		t.Fatalf("3 reads produced %d backend calls and %d spans", queries.Load(), len(recorder.Ended()))
	}
}
