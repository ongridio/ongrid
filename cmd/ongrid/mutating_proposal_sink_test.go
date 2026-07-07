package main

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	manageraiopsdata "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	manageraiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestMutatingProposalSinkWritesDecisionAndExecution(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := manageraiopsdata.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := manageraiopsdata.NewMutatingProposalRepo(db)
	sink := newMutatingProposalSink(repo)
	if sink == nil {
		t.Fatal("sink should be constructed for non-nil repo")
	}

	ctx := context.Background()
	createdAt := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	id, err := sink.Insert(ctx, aiopstoolsdec.MutatingProposalEvent{
		SessionID:      "session-1",
		ToolName:       aiopstools.ToolNameExecuteK8sAction,
		ArgsJSON:       "",
		ToolClass:      "write",
		ReviewerAgent:  "reviewer",
		ReviewerTaskID: "agent-1",
		OperatorUserID: 7,
		CreatedAt:      createdAt,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id == "" {
		t.Fatal("Insert returned empty id")
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get inserted proposal: %v", err)
	}
	if got.SessionID != "session-1" || got.ToolName != aiopstools.ToolNameExecuteK8sAction || got.ToolClass != "write" {
		t.Fatalf("inserted proposal mismatch: %+v", got)
	}
	if got.ArgsJSON != "{}" {
		t.Fatalf("ArgsJSON = %q, want {}", got.ArgsJSON)
	}

	if err := sink.UpdateDecision(ctx, id, manageraiopsmodel.DecisionApprove, "approved by reviewer"); err != nil {
		t.Fatalf("UpdateDecision: %v", err)
	}
	executedAt := time.Date(2026, 6, 29, 10, 1, 0, 0, time.UTC)
	if err := sink.MarkExecuted(ctx, id, executedAt); err != nil {
		t.Fatalf("MarkExecuted: %v", err)
	}

	got, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get updated proposal: %v", err)
	}
	if got.Decision != manageraiopsmodel.DecisionApprove {
		t.Fatalf("Decision = %q, want approve", got.Decision)
	}
	if got.DecisionReason == nil || *got.DecisionReason != "approved by reviewer" {
		t.Fatalf("DecisionReason = %v, want approved by reviewer", got.DecisionReason)
	}
	if got.ExecutedAt == nil || !got.ExecutedAt.Equal(executedAt) {
		t.Fatalf("ExecutedAt = %v, want %v", got.ExecutedAt, executedAt)
	}
}
