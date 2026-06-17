package alertdraft

import (
	"fmt"
	"strings"
)

const (
	defaultJournaldLogSelector = `{ongrid_source=~"journald(:.*)?"}`
	defaultAllLogsSelector     = `{ongrid_source=~".+"}`

	DefaultJournaldLogSelector = defaultJournaldLogSelector
)

type RuleConfigInput struct {
	RuleKey             string                 `json:"rule_key,omitempty"`
	Kind                string                 `json:"kind,omitempty"`
	Name                string                 `json:"name,omitempty"`
	ScopeType           string                 `json:"scope_type,omitempty"`
	JoinMode            string                 `json:"join_mode,omitempty"`
	Window              string                 `json:"window,omitempty"`
	For                 string                 `json:"for,omitempty"`
	Severity            string                 `json:"severity,omitempty"`
	Enabled             *bool                  `json:"enabled,omitempty"`
	Conditions          []RuleCondition        `json:"conditions,omitempty"`
	Spec                map[string]interface{} `json:"spec,omitempty"`
	Labels              map[string]string      `json:"labels,omitempty"`
	RunbookURL          string                 `json:"runbook_url,omitempty"`
	NotifyChannelIDs    []uint64               `json:"notify_channel_ids,omitempty"`
	NotifyWindowSeconds int                    `json:"notify_window_seconds,omitempty"`
	NotifyMinFires      int                    `json:"notify_min_fires,omitempty"`
}

type RuleCondition struct {
	Metric     string  `json:"metric"`
	Operator   string  `json:"operator"`
	Threshold  float64 `json:"threshold"`
	Window     string  `json:"window,omitempty"`
	For        string  `json:"for,omitempty"`
	Aggregator string  `json:"aggregator,omitempty"`
}

type CompileInput struct {
	Action      string
	Rule        RuleConfigInput
	RequestText string
}

type CompiledDraft struct {
	Action  string
	Rule    RuleConfigInput
	Summary string
}

func CompileDraft(in CompileInput) (CompiledDraft, error) {
	action, err := NormalizeConfigAction(in.Action)
	if err != nil {
		return CompiledDraft{}, err
	}
	rule := NormalizeRuleConfigInputForRequest(in.Rule, in.RequestText)
	return CompiledDraft{
		Action:  action,
		Rule:    rule,
		Summary: Summary(action, rule),
	}, nil
}

func NormalizeRuleConfigInput(in RuleConfigInput) RuleConfigInput {
	return normalizeAlertRuleConfigInput(in)
}

func NormalizeRuleConfigInputForRequest(in RuleConfigInput, requestText string) RuleConfigInput {
	return normalizeAlertRuleConfigInputForRequest(in, requestText)
}

func ShouldBlockCreateOnPreviewSkip(reason string) bool {
	return shouldBlockAlertRuleCreateOnPreviewSkip(reason)
}

func NormalizeConfigAction(action string) (string, error) {
	return normalizeConfigAction(action)
}

func Summary(action string, in RuleConfigInput) string {
	name := firstNonEmpty(strings.TrimSpace(in.Name), strings.TrimSpace(in.RuleKey), "new alert rule")
	return fmt.Sprintf("%s alert rule %q", action, name)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
