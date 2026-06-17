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
	switch strings.ToLower(strings.TrimSpace(rule.Kind)) {
	case "metric_raw":
		issues = append(issues, validateMetricRawDraft(rule, requestText, preview)...)
	case "log_match":
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
	return issues
}

func validateLogMatchDraft(rule aiopstools.AlertRuleConfigInput) []aiopstools.ConfigValidationIssue {
	stream := strings.ToLower(stringFromSpec(rule.Spec, "stream_selector", "selector"))
	filter := strings.ToLower(stringFromSpec(rule.Spec, "line_filter", "filter", "query"))
	if filter == "" {
		return nil
	}
	broadFilter := strings.Contains(filter, "error") || strings.Contains(filter, "panic") || strings.Contains(filter, "exception")
	narrowStream := strings.Contains(stream, "unit=") || strings.Contains(stream, "service_name=") || strings.Contains(stream, "identifier=")
	if broadFilter && !narrowStream {
		return []aiopstools.ConfigValidationIssue{{
			Severity:   validationSeverityWarning,
			Code:       "broad_log_match",
			Message:    "日志规则使用了较泛的错误关键字，但 stream_selector 没有限定 unit/service/identifier，容易把 exporter 或系统噪声当成业务故障。",
			Suggestion: "优先把 stream_selector 收窄到明确的 unit/service/identifier，或提高 threshold/增加通知抑制策略。",
		}}
	}
	return nil
}

func metricRawExpr(rule aiopstools.AlertRuleConfigInput) string {
	return stringFromSpec(rule.Spec, "expr", "promql", "query")
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
