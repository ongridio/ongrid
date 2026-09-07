package apm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

// Run through scripts/apm-test/run.sh. Real OTLP -> Tempo 2.10 -> Prometheus
// 2.54 queries protect label names, histogram units and entry-span semantics.
func TestOTLPIntegration(t *testing.T) {
	base := os.Getenv("APM_TEST_PROMETHEUS")
	if base == "" {
		t.Skip("run scripts/apm-test/run.sh for isolated telemetry acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	prom := promquery.New(base, nil)
	traces := tracequery.New(os.Getenv("APM_TEST_TEMPO"), nil)
	svc := New(prom, traces, nil)
	start := time.Now().Add(-time.Minute)
	prod, stage, ns := "production", "staging", "trade"
	// One baseline plus five equal batches. Include server, client, internal,
	// consumer and a paired downstream to detect accidental span overcounting.
	seq := 0
	send := func(batch int) {
		t.Helper()
		resources := []any{}
		for _, identity := range []struct{ environment, namespace string }{{prod, ns}, {stage, ns}, {"", ns}, {prod, "other"}} {
			environment, namespace := identity.environment, identity.namespace
			for i := 0; i < 10; i++ {
				seq++
				traceID := fmt.Sprintf("%032x", seq)
				stamp := time.Now().Add(-time.Second)
				attr := func(k, v string) map[string]any {
					return map[string]any{"key": k, "value": map[string]string{"stringValue": v}}
				}
				resource := func(name string, spans []any) map[string]any {
					attrs := []any{attr("service.name", name), attr("service.namespace", namespace), attr("service.instance.id", "test-instance"), attr("device_id", "42")}
					if environment != "" {
						attrs = append(attrs, attr("deployment.environment.name", environment))
					}
					return map[string]any{"resource": map[string]any{"attributes": attrs}, "scopeSpans": []any{map[string]any{"spans": spans}}}
				}
				span := func(id, parent, name string, kind, status int, duration time.Duration) map[string]any {
					return map[string]any{"traceId": traceID, "spanId": id, "parentSpanId": parent, "name": name, "kind": kind, "startTimeUnixNano": fmt.Sprint(stamp.UnixNano()), "endTimeUnixNano": fmt.Sprint(stamp.Add(duration).UnixNano()), "status": map[string]int{"code": status}}
				}
				status := 1
				if environment == prod && i == 0 {
					status = 2
				}
				resources = append(resources, resource("apm-test-orders", []any{span("0000000000000001", "", "GET /orders/{id}", 2, status, 200*time.Millisecond), span("0000000000000002", "0000000000000001", "GET /db", 3, status, 100*time.Millisecond), span("0000000000000003", "0000000000000001", "internal", 1, 1, time.Millisecond), span("0000000000000004", "", "consume", 5, 1, 50*time.Millisecond)}))
				resources = append(resources, resource("apm-test-db", []any{span("0000000000000005", "0000000000000002", "GET /db", 2, status, 90*time.Millisecond)}))
			}
		}
		body, err := json.Marshal(map[string]any{"resourceSpans": resources})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, os.Getenv("APM_TEST_OTLP"), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("OTLP batch %d: %d %s %v", batch, resp.StatusCode, raw, err)
		}
	}
	for i := 0; i < 6; i++ {
		send(i)
		select {
		case <-time.After(16 * time.Second):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	q := Query{MetricSource: "tempo_spanmetrics", Start: start, End: time.Now(), ServiceName: "apm-test-orders", Environment: &prod, ServiceNamespace: &ns}
	if err := q.Validate(true); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, q, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("prod rows=%+v", list)
	}
	row := list.Items[0]
	if row.RPS == nil || *row.RPS <= 0 || row.ErrorRate == nil || math.Abs(*row.ErrorRate-10) > 0.01 || row.P95Ms == nil || *row.P95Ms < 190 || *row.P95Ms > 300 {
		t.Fatalf("unexpected request summary: %+v", row)
	}
	counter, err := prom.Query(ctx, `sum(traces_spanmetrics_calls_total{service="apm-test-orders",deployment_environment_name="production",service_namespace="trade",span_kind="SPAN_KIND_SERVER"})`, q.End)
	if err != nil {
		t.Fatal(err)
	}
	series, err := decodeSeries(counter, "vector")
	if err != nil || len(series) != 1 {
		t.Fatalf("counter %v %v", counter, err)
	}
	value, err := sampleValue(series[0].Value)
	if err != nil || value == nil || *value != 60 {
		t.Fatalf("expected exactly 60 server calls, got %v %v", value, err)
	}
	q.Environment = &stage
	zero, err := svc.List(ctx, q, false)
	if err != nil || len(zero.Items) != 1 || zero.Items[0].ErrorRate == nil || *zero.Items[0].ErrorRate != 0 {
		t.Fatalf("zero-error service %v %v", zero, err)
	}
	empty := ""
	q.Environment = &empty
	legacy, err := svc.List(ctx, q, false)
	if err != nil || len(legacy.Items) != 1 {
		t.Fatalf("unset environment %v %v", legacy, err)
	}
	if _, err := traces.SearchTraces(ctx, tracequery.SearchOptions{Query: TraceQL(q), Start: q.Start, End: q.End, Limit: 3}); err != nil {
		t.Fatalf("unset TraceQL syntax: %v", err)
	}
	q.Environment = &prod
	other := "other"
	q.ServiceNamespace = &other
	isolated, err := svc.List(ctx, q, false)
	if err != nil || len(isolated.Items) != 1 || isolated.Items[0].Identity.ServiceNamespace != other {
		t.Fatalf("namespace isolation %+v %v", isolated, err)
	}
	q.ServiceNamespace = &ns
	if _, err := svc.Runtime(ctx, q); err != nil {
		t.Fatalf("runtime query syntax: %v", err)
	}
	overview, err := svc.Overview(ctx, q)
	if err != nil || len(overview.Points) == 0 {
		t.Fatalf("trend %v %v", overview, err)
	}
	operations, err := svc.List(ctx, q, true)
	if err != nil || len(operations.Items) != 1 || operations.Items[0].Operation != "GET /orders/{id}" {
		t.Fatalf("operations %v %v", operations, err)
	}
	dependencies, err := svc.Dependencies(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	paired, virtual := false, false
	for _, edge := range dependencies.Items {
		if edge.Client.ServiceName == "apm-test-orders" && edge.Server.ServiceName == "apm-test-db" && edge.Client.Environment == prod && edge.Server.Environment == prod {
			paired = true
		}
		if edge.ConnectionType == "virtual_node" {
			virtual = true
		}
	}
	if !paired || !virtual {
		t.Fatalf("missing paired or virtual dependency: %+v", dependencies)
	}

	diagnostics, err := svc.Diagnostics(ctx, q)
	if err != nil || diagnostics.SampledTraces == 0 || len(diagnostics.Instances) == 0 {
		search, searchErr := traces.SearchTraces(ctx, tracequery.SearchOptions{Query: TraceQL(q), Start: q.Start, End: q.End, Limit: 3})
		t.Logf("diagnostic search: %+v %v", search, searchErr)
		if search != nil {
			t.Logf("raw search %s", search.Traces)
		}
		sample, sampleErr := traces.GetTrace(ctx, fmt.Sprintf("%032x", 1))
		if sample != nil {
			t.Logf("raw sample %s", sample.Body)
		}
		t.Logf("sample err %v", sampleErr)
		t.Fatalf("diagnostics %v %v", diagnostics, err)
	}
	q.SpanKind = "consumer"
	consumers, err := svc.List(ctx, q, true)
	if err != nil || len(consumers.Items) != 1 || consumers.Items[0].Operation != "consume" {
		t.Fatalf("consumer %v %v", consumers, err)
	}
	q.SpanKind = "server"
	q.MetricSource = "application_metrics"
	for _, metric := range []string{"error_rate", "p95_ms"} {
		for _, dwell := range []int{0, 30, 120} {
			template, err := BuildAlertTemplate(q, metric, 1, 1, dwell)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prom.Query(ctx, template.Expr, q.End); err != nil {
				t.Fatalf("alert %s/%d syntax: %v", metric, dwell, err)
			}
		}
	}
	t.Log("Verified 60 exact server calls, 10% errors, zero-error fallback, histogram milliseconds, environment isolation, unset TraceQL, consumer isolation, trends, dependencies, trace diagnostics and alert PromQL on Tempo 2.10 / Prometheus 2.54")
}
