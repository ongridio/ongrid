package approval_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/ongridio/ongrid/internal/manager/biz/approval"
	"github.com/ongridio/ongrid/internal/manager/data/approval/store"
	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func approvalRepo(t *testing.T) *store.Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "approval.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return store.NewRepo(db)
}
func propose(t *testing.T, uc *approval.Usecase) string {
	t.Helper()
	a, err := uc.Propose(context.Background(), approval.ProposeInput{Kind: "action", Title: "test", Payload: struct{}{}, ProposedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}
func TestUnavailableExecutorFailsHistoricalApproval(t *testing.T) {
	for _, registerNil := range []bool{false, true} {
		repo := approvalRepo(t)
		uc := approval.NewUsecase(repo, nil)
		id := propose(t, uc)
		if registerNil {
			uc.RegisterExecutor("action", nil)
		}
		a, err := uc.Approve(context.Background(), 9, id)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status != model.StatusFailed || a.ResultJSON == nil || !strings.Contains(*a.ResultJSON, "unavailable") {
			t.Fatalf("approval=%+v", a)
		}
		if _, err := uc.Approve(context.Background(), 9, id); err == nil {
			t.Fatal("failed action approved twice")
		}
		stored, err := repo.Get(context.Background(), id)
		if err != nil || stored.ApprovedBy == nil || *stored.ApprovedBy != 9 || stored.PayloadJSON != "{}" {
			t.Fatalf("history=%+v err=%v", stored, err)
		}
	}
}
func TestConcurrentApprovalExecutesOnce(t *testing.T) {
	uc := approval.NewUsecase(approvalRepo(t), nil)
	var executed atomic.Int32
	uc.RegisterExecutor("action", func(context.Context, string) (string, error) { executed.Add(1); return `{}`, nil })
	id := propose(t, uc)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := uc.Approve(context.Background(), 9, id)
			if err != nil && !errors.Is(err, errs.ErrNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if executed.Load() != 1 {
		t.Fatalf("executed=%d", executed.Load())
	}
	a, err := uc.Get(context.Background(), id)
	if err != nil || a.Status != model.StatusExecuted {
		t.Fatalf("approval=%+v err=%v", a, err)
	}
}

type failingResultRepo struct {
	approval.Repo
	err error
}

func (r failingResultRepo) SetResult(context.Context, string, string, string, time.Time) error {
	return r.err
}
func TestApprovalResultPersistenceFailureIsReturned(t *testing.T) {
	failure := errors.New("result storage unavailable")
	uc := approval.NewUsecase(failingResultRepo{Repo: approvalRepo(t), err: failure}, nil)
	uc.RegisterExecutor("action", func(context.Context, string) (string, error) { return `{}`, nil })
	if _, err := uc.Approve(context.Background(), 9, propose(t, uc)); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
}

func TestInvalidationCannotOverwriteDecisionOrPermitLaterExecution(t *testing.T) {
	for _, decision := range []string{"pending", "approved", "rejected"} {
		t.Run(decision, func(t *testing.T) {
			uc := approval.NewUsecase(approvalRepo(t), nil)
			uc.RegisterExecutor("action", func(context.Context, string) (string, error) { return `{}`, nil })
			id := propose(t, uc)
			want := model.StatusFailed
			switch decision {
			case "approved":
				if _, err := uc.Approve(context.Background(), 9, id); err != nil {
					t.Fatal(err)
				}
				want = model.StatusExecuted
			case "rejected":
				if err := uc.Reject(context.Background(), 9, id, "denied"); err != nil {
					t.Fatal(err)
				}
				want = model.StatusRejected
			}
			if err := uc.InvalidatePending(context.Background(), id, "expired"); err != nil {
				t.Fatal(err)
			}
			a, err := uc.Get(context.Background(), id)
			if err != nil || a.Status != want {
				t.Fatalf("approval=%+v err=%v", a, err)
			}
			if _, err := uc.Approve(context.Background(), 9, id); err == nil {
				t.Fatal("terminal proposal executed again")
			}
		})
	}
}

func TestApproval_RuntimeOwnershipSurvivesReloadAndNeverFallsBack(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(fmt.Sprint("origin_available=", available), func(t *testing.T) {
			repo := approvalRepo(t)
			producer := approval.NewUsecase(repo, nil)
			row, err := producer.Propose(context.Background(), approval.ProposeInput{Kind: "action", Title: "pinned", Payload: struct{}{}, RuntimeVersion: "v2"})
			if err != nil {
				t.Fatal(err)
			}
			// Reconstruct the usecase to prove routing is based on the stored record.
			consumer := approval.NewUsecase(repo, nil)
			var originCalls, otherCalls atomic.Int32
			wrong := func(context.Context, string) (string, error) { otherCalls.Add(1); return `{}`, nil }
			consumer.RegisterExecutor("action", wrong)
			consumer.RegisterRuntimeExecutor("action", "v3", wrong)
			if available {
				consumer.RegisterRuntimeExecutor("action", "v2", func(context.Context, string) (string, error) { originCalls.Add(1); return `{"origin":"v2"}`, nil })
			}
			result, err := consumer.Approve(context.Background(), 9, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if result.RuntimeVersion != "v2" || otherCalls.Load() != 0 {
				t.Fatalf("runtime leaked: %+v, other=%d", result, otherCalls.Load())
			}
			if available {
				if result.Status != model.StatusExecuted || originCalls.Load() != 1 {
					t.Fatalf("origin failed: %+v", result)
				}
			} else if result.Status != model.StatusFailed {
				t.Fatalf("missing origin did not fail: %+v", result)
			}
			if _, err = consumer.Approve(context.Background(), 9, row.ID); err == nil {
				t.Fatal("terminal approval executed again")
			}
		})
	}
}

func TestApproval_RuntimeRegistrationConcurrentWithExecution(t *testing.T) {
	uc := approval.NewUsecase(approvalRepo(t), nil)
	row, err := uc.Propose(context.Background(), approval.ProposeInput{Kind: "action", Title: "concurrent", Payload: struct{}{}, RuntimeVersion: "v3"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	execute := func(context.Context, string) (string, error) { calls.Add(1); return `{}`, nil }
	uc.RegisterRuntimeExecutor("action", "v3", execute)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			uc.RegisterRuntimeExecutor("action", "v3", execute)
		}
	}()
	result, err := uc.Approve(context.Background(), 9, row.ID)
	wg.Wait()
	if err != nil || result.Status != model.StatusExecuted || calls.Load() != 1 {
		t.Fatalf("execution=%+v %v calls=%d", result, err, calls.Load())
	}
}
