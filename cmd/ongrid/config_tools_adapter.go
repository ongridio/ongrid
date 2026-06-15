package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

var simpleHostMetricPredicateJoinRE = regexp.MustCompile(`(?i)\s+(and|or)\s+`)
var simpleHostMetricPredicatePartRE = regexp.MustCompile(`^\(?\s*([a-zA-Z_][a-zA-Z0-9_\-\s]*)\s*(==|!=|>=|<=|>|<)\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)\s*\)?$`)
var promMetricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

type chatConfigAdapter struct {
	alert *managersvcalert.Service
}

func newChatConfigAdapter(alertSvc *managersvcalert.Service) *chatConfigAdapter {
	return &chatConfigAdapter{alert: alertSvc}
}

func (a *chatConfigAdapter) DraftAlertRuleConfig(ctx context.Context, caller aiopstools.ConfigCaller, in aiopstools.AlertRuleConfigArgs) (*aiopstools.ConfigDraft, error) {
	if a.alert == nil {
		return nil, errs.ErrNotWiredYet
	}
	action, err := normalizeConfigAction(in.Action)
	if err != nil {
		return nil, err
	}
	rule := normalizeAlertRuleConfigInput(in.Rule)
	alertCaller := toAlertServiceCaller(caller)
	ruleInput := toAlertRuleInput(rule)
	res, err := a.alert.PreviewRule(ctx, alertCaller, ruleInput, in.LookbackSeconds)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule: %w", err)
	}
	warnings := []string(nil)
	if res != nil && strings.TrimSpace(res.SkippedReason) != "" {
		warnings = append(warnings, res.SkippedReason)
	}
	preview, err := configRaw(res)
	if err != nil {
		return nil, err
	}
	payload, err := configRaw(map[string]interface{}{
		"action": action,
		"rule":   rule,
	})
	if err != nil {
		return nil, err
	}
	return &aiopstools.ConfigDraft{
		Kind:      aiopstools.ConfigResultKindDraft,
		Domain:    aiopstools.ConfigDomainAlertRule,
		Action:    action,
		Summary:   alertRuleDraftSummary(action, rule),
		Payload:   payload,
		Preview:   preview,
		Warnings:  warnings,
		Rollback:  "可在 Alerts 规则列表中禁用或继续编辑该规则。",
		ApplyTool: aiopstools.ToolNameApplyConfigChange,
	}, nil
}

func (a *chatConfigAdapter) ApplyAlertRuleConfig(ctx context.Context, caller aiopstools.ConfigCaller, in aiopstools.AlertRuleApplyArgs) (*aiopstools.ConfigApplyResult, error) {
	if a.alert == nil {
		return nil, errs.ErrNotWiredYet
	}
	action, err := normalizeConfigAction(in.Action)
	if err != nil {
		return nil, err
	}
	alertCaller := toAlertServiceCaller(caller)
	ruleInput := toAlertRuleInput(in.Rule)
	if _, err := a.alert.PreviewRule(ctx, alertCaller, ruleInput, 0); err != nil {
		return nil, fmt.Errorf("preview alert rule before create: %w", err)
	}
	rule, err := a.alert.CreateRule(ctx, alertCaller, ruleInput)
	if err != nil {
		return nil, fmt.Errorf("create alert rule: %w", err)
	}
	return &aiopstools.ConfigApplyResult{
		Kind:       aiopstools.ConfigResultKindApply,
		Domain:     aiopstools.ConfigDomainAlertRule,
		Action:     action,
		Status:     "applied",
		ResourceID: rule.ID,
		Resource: &aiopstools.ConfigTarget{
			ID:   rule.ID,
			Name: rule.Name,
			Type: rule.Kind,
		},
		Message:  fmt.Sprintf("alert rule %q created", rule.Name),
		Rollback: "可在 Alerts 规则列表中禁用或继续编辑该规则。",
	}, nil
}

func toAlertRuleInput(in aiopstools.AlertRuleConfigInput) managersvcalert.RuleInput {
	in = normalizeAlertRuleConfigInput(in)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	conds := make([]managersvcalert.RuleCondition, 0, len(in.Conditions))
	for _, c := range in.Conditions {
		conds = append(conds, managersvcalert.RuleCondition{
			Metric:     c.Metric,
			Operator:   c.Operator,
			Threshold:  c.Threshold,
			Window:     c.Window,
			For:        c.For,
			Aggregator: c.Aggregator,
		})
	}
	return managersvcalert.RuleInput{
		RuleKey:             in.RuleKey,
		Kind:                in.Kind,
		Name:                in.Name,
		ScopeType:           in.ScopeType,
		JoinMode:            in.JoinMode,
		Severity:            in.Severity,
		Enabled:             enabled,
		Conditions:          conds,
		Spec:                in.Spec,
		Labels:              in.Labels,
		RunbookURL:          in.RunbookURL,
		NotifyChannelIDs:    in.NotifyChannelIDs,
		NotifyWindowSeconds: in.NotifyWindowSeconds,
		NotifyMinFires:      in.NotifyMinFires,
	}
}

