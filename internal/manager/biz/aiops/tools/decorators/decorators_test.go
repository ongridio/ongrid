package decorators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// fakeTool is a minimal BaseTool stub for decorator unit tests. It
// records calls and lets the test plant a result/error.
type fakeTool struct {
	name         string
	desc         string
	whenToUse    string
	params       json.RawMessage
	class        string
	confirmation string
	result       string
	err          error
	delay        time.Duration
	calls        int32
	gotArgs      string
	gotOpts      basetool.Resolved
	infoErr      error
	respectCtx   bool
}

func (f *fakeTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return &basetool.ToolInfo{
		Name:         f.name,
		Description:  f.desc,
		WhenToUse:    f.whenToUse,
		Parameters:   f.params,
		Class:        f.class,
		Confirmation: f.confirmation,
	}, nil
}

func (f *fakeTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	f.gotArgs = argsJSON
	f.gotOpts = basetool.ResolveOptions(opts)
	if f.delay > 0 {
		if f.respectCtx {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		} else {
			time.Sleep(f.delay)
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return f.result, nil
}

func TestTimeout_Fires(t *testing.T) {
	inner := &fakeTool{name: "slow", delay: 50 * time.Millisecond, respectCtx: true, result: "ok"}
	wrapped := WithTimeout(inner, 5*time.Millisecond)

	_, err := wrapped.InvokableRun(context.Background(), "{}")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrToolTimeout) {
		t.Errorf("expected ErrToolTimeout, got %v", err)
	}
}

func TestTimeout_PassesThroughOnFastTool(t *testing.T) {
	inner := &fakeTool{name: "fast", result: `{"ok":1}`}
	wrapped := WithTimeout(inner, 100*time.Millisecond)

	out, err := wrapped.InvokableRun(context.Background(), "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `{"ok":1}` {
		t.Errorf("got %q want %q", out, `{"ok":1}`)
	}
}

func TestTimeout_UnsetAddsNoDeadline(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		inner := &fakeTool{name: "x"}
		if got := WithTimeout(inner, d); got != inner {
			t.Fatalf("timeout %s unexpectedly wrapped tool", d)
		}
	}
}

// A real execution beyond the previous 15s cutoff guards the default chain,
// including callers that do not explicitly populate Deps.Timeout.
func TestWrap_LongToolWithoutImplicitDeadline(t *testing.T) {
	t.Parallel()
	inner := &fakeTool{name: "long_scan", delay: 16 * time.Second, respectCtx: true, result: "scan complete"}
	wrapped := Wrap(inner, Deps{Registerer: prometheus.NewRegistry()})
	out, err := wrapped.InvokableRun(context.Background(), "{}")
	if err != nil || out != "scan complete" {
		t.Fatalf("long scan = %q, %v", out, err)
	}
}

func TestWrap_UnsetTimeoutPreservesCallerCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			inner := &fakeTool{name: "cancel_scan", delay: time.Hour, respectCtx: true}
			wrapped := Wrap(inner, Deps{Registerer: prometheus.NewRegistry()})
			_, err := wrapped.InvokableRun(ctx, "{}")
			if !errors.Is(err, want) || errors.Is(err, ErrToolTimeout) {
				t.Fatalf("caller cancellation = %v, want %v", err, want)
			}
		})
	}
}

// fakeAuditSink records start + end events.
type fakeAuditSink struct {
	mu        sync.Mutex
	starts    []ToolStartEvent
	ends      []ToolEndEvent
	startIDs  []string
	startErr  error
	endErr    error
	nextIDSeq int
}

func (f *fakeAuditSink) OnToolStart(_ context.Context, ev ToolStartEvent) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, ev)
	if f.startErr != nil {
		return "", f.startErr
	}
	f.nextIDSeq++
	id := "audit-id-" + string(rune('A'+f.nextIDSeq-1))
	f.startIDs = append(f.startIDs, id)
	return id, nil
}

func (f *fakeAuditSink) OnToolEnd(_ context.Context, _ string, ev ToolEndEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ends = append(f.ends, ev)
	return f.endErr
}

