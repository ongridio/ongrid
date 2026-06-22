package aiopsconfig

import (
	"context"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/alertconfig"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
)

type alertRuleService interface {
	PreviewRule(ctx context.Context, caller managersvcalert.Caller, in managersvcalert.RuleInput, lookbackSeconds int) (*managersvcalert.PreviewResult, error)
	CreateRule(ctx context.Context, caller managersvcalert.Caller, in managersvcalert.RuleInput) (*managersvcalert.Rule, error)
}

func NewAlertRuleManager(alertSvc alertRuleService) aiopstools.ConfigManager {
	if alertSvc == nil {
		return alertconfig.NewAlertRuleManager(nil)
	}
	return alertconfig.NewAlertRuleManager(alertRulePort{alert: alertSvc})
}

type alertRulePort struct {
	alert alertRuleService
}

func (p alertRulePort) PreviewRule(ctx context.Context, caller aiopstools.ConfigCaller, in alertconfig.RuleInput, lookbackSeconds int) (*alertconfig.PreviewResult, error) {
	res, err := p.alert.PreviewRule(ctx, toAlertServiceCaller(caller), toAlertServiceRuleInput(in), lookbackSeconds)
	if err != nil {
		return nil, err
	}
	return fromAlertServicePreview(res), nil
}

func (p alertRulePort) CreateRule(ctx context.Context, caller aiopstools.ConfigCaller, in alertconfig.RuleInput) (*alertconfig.Rule, error) {
	rule, err := p.alert.CreateRule(ctx, toAlertServiceCaller(caller), toAlertServiceRuleInput(in))
	if err != nil {
		return nil, err
	}
	return &alertconfig.Rule{
		ID:   rule.ID,
		Kind: rule.Kind,
		Name: rule.Name,
	}, nil
}

func toAlertServiceCaller(c aiopstools.ConfigCaller) managersvcalert.Caller {
	return managersvcalert.Caller{UserID: c.UserID, Role: c.Role}
}

func toAlertServiceRuleInput(in alertconfig.RuleInput) managersvcalert.RuleInput {
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
		Enabled:             in.Enabled,
		Conditions:          conds,
		Spec:                in.Spec,
		Labels:              in.Labels,
		RunbookURL:          in.RunbookURL,
		NotifyChannelIDs:    in.NotifyChannelIDs,
		NotifyWindowSeconds: in.NotifyWindowSeconds,
		NotifyMinFires:      in.NotifyMinFires,
	}
}

func fromAlertServicePreview(in *managersvcalert.PreviewResult) *alertconfig.PreviewResult {
	if in == nil {
		return nil
	}
	out := &alertconfig.PreviewResult{
		FireCount:     in.FireCount,
		FirstFireAt:   in.FirstFireAt,
		LastFireAt:    in.LastFireAt,
		Threshold:     in.Threshold,
		Unit:          in.Unit,
		SkippedReason: in.SkippedReason,
	}
	out.Samples = make([]alertconfig.PreviewSample, 0, len(in.Samples))
	for _, sample := range in.Samples {
		out.Samples = append(out.Samples, alertconfig.PreviewSample{
			Timestamp: sample.Timestamp,
			Labels:    sample.Labels,
			Value:     sample.Value,
			Summary:   sample.Summary,
		})
	}
	out.Series = make([]alertconfig.PreviewSeriesPoint, 0, len(in.Series))
	for _, point := range in.Series {
		out.Series = append(out.Series, alertconfig.PreviewSeriesPoint{
			Timestamp: point.Timestamp,
			Value:     point.Value,
		})
	}
	return out
}
