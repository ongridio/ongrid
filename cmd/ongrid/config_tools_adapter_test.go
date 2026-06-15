package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
)

func TestNormalizeAlertRuleConfigInputCanonicalizesHostMetricAliases(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Conditions: []aiopstools.AlertRuleCondition{
			{Metric: "cpu_usage_percent", Operator: ">", Threshold: 30},
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	if got.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Kind)
	}
	if got.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.ScopeType)
	}
	if got.Severity != "warning" {
		t.Fatalf("Severity = %q, want warning", got.Severity)
	}
	if got.RuleKey != "cpu_high" {
		t.Fatalf("RuleKey = %q, want cpu_high", got.RuleKey)
	}
	if got.Name != "CPU > 30%" {
		t.Fatalf("Name = %q, want CPU > 30%%", got.Name)
	}
	if got.RunbookURL == "" {
		t.Fatalf("RunbookURL should be defaulted")
	}
	if len(got.Conditions) != 1 {
		t.Fatalf("conditions = %d, want 1", len(got.Conditions))
	}
	cond := got.Conditions[0]
	if cond.Metric != "cpu_pct" {
		t.Fatalf("condition metric = %q, want cpu_pct", cond.Metric)
	}
	if cond.Window != "5m" {
		t.Fatalf("condition window = %q, want 5m", cond.Window)
	}
	if cond.Aggregator != "avg" {
		t.Fatalf("condition aggregator = %q, want avg", cond.Aggregator)
	}
}

func TestNormalizeAlertRuleConfigInputRewritesSimpleMetricRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": "cpu_usage_percent > 30 and disk_used_pct > 50 and mem_pct > 50",
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	if got.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Kind)
	}
	if got.JoinMode != "all" {
		t.Fatalf("JoinMode = %q, want all", got.JoinMode)
	}
	if got.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.ScopeType)
	}
	if got.Spec != nil {
		t.Fatalf("Spec = %#v, want nil after rewrite", got.Spec)
	}
	if len(got.Conditions) != 3 {
		t.Fatalf("conditions = %d, want 3", len(got.Conditions))
	}
	wantMetrics := []string{"cpu_pct", "disk_used_pct", "mem_pct"}
	for i, want := range wantMetrics {
		if got.Conditions[i].Metric != want {
			t.Fatalf("condition[%d].Metric = %q, want %q", i, got.Conditions[i].Metric, want)
		}
		if got.Conditions[i].Window != "5m" {
			t.Fatalf("condition[%d].Window = %q, want 5m", i, got.Conditions[i].Window)
		}
	}
}

func TestNormalizeAlertRuleConfigInputKeepsRealMetricRawPromQL(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `sum by (device_id) (rate(node_cpu_seconds_total{mode!="idle"}[5m])) > 0.8`,
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	if len(got.Conditions) != 0 {
		t.Fatalf("conditions = %d, want 0", len(got.Conditions))
	}
	if got.Spec["expr"] != in.Spec["expr"] {
		t.Fatalf("Spec expr changed: %#v", got.Spec)
	}
}

func TestNormalizeAlertRuleConfigInputHostMetricSpecBecomesThreshold(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"metric":    "cpu",
			"operator":  ">",
			"threshold": 90,
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	if got.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Kind)
	}
	if got.Spec != nil {
		t.Fatalf("Spec = %#v, want nil", got.Spec)
	}
	if len(got.Conditions) != 1 || got.Conditions[0].Metric != "cpu_pct" {
		t.Fatalf("Conditions = %#v, want cpu_pct threshold condition", got.Conditions)
	}
}

func TestNormalizeAlertRuleConfigInputExpandsDatabaseCatalogMetric(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"catalog_metric": "redis_memory_usage_pct",
			"operator":       ">",
			"threshold":      80,
			"labels": map[string]interface{}{
				"device_id": "7",
			},
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		"redis_memory_used_bytes{device_id=\"7\"}",
		"redis_memory_max_bytes{device_id=\"7\"}",
		"> 80",
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want to contain %q", expr, want)
		}
	}
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	if got.ScopeType != "global" {
		t.Fatalf("ScopeType = %q, want global", got.ScopeType)
	}
}

