package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// newProposalRepo opens an in-memory SQLite DB and applies this
// package's Migrate so chat_mutating_proposals exists.
func newProposalRepo(t *testing.T) *MutatingProposalRepo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewMutatingProposalRepo(db)
}

func TestMutatingProposalRepo_InsertDefaults(t *testing.T) {
	repo := newProposalRepo(t)
	ctx := context.Background()

	p := &model.MutatingProposal{
		SessionID:      "sess-1",
		ToolName:       "host_restart_service",
		ArgsJSON:       `{"device_id":1,"service":"nginx"}`,
		ToolClass:      "write",
		ReviewerAgent:  "reviewer",
		ReviewerTaskID: "agent-deadbeef",
		OperatorUserID: 42,
	}
	if err := repo.Insert(ctx, p); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if p.ID == "" {
		t.Fatalf("expected auto-generated ID")
	}
	if p.Decision != model.DecisionPending {
		t.Errorf("Decision default = %q, want %q", p.Decision, model.DecisionPending)
	}
	if p.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be auto-stamped")
	}
}

func TestMutatingProposalRepo_DecisionUpdate(t *testing.T) {
	repo := newProposalRepo(t)
	ctx := context.Background()

	p := &model.MutatingProposal{
		SessionID:      "sess-1",
		ToolName:       "host_restart_service",
		ArgsJSON:       `{}`,
		ToolClass:      "write",
		ReviewerAgent:  "reviewer",
		ReviewerTaskID: "agent-1",
	}
	if err := repo.Insert(ctx, p); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	reason := "no SOP found"
	if err := repo.UpdateDecision(ctx, p.ID, model.DecisionReject, &reason); err != nil {
		t.Fatalf("UpdateDecision: %v", err)
	}

	got, err := repo.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Decision != model.DecisionReject {
		t.Errorf("Decision = %q, want reject", got.Decision)
	}
	if got.DecisionReason == nil || *got.DecisionReason != reason {
		t.Errorf("DecisionReason = %v, want %q", got.DecisionReason, reason)
	}
	if got.DecidedAt == nil {
		t.Errorf("DecidedAt should be stamped after update")
	}
}

func TestMutatingProposalRepo_DecisionRejectsInvalidValue(t *testing.T) {
	repo := newProposalRepo(t)
	ctx := context.Background()
	if err := repo.UpdateDecision(ctx, "x", "maybe", nil); !errors.Is(err, errs.ErrInvalid) {
		t.Errorf("invalid decision should return ErrInvalid, got %v", err)
	}
}

func TestMutatingProposalRepo_MarkExecuted(t *testing.T) {
	repo := newProposalRepo(t)
	ctx := context.Background()

	p := &model.MutatingProposal{
		SessionID:      "sess-1",
		ToolName:       "host_restart_service",
		ArgsJSON:       `{}`,
		ToolClass:      "write",
		ReviewerAgent:  "reviewer",
		ReviewerTaskID: "agent-1",
	}
	if err := repo.Insert(ctx, p); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	when := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	if err := repo.MarkExecuted(ctx, p.ID, when); err != nil {
		t.Fatalf("MarkExecuted: %v", err)
	}
	got, err := repo.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ExecutedAt == nil || !got.ExecutedAt.Equal(when) {
		t.Errorf("ExecutedAt = %v, want %v", got.ExecutedAt, when)
	}
}

func TestMutatingProposalRepo_GetMissing(t *testing.T) {
	repo := newProposalRepo(t)
	if _, err := repo.Get(context.Background(), "nonexistent"); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("missing proposal should return ErrNotFound, got %v", err)
	}
}

func TestMutatingProposalRepo_ListAndCountByToolAndDecision(t *testing.T) {
	repo := newProposalRepo(t)
	ctx := context.Background()

	rows := []*model.MutatingProposal{
		{
			SessionID:      "sess-k8s-approved",
			ToolName:       "execute_k8s_action",
			ArgsJSON:       `{"cluster_id":1,"action":"scale"}`,
			ToolClass:      "write",
			ReviewerAgent:  "reviewer",
			ReviewerTaskID: "agent-1",
			Decision:       model.DecisionApprove,
			CreatedAt:      time.Date(2026, 6, 30, 10, 2, 0, 0, time.UTC),
		},
		{
			SessionID:      "sess-k8s-pending",
			ToolName:       "execute_k8s_action",
			ArgsJSON:       `{"cluster_id":1,"action":"delete_pod"}`,
			ToolClass:      "write",
			ReviewerAgent:  "reviewer",
			ReviewerTaskID: "agent-2",
			Decision:       model.DecisionPending,
			CreatedAt:      time.Date(2026, 6, 30, 10, 1, 0, 0, time.UTC),
		},
		{
			SessionID:      "sess-host-approved",
			ToolName:       "restart_service",
			ArgsJSON:       `{"service":"nginx"}`,
			ToolClass:      "write",
			ReviewerAgent:  "reviewer",
			ReviewerTaskID: "agent-3",
			Decision:       model.DecisionApprove,
			CreatedAt:      time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC),
		},
	}
	for _, row := range rows {
		if err := repo.Insert(ctx, row); err != nil {
			t.Fatalf("Insert %s: %v", row.SessionID, err)
		}
	}

	filter := biz.MutatingProposalFilter{
		ToolName: "execute_k8s_action",
		Decision: model.DecisionApprove,
		Limit:    10,
	}
	got, err := repo.ListMutatingProposals(ctx, filter)
	if err != nil {
		t.Fatalf("ListMutatingProposals: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-k8s-approved" {
		t.Fatalf("items = %+v", got)
	}
	total, err := repo.CountMutatingProposals(ctx, filter)
	if err != nil {
		t.Fatalf("CountMutatingProposals: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
}
