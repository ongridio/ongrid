package aiops

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	OperationStateCreated          = "created"
	OperationStateQueued           = "queued"
	OperationStateRunning          = "running"
	OperationStateCanceling        = "canceling"
	OperationStateSucceeded        = "succeeded"
	OperationStateFailed           = "failed"
	OperationStateCancelled        = "cancelled"
	OperationStateAwaitingApproval = "awaiting_approval"
	OperationStateUnknown          = "unknown"
	OperationStatePartial          = "partial"
)

// Operation is the durable execution commitment created by an AgentLoop.
// Tool calls remain ephemeral observations; Operations are reserved for work
// that can outlive the current turn, be cancelled, or produce artifacts.
type Operation struct {
	ID             string         `gorm:"primaryKey;type:char(36);column:id"`
	Source         string         `gorm:"size:32;not null;default:chat;index"`
	TokenID        *string        `gorm:"size:32;uniqueIndex:idx_operation_token_key,priority:1"`
	IdempotencyKey *string        `gorm:"size:128;uniqueIndex:idx_operation_token_key,priority:2"`
	RequestDigest  string         `gorm:"size:64"`
	ResultJSON     string         `gorm:"type:mediumtext"`
	AuditPending   bool           `gorm:"not null;default:false;index"`
	ApprovalID     string         `gorm:"size:36;index"`
	LeaseUntil     *time.Time     `gorm:"index"`
	ChatSessionID  string         `gorm:"type:char(36);not null;index;column:chat_session_id"`
	CreatedBy      uint64         `gorm:"not null;index;column:created_by"`
	Kind           string         `gorm:"size:64;not null;index;column:kind"`
	State          string         `gorm:"size:32;not null;index;column:state"`
	Title          string         `gorm:"size:255;not null;column:title"`
	Summary        string         `gorm:"type:text;not null;column:summary"`
	InputJSON      string         `gorm:"type:text;not null;column:input_json"`
	ActionsJSON    string         `gorm:"type:text;not null;column:actions_json"`
	DetailURL      string         `gorm:"size:512;not null;default:'';column:detail_url"`
	TerminalAt     *time.Time     `gorm:"index;column:terminal_at"`
	CreatedAt      time.Time      `gorm:"autoCreateTime;column:created_at"`
	UpdatedAt      time.Time      `gorm:"autoUpdateTime;column:updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index;column:deleted_at"`
}

func (Operation) TableName() string { return "aiops_operations" }

func (o *Operation) BeforeCreate(*gorm.DB) error {
	if o.ID == "" {
		o.ID = uuid.NewString()
	}
	return nil
}

// OperationEvent is an append-only, idempotent fact emitted by an operation.
type OperationEvent struct {
	ID          string    `gorm:"primaryKey;type:char(36);column:id"`
	OperationID string    `gorm:"type:char(36);not null;index:idx_operation_event_dedupe,priority:1;column:operation_id"`
	DedupeKey   string    `gorm:"size:128;not null;index:idx_operation_event_dedupe,priority:2;column:dedupe_key"`
	Type        string    `gorm:"size:64;not null;index;column:type"`
	PayloadJSON string    `gorm:"type:text;not null;column:payload_json"`
	CreatedAt   time.Time `gorm:"autoCreateTime;column:created_at"`
}

func (OperationEvent) TableName() string { return "aiops_operation_events" }

func (e *OperationEvent) BeforeCreate(*gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	return nil
}

// OperationArtifact is a generic, user-navigable result of long-running
// work: a page, report, analysis, file, or external link.
type OperationArtifact struct {
	ID           string    `gorm:"primaryKey;type:char(36);column:id"`
	OperationID  string    `gorm:"type:char(36);not null;index:idx_operation_artifact_url,priority:1;column:operation_id"`
	Kind         string    `gorm:"size:64;not null;column:kind"`
	Title        string    `gorm:"size:255;not null;column:title"`
	URL          string    `gorm:"size:512;not null;uniqueIndex:idx_operation_artifact_url,priority:2;column:url"`
	MetadataJSON string    `gorm:"type:text;not null;column:metadata_json"`
	CreatedAt    time.Time `gorm:"autoCreateTime;column:created_at"`
}

func (OperationArtifact) TableName() string { return "aiops_operation_artifacts" }

func (a *OperationArtifact) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}
