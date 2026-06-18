package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	alertdraft "github.com/ongridio/ongrid/internal/manager/biz/aiops/alertdraft"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const (
	ToolNameDraftConfigChange = "draft_config_change"
	ToolNameApplyConfigChange = "apply_config_change"
)

const (
	ConfigDomainAlertRule            = "alert_rule"
	ConfigResultKindDraft            = "config_draft"
	ConfigResultKindApply            = "config_apply_result"
	ConfigResultKindValidationFailed = "config_validation_failed"
)

// ConfigCaller is the caller identity the config tools hand to the config
// manager. The apply tool enforces admin-only writes before the manager runs.
type ConfigCaller struct {
	UserID      uint64
	Role        string
	IsSuperuser bool
}

// ConfigManager is the consumer-owned seam for conversational alert rule
// creation. The tools package owns JSON schema and tool gating; the concrete
// manager owns draft lifecycle and delegates rule persistence through a narrow
// port.
type ConfigManager interface {
	DraftAlertRuleConfig(ctx context.Context, caller ConfigCaller, in AlertRuleConfigArgs) (*ConfigDraft, error)
	ApplyAlertRuleConfig(ctx context.Context, caller ConfigCaller, in AlertRuleApplyArgs) (*ConfigApplyResult, error)
}

type ConfigChangeDraftArgs struct {
	Domain          string               `json:"domain"`
	Action          string               `json:"action"`
	RequestText     string               `json:"request_text,omitempty"`
	LookbackSeconds int                  `json:"lookback_seconds,omitempty"`
	Rule            AlertRuleConfigInput `json:"rule,omitempty"`
}

type ConfigChangeApplyArgs struct {
	Domain           string               `json:"domain"`
	Action           string               `json:"action"`
	Rule             AlertRuleConfigInput `json:"rule,omitempty"`
	Payload          json.RawMessage      `json:"payload,omitempty"`
	DraftID          string               `json:"draft_id,omitempty"`
	DraftHash        string               `json:"draft_hash,omitempty"`
	Confirmed        bool                 `json:"confirmed"`
	ConfirmationText string               `json:"confirmation_text,omitempty"`
}

type ConfigTarget struct {
	ID       uint64 `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Existing bool   `json:"existing,omitempty"`
}

type ConfigDraft struct {
	Kind               string                  `json:"kind"`
	Domain             string                  `json:"domain"`
	Action             string                  `json:"action"`
	Summary            string                  `json:"summary"`
	Target             *ConfigTarget           `json:"target,omitempty"`
	Payload            json.RawMessage         `json:"payload,omitempty"`
	Preview            json.RawMessage         `json:"preview,omitempty"`
	Diff               json.RawMessage         `json:"diff,omitempty"`
	Validation         *ConfigValidationResult `json:"validation,omitempty"`
	Warnings           []string                `json:"warnings,omitempty"`
	Scope              *ConfigScopeSummary     `json:"scope,omitempty"`
	ConfirmationPrompt string                  `json:"confirmation_prompt,omitempty"`
	Rollback           string                  `json:"rollback,omitempty"`
	ApplyTool          string                  `json:"apply_tool"`
	DraftHash          string                  `json:"draft_hash,omitempty"`
}

type ConfigScopeSummary struct {
	Type       string `json:"type,omitempty"`
	Label      string `json:"label,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ChangeHint string `json:"change_hint,omitempty"`
}

type ConfigValidationResult struct {
	Status string                  `json:"status"`
	Issues []ConfigValidationIssue `json:"issues,omitempty"`
}

type ConfigValidationIssue struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

type ConfigApplyResult struct {
	Kind       string        `json:"kind"`
	Domain     string        `json:"domain"`
	Action     string        `json:"action"`
	Status     string        `json:"status"`
	ResourceID uint64        `json:"resource_id,omitempty"`
	Resource   *ConfigTarget `json:"resource,omitempty"`
	Message    string        `json:"message,omitempty"`
	Rollback   string        `json:"rollback,omitempty"`
}

type AlertRuleConfigArgs struct {
	Action          string               `json:"action"`
	RequestText     string               `json:"request_text,omitempty"`
	LookbackSeconds int                  `json:"lookback_seconds,omitempty"`
	Rule            AlertRuleConfigInput `json:"rule,omitempty"`
}

