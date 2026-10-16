// Package approval is the persistence entity for the human propose-confirm
// inbox (HLD-017). It is a GENERAL approval primitive — one row per
// dangerous action awaiting a human decision, regardless of producer
// (agent cloud-shell command, restart_service, a flow approval node …).
//
// Strictly additive: a brand-new `approvals` table + package. It does NOT
// touch the existing chat_mutating_proposals table or any live path; an
// action only flows through here when a producer explicitly Proposes.
package approval

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Approval is one pending/decided dangerous action.
type Approval struct {
	// RuntimeVersion is immutable execution ownership, independent of the current default.
	RuntimeVersion string `gorm:"size:16;column:runtime_version" json:"runtime_version,omitempty"`
	ID             string `gorm:"primaryKey;type:char(36);column:id" json:"id"`

	// Kind routes execution on approve (biz registers an executor per kind),
	// e.g. "shell_command", "restart_service". Also the UI category.
	Kind string `gorm:"size:64;not null;index" json:"kind"`

	// Title is a one-line human label ("terraform apply (tencent-prod)").
	Title string `gorm:"size:255;not null" json:"title"`
	// Summary is a short preview the inbox shows without opening details
	// (e.g. a terraform plan diff summary).
	Summary string `gorm:"type:text" json:"summary"`
	// PayloadJSON is the opaque action spec the executor needs to run it
	// (command, runner, credential ref, workdir …). Producer-defined.
	PayloadJSON string `gorm:"type:text;not null" json:"payload"`

	// Source records where the proposal came from for the UI: "agent"
	// (chat) / "flow" (an approval node). SessionID optionally links back.
	Source    string `gorm:"size:32;not null;default:agent" json:"source"`
	SessionID string `gorm:"size:64;index" json:"session_id,omitempty"`

	// Status lifecycle. Constants below.
	Status string `gorm:"size:16;not null;default:pending;index" json:"status"`

	// ProposedBy is the user who triggered the proposal (chat owner / flow
	// author). ApprovedBy is the human who decided (NULL until decided).
	ProposedBy uint64  `gorm:"not null;default:0" json:"proposed_by"`
	ApprovedBy *uint64 `gorm:"" json:"approved_by,omitempty"`

	// Reason is the approver's note / reject rationale. ResultJSON holds
	// the execution outcome after an approve runs the action.
	Reason     *string `gorm:"type:text" json:"reason,omitempty"`
	ResultJSON *string `gorm:"type:text" json:"result,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	ExecutedAt *time.Time `json:"executed_at,omitempty"`
}

// TableName pins the schema name.
func (Approval) TableName() string { return "approvals" }

// BeforeCreate fills a UUID when unset.
func (a *Approval) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// Status constants.
const (
	StatusPending  = "pending"  // awaiting human decision
	StatusApproved = "approved" // approved, executor not run / no executor
	StatusRejected = "rejected" // human said no
	StatusExecuted = "executed" // approved + executor ran successfully
	StatusFailed   = "failed"   // approved + executor errored
)

// Source constants.
const (
	SourceAgent = "agent"
	SourceFlow  = "flow"
)
