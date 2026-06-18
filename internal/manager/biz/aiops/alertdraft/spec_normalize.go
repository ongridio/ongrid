package alertdraft

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

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
	case "promql", "prom_query", "raw_promql", "raw_metric", "custom_metric":
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
	case hasAnySpecKey(spec, "expr", "promql", "query", "metric_key", "metric", "metric_name"):
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
		normalizeLogQuerySpec(in.Spec)
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
		normalizeLogQuerySpec(in.Spec)
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

func normalizeLogQuerySpec(spec map[string]interface{}) {
	if spec == nil {
		return
	}
	query := alertSpecStringValue(spec["query"])
	if strings.TrimSpace(query) == "" {
		return
	}
	stream, filter, ok := splitSimpleLogQLQuery(query)
	if !ok {
		return
	}
	if strings.TrimSpace(alertSpecStringValue(spec["stream_selector"])) == "" {
		spec["stream_selector"] = stream
	}
	if strings.TrimSpace(alertSpecStringValue(spec["line_filter"])) == "" {
		spec["line_filter"] = filter
	}
	delete(spec, "query")
}

func splitSimpleLogQLQuery(query string) (string, string, bool) {
	query = strings.TrimSpace(query)
	if query == "" || !strings.HasPrefix(query, "{") {
		return "", "", false
	}
	stream, end, ok := readPromSelector(query, 0)
	if !ok {
		return "", "", false
	}
	rest := strings.TrimSpace(query[end:])
	if rest == "" {
		return stream, "", true
	}
	if len(parseLogLineFilterChain(rest)) == 0 {
		return "", "", false
	}
	filter, selector := normalizeLogLineFilter(rest)
	if selector != "" {
		stream = mergeLogStreamSelector(stream, selector)
	}
	return stream, filter, true
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
	if op == "|=~" {
		op = "|~"
	}
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
		for _, candidate := range []string{"|=~", "|=", "|~", "!=", "!~"} {
			if strings.HasPrefix(rest, candidate) {
				op = candidate
				rest = strings.TrimSpace(rest[len(candidate):])
				break
			}
		}
		if op == "|=~" {
			op = "|~"
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
		for _, op := range []string{"|=~", "|=", "|~", "!=", "!~"} {
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
