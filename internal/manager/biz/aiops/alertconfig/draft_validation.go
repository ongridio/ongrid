package alertconfig

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	alertdraft "github.com/ongridio/ongrid/internal/manager/biz/aiops/alertdraft"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
)

const (
	validationSeverityError   = "error"
	validationSeverityWarning = "warning"
)

var metricRawTrailingComparisonRE = regexp.MustCompile(`^(.+?)\s*(==|!=|>=?|<=?)\s*([+-]?[0-9.]+(?:[eE][+-]?\d+)?)\s*$`)

func validateAlertRuleDraft(rule aiopstools.AlertRuleConfigInput, requestText string, preview *PreviewResult) aiopstools.ConfigValidationResult {
	var issues []aiopstools.ConfigValidationIssue
	if preview != nil && strings.TrimSpace(preview.SkippedReason) != "" {
		severity := validationSeverityWarning
		if alertdraft.ShouldBlockCreateOnPreviewSkip(preview.SkippedReason) {
			severity = validationSeverityError
		}
		issues = append(issues, aiopstools.ConfigValidationIssue{
			Severity:   severity,
			Code:       "preview_skipped",
			Message:    "告警规则预览未完成：" + preview.SkippedReason,
			Suggestion: "根据 skipped_reason 补全缺失字段、修正 selector/metric，或在指标目录中选择实际存在的信号后重新生成草稿。",
		})
	}
	issues = append(issues, validatePreviewContract(rule, preview)...)
	issues = append(issues, validateScopeConsistency(rule, requestText, preview)...)
	switch strings.ToLower(strings.TrimSpace(rule.Kind)) {
	case "metric_raw":
		issues = append(issues, validateMetricRawDraft(rule, requestText, preview)...)
	case "log_match", "log_volume":
		issues = append(issues, validateLogMatchDraft(rule)...)
	}
	status := "passed"
	if len(issues) > 0 {
		status = "warning"
	}
	for _, issue := range issues {
		if issue.Severity == validationSeverityError {
			status = "failed"
			break
		}
	}
	return aiopstools.ConfigValidationResult{Status: status, Issues: issues}
}

func validateMetricRawDraft(rule aiopstools.AlertRuleConfigInput, requestText string, preview *PreviewResult) []aiopstools.ConfigValidationIssue {
	expr := metricRawExpr(rule)
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	var issues []aiopstools.ConfigValidationIssue
	if !hasMetricRawComparisonPredicate(expr) {
		issues = append(issues, aiopstools.ConfigValidationIssue{
			Severity:   validationSeverityError,
			Code:       "metric_raw_predicate_missing",
			Message:    "metric_raw 的 PromQL 必须是布尔告警谓词；裸数值序列会在返回任意样本时被规则引擎当成触发。",
			Suggestion: "为表达式补上明确比较条件，例如 `rate(x[5m]) > 0.5`，或把持续性写进 PromQL 后再比较阈值。",
		})
		return issues
	}
	if lhs, threshold, ok := splitMetricRawComparison(expr); ok {
		if preview != nil && strings.TrimSpace(preview.SkippedReason) == "" && noPreviewSignal(preview) && looksLikeArithmetic(lhs) {
			issues = append(issues, aiopstools.ConfigValidationIssue{
				Severity:   validationSeverityError,
				Code:       "metric_raw_no_preview_series",
				Message:    "PromQL 通过语法检查，但预览窗口内没有任何可评估序列；对带算术/比值的表达式，这通常表示 metric label 不匹配、分子缺失或 selector 过窄。",
				Suggestion: "重新用 list_metric_catalog 返回的 sample_labels 校对表达式。多指标计算请先按同一维度聚合，例如 sum/max by (device_id, ongrid_source)，再用 on(device_id, ongrid_source) group_left 连接；分子可能为空时用 or/vector 或先加存在性条件。",
			})
		}
		if issue, ok := suspiciousMagnitudeIssue(preview, threshold); ok {
			issues = append(issues, issue)
		}
	}
	if mentionsSustained(requestText, rule.Name) && !hasSustainedPromQL(expr) {
		issues = append(issues, aiopstools.ConfigValidationIssue{
			Severity:   validationSeverityWarning,
			Code:       "sustained_window_missing",
			Message:    "用户描述包含“持续/连续”语义，但 metric_raw 表达式主要是即时谓词；当前引擎会在 PromQL 返回序列时立即触发。",
			Suggestion: "把持续性写进 PromQL，例如使用 min_over_time/avg_over_time/count_over_time 的子查询，或让表达式本身只在整个窗口都满足时返回序列。",
		})
	}
	if preview != nil && preview.FireCount > 100 {
		issues = append(issues, aiopstools.ConfigValidationIssue{
			Severity:   validationSeverityWarning,
			Code:       "high_preview_fire_count",
			Message:    fmt.Sprintf("预览窗口内命中 %d 次，创建后可能立即产生大量告警。", preview.FireCount),
			Suggestion: "检查阈值、聚合维度和持续窗口；如果这是预期行为，可以保留，否则提高阈值或增加更窄 selector。",
		})
	}
	if issue, ok := sparseCounterMinRateIssue(expr); ok {
		issues = append(issues, issue)
	}
	return issues
}