func TestAudit_EmitsStartAndEnd(t *testing.T) {
	inner := &fakeTool{name: "tool_a", result: `{"hello":"world"}`}
	sink := &fakeAuditSink{}
	wrapped := WithAudit(inner, sink)

	out, err := wrapped.InvokableRun(context.Background(), `{"x":1}`,
		basetool.WithUserID(42), basetool.WithTenant("42"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if out != `{"hello":"world"}` {
		t.Errorf("result got %q", out)
	}
	if len(sink.starts) != 1 {
		t.Fatalf("expected 1 start, got %d", len(sink.starts))
	}
	if sink.starts[0].ToolName != "tool_a" {
		t.Errorf("start name %q", sink.starts[0].ToolName)
	}
	if sink.starts[0].ArgsJSON != `{"x":1}` {
		t.Errorf("start args %q", sink.starts[0].ArgsJSON)
	}
	if sink.starts[0].UserID != 42 {
		t.Errorf("start user %d", sink.starts[0].UserID)
	}
	if len(sink.ends) != 1 {
		t.Fatalf("expected 1 end, got %d", len(sink.ends))
	}
	if sink.ends[0].Err != nil {
		t.Errorf("end err = %v", sink.ends[0].Err)
	}
	if sink.ends[0].ResultJSON != `{"hello":"world"}` {
		t.Errorf("end result %q", sink.ends[0].ResultJSON)
	}
}

func TestAudit_StartErrorAborts(t *testing.T) {
	inner := &fakeTool{name: "tool_b", result: "ignored"}
	sink := &fakeAuditSink{startErr: errors.New("quota exceeded")}
	wrapped := WithAudit(inner, sink)

	_, err := wrapped.InvokableRun(context.Background(), "{}")
	if err == nil {
		t.Fatalf("expected error from start hook")
	}
	if atomic.LoadInt32(&inner.calls) != 0 {
		t.Errorf("inner should not be called when audit start fails")
	}
}

func TestAudit_EndErrorSwallowed(t *testing.T) {
	inner := &fakeTool{name: "tool_c", result: "ok"}
	sink := &fakeAuditSink{endErr: errors.New("db down")}
	wrapped := WithAudit(inner, sink)

	out, err := wrapped.InvokableRun(context.Background(), "{}")
	if err != nil {
		t.Fatalf("audit end errors must not surface to caller, got %v", err)
	}
	if out != "ok" {
		t.Errorf("result lost: %q", out)
	}
}

func TestAudit_NilSinkPassThrough(t *testing.T) {
	inner := &fakeTool{name: "tool_d", result: "ok"}
	wrapped := WithAudit(inner, nil)
	if wrapped != inner {
		t.Fatalf("nil sink should pass through, got %T", wrapped)
	}
}

func TestRateLimit_BlocksBurst(t *testing.T) {
	inner := &fakeTool{name: "rl_tool", result: "ok"}
	// 60/min => 1/sec. Burst = 60. Drain the bucket.
	limiter := NewTokenBucketLimiter(60)
	wrapped := WithRateLimit(inner, limiter)

	allowed := 0
	denied := 0
	for i := 0; i < 100; i++ {
		_, err := wrapped.InvokableRun(context.Background(), "{}", basetool.WithUserID(1))
		if err == nil {
			allowed++
			continue
		}
		if errors.Is(err, ErrRateLimited) {
			denied++
			continue
		}
		t.Fatalf("unexpected err: %v", err)
	}
	if allowed == 0 {
		t.Errorf("expected some calls to be allowed (burst should fire)")
	}
	if denied == 0 {
		t.Errorf("expected some calls to be denied past burst")
	}
}

func TestRateLimit_PerUserSeparation(t *testing.T) {
	inner := &fakeTool{name: "rl_tool", result: "ok"}
	limiter := NewTokenBucketLimiter(1) // 1/min, burst=1
	wrapped := WithRateLimit(inner, limiter)

	// User 1 burns their token.
	if _, err := wrapped.InvokableRun(context.Background(), "{}", basetool.WithUserID(1)); err != nil {
		t.Fatalf("user 1 first call: %v", err)
	}
	if _, err := wrapped.InvokableRun(context.Background(), "{}", basetool.WithUserID(1)); err == nil {
		t.Errorf("user 1 second call should be denied")
	}
	// User 2 has their own bucket.
	if _, err := wrapped.InvokableRun(context.Background(), "{}", basetool.WithUserID(2)); err != nil {
		t.Errorf("user 2 should not share user 1's bucket: %v", err)
	}
}

func TestRateLimit_NilLimiterPassThrough(t *testing.T) {
	inner := &fakeTool{name: "rl_tool", result: "ok"}
	wrapped := WithRateLimit(inner, nil)
	if wrapped != inner {
		t.Fatalf("nil limiter should pass through, got %T", wrapped)
	}
}

func TestNoopLimiter_AlwaysAllows(t *testing.T) {
	l := NoopLimiter{}
	for i := 0; i < 1000; i++ {
		if !l.Allow(context.Background(), "x", 1) {
			t.Fatalf("noop should always allow, denied at i=%d", i)
		}
	}
}

func TestMetric_Increments(t *testing.T) {
	reg := prometheus.NewRegistry()
	inner := &fakeTool{name: "metric_tool", result: "ok"}
	wrapped := WithMetric(inner, reg)

	if _, err := wrapped.InvokableRun(context.Background(), "{}"); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	failing := &fakeTool{name: "metric_tool", err: errors.New("boom")}
	wrappedFail := WithMetric(failing, reg)
	if _, err := wrappedFail.InvokableRun(context.Background(), "{}"); err == nil {
		t.Fatalf("expected error from failing inner")
	}

	// Use Gather() to inspect the registered counters.
	got := mustCounter(t, reg, "ongrid_tool_invocations_total", "metric_tool", "success")
	if got != 1 {
		t.Errorf("success counter = %f, want 1", got)
	}
	gotE := mustCounter(t, reg, "ongrid_tool_invocations_total", "metric_tool", "error")
	if gotE != 1 {
		t.Errorf("error counter = %f, want 1", gotE)
	}
}

// mustCounter reads counter ongrid_tool_invocations_total{name=..,result=..}
// from reg. Fails the test if not found.
func mustCounter(t *testing.T, reg *prometheus.Registry, metricName, name, result string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			matchName, matchResult := false, false
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "name":
					if lp.GetValue() == name {
						matchName = true
					}
				case "result":
					if lp.GetValue() == result {
						matchResult = true
					}
				}
			}
			if matchName && matchResult {
				if m.Counter != nil {
					return m.Counter.GetValue()
				}
			}
		}
	}
	t.Fatalf("metric %q with labels (name=%q, result=%q) not found", metricName, name, result)
	return 0
}

