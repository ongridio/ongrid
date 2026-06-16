package alertdraft

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

var simpleHostMetricPredicateJoinRE = regexp.MustCompile(`(?i)\s+(and|or)\s+`)
var simpleHostMetricPredicatePartRE = regexp.MustCompile(`^\(?\s*([a-zA-Z_][a-zA-Z0-9_\-\s]*)\s*(==|!=|>=|<=|>|<)\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)\s*\)?$`)
var promMetricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var mysqlConnectionUsageSelectorRE = regexp.MustCompile(`mysql_global_(?:status_threads_connected|variables_max_connections)\s*(\{[^}]*\})?`)
var mongoActiveConnectionMatcherRE = regexp.MustCompile(`conn_type\s*=\s*"active"`)
var mongoSSConnectionsSelectorRE = regexp.MustCompile(`mongodb_ss_connections\s*(\{[^}]*\})?`)
var nodeFilesystemMetricSelectorRE = regexp.MustCompile(`node_filesystem_(?:avail|free|size)_bytes\s*(\{[^}]*\})?`)
var promTrailingComparisonRE = regexp.MustCompile(`(?s)\s*(>=|<=|==|!=|>|<)\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)\s*$`)
var promSimpleRangeSelectorRE = regexp.MustCompile(`\[(\d+(?:ms|s|m|h|d|w|y))\]`)
var promTokenRE = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)
var promMetricSourceIdentityMatcherRE = regexp.MustCompile(`(?i)\b(ongrid_source|device_id|job|instance|service)\s*(=|!=|=~|!~)\s*"([^"]+)"`)
var logLineFilterExprRE = regexp.MustCompile(`^\s*(\|=|\|~|!=|!~)\s*(.+?)\s*$`)
var logLabelPrefixFilterRE = regexp.MustCompile(`(?i)^\s*(?:\(\?i\))?\s*(detected_level|device_id|filename|identifier|level|ongrid_source|priority|service_name|severity|unit)\s*(=|!=|=~|!~)\s*"?([A-Za-z0-9_:/-]+(?:\.[A-Za-z0-9_:/-]+)*)"?\s*(?:\.\*)?(.*)$`)
var logLabelAlternationPrefixFilterRE = regexp.MustCompile(`(?i)^\s*(?:\(\?i\))?\s*\(([^)]+)\)\s*\[=:\]\s*"?([A-Za-z0-9_:/-]+(?:\.[A-Za-z0-9_:/-]+)*)"?\s*(?:\.\*)?(.*)$`)

func shouldBlockAlertRuleCreateOnPreviewSkip(reason string) bool {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false
	}
	blockingNeedles := []string{
		"请补全规则字段",
		"为空",
		"not in closed-set",
		"不在 closed-set",
		"no burn windows",
		"kind not supported",
		"required",
		"unsupported",
		"$window",
		"未发现 service_name",
	}
	for _, needle := range blockingNeedles {
		if strings.Contains(reason, needle) {
			return true
		}
	}
	return false
}

func normalizeAlertRuleConfigInput(in RuleConfigInput) RuleConfigInput {
	in.Kind = normalizeAlertRuleKind(in.Kind)
	in = normalizeTopLevelAlertRuleAliases(in)
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
	in.ScopeType = normalizeAlertScopeType(in.ScopeType, in.Kind)
	in.ScopeType = normalizeAlertScopeForKind(in.ScopeType, in.Kind)
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
	in = normalizeNotifyPolicyAliases(in)
	return in
}

func normalizeTopLevelAlertRuleAliases(in RuleConfigInput) RuleConfigInput {
	window := strings.TrimSpace(in.Window)
	sustainFor := strings.TrimSpace(in.For)
	if window == "" && sustainFor == "" {
		return in
	}
	for i := range in.Conditions {
		if window != "" && strings.TrimSpace(in.Conditions[i].Window) == "" {
			in.Conditions[i].Window = window
		}
		if sustainFor != "" && strings.TrimSpace(in.Conditions[i].For) == "" {
			in.Conditions[i].For = sustainFor
		}
	}
	if in.Kind != "metric_threshold" || len(in.Conditions) == 0 {
		if in.Spec == nil {
			in.Spec = map[string]interface{}{}
		}
		if window != "" && !hasAnySpecKey(in.Spec, "window") {
			in.Spec["window"] = window
		}
		if sustainFor != "" && !hasAnySpecKey(in.Spec, "for") {
			in.Spec["for"] = sustainFor
		}
	}
	in.Window = ""
	in.For = ""
	return in
}