func normalizeAlertRuleConfigInput(in aiopstools.AlertRuleConfigInput) aiopstools.AlertRuleConfigInput {
	in.Kind = normalizeAlertRuleKind(in.Kind)
	in = normalizeAlertRuleSpec(in)
	in = rewriteSimpleHostMetricRawExpr(in)
	for i := range in.Conditions {
		in.Conditions[i].Metric = canonicalAlertMetric(in.Conditions[i].Metric)
		if strings.TrimSpace(in.Conditions[i].Aggregator) == "" {
			in.Conditions[i].Aggregator = "avg"
		}
		if strings.TrimSpace(in.Conditions[i].Window) == "" {
			in.Conditions[i].Window = "5m"
		}
	}
	if strings.TrimSpace(in.Kind) == "" && len(in.Conditions) > 0 {
		in.Kind = "metric_threshold"
	}
	if strings.TrimSpace(in.Kind) == "" {
		in.Kind = inferAlertRuleKind(in)
	}
	if strings.TrimSpace(in.ScopeType) == "" {
		in.ScopeType = defaultAlertScope(in.Kind)
	}
	if strings.TrimSpace(in.Severity) == "" {
		in.Severity = "warning"
	}
	if strings.TrimSpace(in.RuleKey) == "" {
		in.RuleKey = suggestedAlertRuleKey(in)
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = suggestedAlertRuleName(in)
	}
	if strings.TrimSpace(in.RunbookURL) == "" {
		in.RunbookURL = suggestedAlertRunbookURL(in.RuleKey)
	}
	return in
}

func normalizeAlertRuleKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	k = strings.ReplaceAll(k, "-", "_")
	k = strings.ReplaceAll(k, " ", "_")
	switch k {
	case "", "metric_threshold", "metric_raw", "metric_anomaly", "metric_forecast", "metric_burn_rate",
		"log_match", "log_volume", "trace_latency", "trace_error_rate":
		return k
	case "threshold", "host_threshold", "host_metric", "host_metric_threshold":
		return "metric_threshold"
	case "promql", "prom_query", "raw_promql", "raw_metric", "database_metric", "db_metric", "custom_metric":
		return "metric_raw"
	case "anomaly", "metric_baseline", "baseline":
		return "metric_anomaly"
	case "forecast", "predict", "prediction":
		return "metric_forecast"
	case "burn_rate", "slo_burn_rate":
		return "metric_burn_rate"
	case "log", "log_regex", "log_error", "log_keyword":
		return "log_match"
	case "log_rate", "log_count", "log_spike":
		return "log_volume"
	case "latency", "trace_p95", "trace_p99":
		return "trace_latency"
	case "error_rate", "trace_errors", "trace_error":
		return "trace_error_rate"
	default:
		return k
	}
}

func inferAlertRuleKind(in aiopstools.AlertRuleConfigInput) string {
	if len(in.Conditions) > 0 {
		return "metric_threshold"
	}
	spec := in.Spec
	if spec == nil {
		return ""
	}
	switch {
	case hasAnySpecKey(spec, "stream_selector") && hasAnySpecKey(spec, "ratio_op", "ratio_threshold"):
		return "log_volume"
	case hasAnySpecKey(spec, "stream_selector", "line_filter", "filter", "regex", "pattern"):
		return "log_match"
	case hasAnySpecKey(spec, "threshold_ms", "latency_ms", "quantile", "operation"):
		return "trace_latency"
	case hasAnySpecKey(spec, "threshold_pct", "error_rate_pct", "error_rate_threshold"):
		return "trace_error_rate"
	case hasAnySpecKey(spec, "sli", "slo", "burns"):
		return "metric_burn_rate"
	case hasAnySpecKey(spec, "predict_seconds", "fit_window"):
		return "metric_forecast"
	case hasAnySpecKey(spec, "baseline_window", "baseline_step", "deviation", "method"):
		return "metric_anomaly"
	case hasAnySpecKey(spec, "expr", "promql", "query", "catalog_metric", "db_metric", "metric_key", "metric", "metric_name"):
		return "metric_raw"
	default:
		return ""
	}
}

