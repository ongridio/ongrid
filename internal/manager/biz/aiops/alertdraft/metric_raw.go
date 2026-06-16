package alertdraft

import (
	"fmt"
	"strconv"
	"strings"
)

func normalizeMetricRawSpec(in RuleConfigInput) RuleConfigInput {
	if expr, ok := firstSpecString(in.Spec, "expr", "promql", "query"); ok {
		if shouldStripImplicitMetricSourceSelector(in.Spec) ||
			(!sourceSelectorExplicitlyScoped(in.Spec) && metricRawExprContainsDatabaseMetric(expr) && selectorContainsDatabaseIdentityMatcher(alertSpecSelector(in.Spec))) {
			sanitizeDatabaseIdentitySpecSelectors(in.Spec)
		}
		selector := alertSpecSelector(in.Spec)
		if rewritten, changed := rewriteKnownDatabaseMetricRawExpr(expr, selector); changed {
			expr = rewritten
		}
		if selector == "" && !sourceSelectorExplicitlyScoped(in.Spec) {
			if stripped, changed := stripLeakedMetricSourceIdentityMatchersFromPromQL(expr); changed {
				expr = stripped
			}
		} else {
			if merged, changed := mergeSelectorIntoPromQL(expr, selector); changed {
				expr = merged
			}
		}
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

func rewriteKnownDatabaseMetricRawExpr(expr string, explicitSelector string) (string, bool) {
	if rewritten, changed := rewriteMySQLConnectionUsageExpr(expr, explicitSelector); changed {
		return rewritten, true
	}
	if rewritten, changed := rewriteMongoSSConnectionUsageExpr(expr, explicitSelector); changed {
		return rewritten, true
	}
	lower := strings.ToLower(expr)
	if strings.Contains(lower, "mongodb_ss_connections") &&
		strings.Contains(lower, "conn_type") &&
		strings.Contains(lower, "available") &&
		strings.Contains(lower, "active") &&
		strings.Contains(expr, "/") {
		rewritten := mongoActiveConnectionMatcherRE.ReplaceAllString(expr, `conn_type="current"`)
		return rewritten, rewritten != expr
	}
	return expr, false
}

func rewriteMySQLConnectionUsageExpr(expr string, explicitSelector string) (string, bool) {
	lower := strings.ToLower(expr)
	if !strings.Contains(lower, "mysql_global_status_threads_connected") ||
		!strings.Contains(lower, "mysql_global_variables_max_connections") ||
		!strings.Contains(expr, "/") {
		return expr, false
	}
	selector := commonMySQLConnectionUsageSelector(expr)
	if stripped, changed := stripDatabaseIdentityMatchers(selector); changed {
		selector = stripped
	}
	selector = mergeSelector(selector, explicitSelector)
	connected := fmt.Sprintf("max by (device_id, ongrid_source) (%s)",
		metricSelector("mysql_global_status_threads_connected", selector))
	maxConn := fmt.Sprintf("max by (device_id, ongrid_source) (%s)",
		metricSelector("mysql_global_variables_max_connections", selector))
	base := fmt.Sprintf("100 * %s / clamp_min(%s, 1)", connected, maxConn)
	if m := promTrailingComparisonRE.FindStringSubmatch(expr); len(m) == 3 {
		return fmt.Sprintf("(%s) %s %s", base, normalizeAlertOperator(m[1]), normalizePercentThresholdString(m[2])), true
	}
	return base, true
}

func rewriteMongoSSConnectionUsageExpr(expr string, explicitSelector string) (string, bool) {
	lower := strings.ToLower(expr)
	if !strings.Contains(lower, "mongodb_ss_connections") ||
		!strings.Contains(lower, "conn_type") ||
		!strings.Contains(lower, "available") ||
		!(strings.Contains(lower, "current") || strings.Contains(lower, "active")) ||
		!strings.Contains(expr, "/") {
		return expr, false
	}
	selector := commonMongoSSConnectionSelector(expr)
	if stripped, changed := stripDatabaseIdentityMatchers(selector); changed {
		selector = stripped
	}
	selector = mergeSelector(selector, explicitSelector)
	current := fmt.Sprintf("max by (device_id, ongrid_source) (%s)",
		metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="current"`)))
	available := fmt.Sprintf("max by (device_id, ongrid_source) (%s)",
		metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="available"`)))
	base := fmt.Sprintf("100 * %s / clamp_min(%s + %s, 1)", current, current, available)
	if m := promTrailingComparisonRE.FindStringSubmatch(expr); len(m) == 3 {
		return fmt.Sprintf("(%s) %s %s", base, normalizeAlertOperator(m[1]), normalizePercentThresholdString(m[2])), true
	}
	return base, true
}