func normalizeNotifyPolicyAliases(in RuleConfigInput) RuleConfigInput {
	if (in.NotifyWindowSeconds == 0) == (in.NotifyMinFires == 0) {
		return in
	}
	in.NotifyWindowSeconds = 0
	in.NotifyMinFires = 0
	return in
}

func normalizeAlertRuleConfigInputForRequest(in RuleConfigInput, requestText string) RuleConfigInput {
	in = normalizeMetricSourceScopeForRequest(in, requestText)
	in = normalizeAlertRuleConfigInput(in)
	return applyAlertRuleRequestHints(in, requestText)
}

func normalizeMetricSourceScopeForRequest(in RuleConfigInput, requestText string) RuleConfigInput {
	if in.Spec == nil || strings.TrimSpace(requestText) == "" || !sourceSelectorExplicitlyScoped(in.Spec) {
		return in
	}
	if !specContainsMetricSourceIdentity(in.Spec) || requestExplicitlyScopesMetricSource(requestText, in.Spec) {
		return in
	}
	clearSourceExplicitSpecFlags(in.Spec)
	return in
}

func clearSourceExplicitSpecFlags(spec map[string]interface{}) {
	for _, key := range []string{
		"source_explicit",
		"selector_explicit",
		"scope_explicit",
		"explicit_source",
		"explicit_selector",
		"selector_source",
		"scope_source",
		"source_scope",
	} {
		delete(spec, key)
	}
}

type metricSourceIdentityMatcher struct {
	key   string
	value string
}

func specContainsMetricSourceIdentity(spec map[string]interface{}) bool {
	return len(metricSourceIdentityMatchersFromSpec(spec)) > 0
}

func metricSourceIdentityMatchersFromSpec(spec map[string]interface{}) []metricSourceIdentityMatcher {
	if spec == nil {
		return nil
	}
	out := []metricSourceIdentityMatcher{}
	for _, part := range splitPromSelectorMatchers(normalizeSelectorPart(alertSpecSelector(spec))) {
		key, value, _, ok := parsePromLabelMatcherWithOperator(part)
		if !ok || !isMetricSourceRequestIdentityLabel(key) {
			continue
		}
		out = append(out, metricSourceIdentityMatcher{key: key, value: value})
	}
	for _, key := range []string{"expr", "promql", "query"} {
		expr, ok := alertSpecString(spec, key)
		if !ok {
			continue
		}
		for _, match := range promMetricSourceIdentityMatcherRE.FindAllStringSubmatch(expr, -1) {
			if len(match) != 4 {
				continue
			}
			label := strings.ToLower(strings.TrimSpace(match[1]))
			if !isMetricSourceRequestIdentityLabel(label) {
				continue
			}
			out = append(out, metricSourceIdentityMatcher{key: label, value: strings.TrimSpace(match[3])})
		}
	}
	return out
}

func isMetricSourceRequestIdentityLabel(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "ongrid_source", "device_id", "job", "instance", "service":
		return true
	default:
		return false
	}
}

func requestExplicitlyScopesMetricSource(requestText string, spec map[string]interface{}) bool {
	text := strings.ToLower(strings.TrimSpace(requestText))
	if text == "" {
		return false
	}
	if strings.Contains(text, "ongrid_source") || strings.Contains(text, "device_id") ||
		strings.Contains(text, "db:") || strings.Contains(text, "custom:") {
		return true
	}
	for _, matcher := range metricSourceIdentityMatchersFromSpec(spec) {
		if requestMentionsMetricSourceMatcher(text, matcher) {
			return true
		}
	}
	return false
}

func requestMentionsMetricSourceMatcher(lowerText string, matcher metricSourceIdentityMatcher) bool {
	value := strings.ToLower(strings.TrimSpace(matcher.value))
	if value == "" {
		return false
	}
	switch matcher.key {
	case "ongrid_source":
		if strings.Contains(lowerText, value) {
			return true
		}
		if rest, ok := strings.CutPrefix(value, "db:"); ok && strings.Contains(lowerText, rest) && requestHasSourceScopeWord(lowerText) {
			return true
		}
		if rest, ok := strings.CutPrefix(value, "custom:"); ok && strings.Contains(lowerText, rest) && requestHasSourceScopeWord(lowerText) {
			return true
		}
	case "device_id":
		return strings.Contains(lowerText, "device_id") && strings.Contains(lowerText, value)
	case "job", "instance", "service":
		return strings.Contains(lowerText, matcher.key) && strings.Contains(lowerText, value)
	}
	return false
}

func requestHasSourceScopeWord(lowerText string) bool {
	for _, word := range []string{"source", "采集源", "来源", "实例", "instance"} {
		if strings.Contains(lowerText, word) {
			return true
		}
	}
	return false
}

