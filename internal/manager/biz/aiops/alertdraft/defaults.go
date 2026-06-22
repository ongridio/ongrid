package alertdraft

import (
	"fmt"
	"strings"
)

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
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name"); ok {
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
	if metric, ok := firstSpecString(spec, "metric", "metric_key", "metric_name"); ok && isDatabaseMetricName(metric) {
		return true
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
		if metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name"); ok {
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
