package apm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type clusterScopes struct{}

func (clusterScopes) ResolveClusterTelemetryScope(_ context.Context, id uint64) (string, []string, error) {
	switch id {
	case 101:
		return "", []string{"42", "43"}, nil
	case 102:
		return "7", nil, nil
	case 103:
		return "", []string{}, nil
	default:
		return "", nil, errs.ErrNotFound
	}
}

func TestUnifiedClusterScope(t *testing.T) {
	for _, tc := range []struct {
		id            uint64
		metric, trace string
	}{{101, `device_id=~"42|43"`, `resource.device_id =~ "^(42|43)$"`}, {102, `cluster_id="7"`, `resource.cluster_id = "7"`}, {103, `device_id=~"a^"`, `resource.device_id =~ "^(a^)$"`}} {
		prom := &fakeProm{result: `[]`}
		svc := New(prom, nil, nil).WithClusterScopes(clusterScopes{})
		q := testQuery()
		q.ClusterNodeID, q.DeviceID = tc.id, "42"
		result, err := svc.List(context.Background(), q, false)
		if err != nil {
			t.Fatal(err)
		}
		if result.Metadata.ResourceScope == nil || !strings.Contains(prom.expr, tc.metric) || !strings.Contains(prom.expr, `device_id="42"`) {
			t.Fatal(prom.expr)
		}
		if err := svc.validateQuery(context.Background(), &q, true); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(TraceQL(q), tc.trace) {
			t.Fatal(TraceQL(q))
		}
		q.MetricSource = "application_metrics"
		alert, err := svc.AlertTemplate(context.Background(), q, "error_rate", 5, 1, 0)
		if err != nil || !strings.Contains(alert.Expr, tc.metric) {
			t.Fatalf("alert: %v %v", alert, err)
		}
		if _, err := svc.Dependencies(context.Background(), q); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("scoped dependencies: %v", err)
		}
	}
	q := testQuery()
	q.ClusterNodeID = 404
	if _, err := New(&fakeProm{result: `[]`}, nil, nil).WithClusterScopes(clusterScopes{}).List(context.Background(), q, false); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("unknown cluster widened query: %v", err)
	}
}

func TestClusterMigrationExcludesCollidingIDs(t *testing.T) {
	q := testQuery()
	q.resourceScope = &ResourceScope{ClusterID: "132", K8sClusterID: "50"}
	for _, tc := range []struct {
		cluster, internal string
		match             bool
	}{
		{"132", "50", true}, {"50", "", true}, {"50", "75", false}, {"132", "", false}, {"132", "75", false}, {"133", "50", false},
	} {
		if got := q.resourceScope.matches(map[string]string{"cluster_id": tc.cluster, "k8s_cluster_id": tc.internal}); got != tc.match {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	for _, expr := range []string{requestRate(q, 5*time.Minute, identityLabels), q.errorRate(5*time.Minute, identityLabels), runtimeExpression(q, 5*time.Minute)} {
		for _, want := range []string{`cluster_id="132",k8s_cluster_id="50"`, `cluster_id="50",k8s_cluster_id=""`} {
			if !strings.Contains(expr, want) {
				t.Fatalf("missing identity %s: %s", want, expr)
			}
		}
	}
	trace := TraceQL(q)
	for _, want := range []string{`resource.cluster_id = "132" && resource.k8s_cluster_id = "50"`, `resource.cluster_id = "50" && (!(resource.k8s_cluster_id != nil) || resource.k8s_cluster_id = "")`} {
		if !strings.Contains(trace, want) {
			t.Fatalf("missing trace identity: %s", trace)
		}
	}
}

func TestClusterMigrationPromQL(t *testing.T) {
	binary := os.Getenv("APM_TEST_PROMTOOL")
	if binary == "" {
		t.Skip("set APM_TEST_PROMTOOL to Prometheus promtool")
	}
	q := testQuery()
	q.MetricSource = "application_metrics"
	q.resourceScope = &ResourceScope{ClusterID: "132", K8sClusterID: "50"}
	scope := `service_name="orders",service_namespace="trade",deployment_environment_name="production"`
	inputs := []any{}
	for _, tc := range []struct{ labels, values string }{
		{`cluster_id="132",k8s_cluster_id="50"`, "0+60x5"},
		{`cluster_id="50"`, "0+120x5"},
		{`cluster_id="50",k8s_cluster_id="75"`, "0+60000x5"},
		{`cluster_id="132"`, "0+60000x5"},
	} {
		inputs = append(inputs, map[string]any{"series": q.counter() + "{" + scope + "," + tc.labels + "}", "values": tc.values})
	}
	fixture := map[string]any{"evaluation_interval": "1m", "tests": []any{map[string]any{"interval": "1m", "input_series": inputs, "promql_expr_test": []any{map[string]any{
		"expr": requestRate(q, 5*time.Minute, identityLabels), "eval_time": "5m", "exp_samples": []any{map[string]any{"labels": `{service="orders",service_namespace="trade",deployment_environment_name="production"}`, "value": 3}},
	}}}}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "cluster.yml")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), binary, "test", "rules", file).CombinedOutput(); err != nil {
		t.Fatalf("promtool: %v\n%s", err, output)
	}
}
