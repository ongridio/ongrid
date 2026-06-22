package alertdraft

import "strings"

func normalizeAlertScopeForRequest(in RuleConfigInput, requestText string) RuleConfigInput {
	if HostScopeRecommended(in, requestText) {
		in.ScopeType = "host"
	}
	return in
}

func HostScopeRecommended(in RuleConfigInput, requestText string) bool {
	if !kindCanUseHostScope(in.Kind) {
		return false
	}
	if strings.TrimSpace(in.ScopeType) == "monitoring_pipeline" {
		return false
	}
	if explicitGlobalScopeRequested(requestText) && !explicitHostScopeRequested(requestText) {
		return false
	}
	if ruleUsesClosedSetHostMetric(in) {
		return true
	}
	if databaseRuleLooksHostScoped(in, requestText) {
		return true
	}
	if logRuleLooksHostScoped(in, requestText) {
		return true
	}
	if metricRawLooksHostScoped(in, requestText) {
		return true
	}
	return false
}

func kindCanUseHostScope(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "metric_threshold", "metric_raw", "metric_anomaly", "metric_forecast", "log_match", "log_volume":
		return true
	default:
		return false
	}
}

func ruleUsesClosedSetHostMetric(in RuleConfigInput) bool {
	if len(in.Conditions) > 0 {
		return true
	}
	if metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name"); ok {
		return isClosedSetAlertMetric(canonicalAlertMetric(metric))
	}
	return false
}

func databaseRuleLooksHostScoped(in RuleConfigInput, requestText string) bool {
	if !requestLooksDatabaseScoped(requestText) {
		return false
	}
	if metric, ok := firstSpecString(in.Spec, "metric", "metric_key", "metric_name"); ok {
		return isDatabaseMetricName(metric)
	}
	if expr, ok := firstSpecString(in.Spec, "expr", "promql", "query"); ok {
		return metricRawExprContainsDatabaseMetric(expr)
	}
	return false
}

func metricRawLooksHostScoped(in RuleConfigInput, requestText string) bool {
	if strings.TrimSpace(in.Kind) != "metric_raw" {
		return false
	}
	expr, ok := firstSpecString(in.Spec, "expr", "promql", "query")
	if !ok {
		return false
	}
	exprLower := strings.ToLower(expr)
	if strings.Contains(exprLower, "device_id") {
		return true
	}
	if containsAny(exprLower, []string{"node_", "process_", "disk_", "filesystem_", "host_"}) &&
		requestLooksHostResourceScoped(requestText) {
		return true
	}
	return false
}

func logRuleLooksHostScoped(in RuleConfigInput, requestText string) bool {
	switch strings.TrimSpace(in.Kind) {
	case "log_match", "log_volume":
	default:
		return false
	}
	stream := strings.ToLower(alertSpecStringValue(in.Spec["stream_selector"]))
	if strings.Contains(stream, "device_id") {
		return true
	}
	if strings.Contains(stream, "journald") && requestLooksHostResourceScoped(requestText) {
		return true
	}
	return false
}

func explicitHostScopeRequested(text string) bool {
	return containsAny(strings.ToLower(text), []string{
		"主机", "机器", "节点", "服务器", "每台", "各台", "单台", "某台", "按主机", "按机器", "按节点",
		"host", "node", "server", "machine", "device",
	})
}

func explicitGlobalScopeRequested(text string) bool {
	return containsAny(strings.ToLower(text), []string{
		"全局", "整体", "集群", "汇总", "总量", "总数", "服务级", "系统整体",
		"global", "overall", "aggregate", "aggregated", "cluster", "fleet", "service-level",
	})
}

func requestLooksHostResourceScoped(text string) bool {
	return explicitHostScopeRequested(text) || containsAny(strings.ToLower(text), []string{
		"cpu", "内存", "memory", "mem", "磁盘", "硬盘", "分区", "根分区", "文件系统",
		"disk", "filesystem", "mountpoint", "load", "负载", "swap", "网络", "网卡",
		"network", "interface", "系统日志", "journald", "syslog",
	})
}

func requestLooksDatabaseScoped(text string) bool {
	return containsAny(strings.ToLower(text), []string{
		"mysql", "postgres", "postgresql", "redis", "mongodb", "mongo", "数据库", "慢查询", "连接数", "sql",
	})
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
