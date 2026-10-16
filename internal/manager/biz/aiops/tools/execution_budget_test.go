package tools

import (
	"context"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type budgetCaller struct {
	t              *testing.T
	min            time.Duration
	parentDeadline time.Time
}

func (c budgetCaller) Call(ctx context.Context, _ uint64, method string, _ []byte) ([]byte, error) {
	c.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		c.t.Fatal("missing execution deadline")
	}
	if !c.parentDeadline.IsZero() {
		if !deadline.Equal(c.parentDeadline) {
			c.t.Fatalf("caller deadline changed: %v", deadline)
		}
	} else if time.Until(deadline) < c.min {
		c.t.Fatalf("execution budget too short: %s", time.Until(deadline))
	}
	if method == tunnel.MethodBashExec {
		return []byte(`{"allowed":true}`), nil
	}
	return []byte(`{"results":[]}`), nil
}

func TestExecutionBudgets_AllowLongWorkAndPreserveCallerDeadline(t *testing.T) {
	for _, short := range []bool{false, true} {
		ctx := context.Background()
		caller := budgetCaller{t: t}
		if short {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Second)
			defer cancel()
			caller.parentDeadline, _ = ctx.Deadline()
		}
		// A full batch has four waves, then df metadata and response delivery.
		caller.min = 20*time.Minute + 30*time.Second
		if _, err := dispatchEdgeCall(ctx, caller, 7, tunnel.MethodDuSummary, struct{}{}, "du_summary"); err != nil {
			t.Fatal(err)
		}
		caller.min = 5 * time.Minute
		tool := &BashTool{resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, caller: caller}
		if _, err := tool.InvokableRun(ctx, `{"device_ids":[1],"cmd":"du /var","timeout_seconds":300}`); err != nil {
			t.Fatal(err)
		}
	}
}