func applyAlertRuleRequestHints(in RuleConfigInput, requestText string) RuleConfigInput {
	if in.Spec == nil || strings.TrimSpace(requestText) == "" {
		return in
	}
	switch in.Kind {
	case "log_match", "log_volume":
		if selector := logSelectorHintsFromRequest(requestText); selector != "" {
			in.Spec["stream_selector"] = mergeLogStreamSelector(alertSpecStringValue(in.Spec["stream_selector"]), selector)
		}
	case "metric_forecast":
		normalizeMetricForecastRequestHints(in.Spec, requestText)
	}
	return in
}

func logSelectorHintsFromRequest(text string) string {
	fields := []string{"level", "unit", "identifier", "service_name", "filename", "device_id"}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if value, ok := logSelectorHintValueFromRequest(text, field); ok {
			parts = append(parts, formatPromLabelMatcher(field, "=", value))
		}
	}
	return strings.Join(parts, ",")
}

func logSelectorHintValueFromRequest(text, field string) (string, bool) {
	pattern := fmt.Sprintf(`(?i)(?:^|[^\pL\pN_])%s\s*(?:=|:|：|为|是)\s*["']?([A-Za-z0-9_.:/-]+)["']?`, regexp.QuoteMeta(field))
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(text)
	if len(matches) != 2 {
		return "", false
	}
	value := strings.TrimSpace(matches[1])
	value = strings.Trim(value, `"'`)
	if value == "" {
		return "", false
	}
	return value, true
}

func normalizeMetricForecastRequestHints(spec map[string]interface{}, requestText string) {
	if spec == nil || !requestMentionsFilesystemAvailablePercent(requestText) {
		return
	}
	metric, _ := firstSpecString(spec, "metric", "metric_key")
	if canonicalAlertMetric(metric) != "disk_avail_bytes" {
		return
	}
	selector := alertSpecSelector(spec)
	if selector == "" {
		selector = filesystemSelectorFromRequest(requestText)
	}
	rewriteMetricForecastDiskAvailablePercent(spec, selector)
}

func normalizeMetricForecastSpec(spec map[string]interface{}) {
	if spec == nil {
		return
	}
	if expr, ok := firstSpecString(spec, "expr", "promql", "query"); ok && filesystemAvailablePercentExpr(expr) {
		selector := alertSpecSelector(spec)
		if selector == "" {
			selector = commonNodeFilesystemSelector(expr)
		}
		rewriteMetricForecastDiskAvailablePercent(spec, selector)
	}
}

func filesystemAvailablePercentExpr(expr string) bool {
	lower := strings.ToLower(expr)
	return strings.Contains(lower, "node_filesystem_size_bytes") &&
		(strings.Contains(lower, "node_filesystem_avail_bytes") || strings.Contains(lower, "node_filesystem_free_bytes")) &&
		strings.Contains(lower, "/")
}

func requestMentionsFilesystemAvailablePercent(text string) bool {
	lower := strings.ToLower(text)
	hasDisk := strings.Contains(lower, "disk") || strings.Contains(lower, "filesystem") ||
		strings.Contains(text, "磁盘") || strings.Contains(text, "文件系统") || strings.Contains(text, "分区")
	hasAvailable := strings.Contains(lower, "avail") || strings.Contains(lower, "free") ||
		strings.Contains(text, "可用") || strings.Contains(text, "剩余") || strings.Contains(text, "空闲")
	hasPercent := strings.Contains(text, "%") || strings.Contains(text, "百分比") || strings.Contains(text, "使用率") || strings.Contains(text, "占比")
	return hasDisk && hasAvailable && hasPercent
}

func filesystemSelectorFromRequest(text string) string {
	if strings.Contains(text, "根分区") || strings.Contains(text, "根目录") || strings.Contains(text, " / ") || strings.Contains(text, " /的") || strings.Contains(text, "/ 的") {
		return `mountpoint="/"`
	}
	return ""
}

func rewriteMetricForecastDiskAvailablePercent(spec map[string]interface{}, selector string) {
	threshold, ok := firstSpecNumber(spec, "threshold", "threshold_pct", "value")
	if !ok {
		return
	}
	threshold = normalizePercentThresholdNumber(threshold)
	if threshold < 0 || threshold > 100 {
		return
	}
	spec["metric"] = "disk_used_pct"
	spec["operator"] = invertAvailablePercentOperator(normalizeAlertOperator(alertSpecStringValue(spec["operator"])))
	spec["threshold"] = 100 - threshold
	if strings.TrimSpace(selector) != "" {
		spec["selector"] = selector
	}
	delete(spec, "expr")
	delete(spec, "promql")
	delete(spec, "query")
}

