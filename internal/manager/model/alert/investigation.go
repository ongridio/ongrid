package alert

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InvestigationReport is one auto-generated root-cause analysis bound
// to an alert_incidents row. Spawned by the investigator usecase when
// an alert fires, populated by a chatruntime worker running the
// incident-investigator agent, finalised by a second LLM pass that
// extracts the structured fields from the worker transcript.
//
// The transcript lives in chat_sessions (kind='investigation') —
// reference via AuditSessionID for drill-down. The report itself is
// the operator-facing artifact: one row, one alert, one finding.
type InvestigationReport struct {
	ID string `gorm:"primaryKey;type:char(36);column:id"`

	// IncidentID is the alert_incidents.id this report investigates.
	// One report per incident (UNIQUE on first-pass; future re-run
	// versioning may relax this). Indexed for the IncidentDetail page.
	IncidentID uint64 `gorm:"column:incident_id;not null;uniqueIndex:uniq_invreports_incident"`

	// Status drives the IncidentDetail UI badge:
	//   pending  — queued, worker not started yet
	//   running  — worker spawned, tool loop running
	//   ready    — final report written
	//   failed   — worker crashed / LLM error / timeout
	//   skipped  — gate dropped it (severity too low / dedup / budget)
	Status string `gorm:"column:status;size:16;not null;default:'pending';index:idx_invreports_status_created,priority:1"`

	// StatusReason is the human-readable explanation for skipped /
	// failed. Empty for pending / running / ready.
	//
	// MySQL forbids DEFAULT on TEXT columns (Error 1101), so we keep
	// the column NOT NULL and let the biz layer always supply a value
	// (empty string is the canonical "no reason yet"). Same rule
	// applies to every text/longtext column below.
	StatusReason string `gorm:"column:status_reason;type:text;not null"`

	// RootCause is the one-line conclusion. Operator-facing — shows up
	// in alert list inline summary. Set on status=ready only.
	RootCause string `gorm:"column:root_cause;size:1024;not null;default:''"`

	// AffectedWindow describes the time range the incident affected
	// real workload (may be narrower than the alert's fired→resolved
	// window). Stored as ISO-8601 range string "start/end" for
	// portability across stores; parsed in biz layer when needed.
	AffectedWindow string `gorm:"column:affected_window;size:64;not null;default:''"`

	// PinpointedTargetJSON is the structured "what specific entity"
	// — typically {device_id, pid, cmd, service}. Schema is open;
	// LLM fills it per available signal.
	PinpointedTargetJSON string `gorm:"column:pinpointed_target_json;type:text;not null"`

	// RelatedAlertsJSON is the array of other alert_incidents the
	// model judged share a root cause. Used by alert-list grouping.
	RelatedAlertsJSON string `gorm:"column:related_alerts_json;type:text;not null"`

	// EvidenceJSON is the ordered list of tool-call summaries that
	// produced the conclusion. Each entry references a
	// chat_tool_calls row for raw drill-down.
	EvidenceJSON string `gorm:"column:evidence_json;type:text;not null"`
	// Nullable for historical reports and rolling upgrades; absence never means checked.
	EvidenceCheckJSON *string `gorm:"column:evidence_check_json;type:text"`

	// SuggestedActionsJSON is the array of recommended next steps.
	// First version is display-only (no one-click execute) — operators
	// follow the deep-link or copy the command.
	SuggestedActionsJSON string `gorm:"column:suggested_actions_json;type:text;not null"`

	// FindingsMD is the long-form markdown the SPA renders in the
	// expanded report view.
	FindingsMD string `gorm:"column:findings_md;type:longtext;not null"`

	// Confidence is the model's self-assessed 0-1 score, optionally
	// adjusted in biz by evidence-step / cross-signal heuristics.
	Confidence *float64 `gorm:"column:confidence"`

	// ConfidenceFactorsJSON holds the boolean breakdown (had topology?
	// had log corr? had trace corr? evidence_steps count) for
	// transparency in the UI.
	ConfidenceFactorsJSON string `gorm:"column:confidence_factors_json;type:text;not null"`

	// AuditSessionID points at the chat_sessions row (kind='investigation')
	// that holds the full tool-loop transcript. Nullable while pending.
	AuditSessionID *string `gorm:"column:audit_session_id;size:36;index"`

	// WorkerID is the chatruntime worker handle — bookkeeping for
	// cancellation / re-attach if the manager restarts mid-run.
	WorkerID *string `gorm:"column:worker_id;size:64"`

	// ToolCallCount counts how many tools the worker invoked.
	// Drives the "7 tool calls" line in the UI header.
	ToolCallCount int `gorm:"column:tool_call_count;not null;default:0"`

	CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime"`
	ReadyAt   *time.Time     `gorm:"column:ready_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (InvestigationReport) TableName() string { return "investigation_reports" }

// BeforeCreate fills ID with a UUIDv4 when caller didn't pre-set one.
func (r *InvestigationReport) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

const (
	InvestigationStatusPending = "pending"
	InvestigationStatusRunning = "running"
	InvestigationStatusReady   = "ready"
	InvestigationStatusFailed  = "failed"
	InvestigationStatusSkipped = "skipped"
)
