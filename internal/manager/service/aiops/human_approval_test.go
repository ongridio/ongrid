package aiops

import (
	"context"
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
