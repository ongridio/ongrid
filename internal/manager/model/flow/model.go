// Package flow holds persistence entities for the manager/flow
// sub-domain — user-authored workflow orchestrations (HLD-016): a
// visual DAG of trigger / agent / tool / condition / notify nodes,
// edited on a React Flow canvas and executed by the biz/flow engine.
//
// Three entities:
//   - Flow: the user's definition row. GraphJSON is the canvas DAG
//     (nodes + edges) in the wire format biz/flow.ParseGraph accepts.
//   - FlowRun: one execution. Mirrors report.Report conventions —
//     char(36) UUID id, status machine, trigger payload snapshot.
//   - FlowRunNode: one executed node within a run — resolved input,
//     output, status, timing. This is what the run viewer drills into.
//
// MySQL conventions inherited from the report/alert/device models:
//   - config rows use uint64 autoIncrement; artifact rows use char(36)
//     UUID.
//   - TEXT columns are NOT NULL without DEFAULT (MySQL Error 1101);
//     the biz layer always supplies a value ("" / "{}" canonical).
//   - no org_id column — private-MVP single tenant; ownership is
//     created_by.
package flow

import (
	"time"

	"gorm.io/gorm"
)

// FlowRun.Status state machine. pending → running → succeeded /
// failed / canceled. A manager restart sweeps stale running rows to
// failed (the engine is in-process; runs do not survive a crash).
const (
	RunStatusPending   = "pending"
	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"
)

// FlowRunNode.Status values. skipped marks nodes on branches that
// never fired (kept for run-viewer completeness when we backfill;
// MVP only writes rows for nodes that actually executed).
const (
	NodeStatusRunning   = "running"
	NodeStatusSucceeded = "succeeded"
	NodeStatusFailed    = "failed"
)

// TriggerType values for FlowRun.TriggerType.
const (
	TriggerManual = "manual"
)

// Flow is one workflow definition. GraphJSON holds the full canvas
// document (nodes, edges, positions) — the engine re-validates it on
// every run so a hand-edited row can't crash the executor.
type Flow struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	Name        string `gorm:"size:255;not null;index"`
	Description string `gorm:"size:1024;not null"`
	// GraphJSON is the canvas DAG. TEXT NOT NULL — biz supplies "{}"
	// for a freshly created empty flow.
	GraphJSON string `gorm:"type:text;not null"`
	Enabled   bool   `gorm:"not null;default:true"`
	// Version bumps on every graph save; runs snapshot the version they
	// executed so an edited flow doesn't retro-confuse old run views.
	Version   int     `gorm:"not null;default:1"`
	CreatedBy *uint64 `gorm:"index"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// FlowRun is one execution of a Flow.
type FlowRun struct {
	ID          string `gorm:"primaryKey;type:char(36)"`
	FlowID      uint64 `gorm:"not null;index"`
	FlowVersion int    `gorm:"not null;default:1"`
	Status      string `gorm:"size:16;not null;index"`
	TriggerType string `gorm:"size:32;not null"`
	// TriggerJSON is the trigger payload exposed to expressions as
	// {{trigger.*}} (manual: the user-supplied input object).
	TriggerJSON string `gorm:"type:text;not null"`
	// Error is the run-level failure reason ("" while healthy).
	Error     string  `gorm:"size:2048;not null"`
	CreatedBy *uint64 `gorm:"index"`

	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// FlowRunNode is one executed node within a run.
type FlowRunNode struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	RunID string `gorm:"type:char(36);not null;index"`
	// NodeID / NodeType / NodeName are snapshotted from the graph at
	// execution time so the run view survives later graph edits.
	NodeID   string `gorm:"size:64;not null"`
	NodeType string `gorm:"size:64;not null"`
	NodeName string `gorm:"size:255;not null"`
	Status   string `gorm:"size:16;not null"`
	// InputJSON is the node config after expression resolution — what
	// the executor actually received. OutputJSON is its data output.
	InputJSON  string `gorm:"type:text;not null"`
	OutputJSON string `gorm:"type:text;not null"`
	// FiredPort is the control port the node emitted (next / true /
	// false / error / ...) — the run viewer colors edges with it.
	FiredPort string `gorm:"size:32;not null"`
	Error     string `gorm:"size:2048;not null"`

	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
}