func invertAvailablePercentOperator(op string) string {
	switch normalizeAlertOperator(op) {
	case "<":
		return ">"
	case "<=":
		return ">="
	case ">":
		return "<"
	case ">=":
		return "<="
	default:
		return normalizeAlertOperator(op)
	}
}

func commonNodeFilesystemSelector(expr string) string {
	matches := nodeFilesystemMetricSelectorRE.FindAllStringSubmatch(expr, -1)
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

func normalizeAlertScopeType(scopeType, kind string) string {
	scope := strings.ToLower(strings.TrimSpace(scopeType))
	scope = strings.ReplaceAll(scope, "-", "_")
	scope = strings.ReplaceAll(scope, " ", "_")
	switch scope {
	case "":
		return ""
	case "global", "all", "any", "source", "sources", "database", "databases", "db", "service", "cluster":
		return "global"
	case "host", "device", "node", "edge", "machine", "server":
		return "host"
	case "monitoring_pipeline", "pipeline", "scrape", "collector":
		return "monitoring_pipeline"
	default:
		return scope
	}
}

func normalizeAlertScopeForKind(scopeType, kind string) string {
	scope := strings.TrimSpace(scopeType)
	switch strings.TrimSpace(kind) {
	case "log_match", "log_volume", "trace_latency", "trace_error_rate", "metric_burn_rate":
		if scope == "monitoring_pipeline" {
			return "global"
		}
	}
	return scope
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

func inferAlertRuleKind(in RuleConfigInput) string {
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

func normalizeAlertRuleSpec(in RuleConfigInput) RuleConfigInput {
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
		in.Spec["operator"] = normalizeAlertOperator(alertSpecStringValue(in.Spec["operator"]))
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key"); ok {
			in.Spec["metric"] = canonicalAlertMetric(metric)
		}
		normalizeMetricForecastSpec(in.Spec)
	case "metric_burn_rate":
		if sli, ok := firstSpecString(in.Spec, "sli"); ok {
			in.Spec["sli"] = normalizeBurnRateSLIExpression(sli)
		}
		setSpecDefaultNumber(in.Spec, "slo", 99.9)
		if slo, ok := firstSpecNumber(in.Spec, "slo"); ok {
			in.Spec["slo"] = normalizeBurnRateSLOPercent(slo)
		}
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
		extraSelector := ""
		if filter := alertSpecStringValue(in.Spec["line_filter"]); strings.TrimSpace(filter) != "" {
			normalizedFilter, selector := normalizeLogLineFilter(filter)
			in.Spec["line_filter"] = normalizedFilter
			extraSelector = selector
		}
		in.Spec["stream_selector"] = mergeLogStreamSelector(
			normalizeLogStreamSelector(alertSpecStringValue(in.Spec["stream_selector"]), defaultJournaldLogSelector),
			extraSelector,
		)
		setSpecDefaultString(in.Spec, "window", "5m")
		setSpecDefaultString(in.Spec, "operator", ">=")
		in.Spec["operator"] = normalizeAlertOperator(alertSpecStringValue(in.Spec["operator"]))
		setSpecDefaultNumber(in.Spec, "threshold", 1)
	case "log_volume":
		if op, ok := firstSpecString(in.Spec, "operator", "op"); ok && strings.TrimSpace(alertSpecStringValue(in.Spec["ratio_op"])) == "" {
			in.Spec["ratio_op"] = op
		}
		if threshold, ok := firstSpecNumber(in.Spec, "threshold", "value"); ok && !hasAnySpecKey(in.Spec, "ratio_threshold") {
			in.Spec["ratio_threshold"] = threshold
		}
		if v, ok := firstSpecString(in.Spec, "filter", "regex", "pattern", "line_regex"); ok && strings.TrimSpace(alertSpecStringValue(in.Spec["line_filter"])) == "" {
			in.Spec["line_filter"] = v
		}
		extraSelector := ""
		if filter := alertSpecStringValue(in.Spec["line_filter"]); strings.TrimSpace(filter) != "" {
			normalizedFilter, selector := normalizeLogLineFilter(filter)
			in.Spec["line_filter"] = normalizedFilter
			extraSelector = selector
		}
		in.Spec["stream_selector"] = mergeLogStreamSelector(
			normalizeLogStreamSelector(alertSpecStringValue(in.Spec["stream_selector"]), defaultAllLogsSelector),
			extraSelector,
		)
		setSpecDefaultString(in.Spec, "window", "5m")
		setSpecDefaultString(in.Spec, "ratio_op", ">=")
		in.Spec["ratio_op"] = normalizeAlertOperator(alertSpecStringValue(in.Spec["ratio_op"]))
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
		in.Spec["operator"] = normalizeAlertOperator(alertSpecStringValue(in.Spec["operator"]))
		setSpecDefaultNumber(in.Spec, "threshold_pct", 1)
	case "":
		in = normalizeSpecMetricCondition(in)
	}
	return in
}

func normalizeLogLineFilter(raw string) (string, string) {
	filter := strings.TrimSpace(raw)
	if filter == "" {
		return "", ""
	}
	chain := parseLogLineFilterChain(filter)
	if len(chain) > 0 {
		regexParts := make([]string, 0, len(chain))
		selectorParts := make([]string, 0, len(chain))
		for _, part := range chain {
			if filter, selector := normalizePlainLogLineFilter(part.body); selector != "" {
				selectorParts = append(selectorParts, selector)
				if filter != "" {
					regexParts = append(regexParts, filter)
				}
				continue
			}
			if selector, ok := logSelectorMatcherFromLineFilter(part.body); ok {
				selectorParts = append(selectorParts, selector)
				continue
			}
			switch part.op {
			case "|=":
				regexParts = append(regexParts, regexp.QuoteMeta(part.body))
			case "|~":
				regexParts = append(regexParts, part.body)
			default:
				regexParts = append(regexParts, part.body)
			}
		}
		return strings.Join(regexParts, "|"), strings.Join(selectorParts, ",")
	}
	match := logLineFilterExprRE.FindStringSubmatch(filter)
	if len(match) != 3 {
		return normalizePlainLogLineFilter(filter)
	}
	op := match[1]
	body := strings.TrimSpace(match[2])
	if len(body) >= 2 && strings.HasPrefix(body, `"`) && strings.HasSuffix(body, `"`) {
		if unquoted, err := strconv.Unquote(body); err == nil {
			body = unquoted
		}
	}
	if selector, ok := logSelectorMatcherFromLineFilter(body); ok {
		return "", selector
	}
	if op == "|=" {
		filter, selector := normalizePlainLogLineFilter(body)
		if selector != "" {
			return filter, selector
		}
		return regexp.QuoteMeta(body), ""
	}
	return normalizePlainLogLineFilter(body)
}

func normalizePlainLogLineFilter(filter string) (string, string) {
	body := strings.TrimSpace(filter)
	if body == "" {
		return "", ""
	}
	matches := logLabelPrefixFilterRE.FindStringSubmatch(body)
	if len(matches) != 5 {
		return normalizeAlternationLogLineFilter(body)
	}
	key := normalizeLogSelectorLabelKey(matches[1])
	op := matches[2]
	value := matches[3]
	rest := strings.TrimSpace(matches[4])
	if rest == "" || !isKnownLogSelectorLabel(key) {
		return body, ""
	}
	return rest, formatPromLabelMatcher(key, op, value)
}

func normalizeAlternationLogLineFilter(filter string) (string, string) {
	matches := logLabelAlternationPrefixFilterRE.FindStringSubmatch(filter)
	if len(matches) != 4 {
		return filter, ""
	}
	key := ""
	for _, candidate := range strings.Split(matches[1], "|") {
		normalized := normalizeLogSelectorLabelKey(strings.TrimSpace(candidate))
		if isKnownLogSelectorLabel(normalized) {
			key = normalized
			break
		}
	}
	if key == "" {
		return filter, ""
	}
	rest := strings.TrimSpace(matches[3])
	if rest == "" {
		return filter, ""
	}
	return rest, formatPromLabelMatcher(key, "=", matches[2])
}

type parsedLogLineFilter struct {
	op   string
	body string
}

func parseLogLineFilterChain(raw string) []parsedLogLineFilter {
	rest := strings.TrimSpace(raw)
	out := []parsedLogLineFilter(nil)
	for rest != "" {
		op := ""
		for _, candidate := range []string{"|=", "|~", "!=", "!~"} {
			if strings.HasPrefix(rest, candidate) {
				op = candidate
				rest = strings.TrimSpace(rest[len(candidate):])
				break
			}
		}
		if op == "" {
			return nil
		}
		body := ""
		if strings.HasPrefix(rest, `"`) {
			end := consumePromQuotedString(rest, 0)
			if end <= 0 || end > len(rest) {
				return nil
			}
			quoted := rest[:end]
			if unquoted, err := strconv.Unquote(quoted); err == nil {
				body = unquoted
			} else {
				body = strings.Trim(quoted, `"`)
			}
			rest = strings.TrimSpace(rest[end:])
		} else {
			next := nextLogLineFilterOpIndex(rest)
			if next < 0 {
				body = strings.TrimSpace(rest)
				rest = ""
			} else {
				body = strings.TrimSpace(rest[:next])
				rest = strings.TrimSpace(rest[next:])
			}
		}
		if body == "" {
			return nil
		}
		out = append(out, parsedLogLineFilter{op: op, body: body})
	}
	return out
}

func nextLogLineFilterOpIndex(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i-1] != ' ' && s[i-1] != '\t' {
			continue
		}
		for _, op := range []string{"|=", "|~", "!=", "!~"} {
			if strings.HasPrefix(s[i:], op) {
				return i
			}
		}
	}
	return -1
}