func normalizeAlertRuleSpec(in aiopstools.AlertRuleConfigInput) aiopstools.AlertRuleConfigInput {
	if in.Spec == nil {
		return in
	}
	if strings.TrimSpace(in.Kind) == "" {
		in.Kind = inferAlertRuleKind(in)
	}
	switch in.Kind {
	case "metric_raw":
		in = normalizeMetricRawSpec(in)
	case "metric_anomaly":
		setSpecDefaultString(in.Spec, "method", "zscore")
		setSpecDefaultString(in.Spec, "baseline_window", "1h")
		setSpecDefaultString(in.Spec, "baseline_step", "5m")
		setSpecDefaultNumber(in.Spec, "deviation", 3)
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key"); ok {
			in.Spec["metric"] = canonicalAlertMetric(metric)
		}
	case "metric_forecast":
		setSpecDefaultString(in.Spec, "fit_window", "1h")
		setSpecDefaultNumber(in.Spec, "predict_seconds", 21600)
		setSpecDefaultString(in.Spec, "operator", "<=")
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key"); ok {
			in.Spec["metric"] = canonicalAlertMetric(metric)
		}
	case "metric_burn_rate":
		setSpecDefaultNumber(in.Spec, "slo", 99.9)
		if !hasAnySpecKey(in.Spec, "burns") {
			in.Spec["burns"] = []interface{}{
				map[string]interface{}{"window": "1h", "multiplier": 14.4},
				map[string]interface{}{"window": "6h", "multiplier": 6},
			}
		}
	case "log_match":
		if v, ok := firstSpecString(in.Spec, "filter", "regex", "pattern", "line_regex"); ok && strings.TrimSpace(alertSpecStringValue(in.Spec["line_filter"])) == "" {
			in.Spec["line_filter"] = v
		}
		setSpecDefaultString(in.Spec, "stream_selector", `{ongrid_source=~"journald:.+"}`)
		setSpecDefaultString(in.Spec, "window", "5m")
		setSpecDefaultString(in.Spec, "operator", ">=")
		setSpecDefaultNumber(in.Spec, "threshold", 1)
	case "log_volume":
		if op, ok := firstSpecString(in.Spec, "operator", "op"); ok && strings.TrimSpace(alertSpecStringValue(in.Spec["ratio_op"])) == "" {
			in.Spec["ratio_op"] = op
		}
		if threshold, ok := firstSpecNumber(in.Spec, "threshold", "value"); ok && !hasAnySpecKey(in.Spec, "ratio_threshold") {
			in.Spec["ratio_threshold"] = threshold
		}
		setSpecDefaultString(in.Spec, "stream_selector", `{ongrid_source=~".+"}`)
		setSpecDefaultString(in.Spec, "window", "5m")
		setSpecDefaultString(in.Spec, "ratio_op", ">=")
		setSpecDefaultNumber(in.Spec, "ratio_threshold", 2)
	case "trace_latency":
		if threshold, ok := firstSpecNumber(in.Spec, "threshold", "latency_ms", "value"); ok && !hasAnySpecKey(in.Spec, "threshold_ms") {
			in.Spec["threshold_ms"] = threshold
		}
		setSpecDefaultString(in.Spec, "quantile", "p95")
		setSpecDefaultString(in.Spec, "window", "5m")
		setSpecDefaultNumber(in.Spec, "threshold_ms", 500)
	case "trace_error_rate":
		if threshold, ok := firstSpecNumber(in.Spec, "threshold", "error_rate_pct", "error_rate_threshold", "value"); ok && !hasAnySpecKey(in.Spec, "threshold_pct") {
			in.Spec["threshold_pct"] = threshold
		}
		setSpecDefaultString(in.Spec, "window", "5m")
		setSpecDefaultString(in.Spec, "operator", ">=")
		setSpecDefaultNumber(in.Spec, "threshold_pct", 1)
	case "":
		in = normalizeSpecMetricCondition(in)
	}
	return in
}

func normalizeMetricRawSpec(in aiopstools.AlertRuleConfigInput) aiopstools.AlertRuleConfigInput {
	if expr, ok := firstSpecString(in.Spec, "expr", "promql", "query"); ok {
		in.Spec["expr"] = expr
		return in
	}
	if _, hasCatalog := firstSpecString(in.Spec, "catalog_metric", "db_metric", "semantic_metric"); !hasCatalog {
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name"); ok && isClosedSetAlertMetric(canonicalAlertMetric(metric)) {
			return normalizeSpecMetricCondition(in)
		}
	}
	if expr, ok := buildMetricRawExprFromSpec(in.Spec); ok {
		in.Spec["expr"] = expr
		return in
	}
	return normalizeSpecMetricCondition(in)
}

func normalizeSpecMetricCondition(in aiopstools.AlertRuleConfigInput) aiopstools.AlertRuleConfigInput {
	if len(in.Conditions) > 0 || in.Spec == nil {
		return in
	}
	metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name")
	if !ok {
		return in
	}
	operator, opOK := firstSpecString(in.Spec, "operator", "op", "comparison")
	threshold, thresholdOK := firstSpecNumber(in.Spec, "threshold", "threshold_pct", "threshold_ms", "value")
	if !opOK || !thresholdOK {
		return in
	}
	if canonical := canonicalAlertMetric(metric); isClosedSetAlertMetric(canonical) {
		in.Kind = "metric_threshold"
		in.Conditions = []aiopstools.AlertRuleCondition{{
			Metric:     canonical,
			Operator:   normalizeAlertOperator(operator),
			Threshold:  threshold,
			Window:     "5m",
			Aggregator: "avg",
		}}
		in.Spec = nil
		return in
	}
	if expr, ok := buildMetricRawExprFromSpec(in.Spec); ok {
		in.Kind = "metric_raw"
		in.Spec["expr"] = expr
	}
	return in
}

