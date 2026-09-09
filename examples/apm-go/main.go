// A runnable application example using the official OpenTelemetry SDK.
// Run from the repository root: go run ./examples/apm-go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	prombridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Set from the exact source checkout at build time.
var version = "dev"
var commit = ""

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
	buildVersion := version
	if buildVersion == "dev" {
		buildVersion = envDefault("SERVICE_VERSION", "dev")
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithTelemetrySDK(), resource.WithAttributes(
		attribute.String("service.version", buildVersion), attribute.String("vcs.ref.head.revision", commit),
	))
	if err != nil {
		return fmt.Errorf("create resource: %w", err)
	}
	ratio := 1.0
	if raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); raw != "" {
		ratio, err = strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			return fmt.Errorf("OTEL_TRACES_SAMPLER_ARG must be 0..1")
		}
	}
	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return fmt.Errorf("create metrics exporter: %w", err)
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
		sdkmetric.WithProducer(prombridge.NewMetricProducer(prombridge.WithGatherer(registry))))))
	otel.SetMeterProvider(meter)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := meter.Shutdown(shutdown); err != nil {
			slog.Error("flush metrics", "error", err)
		}
	}()
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))))
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdown); err != nil {
			slog.Error("flush telemetry", "error", err)
		}
	}()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service.name", os.Getenv("OTEL_SERVICE_NAME"), "service.namespace", envDefault("SERVICE_NAMESPACE", "trade"), "deployment.environment.name", envDefault("DEPLOYMENT_ENVIRONMENT", "development"), "service.instance.id", os.Getenv("SERVICE_INSTANCE_ID"), "service.version", buildVersion, "commit_sha", commit)
	mux := application(log)
	server := &http.Server{Addr: "127.0.0.1:" + envDefault("HTTP_PORT", "18080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	listener, err := net.Listen("tcp", "127.0.0.1:"+envDefault("RPC_PORT", "18081"))
	if err != nil {
		return fmt.Errorf("listen RPC: %w", err)
	}
	rpcServer := rpcApplication()
	defer rpcServer.Stop()
	result := make(chan error, 2)
	go func() { result <- rpcServer.Serve(listener) }()
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
	mux.Handle("GET /checkout/{id}", otelhttp.NewHandler(checkoutHandler(log), "GET /checkout/{id}"))
	mux.HandleFunc("GET /build", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"version": version, "commit_sha": commit}); err != nil {
			log.Debug("write build info", "error", err)
		}
	})
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

// The standard health service exercises successful and failed unary RPC calls.
func rpcApplication() *grpc.Server {
	server := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	healthpb.RegisterHealthServer(server, health.NewServer())
	return server
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
