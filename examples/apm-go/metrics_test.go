package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/apm"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Real official HTTP/gRPC instrumentation must keep metrics with sampling at zero.
// Optional isolated Collector + Prometheus also exercises the public APM adapter.
func TestExampleMetricsWithoutTraces(t *testing.T) {
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "http,rpc/dup")
	ctx := t.Context()
	reader := sdkmetric.NewManualReader()
	opts := []sdkmetric.Option{sdkmetric.WithReader(reader), sdkmetric.WithResource(resource.NewSchemaless(attribute.String("service.name", "apm-sdk-test"), attribute.String("service.namespace", "trade"), attribute.String("deployment.environment.name", "test")))}
	integrated := os.Getenv("APM_TEST_METRICS") != ""
	if integrated {
		exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(os.Getenv("APM_TEST_METRICS")))
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)))
	}
	meter := sdkmetric.NewMeterProvider(opts...)
	spans := tracetest.NewInMemoryExporter()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans), sdktrace.WithSampler(sdktrace.NeverSample()))
	oldMeter, oldTracer := otel.GetMeterProvider(), otel.GetTracerProvider()
	otel.SetMeterProvider(meter)
	otel.SetTracerProvider(tracer)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldMeter)
		otel.SetTracerProvider(oldTracer)
		if err := meter.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := tracer.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	app := httptest.NewServer(application(slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer app.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := rpcApplication()
	defer rpc.Stop()
	go func() {
		if err := rpc.Serve(listener); err != nil {
			t.Error(err)
		}
	}()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := healthpb.NewHealthClient(conn)
	start := time.Now().Add(-time.Minute)
	for batch := 0; batch < 3; batch++ {
		for i := 0; i < 4; i++ {
			suffix, service := "", ""
			if i == 0 {
				suffix, service = "?fail=1", "missing"
			}
			response, err := http.Get(app.URL + "/orders/42" + suffix)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: service}); (err != nil) != (i == 0) {
				t.Fatalf("RPC error=%v", err)
			}
		}
		// gRPC stats.End may execute just after the client receives its response.
		deadline := time.Now().Add(time.Second)
		for {
			var data metricdata.ResourceMetrics
			if err := reader.Collect(ctx, &data); err != nil {
				t.Fatal(err)
			}
			counts, errors := map[string]uint64{}, map[string]uint64{}
			for _, scope := range data.ScopeMetrics {
				for _, metric := range scope.Metrics {
					hist, ok := metric.Data.(metricdata.Histogram[float64])
					if !ok {
						continue
					}
					for _, point := range hist.DataPoints {
						counts[metric.Name] += point.Count
						status, _ := point.Attributes.Value("rpc.response.status_code")
						typ, _ := point.Attributes.Value("error.type")
						httpStatus, _ := point.Attributes.Value("http.response.status_code")
						if httpStatus.AsInt64() >= 500 || typ.AsString() != "" || (status.AsString() != "" && status.AsString() != "OK") {
							errors[metric.Name] += point.Count
						}
						if metric.Name == "http.server.request.duration" {
							route, _ := point.Attributes.Value("http.route")
							if route.AsString() != "/orders/{id}" {
								t.Fatalf("unbounded or missing route: %v", point.Attributes)
							}
						}
					}
				}
			}
			if counts["rpc.server.call.duration"] != uint64((batch+1)*4) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
				continue
			}
			for _, name := range []string{"http.server.request.duration", "rpc.server.call.duration", "rpc.server.duration"} {
				if counts[name] != uint64((batch+1)*4) || errors[name] != uint64(batch+1) {
					t.Fatalf("%s count=%d errors=%d", name, counts[name], errors[name])
				}
			}
			break
		}
		if integrated {
			if err := meter.ForceFlush(ctx); err != nil {
				t.Fatal(err)
			}
			time.Sleep(7 * time.Second)
		}
	}
	if len(spans.GetSpans()) != 0 {
		t.Fatal("trace sampling was not disabled")
	}
	if !integrated {
		return
	}
	prom := promquery.New(os.Getenv("APM_TEST_PROMETHEUS"), nil)
	svc := apm.New(prom, nil, nil)
	env, ns := "test", "trade"
	for _, tc := range []struct{ protocol, format, operation string }{{"http", "otel", "GET /orders/{id}"}, {"rpc", "otel", "grpc.health.v1.Health/Check"}, {"rpc", "legacy", "grpc.health.v1.Health/Check"}} {
		q := apm.Query{Start: start, End: time.Now(), ServiceName: "apm-sdk-test", Environment: &env, ServiceNamespace: &ns, Protocol: tc.protocol, MetricFormat: tc.format}
		rows, err := svc.List(ctx, q, true)
		if err != nil || len(rows.Items) != 1 {
			t.Fatalf("%+v rows=%+v err=%v", tc, rows, err)
		}
		metricName := "http_server_request_duration_seconds"
		if tc.protocol == "rpc" {
			metricName = "rpc_server_call_duration_seconds"
		}
		if tc.format == "legacy" {
			metricName = "rpc_server_duration_milliseconds"
		}
		count, err := prom.Query(ctx, fmt.Sprintf(`sum(%s_count{service_name="apm-sdk-test"})`, metricName), q.End)
		if err != nil {
			t.Fatal(err)
		}
		var samples []struct {
			Value []json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(count.Result, &samples); err != nil || len(samples) != 1 || len(samples[0].Value) != 2 || string(samples[0].Value[1]) != `"12"` {
			t.Fatalf("export count %s: %s %v", metricName, count.Result, err)
		}
		row := rows.Items[0]
		if row.Operation != tc.operation || row.RPS == nil || *row.RPS <= 0 || row.ErrorRate == nil || math.Abs(*row.ErrorRate-25) > 0.01 || row.P95Ms == nil || *row.P95Ms <= 0 || *row.P95Ms > 500 {
			t.Fatalf("%+v row=%+v", tc, row)
		}
		if rows.Metadata.MetricSource != "application_metrics" || rows.Metadata.Sampling != "independent_of_trace_sampling" {
			t.Fatalf("metadata=%+v", rows.Metadata)
		}
		q.Operation = tc.operation
		detail, err := svc.Overview(ctx, q)
		if err != nil || detail.Summary.RPS == nil {
			t.Fatalf("operation filter %+v err=%v", detail, err)
		}
		for _, metric := range []string{"error_rate", "p95_ms"} {
			rule, err := apm.BuildAlertTemplate(q, metric, 5, 1, 120)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prom.Query(ctx, rule.Expr, q.End); err != nil {
				t.Fatalf("%+v alert syntax: %v", tc, err)
			}
		}
		q.MetricSource = "tempo_spanmetrics"
		sampled, err := svc.List(ctx, q, false)
		if err != nil || len(sampled.Items) != 0 {
			t.Fatalf("unexpected sampled fallback %+v %v", sampled, err)
		}
		t.Logf("%s/%s: 12 SDK calls, zero spans; Prometheus error rate %.2f%%, P95 %.3fms", tc.protocol, tc.format, *row.ErrorRate, *row.P95Ms)
	}
	all, err := svc.List(ctx, apm.Query{Start: start, End: time.Now(), ServiceName: "apm-sdk-test", Environment: &env, ServiceNamespace: &ns, Protocol: "all"}, false)
	if err != nil || all.Total != 1 || len(all.Items) != 1 || len(all.Items[0].Protocols) != 2 {
		t.Fatalf("one dual-protocol service expected: %+v %v", all, err)
	}
	combined := all.Items[0]
	if combined.RPS == nil || combined.ErrorRate == nil || math.Abs(*combined.ErrorRate-25) > 0.01 || combined.P95Ms != nil {
		t.Fatalf("combined rates or uncombined percentiles wrong: %+v", combined)
	}
	if math.Abs(*combined.RPS-*combined.Protocols[0].RPS-*combined.Protocols[1].RPS) > 0.00001 {
		t.Fatalf("protocol rates do not sum to service rate: %+v", combined)
	}
	t.Log("Both native protocols appear under one service; old/current RPC dual export is counted once, and P95 remains per protocol")

}
