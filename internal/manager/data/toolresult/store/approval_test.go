package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	reader, err := r.Open(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(io.LimitReader(reader, 100))
	if err = errors.Join(readErr, reader.Close()); err != nil {
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

func TestRecoveryRefreshesTerminalApprovalWithoutChangingExecution(t *testing.T) {
	for _, failed := range []bool{false, true} {
		r := testRepo(t)
		if err := approvalstore.Migrate(r.db); err != nil {
			t.Fatal(err)
		}
		sid := uuid.NewString()
		uc := approval.NewUsecase(approvalstore.NewRepo(r.db), nil)
		uc.SetToolResults(biz.New(r, ownerSession{sid}, []byte("key"), nil))
		uc.RequireAuthorization(approval.AuthorizerFunc(func(context.Context, uint64) error { return nil }))
		calls := 0
		uc.RegisterExecutor("test", func(context.Context, string) (string, error) {
			calls++
			if failed {
				return "", errors.New("command failed")
			}
			return strings.Repeat("saved\n", 20000), nil
		})
		a, err := uc.Propose(context.Background(), approval.ProposeInput{Kind: "test", Title: "test", Source: "agent", SessionID: sid, ProposedBy: 7, Payload: map[string]string{}})
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
		a, err = uc.Approve(context.Background(), 9, a.ID)
		if err != nil || a.ResultJSON == nil || a.ExecutedAt == nil {
			t.Fatalf("approval did not complete: %+v %v", a, err)
		}
		pending := *a.ResultJSON
		var before map[string]json.RawMessage
		if err = json.Unmarshal([]byte(pending), &before); err != nil || string(before["storage_status"]) != `"pending"` {
			t.Fatalf("expected pending view: %s %v", pending, err)
		}
		if err = r.db.Callback().Create().Remove("fail_result"); err != nil {
			t.Fatal(err)
		}
		for pass := 0; pass < 3; pass++ {
			// A delayed completion write can restore the pending view after publication.
			if pass == 1 {
				if err = r.db.Model(a).Update("result_json", pending).Error; err != nil {
					t.Fatal(err)
				}
			}
			want := 0
			if pass == 0 {
				want = 1
			}
			if n, err := r.Recover(context.Background()); err != nil || n != want {
				t.Fatalf("pass %d: recovery=%d %v", pass, n, err)
			}
			got, err := uc.Get(context.Background(), a.ID)
			if err != nil || got.Status != a.Status || got.ExecutedAt == nil || !got.ExecutedAt.Equal(*a.ExecutedAt) || calls != 1 {
				t.Fatalf("execution changed: %+v calls=%d err=%v", got, calls, err)
			}
			var after map[string]json.RawMessage
			if err = json.Unmarshal([]byte(*got.ResultJSON), &after); err != nil || string(after["storage_status"]) != `"ready"` || len(after["instruction"]) != 0 {
				t.Fatalf("stale recovery view: %s %v", *got.ResultJSON, err)
			}
			for _, key := range []string{"result_id", "status", "execution_status", "capture_status", "preview"} {
				if string(after[key]) != string(before[key]) {
					t.Fatalf("recovery changed %s", key)
				}
			}
		}
		// Never use an old manifest to replace a different result association.
		if err = r.db.Model(a).Updates(map[string]any{"result_id": uuid.NewString(), "result_json": pending}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = r.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := uc.Get(context.Background(), a.ID)
		if err != nil || *got.ResultJSON != pending {
			t.Fatalf("unrelated result overwritten: %+v %v", got, err)
		}
	}
}