func logSelectorMatcherFromLineFilter(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "(?i)")
	text = strings.TrimSpace(text)
	for _, op := range []string{"!=", "=~", "!~", "="} {
		idx := strings.Index(text, op)
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(text[:idx])
		value := strings.TrimSpace(text[idx+len(op):])
		value = strings.Trim(value, `"`)
		if !isKnownLogSelectorLabel(key) || value == "" {
			return "", false
		}
		return formatPromLabelMatcher(key, op, value), true
	}
	return "", false
}

func mergeLogStreamSelector(selector, additions string) string {
	baseParts := splitPromSelectorMatchers(normalizeSelectorPart(selector))
	additionParts := splitPromSelectorMatchers(normalizeSelectorPart(additions))
	if len(additionParts) == 0 {
		return selector
	}
	seen := make(map[string]struct{}, len(baseParts)+len(additionParts))
	out := make([]string, 0, len(baseParts)+len(additionParts))
	for _, part := range baseParts {
		if key, _, _, ok := parsePromLabelMatcherWithOperator(part); ok {
			seen[key] = struct{}{}
		}
		out = append(out, part)
	}
	for _, part := range additionParts {
		key, _, _, ok := parsePromLabelMatcherWithOperator(part)
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		out = append(out, part)
		seen[key] = struct{}{}
	}
	if len(out) == 0 {
		return selector
	}
	return "{" + strings.Join(out, ",") + "}"
}