func TestTenantBind_InjectsTenantID(t *testing.T) {
	inner := &fakeTool{
		name:   "tenant_tool",
		params: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"integer"},"x":{"type":"string"}}}`),
		result: "ok",
	}
	wrapped := WithTenantBind(inner)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "user"})

	if _, err := wrapped.InvokableRun(ctx, `{"x":"hi"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(inner.gotArgs), &got); err != nil {
		t.Fatalf("decode args: %v (raw=%q)", err, inner.gotArgs)
	}
	if v, ok := got["tenant_id"]; !ok {
		t.Errorf("tenant_id not injected: %v", got)
	} else if int(v.(float64)) != 7 {
		t.Errorf("tenant_id = %v, want 7", v)
	}
	if got["x"] != "hi" {
		t.Errorf("original args mutated: %v", got)
	}
	// And opts thread the user id through.
	if inner.gotOpts.UserID != 7 {
		t.Errorf("UserID opt = %d, want 7", inner.gotOpts.UserID)
	}
}

func TestTenantBind_DoesNotOverwriteExisting(t *testing.T) {
	inner := &fakeTool{
		name:   "tenant_tool",
		params: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"integer"}}}`),
	}
	wrapped := WithTenantBind(inner)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7})
	if _, err := wrapped.InvokableRun(ctx, `{"tenant_id":99}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(inner.gotArgs, `"tenant_id":99`) {
		t.Errorf("model-supplied tenant_id should win, got %q", inner.gotArgs)
	}
}

