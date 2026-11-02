package aiops

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	"github.com/ongridio/ongrid/internal/manager/biz/approval"
	"github.com/ongridio/ongrid/internal/manager/data/approval/store"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
)

func TestHumanApprovalExecutorIsSingleUseAndFailsClosedAfterRestart(t *testing.T) {
	const payload = `{"execution_token":"approved-token","tool_name":"test"}`
	broker := NewHumanApprovalBroker()
	var calls, successes atomic.Int32
	broker.pending["approved-token"] = pendingToolExecution{execute: func(context.Context) (string, error) {
		calls.Add(1)
		return `{"status":"executed"}`, nil
	}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := broker.Execute(context.Background(), payload); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || successes.Load() != 1 {
		t.Fatalf("executor calls = %d, successful approvals = %d", calls.Load(), successes.Load())
	}
	if _, err := NewHumanApprovalBroker().Execute(context.Background(), payload); err == nil {
		t.Fatal("a new broker reconstructed a lost execution capability")
	}
}

func TestHumanApprovalExecutorRejectsWrongRuntime(t *testing.T) {
	broker := NewHumanApprovalBroker()
	calls := 0
	broker.pending["pinned"] = pendingToolExecution{version: contract.V3, execute: func(context.Context) (string, error) { calls++; return `{}`, nil }}
	_, err := broker.Execute(contract.WithVersion(context.Background(), contract.V2), `{"execution_token":"pinned"}`)
	if err == nil || calls != 0 {
		t.Fatalf("cross-runtime executor ran: calls=%d err=%v", calls, err)
	}
	if _, err := broker.Execute(contract.WithVersion(context.Background(), contract.V3), `{"execution_token":"pinned"}`); err == nil {
		t.Fatal("rejected capability reused")
	}
}

func TestHumanApprovalExecutorAcceptsOriginRuntimeOnce(t *testing.T) {
	broker := NewHumanApprovalBroker()
	broker.pending["pinned"] = pendingToolExecution{version: contract.V3, execute: func(ctx context.Context) (string, error) {
		if contract.VersionFromContext(ctx) != contract.V3 {
			t.Fatal("origin lost")
		}
		return `{"ok":true}`, nil
	}}
	ctx := contract.WithVersion(context.Background(), contract.V3)
	out, err := broker.Execute(ctx, `{"execution_token":"pinned"}`)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("execution=%q %v", out, err)
	}
	if _, err := broker.Execute(ctx, `{"execution_token":"pinned"}`); err == nil {
		t.Fatal("capability reused")
	}
}

func TestHumanApprovalBrokerReturnsDecisionEvidenceAfterApproval(t *testing.T) {
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
	uc := approval.NewUsecase(store.NewRepo(db), nil)
	uc.RequireAuthorization(approval.AuthorizerFunc(func(context.Context, uint64) error { return nil }))
	broker := NewHumanApprovalBroker()
	broker.SetUsecase(uc)
	uc.RegisterRuntimeExecutor(GenericToolApprovalKind, "v3", broker.Execute)
	ctx, cancel := context.WithTimeout(contract.WithVersion(context.Background(), contract.V3), 10*time.Second)
	defer cancel()
	events := make(chan string, 1)
	ctx = contract.WithEmit(ctx, func(e contract.Event) {
		if e.Type == contract.EventApprovalPending {
			events <- e.Approval.ApprovalID
		}
	})
	var executions atomic.Int32
	type outcome struct {
		value string
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := broker.ProposeAndAwait(ctx, decorators.HumanApprovalRequest{ToolName: "test_write", ArgsJSON: "{}", SessionID: "test-session", UserID: 42}, func(context.Context) (string, error) { executions.Add(1); return `{"ok":true}`, nil })
		done <- outcome{value, err}
	}()
	var id string
	select {
	case id = <-events:
	case <-ctx.Done():
		t.Fatal("approval not proposed")
	}
	if executions.Load() != 0 {
		t.Fatal("executed before approval")
	}
	select {
	case <-done:
		t.Fatal("returned before approval")
	default:
	}
	if _, err := uc.Approve(ctx, 42, id); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		var result struct {
			OK       bool                                  `json:"ok"`
			Approval struct{ ID, Decision, Status string } `json:"_ongrid_approval"`
		}
		if err := json.Unmarshal([]byte(got.value), &result); err != nil {
			t.Fatal(err)
		}
		if !result.OK || result.Approval.ID != id || result.Approval.Decision != "approved" || result.Approval.Status != "executed" {
			t.Fatalf("missing result or decision: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("approval did not resume")
	}
	if executions.Load() != 1 {
		t.Fatal("execution count incorrect")
	}
}
