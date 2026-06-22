package alertdraft

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

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
