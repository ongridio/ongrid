package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// fakeBaseTool is the test double used by tool_adapter + react tests.
// It records every invocation so assertions can inspect args + opts.
type fakeBaseTool struct {
	name        string
	desc        string
	whenToUse   string
	parameters  string // raw JSON Schema
	class       string
	infoErr     error
	runErr      error
	runResp     string
	calls       atomic.Int32
	lastArgs    string
	lastOptsLen int
}

type recordingToolPersistence struct {
	mu     sync.Mutex
	starts []string
	ends   []string
}

type concurrentCountTool struct {
	name  string
	class string
	calls atomic.Int32
}

func (t *concurrentCountTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:       t.name,
		Class:      t.class,
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
	}, nil
}

func (t *concurrentCountTool) InvokableRun(_ context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	t.calls.Add(1)
	return `{"ok":true}`, nil
}

func (r *recordingToolPersistence) PersistToolStart(_ context.Context, name, args string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, name+":"+args)
}

func (r *recordingToolPersistence) PersistToolEnd(_ context.Context, name, response string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ends = append(r.ends, name+":"+response)
}

func (r *recordingToolPersistence) records() (starts, ends []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.starts...), append([]string(nil), r.ends...)
}

func (f *fakeBaseTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return &basetool.ToolInfo{
		Name:        f.name,
		Description: f.desc,
		WhenToUse:   f.whenToUse,
		Parameters:  json.RawMessage(f.parameters),
		Class:       f.class,
	}, nil
}

func (f *fakeBaseTool) InvokableRun(_ context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	f.calls.Add(1)
	f.lastArgs = argsJSON
	f.lastOptsLen = len(opts)
	if f.runErr != nil {
		return "", f.runErr
	}
	return f.runResp, nil
}

func TestWrapBaseTool_NilInner(t *testing.T) {
	t.Parallel()
	if got := WrapBaseTool(nil); got != nil {
		t.Fatalf("WrapBaseTool(nil) = %v, want nil", got)
	}
}

func TestWrapBaseTool_InfoMergesWhenToUse(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{
		name:       "search",
		desc:       "search the web",
		whenToUse:  "user explicitly asks the public web",
		parameters: `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`,
	}
	wrapped := WrapBaseTool(inner)
	info, err := wrapped.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "search" {
		t.Errorf("Name = %q, want search", info.Name)
	}
	if !strings.Contains(info.Desc, "search the web") {
		t.Errorf("Desc missing description body: %q", info.Desc)
	}
	if !strings.Contains(info.Desc, "When to use") {
		t.Errorf("Desc missing when-to-use header: %q", info.Desc)
	}
	if !strings.Contains(info.Desc, "user explicitly asks the public web") {
		t.Errorf("Desc missing when-to-use body: %q", info.Desc)
	}
	if info.ParamsOneOf == nil {
		t.Errorf("ParamsOneOf is nil; expected populated")
	}
}

func TestWrapBaseTool_InfoNoWhenToUse(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "ping", desc: "ping a host"}
	wrapped := WrapBaseTool(inner)
	info, err := wrapped.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Desc != "ping a host" {
		t.Errorf("Desc = %q, want %q", info.Desc, "ping a host")
	}
}

func TestWrapBaseTool_InfoEmptyParameters(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "noargs"}
	wrapped := WrapBaseTool(inner)
	info, err := wrapped.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ParamsOneOf != nil {
		t.Errorf("ParamsOneOf should be nil when no parameters declared")
	}
}

func TestWrapBaseTool_InfoBadJSONSchemaFails(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "bad", parameters: `{not valid json`}
	wrapped := WrapBaseTool(inner)
	if _, err := wrapped.Info(context.Background()); err == nil {
		t.Fatalf("expected Info to fail on invalid JSON Schema")
	}
}

func TestWrapBaseTool_InfoBubbleInnerErr(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "x", infoErr: errors.New("boom")}
	wrapped := WrapBaseTool(inner)
	if _, err := wrapped.Info(context.Background()); err == nil {
		t.Fatalf("expected Info to bubble inner error")
	}
}