func validateLogMatchDraft(rule aiopstools.AlertRuleConfigInput) []aiopstools.ConfigValidationIssue {
	stream := strings.ToLower(stringFromSpec(rule.Spec, "stream_selector", "selector"))
	filter := strings.ToLower(stringFromSpec(rule.Spec, "line_filter", "filter"))
	query := strings.TrimSpace(stringFromSpec(rule.Spec, "query"))
	var issues []aiopstools.ConfigValidationIssue
	if query != "" && filter == "" {
		issues = append(issues, aiopstools.ConfigValidationIssue{
			Severity:   validationSeverityError,
			Code:       "log_query_not_normalized",
			Message:    "日志规则包含 query，但当前 evaluator 只执行 stream_selector + line_filter；直接保存 query 会被忽略。",
			Suggestion: "把完整 LogQL 拆成 stream_selector 和 line_filter，或重新生成草稿让 draft_config_change 完成规范化。",
		})
	}
	broadFilter := strings.Contains(filter, "error") || strings.Contains(filter, "panic") || strings.Contains(filter, "exception")
	narrowStream := strings.Contains(stream, "unit=") || strings.Contains(stream, "service_name=") || strings.Contains(stream, "identifier=")
	if broadFilter && !narrowStream {
		issues = append(issues, aiopstools.ConfigValidationIssue{
			Severity:   validationSeverityWarning,
			Code:       "broad_log_match",
			Message:    "日志规则使用了较泛的错误关键字，但 stream_selector 没有限定 unit/service/identifier，容易把 exporter 或系统噪声当成业务故障。",
			Suggestion: "优先把 stream_selector 收窄到明确的 unit/service/identifier，或提高 threshold/增加通知抑制策略。",
		})
	}
	return issues
}

func validatePreviewContract(rule aiopstools.AlertRuleConfigInput, preview *PreviewResult) []aiopstools.ConfigValidationIssue {
	if preview == nil || preview.FireCount == 0 || strings.TrimSpace(preview.SkippedReason) != "" {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(rule.ScopeType)) != "host" {
		return nil
	}
	if len(preview.Samples) == 0 {
		return nil
	}
	for _, sample := range preview.Samples {
		if sample.Labels != nil && strings.TrimSpace(sample.Labels["device_id"]) != "" {
			return nil
		}
	}
	return []aiopstools.ConfigValidationIssue{{
		Severity:   validationSeverityError,
		Code:       "host_preview_missing_device_id",
		Message:    "host 作用域规则的预览结果没有 device_id label；创建后命中也无法写入主机告警事件。",
		Suggestion: "让 PromQL/规则表达式按 device_id 保留结果标签，或将非主机维度规则改为 global 作用域。",
	}}
}

func validateScopeConsistency(rule aiopstools.AlertRuleConfigInput, requestText string, preview *PreviewResult) []aiopstools.ConfigValidationIssue {
	if strings.ToLower(strings.TrimSpace(rule.ScopeType)) != "global" {
		return nil
	}
	if !alertdraft.HostScopeRecommended(rule, requestText) {
		return nil
	}
	if !previewHasDeviceIDLabel(preview) {
		return nil
	}
	return []aiopstools.ConfigValidationIssue{{
		Severity:   validationSeverityWarning,
		Code:       "host_scope_recommended",
		Message:    "这条规则看起来是按主机维度命中的，但当前 scope_type=global；创建后告警能触发，不过不会作为设备告警关联到主机。",
		Suggestion: "将 scope_type 改为 host，并确保预览结果保留 device_id label。",
	}}
}

func metricRawExpr(rule aiopstools.AlertRuleConfigInput) string {
	return stringFromSpec(rule.Spec, "expr", "promql", "query")
}

func previewHasDeviceIDLabel(preview *PreviewResult) bool {
	if preview == nil || len(preview.Samples) == 0 {
		return false
	}
	for _, sample := range preview.Samples {
		if sample.Labels != nil && strings.TrimSpace(sample.Labels["device_id"]) != "" {
			return true
		}
	}
	return false
}