type AlertRuleApplyArgs struct {
	Action           string               `json:"action"`
	Rule             AlertRuleConfigInput `json:"rule,omitempty"`
	DraftID          string               `json:"draft_id,omitempty"`
	DraftHash        string               `json:"draft_hash,omitempty"`
	Confirmed        bool                 `json:"confirmed"`
	ConfirmationText string               `json:"confirmation_text,omitempty"`
}

type AlertRuleConfigInput = alertdraft.RuleConfigInput

type AlertRuleCondition = alertdraft.RuleCondition

type configToolKind string

const (
	configToolDraftChange configToolKind = "draft_change"
	configToolApplyChange configToolKind = "apply_change"
)

type ConfigTool struct {
	kind    configToolKind
	manager ConfigManager
	log     *slog.Logger
}

func NewDraftConfigChangeTool(manager ConfigManager, log *slog.Logger) *ConfigTool {
	return newConfigTool(configToolDraftChange, manager, log)
}

func NewApplyConfigChangeTool(manager ConfigManager, log *slog.Logger) *ConfigTool {
	return newConfigTool(configToolApplyChange, manager, log)
}

func newConfigTool(kind configToolKind, manager ConfigManager, log *slog.Logger) *ConfigTool {
	if log == nil {
		log = slog.Default()
	}
	return &ConfigTool{kind: kind, manager: manager, log: log}
}

func (t *ConfigTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	switch t.kind {
	case configToolDraftChange:
		return &basetool.ToolInfo{
			Name:        ToolNameDraftConfigChange,
			Description: "Create and validate a read-only configuration draft for a new alert rule across all supported alert rule kinds. It never persists business config.",
			WhenToUse:   "Use when the user asks to create an alert rule and you have enough intent to draft one. For metric-based rules, call list_metric_catalog first in the same user turn unless the user supplied exact PromQL or metric names. This tool validates the candidate and returns either config_validation_failed with issues to fix, or config_draft with draft_hash. Only config_draft is confirmable; after one successful draft, stop tool calls, disclose the draft scope from scope.label/type, and ask the user to confirm, cancel, or request a scope change.",
			Parameters:  draftConfigChangeSchema,
			Class:       "read",
		}, nil
	case configToolApplyChange:
		return &basetool.ToolInfo{
			Name:        ToolNameApplyConfigChange,
			Description: "Apply a previously confirmed new alert-rule configuration draft.",
			WhenToUse:   "MUTATING. Use only after the user explicitly confirms an alert-rule config_draft. Requires confirmed=true, an admin caller, domain=alert_rule, action=create, and the payload from the draft.",
			Parameters:  applyConfigChangeSchema,
			Class:       "write",
		}, nil
	default:
		return nil, fmt.Errorf("unknown config tool kind %q", t.kind)
	}
}

func (t *ConfigTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.manager == nil {
		return "", fmt.Errorf("config tool: manager not configured")
	}
	resolved := basetool.ResolveOptions(opts)
	caller := configCallerFromContext(ctx, opts)
	switch t.kind {
	case configToolDraftChange:
		var in ConfigChangeDraftArgs
		if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
			return "", fmt.Errorf("%s: bad args: %w", ToolNameDraftConfigChange, err)
		}
		if strings.TrimSpace(in.RequestText) == "" {
			in.RequestText = resolved.UserText
		}
		out, err := t.draftConfigChange(ctx, caller, in)
		if err != nil {
			return "", fmt.Errorf("%s: %w", ToolNameDraftConfigChange, err)
		}
		return marshalConfigToolResult(out)
	case configToolApplyChange:
		var in ConfigChangeApplyArgs
		if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
			return "", fmt.Errorf("%s: bad args: %w", ToolNameApplyConfigChange, err)
		}
		if err := validateApplyGate(caller, in.Confirmed); err != nil {
			return "", fmt.Errorf("%s: %w", ToolNameApplyConfigChange, err)
		}
		if err := in.applyPayloadDefaults(); err != nil {
			return "", fmt.Errorf("%s: %w", ToolNameApplyConfigChange, err)
		}
		if err := in.validateDraftHash(); err != nil {
			return "", fmt.Errorf("%s: %w", ToolNameApplyConfigChange, err)
		}
		out, err := t.applyConfigChange(ctx, caller, in)
		if err != nil {
			return "", fmt.Errorf("%s: %w", ToolNameApplyConfigChange, err)
		}
		return marshalConfigToolResult(out)
	default:
		return "", fmt.Errorf("unknown config tool kind %q", t.kind)
	}
}

