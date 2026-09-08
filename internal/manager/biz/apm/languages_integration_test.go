package apm

import (
	"math"
	"os"
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
	for _, language := range []string{"java", "node", "python"} {
		t.Run(language, func(t *testing.T) {
			q := Query{Start: time.Now().Add(-time.Hour), End: time.Now(), Environment: &env, ServiceNamespace: &ns, ServiceName: "apm-" + language, Protocol: "http"}
			rows, err := svc.List(t.Context(), q, true)
			if err != nil || len(rows.Items) != 1 {
				t.Fatalf("HTTP operations: %+v %v", rows, err)
			}
			row := rows.Items[0]
			if row.ErrorRate == nil || math.Abs(*row.ErrorRate-25) > 0.01 || row.P95Ms == nil || *row.P95Ms < 50 || *row.P95Ms > 1000 {
				t.Fatalf("HTTP RED: %+v", row)
			}
			diagnostic, err := svc.Diagnostics(t.Context(), q)
			if err != nil || len(diagnostic.Instances) != 2 || diagnostic.SampledTraces != 0 {
				t.Fatalf("instances independent of traces: %+v %v", diagnostic, err)
			}
			if diagnostic.Instances[0].InstanceID != "sampled" || diagnostic.Instances[1].InstanceID != "unsampled" {
				t.Fatalf("instance identities: %+v", diagnostic.Instances)
			}
			q.Protocol = "rpc"
			rpc, err := svc.List(t.Context(), q, true)
			if err != nil {
				t.Fatal(err)
			}
			if language == "java" {
				if len(rpc.Items) != 1 || rpc.Items[0].ErrorRate == nil || math.Abs(*rpc.Items[0].ErrorRate-25) > 0.01 {
					t.Fatalf("Java RPC RED: %+v", rpc)
				}
			} else if len(rpc.Items) != 0 {
				t.Fatalf("unsupported RPC metrics must not fall back to sampled spans: %+v", rpc)
			}
			t.Logf("%s: production APM query adapter verified HTTP operation %s, 25%% errors, %.2fms P95 and both sampled/unsampled instances", language, row.Operation, *row.P95Ms)
		})
	}
}