func normalizeBurnRateSLIExpression(sli string) string {
	sli = strings.TrimSpace(sli)
	if strings.Contains(sli, "$window") {
		return sli
	}
	return promSimpleRangeSelectorRE.ReplaceAllString(sli, "[$$window]")
}

func normalizeBurnRateSLOPercent(slo float64) float64 {
	if slo > 0 && slo <= 1 {
		return slo * 100
	}
	return slo
}

func normalizeLogStreamSelector(raw, fallback string) string {
	selector := strings.TrimSpace(raw)
	if selector == "" {
		return fallback
	}
	if looksLikeJournaldJobSelector(selector) {
		selector = normalizeGuessedJournaldLogSelector(selector)
	}
	return sanitizeLogStreamSelector(selector, fallback)
}

func normalizeGuessedJournaldLogSelector(selector string) string {
	parts := splitPromSelectorMatchers(normalizeSelectorPart(selector))
	out := []string{`ongrid_source=~"journald(:.*)?"`}
	hasSource := false
	for _, part := range parts {
		key, value, op, ok := parsePromLabelMatcherWithOperator(part)
		if !ok {
			continue
		}
		switch {
		case key == "job":
			continue
		case key == "ongrid_source":
			if value != "" {
				out[0] = formatPromLabelMatcher(key, op, value)
				hasSource = true
			}
		case isKnownLogSelectorLabel(key):
			out = append(out, formatPromLabelMatcher(key, op, value))
		}
	}
	if !hasSource && len(out) == 0 {
		return defaultJournaldLogSelector
	}
	return "{" + strings.Join(out, ",") + "}"
}

func looksLikeJournaldJobSelector(selector string) bool {
	for _, part := range splitPromSelectorMatchers(normalizeSelectorPart(selector)) {
		key, value, ok := parsePromLabelMatcher(part)
		if !ok || key != "job" {
			continue
		}
		if strings.Contains(strings.ToLower(value), "journal") {
			return true
		}
	}
	return false
}

func isKnownLogSelectorLabel(label string) bool {
	switch strings.TrimSpace(label) {
	case "detected_level", "device_id", "filename", "identifier", "level", "ongrid_source", "service_name", "unit":
		return true
	default:
		return false
	}
}

func sanitizeLogStreamSelector(selector, fallback string) string {
	parts := splitPromSelectorMatchers(normalizeSelectorPart(selector))
	if len(parts) == 0 {
		return fallback
	}
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		key, value, op, ok := parsePromLabelMatcherWithOperator(part)
		if !ok {
			continue
		}
		key = normalizeLogSelectorLabelKey(key)
		if !isKnownLogSelectorLabel(key) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		out = append(out, formatPromLabelMatcher(key, op, value))
		seen[key] = struct{}{}
	}
	if len(out) == 0 {
		return fallback
	}
	return "{" + strings.Join(out, ",") + "}"
}

