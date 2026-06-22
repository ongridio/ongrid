package alertdraft

import "testing"

func TestCompileDraft_NormalizesRuleAndSummary(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action: "create",
		Rule: RuleConfigInput{
			Conditions: []RuleCondition{
				{Metric: "cpu_usage_percent", Operator: ">", Threshold: 80},
			},
		},
		RequestText: "创建 CPU 使用率超过 80% 的告警",
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if got.Action != "create" {
		t.Fatalf("Action = %q, want create", got.Action)
	}
	if got.Rule.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Rule.Kind)
	}
	if got.Rule.RuleKey != "cpu_high" {
		t.Fatalf("RuleKey = %q, want cpu_high", got.Rule.RuleKey)
	}
	if got.Rule.Name != "CPU > 80%" {
		t.Fatalf("Name = %q, want CPU > 80%%", got.Rule.Name)
	}
	if got.Summary != `create alert rule "CPU > 80%"` {
		t.Fatalf("Summary = %q", got.Summary)
	}
}

func TestCompileDraft_NormalizesLogQueryIntoEvaluatorFields(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action: "create",
		Rule: RuleConfigInput{
			Kind: "log_match",
			Spec: map[string]interface{}{
				"query": `{job=~".+"} |=~ "(?i)(error|fatal|panic)"`,
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if _, ok := got.Rule.Spec["query"]; ok {
		t.Fatalf("query should be normalized away, got spec=%#v", got.Rule.Spec)
	}
	if got.Rule.Spec["stream_selector"] != DefaultJournaldLogSelector {
		t.Fatalf("stream_selector = %#v, want %s", got.Rule.Spec["stream_selector"], DefaultJournaldLogSelector)
	}
	if got.Rule.Spec["line_filter"] != "(?i)(error|fatal|panic)" {
		t.Fatalf("line_filter = %#v, want regex from query", got.Rule.Spec["line_filter"])
	}
}

func TestCompileDraft_RecommendsHostScopeForHostForecast(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action:      "create",
		RequestText: "创建根分区未来 6 小时写满预测告警",
		Rule: RuleConfigInput{
			Kind:      "metric_forecast",
			ScopeType: "global",
			Spec: map[string]interface{}{
				"metric":          "disk_avail_bytes",
				"selector":        `mountpoint="/"`,
				"operator":        "<=",
				"threshold":       0,
				"predict_seconds": 21600,
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if got.Rule.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.Rule.ScopeType)
	}
}

func TestCompileDraft_RecommendsHostScopeForDatabaseRule(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action:      "create",
		RequestText: "创建 PostgreSQL 连接使用率过高告警",
		Rule: RuleConfigInput{
			Kind:      "metric_raw",
			ScopeType: "global",
			Spec: map[string]interface{}{
				"expr": `100 * sum by (device_id, ongrid_source) (pg_stat_database_numbackends) / max by (device_id, ongrid_source) (pg_settings_max_connections) > 85`,
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if got.Rule.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.Rule.ScopeType)
	}
}

func TestCompileDraft_KeepsExplicitGlobalDatabaseAggregation(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action:      "create",
		RequestText: "创建全局 PostgreSQL 连接使用率汇总告警",
		Rule: RuleConfigInput{
			Kind:      "metric_raw",
			ScopeType: "global",
			Spec: map[string]interface{}{
				"expr": `100 * sum(pg_stat_database_numbackends) / max(pg_settings_max_connections) > 85`,
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if got.Rule.ScopeType != "global" {
		t.Fatalf("ScopeType = %q, want global", got.Rule.ScopeType)
	}
}

func TestCompileDraft_RecommendsHostScopeForSystemLogs(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action:      "create",
		RequestText: "创建系统日志异常关键字告警",
		Rule: RuleConfigInput{
			Kind:      "log_match",
			ScopeType: "global",
			Spec: map[string]interface{}{
				"stream_selector": DefaultJournaldLogSelector,
				"line_filter":     "(?i)(error|fatal|panic)",
				"threshold":       1,
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if got.Rule.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.Rule.ScopeType)
	}
}
