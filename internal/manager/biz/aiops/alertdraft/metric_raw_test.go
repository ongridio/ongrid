package alertdraft

import (
	"strings"
	"testing"
)

func TestBuildMetricRawExprAllowsExactPrometheusMetricName(t *testing.T) {
	expr, ok := buildMetricRawExprFromSpec(map[string]interface{}{
		"metric":    "mysql_global_status_slow_queries",
		"operator":  ">",
		"threshold": 5,
	})
	if !ok {
		t.Fatalf("buildMetricRawExprFromSpec() ok = false, want true")
	}
	if expr != "(mysql_global_status_slow_queries) > 5" {
		t.Fatalf("expr = %q", expr)
	}
}

func TestCompileDraftAllowsExactPrometheusMetricName(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action: "create",
		Rule: RuleConfigInput{
			Kind: "metric_raw",
			Spec: map[string]interface{}{
				"metric":    "mysql_global_status_slow_queries",
				"operator":  ">",
				"threshold": 5,
			},
		},
		RequestText: "创建 MySQL 慢查询告警",
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	expr, _ := got.Rule.Spec["expr"].(string)
	if !strings.Contains(expr, "mysql_global_status_slow_queries") {
		t.Fatalf("expr = %q, want raw Prometheus metric name", expr)
	}
}

func TestCompileDraftCombinesMetricRawExprOperatorThreshold(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action: "create",
		Rule: RuleConfigInput{
			Kind: "metric_raw",
			Spec: map[string]interface{}{
				"expr":      `sum by (device_id, ongrid_source) (pg_stat_database_numbackends) / max by (device_id, ongrid_source) (pg_settings_max_connections) * 100`,
				"operator":  ">",
				"threshold": 85,
			},
		},
		RequestText: "PostgreSQL 连接数长时间接近上限",
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	expr, _ := got.Rule.Spec["expr"].(string)
	want := `(sum by (device_id, ongrid_source) (pg_stat_database_numbackends) / max by (device_id, ongrid_source) (pg_settings_max_connections) * 100) > 85`
	if expr != want {
		t.Fatalf("expr = %q, want %q", expr, want)
	}
}

func TestCompileDraftKeepsMetricRawExprWithExistingComparison(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action: "create",
		Rule: RuleConfigInput{
			Kind: "metric_raw",
			Spec: map[string]interface{}{
				"expr":      `rate(mysql_global_status_slow_queries[5m]) > 5`,
				"operator":  "<",
				"threshold": 1,
			},
		},
		RequestText: "MySQL 慢查询突然变多",
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	expr, _ := got.Rule.Spec["expr"].(string)
	if expr != `rate(mysql_global_status_slow_queries[5m]) > 5` {
		t.Fatalf("expr = %q, want existing comparison unchanged", expr)
	}
}
