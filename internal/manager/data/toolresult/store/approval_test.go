package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	approval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	biz "github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	approvalstore "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

type ownerSession struct{ id string }

func (s ownerSession) GetSession(context.Context, string) (*session.Session, error) {
	return &session.Session{ID: s.id, UserID: 7}, nil
}

func TestApprovalRecoveryNeverRepeatsExecution(t *testing.T) {
	r := testRepo(t)
	if err := approvalstore.Migrate(r.db); err != nil {
		t.Fatal(err)
	}
	sid := uuid.NewString()
	results := biz.New(r, ownerSession{sid}, []byte("cursor-key"), nil)
	approvals := approval.NewUsecase(approvalstore.NewRepo(r.db), nil)
	approvals.SetToolResults(results)
	approvals.RequireAuthorization(approval.AuthorizerFunc(func(context.Context, uint64) error { return nil }))
	executions := 0
	approvals.RegisterExecutor("test", func(context.Context, string) (string, error) {
		executions++
		return strings.Repeat("original-output\n", 10000), nil
	})
	a, err := approvals.Propose(context.Background(), approval.ProposeInput{Kind: "test", Title: "test", Source: "agent", SessionID: sid, ProposedBy: 7, Payload: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Callback().Create().Before("gorm:create").Register("fail_result", func(tx *gorm.DB) {
		if tx.Statement.Table == "tool_results" {
			tx.AddError(errors.New("metadata unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err = r.db.Callback().Update().Before("gorm:update").Register("fail_completion", func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]any); ok && fields["executed_at"] != nil {
			tx.AddError(errors.New("completion unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = approvals.Approve(context.Background(), 9, a.ID); err == nil {
		t.Fatal("expected save failure")
	}
	if err = r.db.Callback().Create().Remove("fail_result"); err != nil {
		t.Fatal(err)
	}
	if err = r.db.Callback().Update().Remove("fail_completion"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Recover(context.Background()); err != nil || n != 1 {
		t.Fatalf("recovery %d %v", n, err)
	}
	stored, err := approvals.Get(context.Background(), a.ID)
	if err != nil || stored.ResultID == "" || stored.Status != "executed" {
		t.Fatalf("approval not recovered: %+v %v", stored, err)
	}
	if _, err = approvals.Approve(context.Background(), 9, a.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("second approval: %v", err)
	}
	if executions != 1 {
		t.Fatalf("tool executed %d times", executions)
	}
	row, err := r.Get(context.Background(), stored.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(context.Background(), row, 0, 100); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataLookupFailureStillSpoolsSmallResult(t *testing.T) {
	r := testRepo(t)
	body := []byte("small response after execution")
	row := testRow(body)
	if err := r.db.Callback().Query().Before("gorm:query").Register("metadata_down", func(tx *gorm.DB) {
		if tx.Statement.Table == "tool_results" {
			tx.AddError(errors.New("metadata unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(context.Background(), row, body, nil); err == nil {
		t.Fatal("expected metadata error")
	}
	if err := r.db.Callback().Query().Remove("metadata_down"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Recover(context.Background()); err != nil || n != 1 {
		t.Fatalf("recovery %d %v", n, err)
	}
}