func TestDraftAlertRuleConfigIncludesMatchingDraftHash(t *testing.T) {
	adapter := newChatConfigAdapter(managersvcalert.NewStub())
	draft, err := adapter.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			Kind: "trace_latency",
			Spec: map[string]interface{}{
				"service":      "checkout",
				"threshold_ms": 750,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if draft.DraftHash == "" {
		t.Fatalf("DraftHash should be populated")
	}
	var payload struct {
		Action string                          `json:"action"`
		Rule   aiopstools.AlertRuleConfigInput `json:"rule"`
	}
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	want, err := aiopstools.AlertRuleConfigDraftHash(payload.Action, payload.Rule)
	if err != nil {
		t.Fatalf("AlertRuleConfigDraftHash() error = %v", err)
	}
	if draft.DraftHash != want {
		t.Fatalf("DraftHash = %q, want %q", draft.DraftHash, want)
	}
}

func TestApplyAlertRuleConfigRejectsSkippedPreview(t *testing.T) {
	adapter := newChatConfigAdapter(managersvcalert.NewStub())
	_, err := adapter.ApplyAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{Role: "admin"}, aiopstools.AlertRuleApplyArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "trace_latency_missing_service",
			Kind:     "trace_latency",
			Name:     "Trace latency missing service",
			Severity: "warning",
			Spec: map[string]interface{}{
				"threshold_ms": 750,
			},
		},
	})
	if err == nil {
		t.Fatalf("expected skipped preview error")
	}
	if !strings.Contains(err.Error(), "preview skipped before create") {
		t.Fatalf("error = %v, want skipped preview", err)
	}
}

func TestNormalizeAlertRuleConfigInputBuildsRawPredicateForCollectedMetricName(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Spec: map[string]interface{}{
			"metric":    "custom_app_queue_depth",
			"operator":  ">=",
			"threshold": "100",
			"selector":  `job="worker"`,
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	expr, _ := got.Spec["expr"].(string)
	if expr != `(custom_app_queue_depth{job="worker"}) >= 100` {
		t.Fatalf("expr = %q, want raw metric predicate", expr)
	}
}

func TestNormalizeAlertRuleConfigInputDoesNotInventExprForInvalidMetricName(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"metric":    "not a valid metric()",
			"operator":  ">",
			"threshold": 1,
		},
	}

	got := normalizeAlertRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	if expr, ok := got.Spec["expr"].(string); ok && expr != "" {
		t.Fatalf("expr = %q, want no invented PromQL for invalid metric name", expr)
	}
}

func TestDatabaseAlertMetricExprCoversSupportedCatalogMetrics(t *testing.T) {
	catalogMetrics := []string{
		"mysql_up",
		"mysql_connection_usage_pct",
		"mysql_threads_running",
		"mysql_qps",
		"mysql_slow_queries_15m",
		"mysql_aborted_connects_15m",
		"mysql_innodb_buffer_pool_hit_pct",
		"mysql_row_lock_waits_15m",
		"mysql_open_files_usage_pct",
		"mysql_temp_disk_tables_15m",
		"postgresql_up",
		"postgresql_connection_usage_pct",
		"postgresql_deadlocks_15m",
		"postgresql_cache_hit_ratio_pct",
		"postgresql_temp_bytes_15m",
		"postgresql_replication_lag_seconds",
		"postgresql_long_transaction_seconds",
		"postgresql_locks_count",
		"postgresql_database_size_bytes",
		"postgresql_tps",
		"redis_up",
		"redis_memory_usage_pct",
		"redis_client_usage_pct",
		"redis_connected_clients",
		"redis_ops_per_second",
		"redis_keyspace_hit_ratio_pct",
		"redis_evicted_keys_15m",
		"redis_rejected_connections_15m",
		"redis_blocked_clients",
		"redis_slowlog_length",
		"redis_latency_usec",
		"redis_key_count",
		"mongodb_up",
		"mongodb_connections_current",
		"mongodb_connections_available",
		"mongodb_operations_per_second",
		"mongodb_asserts_15m",
		"mongodb_page_faults_15m",
		"mongodb_resident_memory_bytes",
		"mongodb_wiredtiger_cache_usage_pct",
		"mongodb_wiredtiger_dirty_cache_pct",
		"mongodb_operation_latency_ms",
		"mongodb_collection_scans_15m",
		"mongodb_sort_spills_15m",
	}

	for _, metric := range catalogMetrics {
		t.Run(metric, func(t *testing.T) {
			expr, ok := databaseAlertMetricExpr(metric, `device_id="db-1"`)
			if !ok {
				t.Fatalf("databaseAlertMetricExpr(%q) ok = false, want true", metric)
			}
			if !strings.Contains(expr, `device_id="db-1"`) {
				t.Fatalf("expr = %q, want selector propagated", expr)
			}
		})
	}
}