func buildMetricRawExprFromSpec(spec map[string]interface{}) (string, bool) {
	if spec == nil {
		return "", false
	}
	selector := alertSpecSelector(spec)
	var base string
	if catalogMetric, ok := firstSpecString(spec, "catalog_metric", "db_metric", "semantic_metric"); ok {
		expr, ok := databaseAlertMetricExpr(catalogMetric, selector)
		if !ok {
			return "", false
		}
		base = expr
	} else if metric, ok := firstSpecString(spec, "metric", "metric_key", "metric_name"); ok {
		if expr, ok := databaseAlertMetricExpr(metric, selector); ok {
			base = expr
		} else if promMetricNameRE.MatchString(metric) {
			base = metricSelector(metric, selector)
		} else {
			return "", false
		}
	} else {
		return "", false
	}
	operator, ok := firstSpecString(spec, "operator", "op", "comparison")
	if !ok {
		return "", false
	}
	threshold, ok := firstSpecNumber(spec, "threshold", "threshold_pct", "threshold_ms", "value")
	if !ok {
		return "", false
	}
	return fmt.Sprintf("(%s) %s %s", base, normalizeAlertOperator(operator), formatAlertFloat(threshold)), true
}

func databaseAlertMetricExpr(metric string, selector string) (string, bool) {
	code := normalizeCatalogMetricCode(metric)
	ms := func(name string) string { return metricSelector(name, selector) }
	switch code {
	case "mysql_up", "mysql_liveness", "mysql_alive":
		return fmt.Sprintf("min(%s)", ms("mysql_up")), true
	case "mysql_connection_usage_pct", "mysql_connection_pressure", "mysql_connections_pct":
		return fmt.Sprintf("100 * max(%s) / clamp_min(max(%s), 1)",
			ms("mysql_global_status_threads_connected"), ms("mysql_global_variables_max_connections")), true
	case "mysql_threads_running":
		return fmt.Sprintf("max(%s)", ms("mysql_global_status_threads_running")), true
	case "mysql_qps", "mysql_query_rate", "mysql_queries_per_second":
		return fmt.Sprintf("sum(rate(%s[5m]))", ms("mysql_global_status_questions")), true
	case "mysql_slow_queries_15m", "mysql_slow_queries":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mysql_global_status_slow_queries")), true
	case "mysql_aborted_connects_15m", "mysql_connection_errors":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mysql_global_status_aborted_connects")), true
	case "mysql_innodb_buffer_pool_hit_pct", "mysql_buffer_pool_hit_pct":
		return fmt.Sprintf("100 * (1 - sum(rate(%s[5m])) / clamp_min(sum(rate(%s[5m])), 1))",
			ms("mysql_global_status_innodb_buffer_pool_reads"), ms("mysql_global_status_innodb_buffer_pool_read_requests")), true
	case "mysql_row_lock_waits_15m", "mysql_lock_waits":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mysql_global_status_innodb_row_lock_waits")), true
	case "mysql_open_files_usage_pct":
		return fmt.Sprintf("100 * max(%s) / clamp_min(max(%s), 1)",
			ms("mysql_global_status_open_files"), ms("mysql_global_variables_open_files_limit")), true
	case "mysql_temp_disk_tables_15m", "mysql_tmp_disk_tables":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mysql_global_status_created_tmp_disk_tables")), true
	case "postgresql_up", "postgres_up", "pg_up":
		return fmt.Sprintf("min(%s)", ms("pg_up")), true
	case "postgresql_connection_usage_pct", "postgres_connection_usage_pct", "pg_connection_usage_pct", "postgresql_connection_pressure":
		return fmt.Sprintf("100 * sum(%s) / clamp_min(max(%s), 1)",
			ms("pg_stat_activity_count"), ms("pg_settings_max_connections")), true
	case "postgresql_deadlocks_15m", "postgres_deadlocks_15m", "pg_deadlocks_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("pg_stat_database_deadlocks")), true
	case "postgresql_cache_hit_ratio_pct", "postgres_cache_hit_ratio_pct", "pg_cache_hit_ratio_pct":
		return fmt.Sprintf("100 * sum(rate(%s[5m])) / clamp_min(sum(rate(%s[5m])) + sum(rate(%s[5m])), 1)",
			ms("pg_stat_database_blks_hit"), ms("pg_stat_database_blks_hit"), ms("pg_stat_database_blks_read")), true
	case "postgresql_temp_bytes_15m", "postgres_temp_bytes_15m", "pg_temp_bytes_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("pg_stat_database_temp_bytes")), true
	case "postgresql_replication_lag_seconds", "postgres_replication_lag_seconds", "pg_replication_lag_seconds":
		return fmt.Sprintf("max(%s)", ms("pg_replication_lag_seconds")), true
	case "postgresql_long_transaction_seconds", "postgres_long_transaction_seconds", "pg_long_transaction_seconds":
		return fmt.Sprintf("max(%s)", ms("pg_stat_activity_max_tx_duration")), true
	case "postgresql_locks_count", "postgres_locks_count", "pg_locks_count":
		return fmt.Sprintf("sum(%s)", ms("pg_locks_count")), true
	case "postgresql_database_size_bytes", "postgres_database_size_bytes", "pg_database_size_bytes":
		return fmt.Sprintf("sum(%s)", ms("pg_database_size_bytes")), true
	case "postgresql_tps", "postgresql_transactions_per_second", "pg_tps":
		return fmt.Sprintf("sum(rate(%s[5m])) + sum(rate(%s[5m]))",
			ms("pg_stat_database_xact_commit"), ms("pg_stat_database_xact_rollback")), true
	case "redis_up", "redis_liveness":
		return fmt.Sprintf("min(%s)", ms("redis_up")), true
	case "redis_memory_usage_pct", "redis_memory_pressure":
		return fmt.Sprintf("100 * max(%s) / clamp_min(max(%s), 1)",
			ms("redis_memory_used_bytes"), ms("redis_memory_max_bytes")), true
	case "redis_client_usage_pct", "redis_client_pressure":
		return fmt.Sprintf("100 * max(%s) / clamp_min(max(%s), 1)",
			ms("redis_connected_clients"), ms("redis_config_maxclients")), true
	case "redis_connected_clients":
		return fmt.Sprintf("max(%s)", ms("redis_connected_clients")), true
	case "redis_ops_per_second", "redis_qps", "redis_commands_per_second":
		return fmt.Sprintf("sum(rate(%s[5m]))", ms("redis_commands_processed_total")), true
	case "redis_keyspace_hit_ratio_pct", "redis_cache_hit_ratio_pct":
		return fmt.Sprintf("100 * sum(rate(%s[5m])) / clamp_min(sum(rate(%s[5m])) + sum(rate(%s[5m])), 1)",
			ms("redis_keyspace_hits_total"), ms("redis_keyspace_hits_total"), ms("redis_keyspace_misses_total")), true
	case "redis_evicted_keys_15m", "redis_evictions":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("redis_evicted_keys_total")), true
	case "redis_rejected_connections_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("redis_rejected_connections_total")), true
	case "redis_blocked_clients":
		return fmt.Sprintf("max(%s)", ms("redis_blocked_clients")), true
	case "redis_slowlog_length":
		return fmt.Sprintf("max(%s)", ms("redis_slowlog_length")), true
	case "redis_latency_usec", "redis_latency_high":
		return fmt.Sprintf("max(%s)", ms("redis_latency_percentiles_usec")), true
	case "mongodb_up", "mongo_up", "mongodb_liveness":
		return fmt.Sprintf("min(%s)", ms("mongodb_up")), true
	case "mongodb_connections_current", "mongodb_current_connections":
		return fmt.Sprintf("max(%s)", metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="current"`))), true
	case "mongodb_operations_per_second", "mongodb_ops_per_second":
		return fmt.Sprintf("sum(rate(%s[5m]))", ms("mongodb_ss_opcounters")), true
	case "mongodb_asserts_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mongodb_ss_asserts")), true
	case "mongodb_page_faults_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mongodb_ss_extra_info_page_faults")), true
	case "mongodb_resident_memory_bytes":
		return fmt.Sprintf("max(%s) * 1024 * 1024", ms("mongodb_ss_mem_resident")), true
	case "mongodb_wiredtiger_cache_usage_pct", "mongodb_cache_usage_pct":
		return fmt.Sprintf("100 * max(%s) / clamp_min(max(%s), 1)",
			ms("mongodb_ss_wt_cache_bytes_currently_in_the_cache"), ms("mongodb_ss_wt_cache_maximum_bytes_configured")), true
	case "mongodb_wiredtiger_dirty_cache_pct":
		return fmt.Sprintf("100 * max(%s) / clamp_min(max(%s), 1)",
			ms("mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache"), ms("mongodb_ss_wt_cache_maximum_bytes_configured")), true
	case "mongodb_operation_latency_ms":
		return fmt.Sprintf("sum(rate(%s[5m])) / clamp_min(sum(rate(%s[5m])), 1) / 1000",
			ms("mongodb_ss_opLatencies_latency"), ms("mongodb_ss_opLatencies_ops")), true
	case "mongodb_collection_scans_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mongodb_ss_metrics_queryExecutor_collectionScans_total")), true
	case "mongodb_sort_spills_15m", "mongodb_query_sort_spills_15m":
		return fmt.Sprintf("sum(increase(%s[15m]))", ms("mongodb_ss_metrics_query_sort_spillToDisk")), true
	default:
		return "", false
	}
}

func normalizeCatalogMetricCode(metric string) string {
	m := strings.ToLower(strings.TrimSpace(metric))
	m = strings.ReplaceAll(m, "-", "_")
	m = strings.ReplaceAll(m, " ", "_")
	m = strings.ReplaceAll(m, ".", "_")
	return m
}

func metricSelector(metric, selector string) string {
	selector = strings.TrimSpace(selector)
	selector = strings.TrimPrefix(selector, "{")
	selector = strings.TrimSuffix(selector, "}")
	if selector == "" {
		return metric
	}
	return fmt.Sprintf("%s{%s}", metric, selector)
}

func mergeSelector(a, b string) string {
	a = strings.TrimSpace(strings.Trim(strings.TrimSpace(a), "{}"))
	b = strings.TrimSpace(strings.Trim(strings.TrimSpace(b), "{}"))
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "," + b
	}
}

func alertSpecSelector(spec map[string]interface{}) string {
	if selector, ok := firstSpecString(spec, "selector", "label_selector", "matchers"); ok {
		return selector
	}
	for _, key := range []string{"labels", "match_labels"} {
		if raw, ok := spec[key]; ok {
			if selector := selectorFromSpecLabels(raw); selector != "" {
				return selector
			}
		}
	}
	return ""
}

func selectorFromSpecLabels(raw interface{}) string {
	pairs := map[string]string{}
	switch labels := raw.(type) {
	case map[string]string:
		for k, v := range labels {
			pairs[k] = v
		}
	case map[string]interface{}:
		for k, v := range labels {
			if s := alertSpecStringValue(v); s != "" {
				pairs[k] = s
			}
		}
	default:
		return ""
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf(`%s=%s`, k, strconv.Quote(pairs[k])))
	}
	return strings.Join(out, ",")
}

func firstSpecString(spec map[string]interface{}, keys ...string) (string, bool) {
	for _, key := range keys {
		if spec == nil {
			return "", false
		}
		if v, ok := spec[key]; ok {
			if s := strings.TrimSpace(alertSpecStringValue(v)); s != "" {
				return s, true
			}
		}
	}
	return "", false
}

func alertSpecStringValue(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return ""
	}
}

func firstSpecNumber(spec map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		if spec == nil {
			return 0, false
		}
		if v, ok := spec[key]; ok {
			switch x := v.(type) {
			case float64:
				return x, true
			case float32:
				return float64(x), true
			case int:
				return float64(x), true
			case int64:
				return float64(x), true
			case int32:
				return float64(x), true
			case json.Number:
				if n, err := x.Float64(); err == nil {
					return n, true
				}
			case string:
				if n, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}

func hasAnySpecKey(spec map[string]interface{}, keys ...string) bool {
	if spec == nil {
		return false
	}
	for _, key := range keys {
		if _, ok := spec[key]; ok {
			return true
		}
	}
	return false
}

func setSpecDefaultString(spec map[string]interface{}, key, value string) {
	if strings.TrimSpace(alertSpecStringValue(spec[key])) == "" {
		spec[key] = value
	}
}

func setSpecDefaultNumber(spec map[string]interface{}, key string, value float64) {
	if _, ok := firstSpecNumber(spec, key); !ok {
		spec[key] = value
	}
}

func normalizeAlertOperator(op string) string {
	switch strings.TrimSpace(op) {
	case ">", ">=", "<", "<=", "==", "!=":
		return strings.TrimSpace(op)
	case "=", "eq":
		return "=="
	case "gte", "=>":
		return ">="
	case "gt":
		return ">"
	case "lte", "=<":
		return "<="
	case "lt":
		return "<"
	case "ne", "neq":
		return "!="
	default:
		return ">"
	}
}

func formatAlertFloat(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func defaultAlertScope(kind string) string {
	switch strings.TrimSpace(kind) {
	case "metric_threshold", "metric_anomaly", "metric_forecast", "log_match", "log_volume":
		return "host"
	case "metric_raw", "metric_burn_rate", "trace_latency", "trace_error_rate":
		return "global"
	default:
		return ""
	}
}

func rewriteSimpleHostMetricRawExpr(in aiopstools.AlertRuleConfigInput) aiopstools.AlertRuleConfigInput {
	if len(in.Conditions) > 0 {
		return in
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != "" && kind != "metric_raw" {
		return in
	}
	expr, ok := alertSpecString(in.Spec, "expr")
	if !ok {
		return in
	}
	conds, joinMode, ok := parseSimpleHostMetricPredicate(expr)
	if !ok {
		return in
	}
	in.Kind = "metric_threshold"
	if joinMode != "" && strings.TrimSpace(in.JoinMode) == "" {
		in.JoinMode = joinMode
	}
	in.Conditions = conds
	in.Spec = nil
	return in
}

func alertSpecString(spec map[string]interface{}, key string) (string, bool) {
	if spec == nil {
		return "", false
	}
	raw, ok := spec[key]
	if !ok {
		return "", false
	}
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func parseSimpleHostMetricPredicate(expr string) ([]aiopstools.AlertRuleCondition, string, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.ContainsAny(expr, "{}[]") {
		return nil, "", false
	}
	joinMode := ""
	joins := simpleHostMetricPredicateJoinRE.FindAllStringSubmatch(expr, -1)
	if len(joins) > 0 {
		first := strings.ToLower(joins[0][1])
		for _, j := range joins[1:] {
			if strings.ToLower(j[1]) != first {
				return nil, "", false
			}
		}
		if first == "or" {
			joinMode = "any"
		} else {
			joinMode = "all"
		}
	}

	parts := simpleHostMetricPredicateJoinRE.Split(expr, -1)
	conds := make([]aiopstools.AlertRuleCondition, 0, len(parts))
	for _, part := range parts {
		matches := simpleHostMetricPredicatePartRE.FindStringSubmatch(strings.TrimSpace(part))
		if matches == nil {
			return nil, "", false
		}
		metric := canonicalAlertMetric(matches[1])
		if !isClosedSetAlertMetric(metric) {
			return nil, "", false
		}
		threshold, err := strconv.ParseFloat(matches[3], 64)
		if err != nil {
			return nil, "", false
		}
		conds = append(conds, aiopstools.AlertRuleCondition{
			Metric:    metric,
			Operator:  matches[2],
			Threshold: threshold,
		})
	}
	if len(conds) == 0 {
		return nil, "", false
	}
	return conds, joinMode, true
}

func isClosedSetAlertMetric(metric string) bool {
	switch metric {
	case "cpu_pct", "mem_pct", "disk_used_pct", "disk_avail_bytes", "load1", "load5", "load15", "net_rx_bps", "net_tx_bps":
		return true
	default:
		return false
	}
}

func canonicalAlertMetric(metric string) string {
	m := strings.ToLower(strings.TrimSpace(metric))
	m = strings.ReplaceAll(m, "-", "_")
	m = strings.ReplaceAll(m, " ", "_")
	switch m {
	case "cpu", "cpu_pct", "cpu_percent", "cpu_percentage", "cpu_usage", "cpu_usage_percent", "cpu_used_percent", "cpu_used_pct", "cpu_util", "cpu_utilization":
		return "cpu_pct"
	case "mem", "memory", "mem_pct", "memory_pct", "mem_percent", "memory_percent", "mem_usage", "memory_usage", "mem_usage_percent", "memory_usage_percent", "mem_used_percent", "memory_used_percent":
		return "mem_pct"
	case "disk", "disk_pct", "disk_percent", "disk_usage", "disk_usage_percent", "disk_used", "disk_used_percent", "disk_used_pct", "filesystem_usage", "filesystem_used_percent":
		return "disk_used_pct"
	case "disk_free", "disk_available", "disk_avail", "disk_available_bytes", "disk_avail_bytes", "filesystem_available", "filesystem_avail_bytes":
		return "disk_avail_bytes"
	case "load", "load_1", "load1", "loadavg", "load_avg":
		return "load1"
	case "load_5", "load5":
		return "load5"
	case "load_15", "load15":
		return "load15"
	case "net_rx", "rx", "rx_bps", "network_rx", "network_receive", "network_receive_bps", "network_in", "net_in":
		return "net_rx_bps"
	case "net_tx", "tx", "tx_bps", "network_tx", "network_transmit", "network_transmit_bps", "network_out", "net_out":
		return "net_tx_bps"
	default:
		return strings.TrimSpace(metric)
	}
}

func suggestedAlertRuleKey(in aiopstools.AlertRuleConfigInput) string {
	if len(in.Conditions) == 0 {
		if key := suggestedAlertRuleKeyFromSpec(in); key != "" {
			return key
		}
		return "custom_alert_rule"
	}
	metric := canonicalAlertMetric(in.Conditions[0].Metric)
	switch metric {
	case "cpu_pct":
		return "cpu_high"
	case "mem_pct":
		return "mem_high"
	case "disk_used_pct":
		return "disk_high"
	case "disk_avail_bytes":
		return "disk_low_available"
	case "load1", "load5", "load15":
		return metric + "_high"
	case "net_rx_bps":
		return "network_rx_high"
	case "net_tx_bps":
		return "network_tx_high"
	default:
		return strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(firstNonEmpty(metric, "custom_alert_rule")))
	}
}

func suggestedAlertRuleKeyFromSpec(in aiopstools.AlertRuleConfigInput) string {
	switch strings.TrimSpace(in.Kind) {
	case "metric_raw":
		if metric, ok := firstSpecString(in.Spec, "catalog_metric", "db_metric", "semantic_metric", "metric", "metric_key", "metric_name"); ok {
			return sanitizeRuleKey(metric)
		}
		if expr, ok := alertSpecString(in.Spec, "expr"); ok {
			if metric := firstPromMetricName(expr); metric != "" {
				return sanitizeRuleKey(metric)
			}
		}
	case "metric_anomaly", "metric_forecast":
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key"); ok {
			return sanitizeRuleKey(in.Kind + "_" + metric)
		}
	case "metric_burn_rate":
		return "slo_burn_rate"
	case "log_match":
		return "log_match"
	case "log_volume":
		return "log_volume"
	case "trace_latency":
		if service, ok := firstSpecString(in.Spec, "service"); ok {
			return sanitizeRuleKey("trace_latency_" + service)
		}
		return "trace_latency"
	case "trace_error_rate":
		if service, ok := firstSpecString(in.Spec, "service"); ok {
			return sanitizeRuleKey("trace_error_rate_" + service)
		}
		return "trace_error_rate"
	}
	return ""
}

func firstPromMetricName(expr string) string {
	for _, token := range regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`).FindAllString(expr, -1) {
		switch token {
		case "sum", "avg", "max", "min", "rate", "increase", "clamp_min", "histogram_quantile", "by", "without", "and", "or", "on":
			continue
		default:
			return token
		}
	}
	return ""
}

func sanitizeRuleKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteRune('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "custom_alert_rule"
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "_")
	}
	if out == "" {
		return "custom_alert_rule"
	}
	return out
}

func suggestedAlertRuleName(in aiopstools.AlertRuleConfigInput) string {
	if len(in.Conditions) == 0 {
		return suggestedAlertRuleNameFromSpec(in)
	}
	c := in.Conditions[0]
	metric := canonicalAlertMetric(c.Metric)
	threshold := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", c.Threshold), "0"), ".")
	switch metric {
	case "cpu_pct":
		return "CPU > " + threshold + "%"
	case "mem_pct":
		return "Memory > " + threshold + "%"
	case "disk_used_pct":
		return "Disk usage > " + threshold + "%"
	case "disk_avail_bytes":
		return "Disk available " + c.Operator + " " + threshold
	case "load1", "load5", "load15":
		return strings.ToUpper(metric) + " " + c.Operator + " " + threshold
	case "net_rx_bps":
		return "Network RX " + c.Operator + " " + threshold
	case "net_tx_bps":
		return "Network TX " + c.Operator + " " + threshold
	default:
		return firstNonEmpty(metric, "Custom alert rule") + " " + c.Operator + " " + threshold
	}
}

func suggestedAlertRuleNameFromSpec(in aiopstools.AlertRuleConfigInput) string {
	switch strings.TrimSpace(in.Kind) {
	case "metric_raw":
		if metric, ok := firstSpecString(in.Spec, "catalog_metric", "db_metric", "semantic_metric", "metric", "metric_key", "metric_name"); ok {
			return "Metric alert: " + metric
		}
		return "PromQL alert"
	case "metric_anomaly":
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key"); ok {
			return "Metric anomaly: " + metric
		}
		return "Metric anomaly alert"
	case "metric_forecast":
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key"); ok {
			return "Metric forecast: " + metric
		}
		return "Metric forecast alert"
	case "metric_burn_rate":
		return "SLO burn rate alert"
	case "log_match":
		return "Log match alert"
	case "log_volume":
		return "Log volume alert"
	case "trace_latency":
		if service, ok := firstSpecString(in.Spec, "service"); ok {
			return "Trace latency: " + service
		}
		return "Trace latency alert"
	case "trace_error_rate":
		if service, ok := firstSpecString(in.Spec, "service"); ok {
			return "Trace error rate: " + service
		}
		return "Trace error rate alert"
	default:
		return "Custom alert rule"
	}
}

func suggestedAlertRunbookURL(ruleKey string) string {
	key := strings.TrimSpace(ruleKey)
	switch key {
	case "cpu_high", "mem_high", "disk_high", "load1_high", "load5_high", "load15_high":
		return "https://github.com/ongridio/vault/blob/main/alerts/host-metrics.md#" + key
	default:
		return "https://github.com/ongridio/vault/blob/main/concepts/alerting.md"
	}
}

func normalizeConfigAction(action string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "", "create":
		return "create", nil
	default:
		return "", fmt.Errorf("%w: action must be create; v1 only supports creating alert rules", errs.ErrInvalid)
	}
}

func toAlertServiceCaller(c aiopstools.ConfigCaller) managersvcalert.Caller {
	return managersvcalert.Caller{UserID: c.UserID, Role: c.Role}
}

func alertRuleDraftSummary(action string, in aiopstools.AlertRuleConfigInput) string {
	name := firstNonEmpty(strings.TrimSpace(in.Name), strings.TrimSpace(in.RuleKey), "new alert rule")
	return fmt.Sprintf("%s alert rule %q", action, name)
}

func configRaw(v interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal config payload: %w", err)
	}
	return b, nil
}
