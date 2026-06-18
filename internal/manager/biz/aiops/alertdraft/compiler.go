package alertdraft

import (
	"fmt"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

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
	in = normalizeAlertScopeForRequest(in, requestText)
	return applyAlertRuleRequestHints(in, requestText)
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