func stripLeakedMetricSourceIdentityMatchersFromPromQL(expr string) (string, bool) {
	if strings.TrimSpace(expr) == "" {
		return expr, false
	}
	var b strings.Builder
	changed := false
	skipNextLabelList := false
	labelListDepth := 0
	for i := 0; i < len(expr); {
		if isPromQuote(expr[i]) {
			end := consumePromQuotedString(expr, i)
			b.WriteString(expr[i:end])
			i = end
			continue
		}
		if labelListDepth > 0 {
			switch expr[i] {
			case '(':
				labelListDepth++
			case ')':
				labelListDepth--
			}
			b.WriteByte(expr[i])
			i++
			continue
		}
		if !isPromIdentStart(expr[i]) {
			if skipNextLabelList && expr[i] == '(' {
				labelListDepth = 1
				skipNextLabelList = false
			}
			b.WriteByte(expr[i])
			i++
			continue
		}
		start := i
		i++
		for i < len(expr) && isPromIdentPart(expr[i]) {
			i++
		}
		if start > 0 && isPromIdentPart(expr[start-1]) {
			b.WriteString(expr[start:i])
			continue
		}
		token := expr[start:i]
		lower := strings.ToLower(token)
		if promQLLabelListModifier(lower) {
			b.WriteString(token)
			skipNextLabelList = true
			continue
		}
		if promQLKeyword(lower) {
			b.WriteString(token)
			continue
		}
		j := skipPromSpaces(expr, i)
		if j < len(expr) && expr[j] == '(' {
			b.WriteString(token)
			continue
		}
		if j < len(expr) && expr[j] == '{' {
			selector, end, ok := readPromSelector(expr, j)
			if ok && shouldStripInlineSourceIdentityMatchers(token, normalizeSelectorPart(selector)) {
				b.WriteString(token)
				if stripped, selectorChanged := stripDatabaseIdentityMatchers(normalizeSelectorPart(selector)); stripped != "" {
					b.WriteString("{")
					b.WriteString(stripped)
					b.WriteString("}")
					changed = changed || selectorChanged
				} else {
					changed = true
				}
				i = end
				continue
			}
		}
		b.WriteString(token)
	}
	return b.String(), changed
}

func stripDatabaseIdentityMatchers(selector string) (string, bool) {
	selector = normalizeSelectorPart(selector)
	if selector == "" {
		return "", false
	}
	parts := splitPromSelectorMatchers(selector)
	out := make([]string, 0, len(parts))
	changed := false
	for _, part := range parts {
		key, _, _, ok := parsePromLabelMatcherWithOperator(part)
		if ok && isDatabaseIdentityLabel(key) {
			changed = true
			continue
		}
		out = append(out, part)
	}
	if !changed {
		return selector, false
	}
	return strings.Join(out, ","), true
}

func isDatabaseMetricName(metric string) bool {
	m := strings.ToLower(strings.TrimSpace(metric))
	return strings.HasPrefix(m, "mysql_") ||
		strings.HasPrefix(m, "pg_") ||
		strings.HasPrefix(m, "postgres_") ||
		strings.HasPrefix(m, "postgresql_") ||
		strings.HasPrefix(m, "redis_") ||
		strings.HasPrefix(m, "mongodb_") ||
		strings.HasPrefix(m, "mongo_")
}

func isCustomMetricName(metric string) bool {
	m := strings.ToLower(strings.TrimSpace(metric))
	return strings.HasPrefix(m, "custom_")
}

func isDatabaseIdentityLabel(label string) bool {
	switch strings.TrimSpace(label) {
	case "ongrid_source", "device_id", "job", "instance", "service":
		return true
	default:
		return false
	}
}

