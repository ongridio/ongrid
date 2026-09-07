package apm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

type fakeProm struct {
	result string
	expr   string
	calls  int
	err    error
}

func (p *fakeProm) Query(_ context.Context, expr string, _ time.Time) (*promquery.InstantResult, error) {
	p.expr = expr
	p.calls++
	return &promquery.InstantResult{ResultType: "vector", Result: json.RawMessage(p.result)}, p.err
}
func (p *fakeProm) QueryRange(_ context.Context, expr string, _, _ time.Time, _ time.Duration) (*promquery.InstantResult, error) {
	p.expr = expr
	p.calls++
	return &promquery.InstantResult{ResultType: "matrix", Result: json.RawMessage(`[]`)}, p.err
}
func testQuery() Query {
	env, ns := "production", "trade"
	return Query{Start: time.Unix(1000, 0), End: time.Unix(1600, 0), Environment: &env, ServiceNamespace: &ns, ServiceName: "orders", MetricSource: "tempo_spanmetrics", Protocol: "http", MetricFormat: "otel"}
}

func TestQueryBoundariesAndEscaping(t *testing.T) {
	q := testQuery()
	q.ServiceName = `orders"} or vector(1)`
	if err := q.Validate(true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`service="orders\"} or vector(1)"`, `span_kind="SPAN_KIND_SERVER"`, `service_namespace="trade"`, `deployment_environment_name="production"`} {
		if !strings.Contains(q.selector(), want) {
			t.Fatalf("selector=%s missing %s", q.selector(), want)
		}
	}
	q.Environment = nil
	if err := q.Validate(true); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("missing scope accepted: %v", err)
	}
	empty := ""
	q.Environment = &empty
	if err := q.Validate(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(TraceQL(q), `resource.deployment.environment.name = nil`) {
		t.Fatal(TraceQL(q))
	}
	for _, change := range []func(*Query){func(q *Query) { q.Start = q.End }, func(q *Query) { q.Start = q.End.Add(-8 * 24 * time.Hour) }, func(q *Query) { q.Page = 201 }, func(q *Query) { q.SpanKind = "internal" }, func(q *Query) { q.Operation = "x\n" }, func(q *Query) { q.ServiceName = strings.Repeat("x", 257) }} {
		bad := testQuery()
		change(&bad)
		if err := bad.Validate(true); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("invalid query accepted %+v", bad)
		}
	}
}

func TestListIdentityNullsAndBoundedQueryCount(t *testing.T) {
	// Same service name, three identities. Present-only must not become zero;
	// no-request ratios and non-finite histograms must never appear healthy.
	p := &fakeProm{result: `[
 {"metric":{"service":"orders","service_namespace":"trade","deployment_environment_name":"production","apm_stat":"rps"},"value":[1600,"2"]},
 {"metric":{"service":"orders","service_namespace":"trade","deployment_environment_name":"production","apm_stat":"error_rate"},"value":[1600,"0"]},
 {"metric":{"service":"orders","service_namespace":"trade","deployment_environment_name":"production","apm_stat":"p95_ms"},"value":[1600,"NaN"]},
 {"metric":{"service":"orders","service_namespace":"other","deployment_environment_name":"production","apm_stat":"rps"},"value":[1600,"0"]},
 {"metric":{"service":"orders","service_namespace":"trade","deployment_environment_name":"staging","apm_stat":"present"},"value":[1600,"1"]}
 ]`}
	q := testQuery()
	q.Environment = nil
	q.ServiceNamespace = nil
	q.ServiceName = ""
	q.PageSize = 2
	result, err := New(p, nil, nil).List(t.Context(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || len(result.Items) != 2 || p.calls != 1 {
		t.Fatalf("list=%+v calls=%d", result, p.calls)
	}
	a, b := result.Items[0], result.Items[1]
	if a.DataStatus != "observed" || a.Requests == nil || *a.Requests != 1200 || a.ErrorRate == nil || *a.ErrorRate != 0 || a.P95Ms != nil || b.DataStatus != "no_requests" || b.ErrorRate != nil {
		t.Fatalf("wrong summaries %+v %+v", a, b)
	}
	q.Page = 2
	result, err = New(p, nil, nil).List(t.Context(), q, false)
	if err != nil || len(result.Items) != 1 || result.Items[0].DataStatus != "insufficient_samples" {
		t.Fatalf("second page %+v %v", result, err)
	}
	if !strings.Contains(p.expr, `0 * sum by`) || !strings.Contains(p.expr, `1000 * histogram_quantile`) || strings.Contains(p.expr, `service_name=`) {
		t.Fatal(p.expr)
	}
	p.result = `[{"metric":{},"value":[0]}]`
	if _, err = New(p, nil, nil).List(t.Context(), q, false); err == nil {
		t.Fatal("malformed sample accepted")
	}
	p.err = errors.New("backend unavailable")
	if _, err = New(p, nil, nil).List(t.Context(), q, false); err == nil {
		t.Fatal("backend failure rendered as empty")
	}
	if _, err = New(nil, nil, nil).List(t.Context(), q, false); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatal(err)
	}
}

func TestAlertDefinitionUsesRequestSemantics(t *testing.T) {
	q := testQuery()
	q.MetricSource = "application_metrics"
	q.Operation = `GET /orders/{id}`
	q.SpanKind = "server"
	for _, metric := range []string{"error_rate", "p95_ms"} {
		template, err := BuildAlertTemplate(q, metric, 5, 100, 120)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{metricExpressions(q, 5*time.Minute, identityLabels)[metric], `http_server_request_duration_seconds_count`, `http_route="/orders/{id}"`, `count_over_time`, `[120s:30s]`, `== scalar(count_over_time((vector(1))[120s:30s]))`, `* 300) >= 100`} {
			if !strings.Contains(template.Expr, want) {
				t.Fatalf("missing %s in %s", want, template.Expr)
			}
		}
	}
	for _, threshold := range []float64{math.NaN(), math.Inf(1), 0, 101} {
		if _, err := BuildAlertTemplate(q, "error_rate", threshold, 1, 0); err == nil {
			t.Fatalf("accepted threshold %v", threshold)
		}
	}
	if _, err := BuildAlertTemplate(q, "p95_ms", 100, 0, 0); err == nil {
		t.Fatal("accepted no traffic gate")
	}
	if _, err := BuildAlertTemplate(q, "p95_ms", 100, 1, 31); err == nil {
		t.Fatal("accepted unaligned duration")
	}
}

func TestTempoCompactTraceIDs(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"3", "00000000000000000000000000000003"}, {"ABCD", "0000000000000000000000000000abcd"}, {"0", ""}, {"../bad", ""}, {strings.Repeat("a", 33), ""}} {
		if got := canonicalTraceID(tc.input); got != tc.want {
			t.Fatalf("%s -> %s, want %s", tc.input, got, tc.want)
		}
	}
}

