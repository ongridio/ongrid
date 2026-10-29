package externalmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

type ruleInput struct {
	RuleKey    string                   `json:"rule_key,omitempty"`
	Kind       string                   `json:"kind"`
	Name       string                   `json:"name"`
	ScopeType  string                   `json:"scope_type"`
	JoinMode   string                   `json:"join_mode,omitempty"`
	Severity   string                   `json:"severity"`
	Enabled    bool                     `json:"enabled"`
	Conditions []alertsvc.RuleCondition `json:"conditions,omitempty"`
	// Spec is validated against the selected kind before reaching the service.
	Spec                map[string]any    `json:"spec,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"`
	RunbookURL          string            `json:"runbook_url,omitempty"`
	NotifyChannelIDs    []uint64          `json:"notify_channel_ids,omitempty"`
	NotifyWindowSeconds int               `json:"notify_window_seconds,omitempty"`
	NotifyMinFires      int               `json:"notify_min_fires,omitempty"`
}

func (in ruleInput) service() alertsvc.RuleInput {
	return alertsvc.RuleInput{RuleKey: in.RuleKey, Kind: in.Kind, Name: in.Name, ScopeType: in.ScopeType, JoinMode: in.JoinMode, Severity: in.Severity, Enabled: in.Enabled, Conditions: in.Conditions, Spec: in.Spec, Labels: in.Labels, RunbookURL: in.RunbookURL, NotifyChannelIDs: in.NotifyChannelIDs, NotifyWindowSeconds: in.NotifyWindowSeconds, NotifyMinFires: in.NotifyMinFires}
}

func (r Resources) ruleTool() biz.Tool {
	schema, err := jsonschema.For[ruleInput](nil)
	if err != nil {
		panic(err)
	}
	return biz.Tool{Name: "manage_alert_rule", Description: "Create, replace, enable/disable or delete one rule. Supports metric_threshold/raw/anomaly/forecast/burn_rate and log_search. Dry-run validates and previews the exact change. New rules execute directly. Existing-rule changes require confirmation at approval_url. Updates replace the full editable rule payload; read it first.", Scopes: []string{"alert-rules:read"}, ActionScopes: map[string][]string{"create": {"alert-rules:write"}, "update": {"alert-rules:write"}, "set_enabled": {"alert-rules:write"}, "delete": {"alert-rules:delete"}}, Mutation: true, Schema: writeSchema(map[string]any{"action": enumSchema("create", "update", "set_enabled", "delete"), "rule_id": idSchema(), "rule": schema, "enabled": map[string]any{"type": "boolean"}}, "action"), Validate: r.validateRule, Policy: func(a biz.Arguments) (biz.Policy, error) {
		return biz.Policy{Target: target("alert_rule", uint64(biz.Number(a, "rule_id"))), LowRisk: biz.String(a, "action") == "create"}, nil
	}, Run: r.manageRule}
}

