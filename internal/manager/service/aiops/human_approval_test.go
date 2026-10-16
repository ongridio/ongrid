package aiops

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHumanApprovalExecutorIsSingleUseAndFailsClosedAfterRestart(t *testing.T) {
	const payload = `{"execution_token":"approved-token","tool_name":"test"}`
	broker := NewHumanApprovalBroker()
	var calls, successes atomic.Int32
	broker.pending["approved-token"] = func(context.Context) (string, error) {
		calls.Add(1)
		return `{"status":"executed"}`, nil
	}
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
