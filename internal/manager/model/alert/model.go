// Package alert holds persistence entities for the manager/alert sub-domain.
package alert

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

const (
	StatusOpen         = "open"
	StatusAcknowledged = "acknowledged"
	StatusSilenced     = "silenced"
	StatusResolved     = "resolved"
)

const (
	IncidentStatusOpen         = StatusOpen
	IncidentStatusAcknowledged = StatusAcknowledged
	IncidentStatusSilenced     = StatusSilenced
	IncidentStatusResolved     = StatusResolved
)

const (
	EventTypeFiring             = "firing"
	EventTypeRepeatSuppressed   = "repeat_suppressed"
	EventTypeAcknowledged       = "acknowledged"
	EventTypeSilenced           = "silenced"
	EventTypeResolved           = "resolved"
	EventTypeReopened           = "reopened"
	EventTypeNote               = "note"
	EventTypeNotificationSent   = "notification_sent"
	EventTypeNotificationFailed = "notification_failed"
	EventTypeInhibited          = "inhibited"
	// EventTypeAIInitialDiagnosis is written by the proactive AI
	// investigator (P2) when an incident first fires. The Message column
	// holds a 3-paragraph LLM-authored initial diagnosis: situation
	// summary, likely root causes, and immediate triage steps. ActorType
	// is "system"; the IncidentDetail SPA renders this row prominently
	// at the top so on-call sees AI's first take without opening /chat.
	EventTypeAIInitialDiagnosis = "ai_initial_diagnosis"
)

const (
	SilenceStatusActive    = "active"
	SilenceStatusExpired   = "expired"
	SilenceStatusCancelled = "cancelled"
)

const (
	RuleSourceBuiltin           = "ongrid_builtin"
	RuleSourcePrometheus        = "prometheus_external"
	RuleJoinModeAll             = "all"
	RuleJoinModeAny             = "any"
	RuleScopeHost               = "host"
	RuleScopeGlobal             = "global"
	RuleScopeMonitoringPipeline = "monitoring_pipeline"
)

// RuleKind discriminates how the evaluator interprets a rule's spec. Each
// kind drives a different sub-evaluator and a different shape of params
// inside Rule.ConditionsJSON. organises kinds along two axes —
// signal source (metric / log / trace) × trigger mode (anomaly / forecast
// / burn_rate / match / raw). Phase-3-final collapse: metric_threshold
// became a UI-only entry form that compiles into metric_raw at save
// time, so storage has a single host-metric kind. The formerly-special
// edge_absence / health_ingest / event_internal kinds were already
// removed in Phase-3 because the manager exposes the same state as
// Prom metrics (edge_last_seen_seconds_ago, prom_write_total,
// alert_events_total) so metric_raw covers each.
//
// metric_threshold is still listed as an INPUT kind below so the
// front-end can POST kind=metric_threshold + conditions[] for the
// friendly form. The biz layer rewrites kind→metric_raw and conditions→
// {expr} during buildRuleRow, so the DB / evaluator never see the value.
//
// Phase-A kinds (evaluable today):
//   - metric_raw: arbitrary PromQL (replaces prom_query). Ticker-driven.
//   - metric_anomaly: deviation from a rolling baseline (z-score).
//     Ticker-driven via PromQuerier.
//   - metric_forecast: linear extrapolation (predict_linear) crossing a
//     static threshold within a future window. Ticker-driven.
//   - metric_burn_rate: SLO error-budget multi-window multi-burn-rate
//     (Google SRE Workbook). Ticker-driven.
//
// Phase-B (persisted and evaluated):
//   - log_search — backend-neutral structured log query.
//   - log_match / log_volume — legacy Loki/LogQL compatibility kinds.
//   - trace_latency / trace_error_rate — depend on trace ingestion ().
const (
	// RuleKindMetricThreshold is a UI-only INPUT kind. The biz layer
	// compiles metric_threshold submissions to metric_raw at save time;
	// no row on disk ever bears this kind.
	RuleKindMetricThreshold = "metric_threshold"
	RuleKindMetricAnomaly   = "metric_anomaly"
	RuleKindMetricForecast  = "metric_forecast"
	RuleKindMetricBurnRate  = "metric_burn_rate"
	RuleKindMetricRaw       = "metric_raw"

	// RuleKindLogSearch stores the product-level log search contract
	// (keywords/scope/field filters/window), never a backend DSL. The
	// evaluator therefore follows the active Loki/Elasticsearch query
	// plan in exactly the same way as the Logs UI and AIOps tools.
	RuleKindLogSearch = "log_search"

	// Listed in IsKnownKind so persistence won't reject them, but absent
	// from IsEvaluableKind so the engine skips them with a "coming soon"
	// log line instead of erroring.
	RuleKindLogMatch       = "log_match"
	RuleKindLogVolume      = "log_volume"
	RuleKindTraceLatency   = "trace_latency"
	RuleKindTraceErrorRate = "trace_error_rate"
)

