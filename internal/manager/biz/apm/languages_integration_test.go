package apm

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

// Uses the metrics emitted by the real language applications, including an
// instance with trace sampling disabled. No synthetic SDK histograms here.
func TestAPMLanguageMetricsIntegration(t *testing.T) {
	endpoint := os.Getenv("APM_TEST_PROMETHEUS")
	if os.Getenv("APM_TEST_LANGUAGES") != "1" || endpoint == "" {
		t.Skip("run scripts/apm-test/run-languages.sh")
	}
	svc := New(promquery.New(endpoint, nil), nil, nil)
	env, ns := "acceptance", "trade"
	languages := []string{"java", "node", "python"}
	if extra := os.Getenv("APM_MORE_LANGUAGES"); extra != "" {
		languages = strings.Fields(extra)
	}
	for _, language := range languages {
		t.Run(language, func(t *testing.T) {
			q := Query{Start: time.Now().Add(-time.Hour), End: time.Now(), Environment: &env, ServiceNamespace: &ns, ServiceName: "apm-" + language, Protocol: "http"}
			if language == "ruby" {
				q.Protocol = "all"
				rows, err := svc.List(t.Context(), q, false)
				if err != nil || len(rows.Items) != 1 || rows.Items[0].MetricSource != "tempo_spanmetrics" || rows.Items[0].RPS == nil || *rows.Items[0].RPS <= 0 || rows.Items[0].ErrorRate == nil || math.Abs(*rows.Items[0].ErrorRate-25) > 5 || rows.Items[0].P95Ms == nil || strings.Join(rows.Items[0].Languages, ",") != "ruby" {
					t.Fatalf("Ruby sampled HTTP RED: %+v %v", rows, err)
				}
				q.Protocol = "http"
				overview, err := svc.Overview(t.Context(), q)
				if err != nil || overview.Metadata.MetricSource != "tempo_spanmetrics" || len(overview.Points) == 0 {
					t.Fatalf("Ruby sampled overview: %+v %v", overview, err)
				}
				operations, err := svc.List(t.Context(), q, true)
				if err != nil || operations.Total == 0 || operations.Metadata.MetricSource != "tempo_spanmetrics" {
					t.Fatalf("Ruby sampled operations: %+v %v", operations, err)
				}
				t.Logf("Ruby HTTP Trace fallback: %.3f sampled RPS, %.2f%% errors, %.2fms P95; overview and operations verified", *rows.Items[0].RPS, *rows.Items[0].ErrorRate, *rows.Items[0].P95Ms)
				return
			}
			if os.Getenv("APM_MORE_LANGUAGES") != "" {
				q.Operation = "GET /orders/{id}"
			}
			rows, err := svc.List(t.Context(), q, true)
			if err != nil || len(rows.Items) != 1 {
				t.Fatalf("HTTP operations: %+v %v", rows, err)
			}
			row := rows.Items[0]
			tolerance := 0.01
			if os.Getenv("APM_MORE_LANGUAGES") != "" {
				// The short run starts cumulative series at different export boundaries;
				// the driver independently checks exact 40-request / 10-error counters.
				tolerance = 5
			}
			if row.ErrorRate == nil || math.Abs(*row.ErrorRate-25) > tolerance || row.P95Ms == nil || *row.P95Ms < 50 || *row.P95Ms > 1000 {
				t.Fatalf("HTTP RED: %+v", row)
			}
			diagnostic, err := svc.Diagnostics(t.Context(), q)
			if err != nil || len(diagnostic.Instances) != 2 || diagnostic.SampledTraces != 0 {
				t.Fatalf("instances independent of traces: %+v %v", diagnostic, err)
			}
			if diagnostic.Instances[0].InstanceID != "sampled" || diagnostic.Instances[1].InstanceID != "unsampled" {
				t.Fatalf("instance identities: %+v", diagnostic.Instances)
			}
			q.Protocol, q.Operation = "rpc", ""
			rpc, err := svc.List(t.Context(), q, true)
			if err != nil {
				t.Fatal(err)
			}
			if language == "java" || language == "python" || language == "node" {
				if len(rpc.Items) != 1 || rpc.Items[0].ErrorRate == nil || math.Abs(*rpc.Items[0].ErrorRate-25) > 0.01 {
					t.Fatalf("%s RPC RED: %+v", language, rpc)
				}
				if rpc.Items[0].Operation != "grpc.health.v1.Health/Check" {
					t.Fatalf("%s RPC method: %q", language, rpc.Items[0].Operation)
				}
				if rpc.Items[0].P95Ms == nil {
					t.Fatal("missing RPC P95")
				}
				if p95 := *rpc.Items[0].P95Ms; p95 < 50 || p95 > 1000 {
					t.Fatalf("%s RPC P95 outside slow request range: %fms", language, p95)
				}
				t.Logf("%s RPC: %.2f%% errors, %.2fms P95", language, *rpc.Items[0].ErrorRate, *rpc.Items[0].P95Ms)
				q.Operation = rpc.Items[0].Operation
				instances, err := svc.Diagnostics(t.Context(), q)
				if err != nil || len(instances.Instances) != 2 || instances.Instances[0].InstanceID != "sampled" || instances.Instances[1].InstanceID != "unsampled" {
					t.Fatalf("%s RPC operation instances: %+v %v", language, instances, err)
				}
			} else if len(rpc.Items) != 0 {
				t.Fatalf("unsupported RPC metrics must not fall back to sampled spans: %+v", rpc)
			}
			t.Logf("%s: production APM query adapter verified HTTP operation %s, %.2f%% errors, %.2fms P95 and both sampled/unsampled instances", language, row.Operation, *row.ErrorRate, *row.P95Ms)
		})
	}
}