func normalizeLogSelectorLabelKey(key string) string {
	k := strings.TrimSpace(key)
	switch strings.ToLower(k) {
	case "priority", "severity":
		return "level"
	default:
		return k
	}
}

func formatPromLabelMatcher(key, op, value string) string {
	if op == "" {
		op = "="
	}
	return fmt.Sprintf("%s%s%s", strings.TrimSpace(key), op, strconv.Quote(value))
}

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

func mergeSelectorIntoPromQL(expr string, selector string) (string, bool) {
	selector = normalizeSelectorPart(selector)
	if strings.TrimSpace(expr) == "" || selector == "" {
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
			sel, end, ok := readPromSelector(expr, j)
			if ok {
				merged := mergeMetricSelectorFragments(sel, selector)
				b.WriteString(token)
				b.WriteString(merged)
				if merged != sel {
					changed = true
				}
				i = end
				continue
			}
		}
		b.WriteString(metricSelector(token, selector))
		changed = true
	}
	return b.String(), changed
}

func mergeMetricSelectorFragments(existing, add string) string {
	add = normalizeSelectorPart(add)
	if add == "" {
		if normalizeSelectorPart(existing) == "" {
			return ""
		}
		return "{" + normalizeSelectorPart(existing) + "}"
	}
	existingParts := splitPromSelectorMatchers(normalizeSelectorPart(existing))
	addParts := splitPromSelectorMatchers(add)
	addKeys := make(map[string]struct{}, len(addParts))
	for _, part := range addParts {
		if key, _, _, ok := parsePromLabelMatcherWithOperator(part); ok {
			addKeys[key] = struct{}{}
		}
	}
	merged := make([]string, 0, len(existingParts)+len(addParts))
	for _, part := range existingParts {
		if key, _, _, ok := parsePromLabelMatcherWithOperator(part); ok {
			if _, replaced := addKeys[key]; replaced {
				continue
			}
		}
		merged = append(merged, part)
	}
	merged = append(merged, addParts...)
	if len(merged) == 0 {
		return ""
	}
	return "{" + strings.Join(merged, ",") + "}"
}

func skipPromSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func promQLKeyword(token string) bool {
	switch token {
	case "and", "or", "unless", "bool", "offset",
		"sum", "avg", "min", "max", "count", "group", "stddev", "stdvar", "topk", "bottomk", "quantile", "count_values":
		return true
	default:
		return false
	}
}

func promQLLabelListModifier(token string) bool {
	switch token {
	case "by", "without", "on", "ignoring", "group_left", "group_right":
		return true
	default:
		return false
	}
}

func alertSpecSelector(spec map[string]interface{}) string {
	for _, key := range []string{"selector", "label_selector", "matchers"} {
		if raw, ok := spec[key]; ok {
			if selector := selectorFromSpecValue(raw); selector != "" {
				return selector
			}
		}
	}
	for _, key := range []string{"labels", "match_labels"} {
		if raw, ok := spec[key]; ok {
			if selector := selectorFromSpecValue(raw); selector != "" {
				return selector
			}
		}
	}
	return ""
}

func selectorFromSpecValue(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return normalizeSelectorPart(v)
	case []string:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if part := normalizeSelectorPart(item); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, ",")
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if part := normalizeSelectorPart(alertSpecStringValue(item)); part != "" {
				parts = append(parts, part)
				continue
			}
			if part := selectorFromSpecLabels(item); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, ",")
	default:
		return selectorFromSpecLabels(raw)
	}
}

func normalizeSelectorPart(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	return strings.TrimSpace(s)
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
	case "metric_threshold", "metric_anomaly", "metric_forecast":
		return "host"
	case "metric_raw", "metric_burn_rate", "log_match", "log_volume", "trace_latency", "trace_error_rate":
		return "global"
	default:
		return ""
	}
}