// legacyKindAliases maps obsolete kind strings to their current names.
// Used by NormalizeKind so a row that escaped a backfill (or arrives
// from an external import) still resolves to a working evaluator. The
// cut maps the deleted edge_absence / health_ingest /
// event_internal kinds to metric_raw — the conditions_json shape will
// be wrong but the migration in data/alert/store.Migrate rewrites it
// to a correct PromQL spec on first boot.
//
// Phase-3-final additions: metric_threshold rule rows on disk also
// alias to metric_raw. The friendly form is UI-only; on save we
// rewrite kind+conditions before the row hits sqlite, but rows that
// pre-date this PR get caught by the migration's metric_threshold→
// metric_raw sweep (data/alert/store/migrate.go) and via this alias
// in case anything slipped through.
var legacyKindAliases = map[string]string{
	// Pre-names.
	"edge_offline":  RuleKindMetricRaw,
	"prom_query":    RuleKindMetricRaw,
	"ingest_health": RuleKindMetricRaw,
	///2 names, deleted in Phase-3.
	"edge_absence":   RuleKindMetricRaw,
	"health_ingest":  RuleKindMetricRaw,
	"event_internal": RuleKindMetricRaw,
}

// NormalizeKind canonicalises a kind string. Empty defaults to
// metric_raw; legacy strings map to their names; metric_threshold
// is preserved (it's a valid INPUT kind the API accepts from the UI's
// friendly form). Anything else is returned unchanged.
func NormalizeKind(k string) string {
	if k == "" {
		return RuleKindMetricRaw
	}
	if alias, ok := legacyKindAliases[k]; ok {
		return alias
	}
	return k
}

// IsKnownKind reports whether k is a kind the system recognises at all.
// Callers writing rules use this to reject typos. metric_threshold stays
// in the set because the UI's friendly form POSTs it as input — biz
// rewrites it to metric_raw before persistence.
func IsKnownKind(k string) bool {
	switch NormalizeKind(k) {
	case RuleKindMetricThreshold, RuleKindMetricAnomaly, RuleKindMetricForecast,
		RuleKindMetricBurnRate, RuleKindMetricRaw,
		RuleKindLogSearch, RuleKindLogMatch, RuleKindLogVolume, RuleKindTraceLatency,
		RuleKindTraceErrorRate:
		return true
	}
	return false
}

// IsEvaluableKind reports whether k has a working evaluator today. UIs
// MAY allow saving a kind whose evaluator is still in flight (log/trace),
// but the engine refuses to fire on one — IsEvaluableKind is the gate.
// metric_threshold is omitted because it's input-only; by the time a
// row reaches the evaluator it has been rewritten to metric_raw.
func IsEvaluableKind(k string) bool {
	switch NormalizeKind(k) {
	case RuleKindMetricAnomaly, RuleKindMetricForecast,
		RuleKindMetricBurnRate, RuleKindMetricRaw,
		RuleKindLogSearch, RuleKindLogMatch, RuleKindLogVolume,
		RuleKindTraceLatency, RuleKindTraceErrorRate:
		return true
	}
	return false
}

// Channel types. The legacy "log" type was removed: manager stdout is
// ephemeral (container restart loses history, no audit trail), and the
// alert_events table already records every notification attempt with
// status — that IS the audit. UI hint surfaces "通知投递记录请查 设置
// → 告警事件" so operators don't look for the old log sink.
//
// 2026-05 addition: ChannelTypeWeCom (企业微信). Bot endpoint shape
// matches DingTalk's text payload — a plain webhook POST with
// {"msgtype":"text","text":{"content":"..."}}; no signing required for
// the v1 wiring (the bot URL itself carries the secret query param).
const (
	ChannelTypeWebhook  = "webhook"
	ChannelTypeSlack    = "slack"
	ChannelTypeFeishu   = "feishu"
	ChannelTypeDingTalk = "dingtalk"
	ChannelTypeWeCom    = "wecom"
	ChannelTypeTelegram = "telegram"
)

