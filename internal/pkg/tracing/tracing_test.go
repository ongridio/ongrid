package tracing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestInstrumentHTTPClient_CreatesChildSpanAndPropagatesContext(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		_ = provider.Shutdown(context.Background())
	})

	traceparent := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := InstrumentHTTPClient(server.Client(), "tempo")
	ctx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	parent.End()

	if got := <-traceparent; got == "" {
		t.Fatal("traceparent header was not propagated")
	}

	suppressedReq, err := http.NewRequestWithContext(WithoutHTTPClientTracing(ctx), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext suppressed: %v", err)
	}
	suppressedResp, err := client.Do(suppressedReq)
	if err != nil {
		t.Fatalf("Do suppressed: %v", err)
	}
	if err := suppressedResp.Body.Close(); err != nil {
		t.Fatalf("close suppressed response body: %v", err)
	}
	if got := <-traceparent; got != "" {
		t.Fatalf("suppressed traceparent = %q, want empty", got)
	}

	var clientSpan sdktrace.ReadOnlySpan
	clientSpans := 0
	for _, span := range recorder.Ended() {
		if span.Name() == "HTTP GET tempo" {
			clientSpan = span
			clientSpans++
		}
	}
	if clientSpan == nil {
		t.Fatalf("client span missing; ended spans = %d", len(recorder.Ended()))
	}
	if clientSpan.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("client parent span = %s, want %s", clientSpan.Parent().SpanID(), parent.SpanContext().SpanID())
	}
	if got := spanAttribute(clientSpan, "peer.service"); got != "tempo" {
		t.Fatalf("peer.service = %q, want tempo", got)
	}
	if clientSpans != 1 {
		t.Fatalf("client spans = %d, want 1", clientSpans)
	}
}

func TestEndSpan_RecordsErrorStatus(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := provider.Tracer("test").Start(context.Background(), "operation")
	EndSpan(span, errors.New("failed"))
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Status().Code.String() != "Error" {
		t.Fatalf("ended spans = %#v, want one error span", ended)
	}
	if len(ended[0].Events()) == 0 || ended[0].Events()[0].Name != "exception" {
		t.Fatalf("error event missing: %#v", ended[0].Events())
	}
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) string {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func TestInitResource(t *testing.T) {
	for _, tc := range []struct{ name, attrs, namespace, environment string }{
		{"defaults", "", "ongrid", "internal"},
		{"deployment overrides", "service.namespace=platform,deployment.environment.name=staging", "platform", "staging"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", tc.attrs)
			t.Setenv("OTEL_SERVICE_NAME", "external-name")
			previousProvider, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
			t.Cleanup(func() { otel.SetTracerProvider(previousProvider); otel.SetTextMapPropagator(previousPropagator) })
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer collector.Close()
			shutdown, err := Init(context.Background(), Config{ServiceName: "ongrid-manager", ServiceNamespace: "ongrid", Environment: "internal", Endpoint: strings.TrimPrefix(collector.URL, "http://"), Insecure: true, SamplingRatio: 1})
			if err != nil {
				t.Fatal(err)
			}
			recorder := tracetest.NewSpanRecorder()
			otel.GetTracerProvider().(*sdktrace.TracerProvider).RegisterSpanProcessor(recorder)
			_, span := otel.Tracer("test").Start(context.Background(), "resource check")
			span.End()
			if err := shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("got %d spans", len(spans))
			}
			attrs := map[string]string{}
			for _, attr := range spans[0].Resource().Attributes() {
				attrs[string(attr.Key)] = attr.Value.AsString()
			}
			for key, want := range map[string]string{"service.name": "ongrid-manager", "service.namespace": tc.namespace, "deployment.environment.name": tc.environment, "telemetry.sdk.language": "go", "telemetry.sdk.name": "opentelemetry"} {
				if attrs[key] != want {
					t.Errorf("%s = %q, want %q", key, attrs[key], want)
				}
			}
			if attrs["telemetry.sdk.version"] == "" {
				t.Error("missing SDK version")
			}
		})
	}
}