func rewriteSimpleHostMetricRawExpr(in RuleConfigInput) RuleConfigInput {
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
		if rewritten, changed := rewriteFriendlyHostMetricPromQL(expr); changed {
			in.Kind = "metric_raw"
			in.Spec["expr"] = rewritten
		}
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

func parseSimpleHostMetricPredicate(expr string) ([]RuleCondition, string, bool) {
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
	conds := make([]RuleCondition, 0, len(parts))
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
		conds = append(conds, RuleCondition{
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

func rewriteFriendlyHostMetricPromQL(expr string) (string, bool) {
	var b strings.Builder
	changed := false
	for i := 0; i < len(expr); {
		if isPromQuote(expr[i]) {
			end := consumePromQuotedString(expr, i)
			b.WriteString(expr[i:end])
			i = end
			continue
		}
		if !isPromIdentStart(expr[i]) {
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
		if i < len(expr) && expr[i] == '{' {
			if _, end, ok := readPromSelector(expr, i); ok {
				i = end
			}
		}
		original := expr[start:i]
		if i < len(expr) && expr[i] == '[' {
			b.WriteString(original)
			continue
		}
		selector := strings.TrimPrefix(strings.TrimSpace(original[len(token):]), "{")
		selector = strings.TrimSuffix(selector, "}")
		replacement, ok := friendlyHostMetricPromQL(token, selector)
		if !ok {
			b.WriteString(original)
			continue
		}
		b.WriteString("(")
		b.WriteString(replacement)
		b.WriteString(")")
		changed = true
	}
	return b.String(), changed
}

func friendlyHostMetricPromQL(metric string, selector string) (string, bool) {
	switch canonicalAlertMetric(metric) {
	case "cpu_pct":
		return fmt.Sprintf("100 * (1 - avg by (device_id) (rate(%s[5m])))",
			metricSelector("node_cpu_seconds_total", mergeSelector(selector, `mode="idle"`))), true
	case "mem_pct":
		return fmt.Sprintf("100 * (1 - %s / %s)",
			metricSelector("node_memory_MemAvailable_bytes", selector),
			metricSelector("node_memory_MemTotal_bytes", selector)), true
	case "disk_used_pct":
		return fmt.Sprintf("100 * (1 - %s / %s)",
			metricSelector("node_filesystem_avail_bytes", selector),
			metricSelector("node_filesystem_size_bytes", selector)), true
	case "disk_avail_bytes":
		return metricSelector("node_filesystem_avail_bytes", selector), true
	case "load1":
		return metricSelector("node_load1", selector), true
	case "load5":
		return metricSelector("node_load5", selector), true
	case "load15":
		return metricSelector("node_load15", selector), true
	case "net_rx_bps":
		return fmt.Sprintf("sum by (device_id) (rate(%s[1m]))",
			metricSelector("node_network_receive_bytes_total", selector)), true
	case "net_tx_bps":
		return fmt.Sprintf("sum by (device_id) (rate(%s[1m]))",
			metricSelector("node_network_transmit_bytes_total", selector)), true
	default:
		return "", false
	}
}

func readPromSelector(expr string, start int) (string, int, bool) {
	if start >= len(expr) || expr[start] != '{' {
		return "", start, false
	}
	depth := 0
	quote := byte(0)
	escaped := false
	for i := start; i < len(expr); i++ {
		ch := expr[i]
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
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return expr[start : i+1], i + 1, true
			}
		}
	}
	return "", start, false
}

func consumePromQuotedString(expr string, start int) int {
	quote := expr[start]
	escaped := false
	for i := start + 1; i < len(expr); i++ {
		ch := expr[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == quote {
			return i + 1
		}
	}
	return len(expr)
}

func isPromQuote(ch byte) bool {
	return ch == '"' || ch == '\'' || ch == '`'
}

func isPromIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == ':'
}

func isPromIdentPart(ch byte) bool {
	return isPromIdentStart(ch) || (ch >= '0' && ch <= '9')
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
	if strings.Contains(m, "node_cpu_seconds_total") {
		return "cpu_pct"
	}
	if strings.Contains(m, "node_filesystem_avail_bytes") ||
		strings.Contains(m, "node_filesystem_free_bytes") {
		return "disk_avail_bytes"
	}
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

func suggestedAlertRuleKey(in RuleConfigInput) string {
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

func suggestedAlertRuleKeyFromSpec(in RuleConfigInput) string {
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
	for _, token := range promTokenRE.FindAllString(expr, -1) {
		switch token {
		case "sum", "avg", "max", "min", "rate", "increase", "clamp_min", "histogram_quantile", "by", "without", "and", "or", "on":
			continue
		default:
			return token
		}
	}
	return ""
}

func specReferencesDatabaseMetric(spec map[string]interface{}) bool {
	if spec == nil {
		return false
	}
	if _, ok := firstSpecString(spec, "db_metric", "semantic_metric"); ok {
		return true
	}
	if metric, ok := firstSpecString(spec, "catalog_metric", "metric", "metric_key", "metric_name"); ok {
		if isDatabaseMetricName(metric) {
			return true
		}
		if _, ok := databaseAlertMetricExpr(metric, ""); ok {
			return true
		}
	}
	return false
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

func suggestedAlertRuleName(in RuleConfigInput) string {
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

func suggestedAlertRuleNameFromSpec(in RuleConfigInput) string {
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