func stringFromSpec(spec map[string]interface{}, keys ...string) string {
	if len(spec) == 0 {
		return ""
	}
	for _, key := range keys {
		if v, ok := spec[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func splitMetricRawComparison(expr string) (string, float64, bool) {
	m := metricRawTrailingComparisonRE.FindStringSubmatch(strings.TrimSpace(expr))
	if len(m) != 4 {
		return "", 0, false
	}
	var threshold float64
	if _, err := fmt.Sscanf(m[3], "%f", &threshold); err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(m[1]), threshold, true
}

func hasMetricRawComparisonPredicate(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	var quote byte
	selectorDepth := 0
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(expr) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
			continue
		case '{':
			selectorDepth++
			continue
		case '}':
			if selectorDepth > 0 {
				selectorDepth--
			}
			continue
		}
		if selectorDepth > 0 {
			continue
		}
		switch c {
		case '>', '<':
			return true
		case '=', '!':
			if i+1 < len(expr) && expr[i+1] == '=' {
				return true
			}
		}
	}
	return false
}

func noPreviewSignal(preview *PreviewResult) bool {
	return preview == nil || (preview.FireCount == 0 && len(preview.Series) == 0 && len(preview.Samples) == 0)
}

func looksLikeArithmetic(expr string) bool {
	return strings.Contains(expr, "/") || strings.Contains(expr, "*") || strings.Contains(expr, "+") || strings.Contains(expr, "-")
}

func suspiciousMagnitudeIssue(preview *PreviewResult, threshold float64) (aiopstools.ConfigValidationIssue, bool) {
	if preview == nil || len(preview.Samples) == 0 || math.Abs(threshold) > 1000 {
		return aiopstools.ConfigValidationIssue{}, false
	}
	limit := math.Max(10000, math.Abs(threshold)*1000)
	for _, sample := range preview.Samples {
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			return aiopstools.ConfigValidationIssue{
				Severity:   validationSeverityError,
				Code:       "metric_raw_non_finite_value",
				Message:    "PromQL 预览产生了 NaN/Inf，通常是分母为 0 或缺失数据造成的。",
				Suggestion: "为除法分母增加非零保护，或先用存在性条件过滤分母为 0 的序列后再计算比例。",
			}, true
		}
		if math.Abs(sample.Value) > limit {
			return aiopstools.ConfigValidationIssue{
				Severity:   validationSeverityError,
				Code:       "metric_raw_suspicious_magnitude",
				Message:    fmt.Sprintf("PromQL 预览值 %.4g 远高于阈值 %.4g，量纲或分母很可能不正确。", sample.Value, threshold),
				Suggestion: "检查百分比/比值表达式的分母是否可能为 0，避免用 clamp_min(..., 1) 掩盖未配置容量；优先加分母存在性条件，或按相同标签维度聚合后再相除。",
			}, true
		}
	}
	return aiopstools.ConfigValidationIssue{}, false
}

func sparseCounterMinRateIssue(expr string) (aiopstools.ConfigValidationIssue, bool) {
	lower := strings.ToLower(strings.ReplaceAll(expr, " ", ""))
	if !strings.Contains(lower, "min_over_time(rate(") || !strings.Contains(lower, ")>0") {
		return aiopstools.ConfigValidationIssue{}, false
	}
	if !(strings.Contains(lower, "_total") || strings.Contains(lower, "deadlock") || strings.Contains(lower, "error") || strings.Contains(lower, "fail")) {
		return aiopstools.ConfigValidationIssue{}, false
	}
	return aiopstools.ConfigValidationIssue{
		Severity:   validationSeverityWarning,
		Code:       "sparse_counter_min_rate",
		Message:    "表达式对稀疏事件计数器使用 min_over_time(rate(...)) > 0，可能要求整个窗口持续增长，容易漏掉单次事件。",
		Suggestion: "如果目标是检测窗口内发生过事件，优先使用 increase(counter[窗口]) > 0；只有确实要求持续增长时才保留当前写法。",
	}, true
}

func mentionsSustained(vals ...string) bool {
	for _, v := range vals {
		v = strings.ToLower(v)
		if strings.Contains(v, "持续") || strings.Contains(v, "连续") || strings.Contains(v, "for ") ||
			strings.Contains(v, "sustained") || strings.Contains(v, "for_") {
			return true
		}
	}
	return false
}

func hasSustainedPromQL(expr string) bool {
	lower := strings.ToLower(expr)
	return strings.Contains(lower, "_over_time") ||
		strings.Contains(lower, "count_over_time") ||
		strings.Contains(lower, "[") && strings.Contains(lower, ":")
}

func validationHasErrors(v aiopstools.ConfigValidationResult) bool {
	for _, issue := range v.Issues {
		if issue.Severity == validationSeverityError {
			return true
		}
	}
	return false
}

func validationWarnings(v aiopstools.ConfigValidationResult) []string {
	out := make([]string, 0, len(v.Issues))
	for _, issue := range v.Issues {
		if issue.Severity == validationSeverityWarning {
			if issue.Suggestion != "" {
				out = append(out, issue.Message+" 建议："+issue.Suggestion)
			} else {
				out = append(out, issue.Message)
			}
		}
	}
	return out
}

func validationErrorMessage(v aiopstools.ConfigValidationResult) string {
	out := make([]string, 0, len(v.Issues))
	for _, issue := range v.Issues {
		if issue.Severity != validationSeverityError {
			continue
		}
		msg := strings.TrimSpace(issue.Message)
		if issue.Suggestion != "" {
			msg += " 建议：" + issue.Suggestion
		}
		if issue.Code != "" {
			msg = issue.Code + ": " + msg
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return "unknown validation error"
	}
	return strings.Join(out, "; ")
}