func TestWrapBaseTool_InvokableRunForwardsArgs(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "echo", runResp: `{"ok":true}`}
	wrapped := WrapBaseTool(inner)
	out, err := wrapped.InvokableRun(context.Background(), `{"a":1}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if out != `{"ok":true}` {
		t.Errorf("response = %q, want %q", out, `{"ok":true}`)
	}
	if inner.calls.Load() != 1 {
		t.Errorf("inner call count = %d, want 1", inner.calls.Load())
	}
	if inner.lastArgs != `{"a":1}` {
		t.Errorf("lastArgs = %q, want %q", inner.lastArgs, `{"a":1}`)
	}
}

func TestWrapBaseTool_PersistsActualInvocationAtAdapterBoundary(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "host_bash", class: "read", runResp: `{"devices":[{"ok":true}]}`}
	sink := &recordingToolPersistence{}
	wrapped := WrapBaseTool(inner)
	out, err := wrapped.InvokableRun(context.Background(), `{"device_ids":[1],"cmd":"docker images"}`,
		WithToolInvocationPersistence(sink),
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	starts, ends := sink.records()
	if len(starts) != 1 || starts[0] != `host_bash:{"device_ids":[1],"cmd":"docker images"}` {
		t.Fatalf("start records = %v", starts)
	}
	if len(ends) != 1 || ends[0] != "host_bash:"+out {
		t.Fatalf("end records = %v", ends)
	}
}

func TestWrapBaseTool_WithInvokeOpts(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "echo", runResp: `{}`}
	wrapped := WrapBaseTool(inner)
	_, err := wrapped.InvokableRun(context.Background(), `{}`,
		WithInvokeOpts(basetool.WithUserID(42), basetool.WithTenant("acme")),
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if inner.lastOptsLen != 2 {
		t.Errorf("inner saw %d opts, want 2", inner.lastOptsLen)
	}
}

func TestWrapBaseTool_RunErrIntoEnvelope(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "bad", runErr: errors.New("boom: sandbox path /etc not allowed")}
	wrapped := WrapBaseTool(inner)
	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	// Adapter folds tool errors into a JSON envelope so eino's ToolsNode
	// doesn't treat them as graph-fatal. The LLM consumes the envelope
	// as a tool result and decides to retry / switch / ask the user.
	if err != nil {
		t.Fatalf("err should be nil (folded into envelope), got %v", err)
	}
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "boom") {
		t.Errorf("envelope should carry error text, got %q", out)
	}
	if !strings.Contains(out, `"status":"failed"`) {
		t.Errorf("envelope should mark status=failed, got %q", out)
	}
}

func TestWrapBaseTools_SkipsNil(t *testing.T) {
	t.Parallel()
	tools := []basetool.BaseTool{nil, &fakeBaseTool{name: "a"}, nil, &fakeBaseTool{name: "b"}}
	out := WrapBaseTools(tools)
	if len(out) != 2 {
		t.Fatalf("WrapBaseTools dropped wrong count: got %d entries, want 2", len(out))
	}
}

// Per-run memo: an identical read-tool call returns the cached result
// without re-executing; distinct args re-execute.
func TestEinoToolAdapter_MemoizesIdenticalReadCalls(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "query_promql", class: "read", runResp: `{"v":1}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()
	r1, err1 := a.InvokableRun(ctx, `{"q":"up"}`)
	r2, err2 := a.InvokableRun(ctx, `{"q":"up"}`)
	if err1 != nil || err2 != nil {
		t.Fatalf("errs: %v %v", err1, err2)
	}
	if r1 != `{"v":1}` || r2 != `{"v":1}` {
		t.Fatalf("results = %q, %q; want cached identical", r1, r2)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Errorf("identical read calls should execute once, got %d", got)
	}
	if _, err := a.InvokableRun(ctx, `{"q":"down"}`); err != nil {
		t.Fatalf("distinct call err: %v", err)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Errorf("distinct args should re-execute, got %d", got)
	}
}

