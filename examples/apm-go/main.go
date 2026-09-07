// A runnable application example using the official OpenTelemetry SDK.
// Run from the repository root: go run ./examples/apm-go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	if err := run(); err != nil {
		slog.Error("APM example failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return fmt.Errorf("create OTLP exporter: %w", err)
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return fmt.Errorf("create resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdown); err != nil {
			slog.Error("flush telemetry", "error", err)
		}
	}()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service.name", os.Getenv("OTEL_SERVICE_NAME"), "service.namespace", "trade", "deployment.environment.name", "development")
	mux := application(log)
	server := &http.Server{Addr: "127.0.0.1:18080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve example: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shutdown example: %w", err)
	}
	return nil
}

func application(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /orders/{id}", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		sc := span.SpanContext()
		status := http.StatusOK
		if r.URL.Query().Get("fail") == "1" {
			status = http.StatusInternalServerError
			span.SetStatus(codes.Error, "example failure")
		}
		if r.URL.Query().Get("slow") == "1" {
			select {
			case <-time.After(1200 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		log.InfoContext(r.Context(), "order handled", "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "status", status)
		w.WriteHeader(status)
		if _, err := fmt.Fprintln(w, http.StatusText(status)); err != nil {
			log.Debug("write response", "error", err)
		}
	}), "GET /orders/{id}"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := fmt.Fprintln(w, "apm_example_up 1"); err != nil {
			log.Debug("write metrics", "error", err)
		}
	})
	return mux
}