const (
	DeliveryStatusPending = "pending"
	DeliveryStatusSuccess = "success"
	DeliveryStatusFailed  = "failed"
)

const (
	ActorTypeSystem = "system"
	ActorTypeUser   = "user"
)

type Incident struct {
	ID     uint64  `gorm:"column:id;primaryKey;autoIncrement"`
	RuleID *uint64 `gorm:"column:rule_id;index:idx_alert_incidents_rule_id"`
	// DeviceID is the host device this incident fired against. Renamed
	// from EdgeID in May 2026 (entity split); the underlying integer
	// matches the legacy edge_id 1:1 because the migration reuses it.
	DeviceID        *uint64        `gorm:"column:device_id;index:idx_alert_incidents_device_id"`
	Title           string         `gorm:"column:title;size:256;not null;default:''"`
	Scope           string         `gorm:"column:scope;size:32;not null;default:''"`
	ScopeType       string         `gorm:"column:scope_type;size:32;not null;default:''"`
	Rule            string         `gorm:"column:rule;size:128;not null;default:'';index:idx_alert_incidents_rule"`
	RuleName        string         `gorm:"column:rule_name;size:128;not null;default:''"`
	Severity        string         `gorm:"column:severity;size:16;not null;default:''"`
	Status          string         `gorm:"column:status;size:16;not null;default:open;index:idx_alert_incidents_status"`
	Summary         string         `gorm:"column:summary;type:text;not null"`
	Description     string         `gorm:"column:description;type:text;not null"`
	DedupeKey       string         `gorm:"column:dedupe_key;size:191;not null;default:'';uniqueIndex"`
	Value           *float64       `gorm:"column:value"`
	Threshold       *float64       `gorm:"column:threshold"`
	LabelsJSON      string         `gorm:"column:labels_json;type:text;not null"`
	AnnotationsJSON string         `gorm:"column:annotations_json;type:text;not null"`
	RunbookURL      string         `gorm:"column:runbook_url;size:512;not null;default:''"`
	EventCount      uint64         `gorm:"column:event_count;not null;default:0"`
	FirstFiredAt    time.Time      `gorm:"column:first_fired_at;not null;index:idx_alert_incidents_first_fired_at"`
	LastFiredAt     time.Time      `gorm:"column:last_fired_at;not null;index:idx_alert_incidents_last_fired_at"`
	LastNotifiedAt  *time.Time     `gorm:"column:last_notified_at"`
	SilencedUntil   *time.Time     `gorm:"column:silenced_until"`
	AcknowledgedAt  *time.Time     `gorm:"column:acknowledged_at"`
	AcknowledgedBy  *uint64        `gorm:"column:acknowledged_by"`
	ResolvedAt      *time.Time     `gorm:"column:resolved_at"`
	ResolvedBy      *uint64        `gorm:"column:resolved_by"`
	SourceType      string         `gorm:"column:source_type;size:32;not null;default:''"`
	CreatedAt       time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt       gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Incident) TableName() string { return "alert_incidents" }

type Event struct {
	ID             uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	IncidentID     uint64         `gorm:"column:incident_id;not null;index:idx_alert_events_incident_created,priority:1"`
	EventType      string         `gorm:"column:event_type;size:24;not null;default:''"`
	StatusAfter    string         `gorm:"column:status_after;size:16;not null;default:''"`
	Severity       string         `gorm:"column:severity;size:16;not null;default:''"`
	Title          string         `gorm:"column:title;size:256;not null;default:''"`
	Message        *string        `gorm:"column:message;type:text"`
	ActorType      string         `gorm:"column:actor_type;size:16;not null;default:system"`
	ActorID        *uint64        `gorm:"column:actor_id"`
	OperatorUserID *uint64        `gorm:"column:operator_user_id"`
	SnapshotJSON   string         `gorm:"column:snapshot_json;type:text;not null"`
	Reason         string         `gorm:"column:reason;type:text;not null"`
	OccurredAt     time.Time      `gorm:"column:occurred_at"`
	CreatedAt      time.Time      `gorm:"column:created_at;autoCreateTime;index:idx_alert_events_incident_created,priority:2"`
	UpdatedAt      time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt      gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Event) TableName() string { return "alert_events" }