// Write/destructive tools are never memoized — the review/mutation flow
// must see every call.
func TestEinoToolAdapter_DoesNotMemoizeWriteTool(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "host_restart_service", class: "destructive", runResp: `{"ok":true}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()
	_, _ = a.InvokableRun(ctx, `{"svc":"nginx"}`)
	_, _ = a.InvokableRun(ctx, `{"svc":"nginx"}`)
	if got := inner.calls.Load(); got != 2 {
		t.Errorf("destructive tool must NOT be memoized; want 2 executions, got %d", got)
	}
}

// A failed call stays retryable — the error envelope is not cached.
func TestEinoToolAdapter_DoesNotMemoizeErrors(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "query_x", class: "read", runErr: errors.New("boom")}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()
	_, _ = a.InvokableRun(ctx, `{"q":"a"}`)
	_, _ = a.InvokableRun(ctx, `{"q":"a"}`)
	if got := inner.calls.Load(); got != 2 {
		t.Errorf("errored read calls must stay retryable; want 2 executions, got %d", got)
	}
}

// After maxToolCallsPerRun distinct executions, the tool stops running and
// returns a "synthesize now" directive (catches the varying-args repeat loop
// the memo can't).
func TestEinoToolAdapter_PerToolCallCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "query_x", class: "read", runResp: `{"v":1}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()
	// Distinct args each time so the identical-call memo doesn't short-circuit;
	// the cap counts executions.
	for i := 0; i < maxToolCallsPerRun; i++ {
		out, _ := a.InvokableRun(ctx, fmt.Sprintf(`{"q":"m%d"}`, i))
		if strings.Contains(out, "call_budget_exceeded") {
			t.Fatalf("call %d should execute, got budget directive: %s", i, out)
		}
	}
	if got := inner.calls.Load(); got != int32(maxToolCallsPerRun) {
		t.Fatalf("expected %d executions, got %d", maxToolCallsPerRun, got)
	}
	// One past the cap → directive, no execution.
	out, _ := a.InvokableRun(ctx, `{"q":"over"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Errorf("past the cap should return the budget directive, got %q", out)
	}
	if got := inner.calls.Load(); got != int32(maxToolCallsPerRun) {
		t.Errorf("over-cap call must NOT execute; still want %d, got %d", maxToolCallsPerRun, got)
	}
}

func TestEinoToolAdapter_QueryPromQLUsesStrictCallCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "query_promql", class: "read", runResp: `{"v":1}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()
	limit := maxCallsForTool("query_promql")

	if limit != 10 {
		t.Fatalf("query_promql limit = %d, want 10", limit)
	}
	for i := 0; i < limit; i++ {
		out, _ := a.InvokableRun(ctx, fmt.Sprintf(`{"q":"m%d"}`, i))
		if strings.Contains(out, "call_budget_exceeded") {
			t.Fatalf("call %d should execute, got budget directive: %s", i, out)
		}
	}
	out, _ := a.InvokableRun(ctx, `{"q":"over"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("past query_promql cap should return budget directive, got %q", out)
	}
	if !strings.Contains(out, "aggregated PromQL") {
		t.Fatalf("query_promql cap should nudge aggregation, got %q", out)
	}
	if got := inner.calls.Load(); got != int32(limit) {
		t.Fatalf("query_promql executions = %d, want %d", got, limit)
	}
}

func TestEinoToolAdapter_ReadToolsUseGenericCallCap(t *testing.T) {
	t.Parallel()
	for name, wantLimit := range map[string]int{
		"AgentTool":             maxToolCallsPerRun,
		"query_logql":           maxToolCallsPerRun,
		"query_traceql":         maxToolCallsPerRun,
		"host_bash":             maxToolCallsPerRun,
		"host_du_summary":       maxToolCallsPerRun,
		"host_find_large_files": maxToolCallsPerRun,
		"query_k8s_snapshot":    maxToolCallsPerRun,
		"ToolSearch":            maxToolCallsPerRun,
	} {
		name := name
		wantLimit := wantLimit
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			limit := maxCallsForTool(name)
			if limit != wantLimit {
				t.Fatalf("%s limit = %d, want %d", name, limit, wantLimit)
			}
			inner := &fakeBaseTool{name: name, class: "read", runResp: `{"v":1}`}
			a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
			ctx := context.Background()
			for i := 0; i < limit; i++ {
				out, _ := a.InvokableRun(ctx, fmt.Sprintf(`{"q":"m%d"}`, i))
				if strings.Contains(out, "call_budget_exceeded") {
					t.Fatalf("call %d should execute, got budget directive: %s", i, out)
				}
			}
			out, _ := a.InvokableRun(ctx, `{"q":"over"}`)
			if !strings.Contains(out, "call_budget_exceeded") || !strings.Contains(out, `"final_answer_required":false`) {
				t.Fatalf("past %s cap should block only that tool, got %q", name, out)
			}
			if got := inner.calls.Load(); got != int32(limit) {
				t.Fatalf("%s executions = %d, want %d", name, got, limit)
			}
		})
	}
}

func TestEinoToolAdapter_PerToolCallCapReservesBeforeConcurrentExecution(t *testing.T) {
	t.Parallel()
	inner := &concurrentCountTool{name: "host_bash", class: "read"}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()
	limit := maxCallsForTool("host_bash")
	start := make(chan struct{})
	var wg sync.WaitGroup
	outs := make(chan string, limit*3)
	for i := 0; i < limit*3; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, _ := a.InvokableRun(ctx, fmt.Sprintf(`{"cmd":"echo %d"}`, i))
			outs <- out
		}()
	}
	close(start)
	wg.Wait()
	close(outs)

	var budgeted int
	for out := range outs {
		if strings.Contains(out, "call_budget_exceeded") {
			budgeted++
		}
	}
	if got := inner.calls.Load(); got != int32(limit) {
		t.Fatalf("concurrent executions = %d, want exactly limit %d", got, limit)
	}
	if budgeted == 0 {
		t.Fatalf("expected over-limit concurrent calls to receive budget results")
	}
}

func TestEinoToolAdapter_DoesNotCapAggregateToolCalls(t *testing.T) {
	t.Parallel()
	memo := newToolMemo()
	ctx := context.Background()
	var executed int32
	const distinctTools = 24
	for i := 0; i < distinctTools; i++ {
		inner := &concurrentCountTool{name: fmt.Sprintf("tool_%02d", i), class: "read"}
		a := &einoToolAdapter{inner: inner, memo: memo}
		out, _ := a.InvokableRun(ctx, `{}`)
		if strings.Contains(out, "call_budget_exceeded") {
			t.Fatalf("distinct tool %d was blocked by an aggregate cap: %s", i, out)
		}
		executed += inner.calls.Load()
	}
	if executed != distinctTools {
		t.Fatalf("executed = %d, want all %d distinct tools", executed, distinctTools)
	}
}

func TestEinoToolAdapter_DraftConfigChangeConfirmableDraftCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_draft","draft_hash":"sha256:ok"}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	out, _ := a.InvokableRun(ctx, `{"rule":"one"}`)
	if strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("first draft should execute, got budget directive: %s", out)
	}
	out, _ = a.InvokableRun(ctx, `{"rule":"two"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("second draft should be capped, got %q", out)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("draft executions = %d, want 1", got)
	}
}

func TestEinoToolAdapter_DraftConfigChangeRequiresMetricCatalogForMetricRules(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_draft"}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	out, err := a.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","rule":{"kind":"metric_threshold","conditions":[{"metric":"cpu_pct","operator":">","threshold":85}]}}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if !strings.Contains(out, "metric_catalog_required") {
		t.Fatalf("metric draft without catalog should be blocked, got %q", out)
	}
	if got := inner.calls.Load(); got != 0 {
		t.Fatalf("draft executions = %d, want 0", got)
	}
}

func TestEinoToolAdapter_DraftConfigChangeAllowsAfterMetricCatalog(t *testing.T) {
	t.Parallel()
	memo := newToolMemo()
	catalog := &fakeBaseTool{
		name:    "list_metric_catalog",
		class:   "read",
		runResp: `{"status":"ok","metric_count":1,"returned":1,"metrics":[{"name":"node_cpu_seconds_total"}]}`,
	}
	draft := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_draft","draft_hash":"sha256:ok"}`}
	catalogAdapter := &einoToolAdapter{inner: catalog, memo: memo}
	draftAdapter := &einoToolAdapter{inner: draft, memo: memo}
	ctx := context.Background()

	if out, err := catalogAdapter.InvokableRun(ctx, `{"query":"cpu"}`); err != nil || !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("catalog out=%q err=%v", out, err)
	}
	out, err := draftAdapter.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","rule":{"kind":"metric_raw","spec":{"metric":"node_cpu_seconds_total","operator":">","threshold":80}}}`)
	if err != nil {
		t.Fatalf("draft InvokableRun() error = %v", err)
	}
	if !strings.Contains(out, `"kind":"config_draft"`) {
		t.Fatalf("draft output = %q, want config_draft", out)
	}
	if got := catalog.calls.Load(); got != 1 {
		t.Fatalf("catalog executions = %d, want 1", got)
	}
	if got := draft.calls.Load(); got != 1 {
		t.Fatalf("draft executions = %d, want 1", got)
	}
}

