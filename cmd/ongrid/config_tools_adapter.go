package main

import (
	"context"
	"encoding/json"
	"fmt"

	alertdraft "github.com/ongridio/ongrid/internal/manager/biz/aiops/alertdraft"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	configDraftPreviewSeriesLimit = 60
	configDraftPreviewSampleLimit = 20
)

type chatConfigAdapter struct {
	alert *managersvcalert.Service
}

func newChatConfigAdapter(alertSvc *managersvcalert.Service) *chatConfigAdapter {
	return &chatConfigAdapter{alert: alertSvc}
}

func (a *chatConfigAdapter) DraftAlertRuleConfig(ctx context.Context, caller aiopstools.ConfigCaller, in aiopstools.AlertRuleConfigArgs) (*aiopstools.ConfigDraft, error) {
	if a.alert == nil {
		return nil, errs.ErrNotWiredYet
	}
	compiled, err := alertdraft.CompileDraft(alertdraft.CompileInput{
		Action:      in.Action,
		Rule:        in.Rule,
		RequestText: in.RequestText,
	})
	if err != nil {
		return nil, err
	}
	alertCaller := toAlertServiceCaller(caller)
	ruleInput := toAlertRuleInput(compiled.Rule)
	res, err := a.alert.PreviewRule(ctx, alertCaller, ruleInput, in.LookbackSeconds)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule: %w", err)
	}
	warnings := []string(nil)
	if res != nil && res.SkippedReason != "" {
		reason := res.SkippedReason
		if alertdraft.ShouldBlockCreateOnPreviewSkip(reason) {
			return nil, fmt.Errorf("%w: preview skipped before draft: %s", errs.ErrInvalid, reason)
		}
		warnings = append(warnings, reason)
	}
	previewRes := compactAlertPreview(res)
	preview, err := configRaw(previewRes)
	if err != nil {
		return nil, err
	}
	payload, draftHash, err := aiopstools.AlertRuleConfigDraftPayload(compiled.Action, compiled.Rule)
	if err != nil {
		return nil, err
	}
	return &aiopstools.ConfigDraft{
		Kind:      aiopstools.ConfigResultKindDraft,
		Domain:    aiopstools.ConfigDomainAlertRule,
		Action:    compiled.Action,
		Summary:   compiled.Summary,
		Payload:   payload,
		Preview:   preview,
		Warnings:  warnings,
		Rollback:  "可在 Alerts 规则列表中禁用或继续编辑该规则。",
		ApplyTool: aiopstools.ToolNameApplyConfigChange,
		DraftHash: draftHash,
	}, nil
}

func compactAlertPreview(in *managersvcalert.PreviewResult) *managersvcalert.PreviewResult {
	if in == nil {
		return nil
	}
	out := *in
	out.Series = samplePreviewSeries(in.Series, configDraftPreviewSeriesLimit)
	out.Samples = samplePreviewSamples(in.Samples, configDraftPreviewSampleLimit)
	return &out
}

func samplePreviewSeries(in []managersvcalert.PreviewSeriesPoint, limit int) []managersvcalert.PreviewSeriesPoint {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= limit {
		return append([]managersvcalert.PreviewSeriesPoint(nil), in...)
	}
	if limit == 1 {
		return []managersvcalert.PreviewSeriesPoint{in[len(in)-1]}
	}
	out := make([]managersvcalert.PreviewSeriesPoint, 0, limit)
	last := len(in) - 1
	for i := 0; i < limit; i++ {
		out = append(out, in[i*last/(limit-1)])
	}
	return out
}

func samplePreviewSamples(in []managersvcalert.PreviewSample, limit int) []managersvcalert.PreviewSample {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= limit {
		return append([]managersvcalert.PreviewSample(nil), in...)
	}
	if limit == 1 {
		return []managersvcalert.PreviewSample{in[len(in)-1]}
	}
	out := make([]managersvcalert.PreviewSample, 0, limit)
	last := len(in) - 1
	for i := 0; i < limit; i++ {
		out = append(out, in[i*last/(limit-1)])
	}
	return out
}

func (a *chatConfigAdapter) ApplyAlertRuleConfig(ctx context.Context, caller aiopstools.ConfigCaller, in aiopstools.AlertRuleApplyArgs) (*aiopstools.ConfigApplyResult, error) {
	if a.alert == nil {
		return nil, errs.ErrNotWiredYet
	}
	action, err := alertdraft.NormalizeConfigAction(in.Action)
	if err != nil {
		return nil, err
	}
	alertCaller := toAlertServiceCaller(caller)
	ruleInput := toAlertRuleInput(in.Rule)
	res, err := a.alert.PreviewRule(ctx, alertCaller, ruleInput, 0)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule before create: %w", err)
	}
	if res != nil && res.SkippedReason != "" {
		reason := res.SkippedReason
		if alertdraft.ShouldBlockCreateOnPreviewSkip(reason) {
			return nil, fmt.Errorf("%w: preview skipped before create: %s", errs.ErrInvalid, reason)
		}
	}
	rule, err := a.alert.CreateRule(ctx, alertCaller, ruleInput)
	if err != nil {
		return nil, fmt.Errorf("create alert rule: %w", err)
	}
	return &aiopstools.ConfigApplyResult{
		Kind:       aiopstools.ConfigResultKindApply,
		Domain:     aiopstools.ConfigDomainAlertRule,
		Action:     action,
		Status:     "applied",
		ResourceID: rule.ID,
		Resource: &aiopstools.ConfigTarget{
			ID:   rule.ID,
			Name: rule.Name,
			Type: rule.Kind,
		},
		Message:  fmt.Sprintf("alert rule %q created", rule.Name),
		Rollback: "可在 Alerts 规则列表中禁用或继续编辑该规则。",
	}, nil
}

func toAlertRuleInput(in aiopstools.AlertRuleConfigInput) managersvcalert.RuleInput {
	in = alertdraft.NormalizeRuleConfigInput(in)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	conds := make([]managersvcalert.RuleCondition, 0, len(in.Conditions))
	for _, c := range in.Conditions {
		conds = append(conds, managersvcalert.RuleCondition{
			Metric:     c.Metric,
			Operator:   c.Operator,
			Threshold:  c.Threshold,
			Window:     c.Window,
			For:        c.For,
			Aggregator: c.Aggregator,
		})
	}
	return managersvcalert.RuleInput{
		RuleKey:             in.RuleKey,
		Kind:                in.Kind,
		Name:                in.Name,
		ScopeType:           in.ScopeType,
		JoinMode:            in.JoinMode,
		Severity:            in.Severity,
		Enabled:             enabled,
		Conditions:          conds,
		Spec:                in.Spec,
		Labels:              in.Labels,
		RunbookURL:          in.RunbookURL,
		NotifyChannelIDs:    in.NotifyChannelIDs,
		NotifyWindowSeconds: in.NotifyWindowSeconds,
		NotifyMinFires:      in.NotifyMinFires,
	}
}

func toAlertServiceCaller(c aiopstools.ConfigCaller) managersvcalert.Caller {
	return managersvcalert.Caller{UserID: c.UserID, Role: c.Role}
}

func configRaw(v interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal config payload: %w", err)
	}
	return b, nil
}