func selectorContainsDatabaseIdentityMatcher(selector string) bool {
	for _, part := range splitPromSelectorMatchers(normalizeSelectorPart(selector)) {
		key, _, _, ok := parsePromLabelMatcherWithOperator(part)
		if ok && isDatabaseIdentityLabel(key) {
			return true
		}
	}
	return false
}

func metricRawExprContainsDatabaseMetric(expr string) bool {
	for _, token := range promTokenRE.FindAllString(expr, -1) {
		if isDatabaseMetricName(token) {
			return true
		}
	}
	return false
}

func shouldStripImplicitMetricSourceSelector(spec map[string]interface{}) bool {
	if spec == nil || sourceSelectorExplicitlyScoped(spec) {
		return false
	}
	return selectorContainsImplicitMetricSourceIdentity(alertSpecSelector(spec))
}

func sourceSelectorExplicitlyScoped(spec map[string]interface{}) bool {
	if spec == nil {
		return false
	}
	for _, key := range []string{
		"source_explicit",
		"selector_explicit",
		"scope_explicit",
		"explicit_source",
		"explicit_selector",
	} {
		if b, ok := specBoolValue(spec[key]); ok && b {
			return true
		}
	}
	for _, key := range []string{"selector_source", "scope_source", "source_scope"} {
		if s := strings.ToLower(strings.TrimSpace(alertSpecStringValue(spec[key]))); s != "" {
			switch s {
			case "user", "explicit", "requested", "specified", "user_requested":
				return true
			}
		}
	}
	return false
}

func selectorContainsImplicitMetricSourceIdentity(selector string) bool {
	for _, part := range splitPromSelectorMatchers(normalizeSelectorPart(selector)) {
		key, _, _, ok := parsePromLabelMatcherWithOperator(part)
		if ok && isMetricSourceIdentityLabel(key) {
			return true
		}
	}
	return false
}

func isMetricSourceIdentityLabel(label string) bool {
	switch strings.TrimSpace(label) {
	case "ongrid_source", "device_id", "instance":
		return true
	default:
		return false
	}
}

func shouldStripInlineSourceIdentityMatchers(metric, selector string) bool {
	if isDatabaseMetricName(metric) {
		return selectorContainsDatabaseIdentityMatcher(selector)
	}
	if selectorContainsImplicitMetricSourceIdentity(selector) {
		return true
	}
	return isCustomMetricName(metric) && selectorContainsKnownCollectedSource(selector)
}

func selectorContainsJobMatcher(selector string) bool {
	for _, part := range splitPromSelectorMatchers(normalizeSelectorPart(selector)) {
		key, _, _, ok := parsePromLabelMatcherWithOperator(part)
		if ok && key == "job" {
			return true
		}
	}
	return false
}

func selectorContainsKnownCollectedSource(selector string) bool {
	for _, part := range splitPromSelectorMatchers(normalizeSelectorPart(selector)) {
		key, value, _, ok := parsePromLabelMatcherWithOperator(part)
		if !ok || key != "ongrid_source" {
			continue
		}
		if strings.HasPrefix(value, "db:") || strings.HasPrefix(value, "custom:") {
			return true
		}
	}
	return false
}

func sanitizeDatabaseIdentitySpecSelectors(spec map[string]interface{}) bool {
	if spec == nil {
		return false
	}
	changed := false
	for _, key := range []string{"selector", "label_selector", "matchers", "labels", "match_labels"} {
		raw, ok := spec[key]
		if !ok {
			continue
		}
		sanitized, empty, itemChanged := sanitizeDatabaseIdentitySelectorValue(raw)
		if !itemChanged {
			continue
		}
		changed = true
		if empty {
			delete(spec, key)
			continue
		}
		spec[key] = sanitized
	}
	return changed
}

