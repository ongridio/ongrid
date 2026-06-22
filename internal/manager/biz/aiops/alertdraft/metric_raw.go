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
		if selector == "" && !sourceSelectorExplicitlyScoped(in.Spec) {
			if stripped, changed := stripLeakedMetricSourceIdentityMatchersFromPromQL(expr); changed {
				expr = stripped
			}
		} else {
			if merged, changed := mergeSelectorIntoPromQL(expr, selector); changed {
				expr = merged
			}
		}
		expr = appendMetricRawComparisonFromSpec(expr, in.Spec)
		in.Spec["expr"] = expr
		return in
	}
	if metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name"); ok && isClosedSetAlertMetric(canonicalAlertMetric(metric)) {
		return normalizeSpecMetricCondition(in)
	}
	if expr, ok := buildMetricRawExprFromSpec(in.Spec); ok {
		in.Spec["expr"] = expr
		return in
	}
	return normalizeSpecMetricCondition(in)
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
	metric, ok := firstSpecString(spec, "metric", "metric_key", "metric_name")
	if !ok || !promMetricNameRE.MatchString(metric) {
		return "", false
	}
	base := metricSelector(metric, selector)
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

func appendMetricRawComparisonFromSpec(expr string, spec map[string]interface{}) string {
	expr = strings.TrimSpace(expr)
	if expr == "" || spec == nil {
		return expr
	}
	if m := promTrailingComparisonRE.FindStringSubmatch(expr); len(m) == 3 {
		return expr
	}
	operator, ok := firstSpecString(spec, "operator", "op", "comparison")
	if !ok {
		return expr
	}
	threshold, ok := firstSpecNumber(spec, "threshold", "threshold_pct", "threshold_ms", "value")
	if !ok {
		return expr
	}
	return fmt.Sprintf("(%s) %s %s", expr, normalizeAlertOperator(operator), formatAlertFloat(threshold))
}

func normalizePercentThresholdNumber(n float64) float64 {
	if n > 0 && n <= 1 {
		return n * 100
	}
	return n
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