// Promtool evaluates real time-series history, including a healthy interval
// and a missing interval inside an otherwise firing hold window.
func TestAlertDwellPromtool(t *testing.T) {
	binary := os.Getenv("APM_TEST_PROMTOOL")
	if binary == "" {
		t.Skip("set APM_TEST_PROMTOOL to Prometheus 2.54 promtool")
	}
	q := testQuery()
	q.SpanKind = "server"
	q.MetricSource = "application_metrics"
	template, err := BuildAlertTemplate(q, "error_rate", 5, 1, 120)
	if err != nil {
		t.Fatal(err)
	}
	// Use the generated structure with a deterministic predicate to isolate
	// hold behavior from rate()/histogram interpolation (covered end-to-end).
	exprs := metricExpressions(q, 5*time.Minute, identityLabels)
	predicate := fmt.Sprintf("(%s > 5) and on (%s) ((%s * 300) >= 1)", exprs["error_rate"], identityLabels, exprs["rps"])
	expr := strings.ReplaceAll(template.Expr, predicate, `(test_signal > 0)`)
	fixture := map[string]any{"evaluation_interval": "30s", "tests": []any{map[string]any{
		"interval": "30s", "input_series": []any{
			map[string]any{"series": `test_signal{service="steady"}`, "values": "1+0x10"},
			map[string]any{"series": `test_signal{service="healthy-gap"}`, "values": "1 1 1 1 0 1 1 1 1 1 1"},
			map[string]any{"series": `test_signal{service="missing-gap"}`, "values": "1 1 1 1 stale 1 1 1 1 1 1"},
		}, "promql_expr_test": []any{map[string]any{"expr": expr, "eval_time": "3m", "exp_samples": []any{map[string]any{"labels": `{__name__="test_signal",service="steady"}`, "value": 1}}}},
	}}}
	raw, err := yaml.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), binary, "test", "rules", file).CombinedOutput()
	if err != nil {
		t.Fatalf("promtool: %v\n%s\n%s", err, output, raw)
	}
}

func TestApplicationMetricSchemas(t *testing.T) {
	for _, tc := range []struct {
		protocol, format, histogram, operation, attribute string
		multiplier                                        float64
	}{
		{"http", "otel", "http_server_request_duration_seconds", "GET /orders/{id}", "span.http.route", 1000},
		{"http", "legacy", "http_server_duration_milliseconds", "GET /orders/{id}", "span.http.route", 1},
		{"rpc", "otel", "rpc_server_call_duration_seconds", "trade.Orders/Get", "span.rpc.method", 1000},
		{"rpc", "legacy", "rpc_server_duration_milliseconds", "trade.Orders/Get", "span.rpc.method", 1},
	} {
		q := testQuery()
		q.MetricSource = ""
		q.Protocol = tc.protocol
		q.MetricFormat = tc.format
		q.Operation = tc.operation
		if err := q.Validate(true); err != nil {
			t.Fatal(err)
		}
		if q.histogram() != tc.histogram || q.latencyMultiplier() != tc.multiplier || metadata(q).Sampling != "independent_of_trace_sampling" {
			t.Fatalf("schema %+v", q)
		}
		expression := combineExpressions(metricExpressions(q, time.Minute, identityLabels+",span_name"))
		if strings.Contains(expression, "traces_spanmetrics") || !strings.Contains(expression, tc.histogram) || !strings.Contains(expression, `service_name="orders"`) {
			t.Fatal(expression)
		}
		if !strings.Contains(TraceQL(q), tc.attribute) || strings.Contains(TraceQL(q), " && name =") || !strings.Contains(TraceQL(q), "kind = server") {
			t.Fatal(TraceQL(q))
		}
	}
	for _, mutate := range []func(*Query){func(q *Query) { q.Protocol = "sql" }, func(q *Query) { q.MetricFormat = "auto" }, func(q *Query) { q.SpanKind = "consumer" }, func(q *Query) { q.MetricSource = "mixed" }} {
		q := testQuery()
		q.MetricSource = "application_metrics"
		mutate(&q)
		if err := q.Validate(true); err == nil {
			t.Fatalf("invalid accepted %+v", q)
		}
	}
	q := testQuery()
	if _, err := BuildAlertTemplate(q, "error_rate", 5, 100, 120); err == nil {
		t.Fatal("allowed request alert over sampled spans")
	}
}
