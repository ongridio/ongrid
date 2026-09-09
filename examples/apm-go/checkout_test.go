package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCheckoutVersionRegression(t *testing.T) {
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
		path    string
		status  int
		message string
	}{
		{"/checkout/42?region=US", 200, "checkout completed"},
		{"/checkout/42?coupon=SAVE20&region=EU&quantity=2", 200, `"total_cents":4900`},
		{"/checkout/42?coupon=SAVE20&region=eu&quantity=2", 500, "SHIPPING_RATE_NOT_FOUND"},
		{"/checkout/42?quantity=-1", 400, "quantity must"},
		{"/checkout/42?quantity=9999999999999999999999", 400, "quantity must"},
		{"/checkout/42?region=unknown", 400, "unsupported"},
	} {
		w := httptest.NewRecorder()
		app.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) {
			t.Fatalf("%s: status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
	}
	found := false
	for _, span := range exporter.GetSpans() {
		if span.Name == "shipping.quote" && span.Status.Code == codes.Error {
			found = true
			if !span.Parent.IsValid() || len(span.Events) == 0 {
				t.Fatal("missing error evidence or parent")
			}
		}
	}
	if !found {
		t.Fatal("missing failing child span")
	}
}
