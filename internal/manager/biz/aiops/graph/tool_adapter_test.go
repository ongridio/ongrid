package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	inner := &fakeBaseTool{name: "query_promql", class: "read", runResp: `{"v":1}`}
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

	out, err := a.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","rule":{"kind":"log_match","spec":{"line_filter":"error","window":"5m"}}}`)
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

func TestEinoToolAdapter_ListMetricCatalogAllowsOneRefinementBeforeCap(t *testing.T) {
	t.Parallel()
	inner := &fakeBaseTool{name: "list_metric_catalog", class: "read", runResp: `{"status":"ok"}`}
	a := &einoToolAdapter{inner: inner, memo: newToolMemo()}
	ctx := context.Background()

	out, _ := a.InvokableRun(ctx, `{"query":"mongo connection usage"}`)
	if strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("first metric catalog lookup should execute, got budget directive: %s", out)
	}
	out, _ = a.InvokableRun(ctx, `{"query":"mongo conn_type values"}`)
	if strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("second metric catalog lookup should allow one refinement, got %q", out)
	}
	out, _ = a.InvokableRun(ctx, `{"query":"mongo all metrics"}`)
	if !strings.Contains(out, "call_budget_exceeded") {
		t.Fatalf("third metric catalog lookup should be capped, got %q", out)
	}
	if !strings.Contains(out, `"scope":"current_user_turn"`) || !strings.Contains(out, "expires on the next user message") {
		t.Fatalf("budget directive should be explicitly scoped to the current turn, got %q", out)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("metric catalog executions = %d, want 2", got)
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