func TestDatabaseAlertMetricExprCoversAnalyzerFallbackMetrics(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "redis_ops_per_second", want: []string{"redis_commands_processed_total", "redis_commands_total"}},
		{name: "redis_key_count", want: []string{"redis_db_keys", "redis_keys_count"}},
		{name: "mongodb_connections_current", want: []string{"mongodb_ss_connections", "conn_type=\"current\"", "mongodb_connections", "state=\"current\""}},
		{name: "mongodb_operations_per_second", want: []string{"mongodb_ss_opcounters", "mongodb_op_counters_total"}},
		{name: "mongodb_asserts_15m", want: []string{"mongodb_ss_asserts", "mongodb_asserts_total"}},
		{name: "mongodb_page_faults_15m", want: []string{"mongodb_ss_extra_info_page_faults", "mongodb_ss_extra_info_page_faults_total"}},
		{name: "mongodb_resident_memory_bytes", want: []string{"mongodb_ss_mem_resident", "mongodb_mongod_mem_resident_megabytes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, ok := databaseAlertMetricExpr(tt.name, `device_id="db-1"`)
			if !ok {
				t.Fatalf("databaseAlertMetricExpr(%q) ok = false, want true", tt.name)
			}
			for _, want := range tt.want {
				if !strings.Contains(expr, want) {
					t.Fatalf("expr = %q, want to contain %q", expr, want)
				}
			}
		})
	}
}

func TestNormalizeAlertRuleConfigInputDefaultsAllSupportedKinds(t *testing.T) {
	tests := []struct {
		name   string
		in     aiopstools.AlertRuleConfigInput
		assert func(t *testing.T, got aiopstools.AlertRuleConfigInput)
	}{
		{
			name: "metric_anomaly",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "anomaly",
				Spec: map[string]interface{}{"metric": "memory"},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "metric_anomaly" || got.Spec["metric"] != "mem_pct" || got.Spec["method"] != "zscore" || got.Spec["baseline_window"] != "1h" {
					t.Fatalf("got = %#v, want canonical metric_anomaly defaults", got)
				}
			},
		},
		{
			name: "metric_forecast",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "forecast",
				Spec: map[string]interface{}{"metric": "disk_available"},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "metric_forecast" || got.Spec["metric"] != "disk_avail_bytes" || got.Spec["fit_window"] != "1h" || got.Spec["operator"] != "<=" {
					t.Fatalf("got = %#v, want metric_forecast defaults", got)
				}
				if got.Spec["predict_seconds"] != float64(21600) {
					t.Fatalf("predict_seconds = %#v, want 21600", got.Spec["predict_seconds"])
				}
			},
		},
		{
			name: "metric_burn_rate",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "burn_rate",
				Spec: map[string]interface{}{"sli": `sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))`},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				burns, ok := got.Spec["burns"].([]interface{})
				if got.Kind != "metric_burn_rate" || got.Spec["slo"] != float64(99.9) || !ok || len(burns) != 2 {
					t.Fatalf("got = %#v, want metric_burn_rate defaults", got)
				}
			},
		},
		{
			name: "log_match",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "log",
				Spec: map[string]interface{}{"pattern": "(?i)error|panic"},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "log_match" || got.Spec["line_filter"] != "(?i)error|panic" || got.Spec["operator"] != ">=" || got.Spec["threshold"] != float64(1) {
					t.Fatalf("got = %#v, want log_match defaults", got)
				}
			},
		},
		{
			name: "log_volume",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "log_volume",
				Spec: map[string]interface{}{"operator": ">", "threshold": 3},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Spec["ratio_op"] != ">" || got.Spec["ratio_threshold"] != float64(3) {
					t.Fatalf("got = %#v, want log_volume ratio aliases", got)
				}
			},
		},
		{
			name: "trace_latency",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "latency",
				Spec: map[string]interface{}{"service": "checkout", "threshold": 750},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "trace_latency" || got.Spec["threshold_ms"] != float64(750) || got.Spec["quantile"] != "p95" {
					t.Fatalf("got = %#v, want trace_latency defaults", got)
				}
			},
		},
		{
			name: "trace_error_rate",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "error_rate",
				Spec: map[string]interface{}{"service": "checkout", "threshold": 2.5},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "trace_error_rate" || got.Spec["threshold_pct"] != 2.5 || got.Spec["operator"] != ">=" {
					t.Fatalf("got = %#v, want trace_error_rate defaults", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeAlertRuleConfigInput(tt.in)
			tt.assert(t, got)
			if got.RuleKey == "" {
				t.Fatalf("RuleKey should be defaulted")
			}
			if got.Name == "" {
				t.Fatalf("Name should be defaulted")
			}
			if got.RunbookURL == "" {
				t.Fatalf("RunbookURL should be defaulted")
			}
		})
	}
}
