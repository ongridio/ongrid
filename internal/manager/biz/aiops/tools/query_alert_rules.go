package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// ToolNameQueryAlertRules is the stable wire name the LLM sees.
const ToolNameQueryAlertRules = "query_alert_rules"

// QueryAlertRulesDescription pushes the model toward this tool when the
// question is "which rules exist / who's using rule X".
const QueryAlertRulesDescription = "List ongrid alert rules filtered by kind, enabled flag, or a name substring. " +
	"Use this for questions like '这条规则是谁在用' or 'show all metric_threshold rules'. " +
	"Returns array of {id, rule_key, kind, name, scope_type, severity, enabled}."

// QueryAlertRulesSchema is the JSON Schema of the tool's argument object.
var QueryAlertRulesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind": {
      "type": "string",
      "description": "Filter by rule kind (metric_threshold | metric_anomaly | metric_forecast | metric_burn_rate | metric_raw | log_search | log_match | log_volume | trace_latency | trace_error_rate)."
    },
    "enabled": {
      "type": "boolean",
      "description": "Filter by enabled flag. Optional."
    },
    "name_contains": {
      "type": "string",
      "description": "Substring filter against rule name OR rule_key."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 500,
      "description": "Max rows returned (default 100)."
    }
  }
}`)

// QueryAlertRulesArgs is the typed form of QueryAlertRulesSchema.
type QueryAlertRulesArgs struct {
	Kind         string `json:"kind,omitempty"`
	Enabled      *bool  `json:"enabled,omitempty"`
	NameContains string `json:"name_contains,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

// AlertRuleRow is the trimmed rule envelope returned by query_alert_rules.
type AlertRuleRow struct {
	ID         uint64    `json:"id"`
	RuleKey    string    `json:"rule_key"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	ScopeType  string    `json:"scope_type"`
	Severity   string    `json:"severity"`
	Enabled    bool      `json:"enabled"`
	SourceType string    `json:"source_type"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const queryAlertRulesCallTimeout = 10 * time.Second

func (r *Registry) executeQueryAlertRules(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.alertUC == nil {
		return ExecuteResult{}, fmt.Errorf("query_alert_rules: alert usecase not configured")
	}
	var in QueryAlertRulesArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("query_alert_rules: bad args: %w", err)
	}
	if in.Limit <= 0 {
		in.Limit = 100
	}
	if in.Limit > 500 {
		in.Limit = 500
	}
	if in.Kind != "" && !alertmodel.IsKnownKind(in.Kind) {
		return ExecuteResult{}, fmt.Errorf("query_alert_rules: invalid kind %q", in.Kind)
	}

	callCtx, cancel := context.WithTimeout(ctx, queryAlertRulesCallTimeout)
	defer cancel()
	// ListRules's only filter is scope_type, which we don't use here.
	all, err := r.alertUC.ListRules(callCtx, "")
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_alert_rules: list: %w", err)
	}

	rows := make([]AlertRuleRow, 0, len(all))
	wantKind := alertmodel.NormalizeKind(in.Kind)
	for _, rule := range all {
		if in.Kind != "" && alertmodel.NormalizeKind(rule.Kind) != wantKind {
			continue
		}
		if in.Enabled != nil && rule.Enabled != *in.Enabled {
			continue
		}
		if in.NameContains != "" {
			if !strings.Contains(rule.Name, in.NameContains) && !strings.Contains(rule.RuleKey, in.NameContains) {
				continue
			}
		}
		rows = append(rows, AlertRuleRow{
			ID:         rule.ID,
			RuleKey:    rule.RuleKey,
			Kind:       rule.Kind,
			Name:       rule.Name,
			ScopeType:  rule.ScopeType,
			Severity:   rule.Severity,
			Enabled:    rule.Enabled,
			SourceType: rule.SourceType,
			UpdatedAt:  rule.UpdatedAt,
		})
		if len(rows) >= in.Limit {
			break
		}
	}

	out, err := json.Marshal(map[string]any{
		"rules": rows,
		"count": len(rows),
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_alert_rules: marshal: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}