func sanitizeDatabaseIdentitySelectorValue(raw interface{}) (interface{}, bool, bool) {
	switch v := raw.(type) {
	case string:
		stripped, changed := stripDatabaseIdentityMatchers(v)
		if !changed {
			return raw, false, false
		}
		return stripped, strings.TrimSpace(stripped) == "", true
	case []string:
		out := make([]string, 0, len(v))
		changed := false
		for _, item := range v {
			stripped, itemChanged := stripDatabaseIdentityMatchers(item)
			changed = changed || itemChanged
			if strings.TrimSpace(stripped) != "" {
				out = append(out, stripped)
			}
		}
		if !changed {
			return raw, false, false
		}
		return out, len(out) == 0, true
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		changed := false
		for _, item := range v {
			sanitized, empty, itemChanged := sanitizeDatabaseIdentitySelectorValue(item)
			changed = changed || itemChanged
			if !empty {
				out = append(out, sanitized)
			}
		}
		if !changed {
			return raw, false, false
		}
		return out, len(out) == 0, true
	case map[string]string:
		out := make(map[string]string, len(v))
		changed := false
		for k, value := range v {
			if isDatabaseIdentityLabel(k) {
				changed = true
				continue
			}
			out[k] = value
		}
		if !changed {
			return raw, false, false
		}
		return out, len(out) == 0, true
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		changed := false
		for k, value := range v {
			if isDatabaseIdentityLabel(k) {
				changed = true
				continue
			}
			out[k] = value
		}
		if !changed {
			return raw, false, false
		}
		return out, len(out) == 0, true
	default:
		return raw, false, false
	}
}

func specBoolValue(raw interface{}) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "y", "1", "explicit", "user", "requested", "specified":
			return true, true
		case "false", "no", "n", "0", "implicit", "sample":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func commonMongoSSConnectionSelector(expr string) string {
	matches := mongoSSConnectionsSelectorRE.FindAllStringSubmatch(expr, -1)
	if len(matches) == 0 {
		return ""
	}
	var common map[string]string
	for i, match := range matches {
		labels := exactPromSelectorLabels(match[1])
		delete(labels, "conn_type")
		if i == 0 {
			common = labels
			continue
		}
		for k, v := range common {
			if labels[k] != v {
				delete(common, k)
			}
		}
	}
	return selectorFromSpecLabels(common)
}

func commonMySQLConnectionUsageSelector(expr string) string {
	matches := mysqlConnectionUsageSelectorRE.FindAllStringSubmatch(expr, -1)
	if len(matches) == 0 {
		return ""
	}
	var common map[string]string
	for i, match := range matches {
		labels := exactPromSelectorLabels(match[1])
		if i == 0 {
			common = labels
			continue
		}
		for k, v := range common {
			if labels[k] != v {
				delete(common, k)
			}
		}
	}
	return selectorFromSpecLabels(common)
}

func exactPromSelectorLabels(selector string) map[string]string {
	selector = normalizeSelectorPart(selector)
	out := map[string]string{}
	if selector == "" {
		return out
	}
	for _, part := range splitPromSelectorMatchers(selector) {
		key, value, ok := parseExactPromLabelMatcher(part)
		if ok {
			out[key] = value
		}
	}
	return out
}