func TestEinoToolAdapter_DraftConfigChangeIgnoresCatalogInstructionWhenMetricsExist(t *testing.T) {
	t.Parallel()
	memo := newToolMemo()
	catalog := &fakeBaseTool{
		name:    "list_metric_catalog",
		class:   "read",
		runResp: `{"status":"ok","instruction":"The returned metrics do not expose any HTTP status/code label in sample_labels, so they cannot build a 5xx/error-rate/burn-rate SLI. Stop tool use now.","metric_count":1,"returned":1,"metrics":[{"name":"mysql_global_status_slow_queries"}]}`,
	}
	draft := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_draft","draft_hash":"sha256:ok"}`}
	catalogAdapter := &einoToolAdapter{inner: catalog, memo: memo}
	draftAdapter := &einoToolAdapter{inner: draft, memo: memo}
	ctx := context.Background()

	if _, err := catalogAdapter.InvokableRun(ctx, `{"query":"MySQL slow queries"}`); err != nil {
		t.Fatalf("catalog InvokableRun() error = %v", err)
	}
	out, err := draftAdapter.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","rule":{"kind":"metric_raw","spec":{"expr":"rate(mysql_global_status_slow_queries[5m]) > 0.1"}}}`)
	if err != nil {
		t.Fatalf("draft InvokableRun() error = %v", err)
	}
	if !strings.Contains(out, `"kind":"config_draft"`) {
		t.Fatalf("draft output = %q, want config_draft", out)
	}
	if got := draft.calls.Load(); got != 1 {
		t.Fatalf("draft executions = %d, want 1", got)
	}
}

