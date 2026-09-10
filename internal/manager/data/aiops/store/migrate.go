package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// Migrate registers the aiops chat tables (sessions, messages, tool calls,
// mutating proposals) with gorm's AutoMigrate. CHECK constraints on Role /
// Status / Decision are carried in the model tags and reproduced on both
// MySQL and SQLite.
//
// chat_mutating_proposals (added PR-7 of / reviewer
// reality-check) is the audit source of truth for
// — every mutating tool_call leaves one row regardless of approve /
// reject. See model.MutatingProposal for the schema rationale.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.Session{},
		&model.Message{},
		&model.Attachment{},
		&model.ToolCall{},
		&model.MutatingProposal{},
		&model.UserAgent{},
		&model.Operation{},
		&model.OperationEvent{},
		&model.OperationArtifact{},
	)
}