func splitPromSelectorMatchers(selector string) []string {
	var parts []string
	start := 0
	quote := byte(0)
	escaped := false
	for i := 0; i < len(selector); i++ {
		ch := selector[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if isPromQuote(ch) {
			quote = ch
			continue
		}
		if ch == ',' {
			if part := strings.TrimSpace(selector[start:i]); part != "" {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	if part := strings.TrimSpace(selector[start:]); part != "" {
		parts = append(parts, part)
	}
	return parts
}

func parseExactPromLabelMatcher(part string) (string, string, bool) {
	key, value, op, ok := parsePromLabelMatcherWithOperator(part)
	if !ok || op != "=" {
		return "", "", false
	}
	return key, value, true
}

func parsePromLabelMatcher(part string) (string, string, bool) {
	key, value, _, ok := parsePromLabelMatcherWithOperator(part)
	return key, value, ok
}

func parsePromLabelMatcherWithOperator(part string) (string, string, string, bool) {
	part = strings.TrimSpace(part)
	for _, op := range []string{"!=", "=~", "!~", "="} {
		idx := strings.Index(part, op)
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(part[:idx])
		value := strings.TrimSpace(part[idx+len(op):])
		if key == "" || !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
			return "", "", "", false
		}
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", "", "", false
		}
		return key, unquoted, op, true
	}
	return "", "", "", false
}

func formatAlertFloatFromString(raw string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return formatAlertFloat(n)
}

func normalizeSpecMetricCondition(in RuleConfigInput) RuleConfigInput {
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
		in.Conditions = []RuleCondition{{
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
	if shouldStripImplicitMetricSourceSelector(spec) ||
		(!sourceSelectorExplicitlyScoped(spec) && specReferencesDatabaseMetric(spec) && selectorContainsDatabaseIdentityMatcher(alertSpecSelector(spec))) {
		sanitizeDatabaseIdentitySpecSelectors(spec)
	}
	selector := alertSpecSelector(spec)
	var base string
	percentMetric := false
	if catalogMetric, ok := firstSpecString(spec, "catalog_metric", "db_metric", "semantic_metric"); ok {
		expr, ok := databaseAlertMetricExpr(catalogMetric, selector)
		if !ok {
			return "", false
		}
		base = expr
		percentMetric = isPercentCatalogMetric(catalogMetric)
	} else if metric, ok := firstSpecString(spec, "metric", "metric_key", "metric_name"); ok {
		if expr, ok := databaseAlertMetricExpr(metric, selector); ok {
			base = expr
			percentMetric = isPercentCatalogMetric(metric)
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
	if percentMetric {
		threshold = normalizePercentThresholdNumber(threshold)
	}
	return fmt.Sprintf("(%s) %s %s", base, normalizeAlertOperator(operator), formatAlertFloat(threshold)), true
}

func isPercentCatalogMetric(metric string) bool {
	code := normalizeCatalogMetricCode(metric)
	return strings.Contains(code, "_pct") ||
		strings.Contains(code, "_percent") ||
		strings.Contains(code, "_usage") ||
		strings.Contains(code, "_pressure") ||
		strings.Contains(code, "hit_ratio")
}

func normalizePercentThresholdNumber(n float64) float64 {
	if n > 0 && n <= 1 {
		return n * 100
	}
	return n
}

func normalizePercentThresholdString(raw string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return formatAlertFloatFromString(raw)
	}
	return formatAlertFloat(normalizePercentThresholdNumber(n))
}

func databaseAlertMetricExpr(metric string, selector string) (string, bool) {
	code := normalizeCatalogMetricCode(metric)
	ms := func(name string) string { return metricSelector(name, selector) }
	sumBySource := func(expr string) string {
		return fmt.Sprintf("sum by (device_id, ongrid_source) (%s)", expr)
	}
	maxBySource := func(expr string) string {
		return fmt.Sprintf("max by (device_id, ongrid_source) (%s)", expr)
	}
	minBySource := func(expr string) string {
		return fmt.Sprintf("min by (device_id, ongrid_source) (%s)", expr)
	}
	switch code {
	case "mysql_up", "mysql_liveness", "mysql_alive":
		return minBySource(ms("mysql_up")), true
	case "mysql_connection_usage_pct", "mysql_connection_pressure", "mysql_connections_pct":
		return fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
			maxBySource(ms("mysql_global_status_threads_connected")),
			maxBySource(ms("mysql_global_variables_max_connections"))), true
	case "mysql_threads_running":
		return maxBySource(ms("mysql_global_status_threads_running")), true
	case "mysql_qps", "mysql_query_rate", "mysql_queries_per_second":
		return sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mysql_global_status_questions"))), true
	case "mysql_slow_queries_15m", "mysql_slow_queries":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mysql_global_status_slow_queries"))), true
	case "mysql_aborted_connects_15m", "mysql_connection_errors":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mysql_global_status_aborted_connects"))), true
	case "mysql_innodb_buffer_pool_hit_pct", "mysql_buffer_pool_hit_pct":
		reads := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mysql_global_status_innodb_buffer_pool_reads")))
		requests := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mysql_global_status_innodb_buffer_pool_read_requests")))
		return fmt.Sprintf("100 * (1 - %s / clamp_min(%s, 1))", reads, requests), true
	case "mysql_row_lock_waits_15m", "mysql_lock_waits":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mysql_global_status_innodb_row_lock_waits"))), true
	case "mysql_open_files_usage_pct":
		return fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
			maxBySource(ms("mysql_global_status_open_files")),
			maxBySource(ms("mysql_global_variables_open_files_limit"))), true
	case "mysql_temp_disk_tables_15m", "mysql_tmp_disk_tables":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mysql_global_status_created_tmp_disk_tables"))), true
	case "postgresql_up", "postgres_up", "pg_up":
		return minBySource(ms("pg_up")), true
	case "postgresql_connection_usage_pct", "postgres_connection_usage_pct", "pg_connection_usage_pct", "postgresql_connection_pressure":
		return promOr(
			fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
				sumBySource(ms("pg_stat_database_numbackends")),
				maxBySource(ms("pg_settings_max_connections"))),
			fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
				sumBySource(ms("pg_stat_activity_count")),
				maxBySource(ms("pg_settings_max_connections"))),
		), true
	case "postgresql_active_connections", "postgres_active_connections", "pg_active_connections", "postgresql_numbackends", "postgres_numbackends", "pg_numbackends":
		return sumBySource(ms("pg_stat_database_numbackends")), true
	case "postgresql_deadlocks_15m", "postgres_deadlocks_15m", "pg_deadlocks_15m":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("pg_stat_database_deadlocks"))), true
	case "postgresql_cache_hit_ratio_pct", "postgres_cache_hit_ratio_pct", "pg_cache_hit_ratio_pct":
		hit := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("pg_stat_database_blks_hit")))
		read := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("pg_stat_database_blks_read")))
		return fmt.Sprintf("100 * %s / clamp_min(%s + %s, 1)", hit, hit, read), true
	case "postgresql_temp_bytes_15m", "postgres_temp_bytes_15m", "pg_temp_bytes_15m":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("pg_stat_database_temp_bytes"))), true
	case "postgresql_replication_lag_seconds", "postgres_replication_lag_seconds", "pg_replication_lag_seconds":
		return maxBySource(ms("pg_replication_lag_seconds")), true
	case "postgresql_long_transaction_seconds", "postgres_long_transaction_seconds", "pg_long_transaction_seconds":
		return maxBySource(ms("pg_stat_activity_max_tx_duration")), true
	case "postgresql_locks_count", "postgres_locks_count", "pg_locks_count":
		return sumBySource(ms("pg_locks_count")), true
	case "postgresql_database_size_bytes", "postgres_database_size_bytes", "pg_database_size_bytes":
		return sumBySource(ms("pg_database_size_bytes")), true
	case "postgresql_tps", "postgresql_transactions_per_second", "pg_tps":
		return fmt.Sprintf("%s + %s",
			sumBySource(fmt.Sprintf("rate(%s[5m])", ms("pg_stat_database_xact_commit"))),
			sumBySource(fmt.Sprintf("rate(%s[5m])", ms("pg_stat_database_xact_rollback")))), true
	case "redis_up", "redis_liveness":
		return minBySource(ms("redis_up")), true
	case "redis_memory_usage_pct", "redis_memory_pressure":
		return fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
			maxBySource(ms("redis_memory_used_bytes")),
			maxBySource(ms("redis_memory_max_bytes"))), true
	case "redis_client_usage_pct", "redis_client_pressure":
		return fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
			maxBySource(ms("redis_connected_clients")),
			maxBySource(ms("redis_config_maxclients"))), true
	case "redis_connected_clients":
		return maxBySource(ms("redis_connected_clients")), true
	case "redis_ops_per_second", "redis_qps", "redis_commands_per_second", "redis_commands_total", "redis_throughput":
		return promOr(
			sumBySource(fmt.Sprintf("rate(%s[5m])", ms("redis_commands_processed_total"))),
			sumBySource(fmt.Sprintf("rate(%s[5m])", ms("redis_commands_total"))),
		), true
	case "redis_keyspace_hit_ratio_pct", "redis_cache_hit_ratio_pct":
		hit := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("redis_keyspace_hits_total")))
		miss := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("redis_keyspace_misses_total")))
		return fmt.Sprintf("100 * %s / clamp_min(%s + %s, 1)", hit, hit, miss), true
	case "redis_evicted_keys_15m", "redis_evictions":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("redis_evicted_keys_total"))), true
	case "redis_rejected_connections_15m":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("redis_rejected_connections_total"))), true
	case "redis_blocked_clients":
		return maxBySource(ms("redis_blocked_clients")), true
	case "redis_slowlog_length":
		return maxBySource(ms("redis_slowlog_length")), true
	case "redis_latency_usec", "redis_latency_high":
		return maxBySource(ms("redis_latency_percentiles_usec")), true
	case "redis_key_count", "redis_keys_count", "redis_db_keys":
		return promOr(
			sumBySource(ms("redis_db_keys")),
			sumBySource(ms("redis_keys_count")),
		), true
	case "mongodb_up", "mongo_up", "mongodb_liveness":
		return minBySource(ms("mongodb_up")), true
	case "mongodb_connections_current", "mongodb_current_connections":
		return promOr(
			maxBySource(metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="current"`))),
			maxBySource(metricSelector("mongodb_connections", mergeSelector(selector, `state="current"`))),
		), true
	case "mongodb_connections_active", "mongodb_active_connections":
		return maxBySource(metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="active"`))), true
	case "mongodb_connection_usage_pct", "mongodb_connections_usage_pct", "mongodb_connection_pressure":
		current := maxBySource(metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="current"`)))
		available := maxBySource(metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="available"`)))
		return promOr(
			fmt.Sprintf("100 * %s / clamp_min(%s + %s, 1)", current, current, available),
			fmt.Sprintf("100 * %s / clamp_min(%s + %s, 1)",
				maxBySource(metricSelector("mongodb_connections", mergeSelector(selector, `state="current"`))),
				maxBySource(metricSelector("mongodb_connections", mergeSelector(selector, `state="current"`))),
				maxBySource(metricSelector("mongodb_connections", mergeSelector(selector, `state="available"`))),
			),
		), true
	case "mongodb_connections_available", "mongodb_available_connections":
		return promOr(
			maxBySource(metricSelector("mongodb_ss_connections", mergeSelector(selector, `conn_type="available"`))),
			maxBySource(metricSelector("mongodb_connections", mergeSelector(selector, `state="available"`))),
		), true
	case "mongodb_global_lock_queue", "mongodb_global_lock_queue_total", "mongodb_lock_queue", "mongodb_lock_queue_total":
		return maxBySource(metricSelector("mongodb_ss_globalLock_currentQueue", mergeSelector(selector, `count_type="total"`))), true
	case "mongodb_operations_per_second", "mongodb_ops_per_second", "mongodb_operations":
		return promOr(
			sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mongodb_ss_opcounters"))),
			sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mongodb_op_counters_total"))),
		), true
	case "mongodb_asserts_15m", "mongodb_asserts":
		return promOr(
			sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mongodb_ss_asserts"))),
			sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mongodb_asserts_total"))),
		), true
	case "mongodb_page_faults_15m", "mongodb_page_faults":
		return promOr(
			sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mongodb_ss_extra_info_page_faults"))),
			sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mongodb_ss_extra_info_page_faults_total"))),
		), true
	case "mongodb_resident_memory_bytes", "mongodb_resident_memory":
		return promOr(
			fmt.Sprintf("%s * 1024 * 1024", maxBySource(ms("mongodb_ss_mem_resident"))),
			fmt.Sprintf("%s * 1024 * 1024", maxBySource(ms("mongodb_mongod_mem_resident_megabytes"))),
		), true
	case "mongodb_wiredtiger_cache_usage_pct", "mongodb_cache_usage_pct":
		return fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
			maxBySource(ms("mongodb_ss_wt_cache_bytes_currently_in_the_cache")),
			maxBySource(ms("mongodb_ss_wt_cache_maximum_bytes_configured"))), true
	case "mongodb_wiredtiger_dirty_cache_pct":
		return fmt.Sprintf("100 * %s / clamp_min(%s, 1)",
			maxBySource(ms("mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache")),
			maxBySource(ms("mongodb_ss_wt_cache_maximum_bytes_configured"))), true
	case "mongodb_operation_latency_ms":
		latency := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mongodb_ss_opLatencies_latency")))
		ops := sumBySource(fmt.Sprintf("rate(%s[5m])", ms("mongodb_ss_opLatencies_ops")))
		return fmt.Sprintf("%s / clamp_min(%s, 1) / 1000", latency, ops), true
	case "mongodb_collection_scans_15m":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mongodb_ss_metrics_queryExecutor_collectionScans_total"))), true
	case "mongodb_sort_spills_15m", "mongodb_query_sort_spills_15m":
		return sumBySource(fmt.Sprintf("increase(%s[15m])", ms("mongodb_ss_metrics_query_sort_spillToDisk"))), true
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

func promOr(lhs, rhs string) string {
	return fmt.Sprintf("(%s) or (%s)", lhs, rhs)
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