func TestEinoToolAdapter_DraftConfigChangeAllowsValidationAfterEmptyMetricCatalog(t *testing.T) {
	t.Parallel()
	memo := newToolMemo()
	catalog := &fakeBaseTool{name: "list_metric_catalog", class: "read", runResp: `{"status":"empty","query":"cpu","metric_count":0,"returned":0,"metrics":[]}`}
	draft := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_draft"}`}
	catalogAdapter := &einoToolAdapter{inner: catalog, memo: memo}
	draftAdapter := &einoToolAdapter{inner: draft, memo: memo}
	ctx := context.Background()

	if _, err := catalogAdapter.InvokableRun(ctx, `{"query":"cpu"}`); err != nil {
		t.Fatalf("catalog InvokableRun() error = %v", err)
	}
	out, err := draftAdapter.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","rule":{"kind":"metric_raw","spec":{"metric":"cpu","operator":">","threshold":80}}}`)
	if err != nil {
		t.Fatalf("draft InvokableRun() error = %v", err)
	}
	if !strings.Contains(out, `"kind":"config_draft"`) {
		t.Fatalf("draft after empty catalog should execute for validation, got %q", out)
	}
	if got := draft.calls.Load(); got != 1 {
		t.Fatalf("draft executions = %d, want 1", got)
	}
}