func (t *ConfigTool) draftConfigChange(ctx context.Context, caller ConfigCaller, in ConfigChangeDraftArgs) (*ConfigDraft, error) {
	if _, err := normalizeConfigDomain(in.Domain); err != nil {
		return nil, err
	}
	action, err := normalizeConfigCreateAction(in.Action)
	if err != nil {
		return nil, err
	}
	if isZeroAlertRuleInput(in.Rule) {
		return nil, fmt.Errorf("%w: rule is required before drafting alert rule config", errs.ErrInvalid)
	}
	return t.manager.DraftAlertRuleConfig(ctx, caller, AlertRuleConfigArgs{
		Action:          action,
		RequestText:     in.RequestText,
		LookbackSeconds: in.LookbackSeconds,
		Rule:            in.Rule,
	})
}

func (t *ConfigTool) applyConfigChange(ctx context.Context, caller ConfigCaller, in ConfigChangeApplyArgs) (*ConfigApplyResult, error) {
	if _, err := normalizeConfigDomain(in.Domain); err != nil {
		return nil, err
	}
	action, err := normalizeConfigCreateAction(in.Action)
	if err != nil {
		return nil, err
	}
	return t.manager.ApplyAlertRuleConfig(ctx, caller, AlertRuleApplyArgs{
		Action:           action,
		Rule:             in.Rule,
		DraftID:          in.DraftID,
		DraftHash:        in.DraftHash,
		Confirmed:        in.Confirmed,
		ConfirmationText: in.ConfirmationText,
	})
}

func configCallerFromContext(ctx context.Context, opts []basetool.InvokeOption) ConfigCaller {
	resolved := basetool.ResolveOptions(opts)
	caller := ConfigCaller{UserID: resolved.UserID}
	if t, ok := tenantctx.From(ctx); ok {
		caller.UserID = t.UserID
		caller.Role = t.Role
		caller.IsSuperuser = t.IsSuperuser
	}
	return caller
}

func validateApplyGate(caller ConfigCaller, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("%w: confirmed=true required before applying a configuration draft", errs.ErrInvalid)
	}
	if caller.Role != "admin" && !caller.IsSuperuser {
		return fmt.Errorf("%w: admin role required to apply configuration", errs.ErrForbidden)
	}
	return nil
}

func normalizeConfigDomain(domain string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	switch d {
	case ConfigDomainAlertRule, "alert", "alert_rule_config":
		return ConfigDomainAlertRule, nil
	default:
		return "", fmt.Errorf("%w: domain must be alert_rule; v1 only supports creating alert rules", errs.ErrInvalid)
	}
}

func normalizeConfigCreateAction(action string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "", "create":
		return "create", nil
	default:
		return "", fmt.Errorf("%w: action must be create; v1 only supports creating alert rules", errs.ErrInvalid)
	}
}

func (in *ConfigChangeApplyArgs) applyPayloadDefaults() error {
	if len(in.Payload) == 0 {
		return fmt.Errorf("%w: payload from config_draft is required before applying", errs.ErrInvalid)
	}
	var payload struct {
		Action    string               `json:"action"`
		DraftID   string               `json:"draft_id,omitempty"`
		Rule      AlertRuleConfigInput `json:"rule,omitempty"`
		DraftHash string               `json:"draft_hash,omitempty"`
	}
	if err := json.Unmarshal(in.Payload, &payload); err != nil {
		return fmt.Errorf("bad draft payload: %w", err)
	}
	in.Action = payload.Action
	in.DraftID = payload.DraftID
	in.Rule = payload.Rule
	if in.DraftHash == "" {
		in.DraftHash = payload.DraftHash
	}
	if isZeroAlertRuleInput(in.Rule) {
		return fmt.Errorf("%w: payload.rule from config_draft is required before applying", errs.ErrInvalid)
	}
	return nil
}

func (in ConfigChangeApplyArgs) validateDraftHash() error {
	got := strings.TrimSpace(in.DraftHash)
	if got == "" {
		return fmt.Errorf("%w: draft_hash from config_draft is required before applying", errs.ErrInvalid)
	}
	want, err := AlertRuleConfigDraftHashForID(in.Action, in.Rule, in.DraftID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: draft_hash does not match config_draft payload", errs.ErrInvalid)
	}
	return nil
}

