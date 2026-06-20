package alertdraft

import (
	"fmt"
	"regexp"
	"strings"
)

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