func TestEinoToolAdapter_DraftConfigChangeAllowsLogRuleWithoutMetricCatalog(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_draft"}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	out, err := a.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","rule":{"kind":"log_search","spec":{"keywords":{"include":["error"],"mode":"any"},"window":"5m"}}}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if !strings.Contains(out, `"kind":"config_draft"`) {
		t.Fatalf("log draft should execute without metric catalog, got %q", out)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("draft executions = %d, want 1", got)
	}
}

func TestEinoToolAdapter_ListMetricCatalogUsesGenericCallCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "list_metric_catalog", class: "read", runResp: `{"status":"ok"}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	for i := 0; i < maxToolCallsPerRun; i++ {
		out, _ := a.InvokableRun(ctx, fmt.Sprintf(`{"query":"mongo metric lookup %d"}`, i))
		if strings.Contains(out, "call_budget_exceeded") {
			t.Fatalf("metric catalog lookup %d should execute, got budget directive: %s", i, out)
		}
	}
	out, _ := a.InvokableRun(ctx, `{"query":"mongo all metrics over cap"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("over-cap metric catalog lookup should be capped, got %q", out)
	}
	if !strings.Contains(out, `"scope":"current_user_turn"`) || !strings.Contains(out, "expires on the next user message") {
		t.Fatalf("budget directive should be explicitly scoped to the current turn, got %q", out)
	}
	if got := inner.calls.Load(); got != maxToolCallsPerRun {
		t.Fatalf("metric catalog executions = %d, want %d", got, maxToolCallsPerRun)
	}
}

func TestEinoToolAdapter_DraftConfigChangeFailureDoesNotConsumeConfirmableDraftCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "draft_config_change", class: "read", runErr: errors.New("invalid scope")}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	out, _ := a.InvokableRun(ctx, `{"rule":"bad"}`)
	if !strings.Contains(out, `"status":"failed"`) {
		t.Fatalf("failed draft should return failure envelope, got %q", out)
	}
	inner.runErr = nil
	inner.runResp = `{"kind":"config_draft","draft_hash":"sha256:ok"}`
	out, _ = a.InvokableRun(ctx, `{"rule":"fixed"}`)
	if strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("fixed draft after validation failure should execute, got %q", out)
	}
	out, _ = a.InvokableRun(ctx, `{"rule":"extra"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("second successful draft should be capped, got %q", out)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("draft executions = %d, want failed+fixed executions", got)
	}
}

func TestEinoToolAdapter_DraftConfigValidationFailedDoesNotConsumeConfirmableDraftCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "draft_config_change", class: "read", runResp: `{"kind":"config_validation_failed","validation":{"status":"failed"}}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	out, _ := a.InvokableRun(ctx, `{"rule":"bad"}`)
	if !strings.Contains(out, `"kind":"config_validation_failed"`) {
		t.Fatalf("validation failure should return structured result, got %q", out)
	}
	inner.runResp = `{"kind":"config_draft","draft_hash":"sha256:ok"}`
	out, _ = a.InvokableRun(ctx, `{"rule":"fixed"}`)
	if strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("fixed draft after validation failed result should execute, got %q", out)
	}
	out, _ = a.InvokableRun(ctx, `{"rule":"extra"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("second successful draft should be capped, got %q", out)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("draft executions = %d, want validation+fixed executions", got)
	}
}

// The single-tool WrapBaseTool path leaves memo nil — no caching.
func TestEinoToolAdapter_NoMemoByDefault(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "query_x", class: "read", runResp: "ok"}
	a := &einoToolAdapter{inner: inner} // memo nil
	ctx := context.Background()
	_, _ = a.InvokableRun(ctx, `{"q":"a"}`)
	_, _ = a.InvokableRun(ctx, `{"q":"a"}`)
	if got := inner.calls.Load(); got != 2 {
		t.Errorf("memo-less adapter must execute each call; want 2, got %d", got)
	}
}