// AlertRuleConfigDraftPayload returns the canonical draft payload and its hash.
func AlertRuleConfigDraftPayload(action string, rule AlertRuleConfigInput) (json.RawMessage, string, error) {
	return AlertRuleConfigDraftPayloadForID(action, rule, "")
}

// AlertRuleConfigDraftPayloadForID returns the canonical draft payload for a
// service-issued draft id and its matching hash.
func AlertRuleConfigDraftPayloadForID(action string, rule AlertRuleConfigInput, draftID string) (json.RawMessage, string, error) {
	payload := struct {
		DraftID string               `json:"draft_id,omitempty"`
		Action  string               `json:"action"`
		Rule    AlertRuleConfigInput `json:"rule"`
	}{
		DraftID: draftID,
		Action:  action,
		Rule:    rule,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal alert rule draft payload: %w", err)
	}
	hash := alertRuleConfigDraftHashBytes(b)
	return json.RawMessage(b), hash, nil
}

// AlertRuleConfigDraftHash returns the hash expected when applying a draft.
func AlertRuleConfigDraftHash(action string, rule AlertRuleConfigInput) (string, error) {
	return AlertRuleConfigDraftHashForID(action, rule, "")
}

// AlertRuleConfigDraftHashForID returns the hash expected when applying a
// service-issued draft id.
func AlertRuleConfigDraftHashForID(action string, rule AlertRuleConfigInput, draftID string) (string, error) {
	payload, hash, err := AlertRuleConfigDraftPayloadForID(action, rule, draftID)
	if err != nil {
		return "", err
	}
	if len(payload) == 0 {
		return "", fmt.Errorf("%w: empty alert rule draft payload", errs.ErrInvalid)
	}
	return hash, nil
}

func alertRuleConfigDraftHashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func isZeroAlertRuleInput(in AlertRuleConfigInput) bool {
	return in.RuleKey == "" &&
		in.Kind == "" &&
		in.Name == "" &&
		in.ScopeType == "" &&
		in.JoinMode == "" &&
		in.Window == "" &&
		in.For == "" &&
		in.Severity == "" &&
		in.Enabled == nil &&
		len(in.Conditions) == 0 &&
		len(in.Spec) == 0 &&
		len(in.Labels) == 0 &&
		in.RunbookURL == "" &&
		len(in.NotifyChannelIDs) == 0 &&
		in.NotifyWindowSeconds == 0 &&
		in.NotifyMinFires == 0
}

func marshalConfigToolResult(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal config result: %w", err)
	}
	var decoded interface{}
	if err := json.Unmarshal(b, &decoded); err == nil {
		scrubConfigSecrets(decoded)
		b, err = json.Marshal(decoded)
		if err != nil {
			return "", fmt.Errorf("marshal scrubbed config result: %w", err)
		}
	}
	return string(b), nil
}

func scrubConfigSecrets(v interface{}) {
	switch x := v.(type) {
	case map[string]interface{}:
		for k, value := range x {
			if isSensitiveConfigKey(k) {
				if s, ok := value.(string); ok && s != "" {
					x[k] = "******"
					continue
				}
			}
			scrubConfigSecrets(value)
		}
	case []interface{}:
		for _, item := range x {
			scrubConfigSecrets(item)
		}
	}
}

func isSensitiveConfigKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "secret", "app_secret", "verify_token", "encrypt_key", "token", "password", "api_key", "apikey":
		return true
	default:
		return false
	}
}

