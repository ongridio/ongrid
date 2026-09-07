package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestExampleRequestInstrumentation(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	app := application(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, tc := range []struct {
		name, path string
		status     int
	}{{"success", "/orders/42", 200}, {"failure", "/orders/42?fail=1", 500}} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[0].Name != "GET /orders/{id}" || spans[0].SpanKind != trace.SpanKindServer || spans[1].Status.Code != codes.Error {
		t.Fatalf("unexpected spans: %+v", spans)
	}
}