type Silence struct {
	ID           uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	Name         string         `gorm:"column:name;size:256;not null;default:''"`
	Scope        string         `gorm:"column:scope;size:32;not null;default:''"`
	ScopeType    string         `gorm:"column:scope_type;size:32;not null;default:''"`
	// DeviceID renamed from EdgeID in May 2026 (entity split).
	DeviceID     *uint64        `gorm:"column:device_id;index:idx_alert_silences_device_rule,priority:1"`
	Rule         string         `gorm:"column:rule;size:128;not null;default:'';index:idx_alert_silences_device_rule,priority:2"`
	Status       string         `gorm:"column:status;size:16;not null;default:active;index:idx_alert_silences_status_ends_at,priority:1"`
	MatchersJSON string         `gorm:"column:matchers_json;type:text;not null"`
	Reason       *string        `gorm:"column:reason;type:text"`
	CreatedBy    *uint64        `gorm:"column:created_by"`
	CancelledBy  *uint64        `gorm:"column:cancelled_by"`
	StartsAt     time.Time      `gorm:"column:starts_at;not null"`
	EndsAt       time.Time      `gorm:"column:ends_at;not null;index:idx_alert_silences_status_ends_at,priority:2"`
	CancelledAt  *time.Time     `gorm:"column:cancelled_at"`
	CreatedAt    time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Silence) TableName() string { return "alert_silences" }

type Rule struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	// RuleKey is the stable lower_snake identifier used in dedupe keys and
	// incident.rule (e.g. "cpu_high"). Unique across non-soft-deleted rows.
	// Built-in rules use canonical keys; user-created rules pick their own.
	RuleKey string `gorm:"column:rule_key;size:128;not null;default:'';uniqueIndex:idx_alert_rules_rulekey"`
	// Kind drives how ConditionsJSON is interpreted and which evaluator
	// runs. Defaults to "metric_raw" — the canonical post-Phase-3-final
	// shape; data/alert/store.Migrate also backfills legacy NULL/'' rows
	// to metric_raw and rewrites any lingering metric_threshold rows.
	Kind            string  `gorm:"column:kind;size:32;not null;default:'metric_raw';index:idx_alert_rules_kind"`
	Name            string  `gorm:"column:name;size:128;not null;default:''"`
	SourceType      string  `gorm:"column:source_type;size:32;not null;default:'';index:idx_alert_rules_scope_enabled,priority:1"`
	ScopeType       string  `gorm:"column:scope_type;size:32;not null;default:'';index:idx_alert_rules_scope_enabled,priority:2"`
	JoinMode        string  `gorm:"column:join_mode;size:8;not null;default:all"`
	Severity        string  `gorm:"column:severity;size:16;not null;default:''"`
	Enabled         bool    `gorm:"column:enabled;not null;default:true;index:idx_alert_rules_scope_enabled,priority:3"`
	ConditionsJSON  string  `gorm:"column:conditions_json;type:text;not null"`
	LabelsJSON      *string `gorm:"column:labels_json;type:text"`
	AnnotationsJSON *string `gorm:"column:annotations_json;type:text"`
	RunbookURL      *string `gorm:"column:runbook_url;size:512"`
	// NotifyChannelIDsJSON optionally pins this rule's incidents to a
	// specific subset of notification channels (JSON-encoded []uint64).
	// nil / empty → router falls back to the global severity/scope
	// filters on each channel (legacy behavior). Non-empty → only those
	// channel IDs receive the incident (still subject to each channel's
	// own enabled flag for safety).
	NotifyChannelIDsJSON *string `gorm:"column:notify_channel_ids_json;type:text"`
	// NotifyWindowSeconds + NotifyMinFires together implement the per-rule
	// 「发送策略」(send-policy) dampening gate: a rule that fires fewer than
	// NotifyMinFires times within the trailing NotifyWindowSeconds does NOT
	// notify (the firing event still goes into alert_events and the
	// incident.event_count still increments — only the notify step is
	// skipped, with a repeat_suppressed event recorded so operators can see
	// the dampening took effect). Both zero (default) → dampening
	// disabled, every firing notifies subject to the existing cooldown +
	// silence + inhibition gates. Both > 0 → dampening enabled.
	// Mixed (one zero, one >0) is rejected at the biz layer.
	NotifyWindowSeconds int            `gorm:"column:notify_window_seconds;not null;default:0"`
	NotifyMinFires      int            `gorm:"column:notify_min_fires;not null;default:0"`
	CreatedBy           *uint64        `gorm:"column:created_by"`
	CreatedAt           time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt           gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Rule) TableName() string { return "alert_rules" }