var draftConfigChangeSchema = json.RawMessage(`{
  "type": "object",
  "required": ["domain", "action", "request_text", "rule"],
  "properties": {
    "domain": {
      "type": "string",
      "enum": ["alert_rule"],
      "description": "Only alert_rule is supported in v1."
    },
    "action": {"type": "string", "enum": ["create"]},
    "request_text": {
      "type": "string",
      "description": "Exact current user request text that triggered this draft. Always copy the user's latest natural-language request verbatim; backend normalization uses it to verify scope labels such as log level/unit and explicit database source intent."
    },
    "lookback_seconds": {
      "type": "integer",
      "minimum": 60,
      "maximum": 604800,
      "description": "alert_rule preview lookback window; default 86400."
    },
    "rule": {"$ref": "#/$defs/rule", "description": "Required for creating an alert rule."}
  },
  "$defs": {
    "rule": {
      "type": "object",
      "properties": {
        "rule_key": {"type": "string", "description": "Required for create. Lower snake case."},
        "kind": {
          "type": "string",
          "enum": ["metric_threshold", "metric_raw", "metric_anomaly", "metric_forecast", "metric_burn_rate", "log_match", "log_volume", "trace_latency", "trace_error_rate"],
          "description": "Choose the existing alert creation mode. metric_threshold is only for host closed-set metrics. metric_raw is for arbitrary PromQL predicates, database metrics, custommetrics, and any exact collected metric name."
        },
        "name": {"type": "string"},
        "scope_type": {"type": "string", "enum": ["global", "host", "monitoring_pipeline"], "description": "Use host when the alert should be associated with a specific machine or device-collected instance, such as CPU, memory, disk/filesystem, load, network, system/journald logs, database/Redis/MongoDB metrics, or when the final PromQL/LogQL result keeps a device_id label. Use global only for service, SLO, trace, or intentionally aggregated fleet-wide rules where no single host/device should own the incident."},
        "join_mode": {"type": "string", "enum": ["all", "any"]},
        "window": {"type": "string", "description": "Compatibility alias. Prefer kind-specific spec.window or condition.window; backend normalizes this field into the correct place."},
        "for": {"type": "string", "description": "Compatibility alias for sustained duration. Prefer spec.for for metric_raw or condition.for for metric_threshold; backend normalizes this field into the correct place."},
        "severity": {"type": "string", "enum": ["info", "warning", "critical"]},
        "enabled": {"type": "boolean"},
        "conditions": {
          "type": "array",
          "description": "Only for kind=metric_threshold host rules. Canonical host metrics: cpu_pct, mem_pct, disk_used_pct, disk_avail_bytes, load1, load5, load15, net_rx_bps, net_tx_bps. Do not put MySQL/PostgreSQL/Redis/MongoDB/custom metrics here; use metric_raw instead.",
          "items": {"$ref": "#/$defs/condition"}
        },
	        "spec": {
	          "type": "object",
	          "additionalProperties": true,
	          "description": "Kind-specific spec. metric_raw accepts expr/promql/query as a full boolean PromQL predicate, or metric plus operator and threshold for one exact Prometheus metric. Use metric names and label keys from list_metric_catalog when available. Only scope selectors when the user explicitly asked for that source/device/job/service/instance; mark that with source_explicit=true. Other kinds use their natural fields: metric_anomaly, metric_forecast, metric_burn_rate, log_match, log_volume, trace_latency, trace_error_rate."
	        },
        "labels": {"type": "object", "additionalProperties": {"type": "string"}},
        "runbook_url": {"type": "string"},
        "notify_channel_ids": {"type": "array", "items": {"type": "integer"}},
        "notify_window_seconds": {"type": "integer", "minimum": 0},
        "notify_min_fires": {"type": "integer", "minimum": 0}
      }
    },
    "condition": {
      "type": "object",
      "properties": {
        "metric": {"type": "string"},
        "operator": {"type": "string"},
        "threshold": {"type": "number"},
        "window": {"type": "string"},
        "for": {"type": "string"},
        "aggregator": {"type": "string"}
      }
    }
  }
}`)

var applyConfigChangeSchema = json.RawMessage(`{
  "type": "object",
  "required": ["domain", "action", "confirmed", "payload", "draft_hash"],
  "properties": {
    "domain": {
      "type": "string",
      "enum": ["alert_rule"],
      "description": "Configuration domain from the config_draft."
    },
	    "action": {"type": "string", "enum": ["create"]},
	    "confirmed": {"type": "boolean", "description": "Must be true after explicit user confirmation."},
	    "draft_id": {"type": "string", "description": "Optional top-level copy of payload.draft_id; the exact payload remains the source of truth."},
	    "draft_hash": {"type": "string", "description": "Exact draft_hash returned by draft_config_change. The payload is rejected if this hash does not match."},
    "confirmation_text": {"type": "string"},
    "payload": {
      "type": "object",
      "description": "Exact payload object returned by draft_config_change; it is the source of truth for action/rule."
    },
    "rule": {"type": "object", "additionalProperties": true}
  }
}`)