func (r Resources) validateRule(a biz.Arguments) error {
	action := biz.String(a, "action")
	if action != "create" {
		if biz.Number(a, "rule_id") < 1 {
			return errs.ErrInvalid
		}
		if err := requireRevision(a); err != nil {
			return err
		}
	} else {
		if _, ok := a["rule_id"]; ok {
			return errs.ErrInvalid
		}
	}
	if action == "delete" {
		if err := only(a, "action", "rule_id"); err != nil {
			return err
		}
		return requireReason(a)
	}
	if action == "set_enabled" {
		if err := only(a, "action", "rule_id", "enabled"); err != nil {
			return err
		}
		if _, ok := a["enabled"].(bool); !ok {
			return errs.ErrInvalid
		}
		if !biz.Bool(a, "enabled") {
			return requireReason(a)
		}
		return nil
	}
	if err := only(a, "action", "rule_id", "rule"); err != nil {
		return err
	}
	var in ruleInput
	b, err := json.Marshal(a["rule"])
	if err != nil {
		return errs.ErrInvalid
	}
	if json.Unmarshal(b, &in) != nil {
		return errs.ErrInvalid
	}
	if len(in.Name) == 0 || len(in.Name) > 128 || len(in.RuleKey) > 128 || len(in.Conditions) > 16 || len(in.Labels) > 20 || len(in.RunbookURL) > 512 || len(in.NotifyChannelIDs) > 20 {
		return errs.ErrInvalid
	}
	if !slices.Contains([]string{"info", "warning", "critical"}, in.Severity) {
		return errs.ErrInvalid
	}
	if !in.Enabled {
		if err := requireReason(a); err != nil {
			return err
		}
	}
	var fields []string
	switch in.Kind {
	case "metric_threshold":
		if len(in.Spec) != 0 {
			return errs.ErrInvalid
		}
	case "metric_raw":
		fields = []string{"expr"}
	case "metric_anomaly":
		fields = []string{"metric", "selector", "method", "baseline_window", "baseline_step", "deviation", "for_seconds"}
	case "metric_forecast":
		fields = []string{"metric", "selector", "fit_window", "predict_seconds", "operator", "threshold", "for_seconds"}
	case "metric_burn_rate":
		fields = []string{"sli", "slo", "burns"}
	case "log_search":
		fields = []string{"keywords", "scope", "filters", "group_by", "window", "operator", "threshold"}
	default:
		return errs.ErrInvalid
	}
	for key, value := range in.Spec {
		if !slices.Contains(fields, key) {
			return errs.ErrInvalid
		}
		if s, ok := value.(string); ok && len(s) > 8192 {
			return errs.ErrInvalid
		}
	}
	// Nested product query objects must not silently discard unknown fields.
	if in.Kind == "log_search" {
		var spec struct {
			Keywords  logquery.Keywords      `json:"keywords"`
			Scope     logquery.Scope         `json:"scope"`
			Filters   []logquery.FieldFilter `json:"filters"`
			GroupBy   []string               `json:"group_by"`
			Window    string                 `json:"window"`
			Operator  string                 `json:"operator"`
			Threshold *float64               `json:"threshold"`
		}
		if err := decodeStrict(in.Spec, &spec); err != nil {
			return errs.ErrInvalid
		}
	}
	if in.Kind == "metric_burn_rate" {
		var burns []struct {
			Window     string  `json:"window"`
			Multiplier float64 `json:"multiplier"`
		}
		if err := decodeStrict(in.Spec["burns"], &burns); err != nil || len(burns) == 0 || len(burns) > 8 {
			return errs.ErrInvalid
		}
	}
	if in.Kind != "metric_threshold" && len(in.Conditions) > 0 {
		return errs.ErrInvalid
	}
	for key, value := range in.Labels {
		if len(key) > 128 || len(value) > 512 || strings.TrimSpace(key) == "" {
			return errs.ErrInvalid
		}
	}
	return r.Alerts.ValidateRule(in.service(), action == "create")
}

func decodeStrict(value, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func (r Resources) manageRule(ctx context.Context, a biz.Arguments) (any, error) {
	action := biz.String(a, "action")
	id := uint64(biz.Number(a, "rule_id"))
	c := caller(ctx)
	var before *alertsvc.Rule
	if action != "create" {
		var err error
		before, err = r.Alerts.GetRule(ctx, c, id)
		if err != nil {
			return nil, err
		}
		if err := checkRevision(a, before); err != nil {
			return nil, err
		}
	}
	if biz.Bool(a, "dry_run") {
		return map[string]any{"before": before, "changes": a, "validation": "passed", "applied": false}, nil
	}
	if action == "delete" {
		if err := r.Alerts.DeleteRule(ctx, c, id); err != nil {
			return nil, err
		}
		return map[string]any{"rule_id": id, "deleted": true}, nil
	}
	var result *alertsvc.Rule
	var err error
	if action == "set_enabled" {
		result, err = r.Alerts.SetRuleEnabled(ctx, c, id, biz.Bool(a, "enabled"))
	} else {
		var in ruleInput
		b, marshalErr := json.Marshal(a["rule"])
		if marshalErr != nil {
			return nil, marshalErr
		}
		if err := json.Unmarshal(b, &in); err != nil {
			return nil, err
		}
		if action == "create" {
			result, err = r.Alerts.CreateRule(ctx, c, in.service())
		} else {
			result, err = r.Alerts.UpdateRule(ctx, c, id, in.service())
		}
	}
	if err != nil {
		return nil, err
	}
	return ruleResult(result), nil
}