type Channel struct {
	ID          uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	Name        string `gorm:"column:name;size:128;not null;default:''"`
	ChannelType string `gorm:"column:channel_type;size:32;not null;default:'';index:idx_alert_channels_type_enabled,priority:1"`
	Enabled     bool   `gorm:"column:enabled;not null;default:true;index:idx_alert_channels_type_enabled,priority:2"`
	ConfigJSON  string `gorm:"column:config_json;type:text;not null"`
	// MatchSeverityMin gates this channel by severity floor. Empty matches
	// any severity; "warning" matches warning + critical; "critical" only
	// critical. Used by the Notification Router.
	MatchSeverityMin string `gorm:"column:match_severity_min;size:16;not null;default:''"`
	// MatchScopeTypes is a comma-separated allowlist of scope_type values
	// (e.g. "host" or "host,monitoring_pipeline"). Empty matches any.
	MatchScopeTypes string         `gorm:"column:match_scope_types;size:128;not null;default:''"`
	CreatedBy       *uint64        `gorm:"column:created_by"`
	CreatedAt       time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt       gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Channel) TableName() string { return "notification_channels" }

type Delivery struct {
	ID                uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	IncidentID        *uint64        `gorm:"column:incident_id;index:idx_alert_deliveries_incident_channel,priority:1"`
	EventID           *uint64        `gorm:"column:event_id;index:idx_alert_deliveries_event_id"`
	ChannelID         uint64         `gorm:"column:channel_id;not null;index:idx_alert_deliveries_incident_channel,priority:2"`
	Status            string         `gorm:"column:status;size:16;not null;default:pending;index:idx_alert_deliveries_status"`
	AttemptCount      uint32         `gorm:"column:attempt_count;not null;default:0"`
	ProviderMessageID *string        `gorm:"column:provider_message_id;size:128"`
	RequestJSON       *string        `gorm:"column:request_json;type:text"`
	ResponseJSON      *string        `gorm:"column:response_json;type:text"`
	ErrorMessage      *string        `gorm:"column:error_message;type:text"`
	SentAt            *time.Time     `gorm:"column:sent_at"`
	FinishedAt        *time.Time     `gorm:"column:finished_at"`
	CreatedAt         time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt         time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt         gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Delivery) TableName() string { return "notification_deliveries" }

type Labels map[string]string

type RuleCondition struct {
	Metric     string  `json:"metric"`
	Operator   string  `json:"operator"`
	Threshold  float64 `json:"threshold"`
	Window     string  `json:"window,omitempty"`
	For        string  `json:"for,omitempty"`
	Aggregator string  `json:"aggregator,omitempty"`
}

type SilenceMatcher struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

func (i Incident) Labels() (Labels, error) {
	return parseLabels(i.LabelsJSON)
}

func (i Incident) Annotations() (Labels, error) {
	return parseLabels(i.AnnotationsJSON)
}

func (r Rule) Conditions() ([]RuleCondition, error) {
	var out []RuleCondition
	if r.ConditionsJSON == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r Rule) Labels() (Labels, error) {
	return parseLabelsPtr(r.LabelsJSON)
}

func (r Rule) Annotations() (Labels, error) {
	return parseLabelsPtr(r.AnnotationsJSON)
}

func (c Channel) Config() (map[string]string, error) {
	var out map[string]string
	if c.ConfigJSON == "" {
		return map[string]string{}, nil
	}
	if err := json.Unmarshal([]byte(c.ConfigJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Silence) Matchers() ([]SilenceMatcher, error) {
	var out []SilenceMatcher
	if s.MatchersJSON == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s.MatchersJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseLabels(raw string) (Labels, error) {
	if raw == "" {
		return Labels{}, nil
	}
	var out Labels
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseLabelsPtr(raw *string) (Labels, error) {
	if raw == nil {
		return Labels{}, nil
	}
	return parseLabels(*raw)
}