func TestTenantBind_NoTenantOnCtxIsPassThrough(t *testing.T) {
	inner := &fakeTool{
		name:   "tenant_tool",
		params: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"integer"}}}`),
	}
	wrapped := WithTenantBind(inner)
	if _, err := wrapped.InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if inner.gotArgs != `{}` {
		t.Errorf("args mutated despite no tenant in ctx: %q", inner.gotArgs)
	}
}

func TestTenantBind_SkipsWhenSchemaLacksTenantID(t *testing.T) {
	inner := &fakeTool{
		name:   "no_tenant_tool",
		params: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
	}
	wrapped := WithTenantBind(inner)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7})
	if _, err := wrapped.InvokableRun(ctx, `{"x":"hi"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if inner.gotArgs != `{"x":"hi"}` {
		t.Errorf("args should be unchanged for tools that don't declare tenant_id, got %q", inner.gotArgs)
	}
	// User id still threaded onto opts (audit/ratelimit need it).
	if inner.gotOpts.UserID != 7 {
		t.Errorf("UserID opt = %d, want 7", inner.gotOpts.UserID)
	}
}

// TestChain_RunsAllInOrder asserts the composed Wrap() applies all five
// decorators in the documented order: tenant_bind → timeout → audit →
// ratelimit → metric. We verify by observing each layer's side
// effect after one successful call.
func TestChain_RunsAllInOrder(t *testing.T) {
	inner := &fakeTool{
		name:   "chain_tool",
		params: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"integer"}}}`),
		result: "ok",
	}
	sink := &fakeAuditSink{}
	limiter := NewTokenBucketLimiter(60)
	reg := prometheus.NewRegistry()

	wrapped := Wrap(inner, Deps{
		Timeout:    50 * time.Millisecond,
		Audit:      sink,
		Limiter:    limiter,
		Registerer: reg,
	})

	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 5})
	out, err := wrapped.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if out != "ok" {
		t.Errorf("result lost: %q", out)
	}

	// tenant_bind: args got tenant_id=5 injected before reaching inner.
	if !strings.Contains(inner.gotArgs, `"tenant_id":5`) {
		t.Errorf("tenant_bind not applied, args=%q", inner.gotArgs)
	}
	// audit: start + end fired.
	if len(sink.starts) != 1 || len(sink.ends) != 1 {
		t.Errorf("audit hooks fired %d/%d, want 1/1", len(sink.starts), len(sink.ends))
	}
	// metric: invocations counter ticked.
	if got := mustCounter(t, reg, "ongrid_tool_invocations_total", "chain_tool", "success"); got != 1 {
		t.Errorf("metric counter = %f, want 1", got)
	}
	// ratelimit: confirm by exhausting the limiter — set up a tight
	// limiter and verify Wrap() includes it.
	tightLim := NewTokenBucketLimiter(1)
	tightWrapped := Wrap(&fakeTool{name: "tight_tool", result: "x"}, Deps{
		Timeout: 50 * time.Millisecond,
		Limiter: tightLim,
	})
	if _, err := tightWrapped.InvokableRun(ctx, `{}`); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := tightWrapped.InvokableRun(ctx, `{}`); err == nil || !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited on second call, got %v", err)
	}
}

func TestChain_TimeoutStillAudited(t *testing.T) {
	// Audit start should fire before the timeout wrapper kicks in,
	// proving the chain order — timeout outside audit means a
	// chat_tool_calls row exists even on timeout.
	inner := &fakeTool{
		name:       "slow_tool",
		delay:      100 * time.Millisecond,
		respectCtx: true,
	}
	sink := &fakeAuditSink{}
	wrapped := Wrap(inner, Deps{
		Timeout: 5 * time.Millisecond,
		Audit:   sink,
	})

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !errors.Is(err, ErrToolTimeout) {
		t.Errorf("expected ErrToolTimeout, got %v", err)
	}
	if len(sink.starts) != 1 {
		t.Errorf("audit start should fire even on timeout, got %d", len(sink.starts))
	}
	if len(sink.ends) != 1 {
		t.Errorf("audit end should fire even on timeout, got %d", len(sink.ends))
	}
	if sink.ends[0].Err == nil {
		t.Errorf("audit end err = nil, want timeout err")
	}
}

func TestChain_NilDepsAreSafe(t *testing.T) {
	inner := &fakeTool{name: "bare", result: "ok"}
	wrapped := Wrap(inner, Deps{}) // all zero
	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("bare chain failed: %v", err)
	}
	if out != "ok" {
		t.Errorf("result lost: %q", out)
	}
}

func TestChain_NilInnerReturnsNil(t *testing.T) {
	if got := Wrap(nil, Deps{}); got != nil {
		t.Errorf("Wrap(nil) = %T, want nil", got)
	}
}
