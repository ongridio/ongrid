package decorators

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// fakeReviewSpawner is the test seam for ReviewGate. Each test plants
// a result + optional error; the spawner records what prompt it was
// given so the test can assert on the brief assembly.
type fakeReviewSpawner struct {
	mu          sync.Mutex
	calls       int
	gotPrompt   string
	gotAgent    string
	plannedRes  *ReviewSpawnResult
	plannedErr  error
	delay       time.Duration
	respectCtx  bool
	gotCtxErr   error
	lastSpawnAt time.Time
}

func (f *fakeReviewSpawner) SpawnReviewer(ctx context.Context, req ReviewSpawnRequest) (*ReviewSpawnResult, error) {
	f.mu.Lock()
	f.calls++
	f.gotPrompt = req.Prompt
	f.gotAgent = req.AgentName
	f.lastSpawnAt = time.Now()
	f.mu.Unlock()
	if f.delay > 0 {
		if f.respectCtx {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				f.mu.Lock()
				f.gotCtxErr = ctx.Err()
				f.mu.Unlock()
				return nil, ctx.Err()
			}
		} else {
			time.Sleep(f.delay)
		}
	}
	if f.plannedErr != nil {
		return nil, f.plannedErr
	}
	return f.plannedRes, nil
}

// fakeProposalSink is the in-memory MutatingProposalSink for tests.
type fakeProposalSink struct {
	mu        sync.Mutex
	inserts   []MutatingProposalEvent
	updates   []sinkUpdate
	executed  []string
	idCounter int
}

type sinkUpdate struct {
	ID       string
	Decision string
	Reason   string
}

func (s *fakeProposalSink) Insert(_ context.Context, ev MutatingProposalEvent) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idCounter++
	id := "prop-" + itoa(s.idCounter)
	s.inserts = append(s.inserts, ev)
	return id, nil
}

func (s *fakeProposalSink) UpdateDecision(_ context.Context, id, decision, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, sinkUpdate{ID: id, Decision: decision, Reason: reason})
	return nil
}

func (s *fakeProposalSink) MarkExecuted(_ context.Context, id string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executed = append(s.executed, id)
	return nil
}

// itoa avoids the strconv import for one helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestReviewGate_ReadClassPassthrough(t *testing.T) {
	// Class="read" must NOT spawn a reviewer.
	inner := &fakeTool{name: "query_promql", class: "read", result: `{"ok":true}`}
	spawner := &fakeReviewSpawner{}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{})

	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("read-class call failed: %v", err)
	}
	if out != `{"ok":true}` {
		t.Errorf("inner result not propagated: %q", out)
	}
	if spawner.calls != 0 {
		t.Errorf("read-class call must NOT spawn reviewer (got %d spawns)", spawner.calls)
	}
}

func TestReviewGate_WriteClassApprove(t *testing.T) {
	inner := &fakeTool{name: "host_restart_service", class: "write", result: `{"restarted":true}`}
	spawner := &fakeReviewSpawner{
		plannedRes: &ReviewSpawnResult{
			TaskID: "agent-deadbeef",
			Result: "**Decision: approve**\n\nGates all pass; rollback is `systemctl start nginx`.\n",
		},
	}
	sink := &fakeProposalSink{}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{Sink: sink})

	out, err := wrapped.InvokableRun(context.Background(), `{"device_id":1,"service":"nginx"}`)
	if err != nil {
		t.Fatalf("approved call should not error: %v", err)
	}
	if out != `{"restarted":true}` {
		t.Errorf("inner result not propagated after approve: %q", out)
	}
	if spawner.calls != 1 {
		t.Errorf("expected 1 spawn, got %d", spawner.calls)
	}
	if spawner.gotAgent != DefaultReviewerAgent {
		t.Errorf("spawned agent = %q, want %q", spawner.gotAgent, DefaultReviewerAgent)
	}
	// Prompt should mention the tool name + class so the reviewer
	// knows what it's looking at.
	if !strings.Contains(spawner.gotPrompt, "host_restart_service") {
		t.Errorf("prompt missing tool name: %q", spawner.gotPrompt)
	}
	if !strings.Contains(spawner.gotPrompt, "write") {
		t.Errorf("prompt missing class: %q", spawner.gotPrompt)
	}
	// Audit row sequence: insert → UpdateDecision(approve) → MarkExecuted.
	if len(sink.inserts) != 1 {
		t.Errorf("expected 1 insert, got %d", len(sink.inserts))
	}
	if got := sink.inserts[0].ToolClass; got != "write" {
		t.Errorf("ToolClass on row = %q, want write", got)
	}
	if len(sink.updates) != 1 || sink.updates[0].Decision != "approve" {
		t.Errorf("expected one approve update, got %+v", sink.updates)
	}
	if len(sink.executed) != 1 {
		t.Errorf("expected MarkExecuted, got %v", sink.executed)
	}
}

func TestReviewGate_ApplyConfigChangeAlertRuleCreateUsesDeterministicApproval(t *testing.T) {
	inner := &fakeTool{name: "apply_config_change", class: "write", result: `{"status":"applied"}`}
	spawner := &fakeReviewSpawner{plannedErr: errors.New("reviewer should not be spawned")}
	sink := &fakeProposalSink{}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{Sink: sink})

	args := `{"domain":"alert_rule","action":"create","confirmed":true,"draft_hash":"sha256:test","payload":{"action":"create","draft_id":"draft-1","rule":{"rule_key":"cpu_high"}}}`
	out, err := wrapped.InvokableRun(context.Background(), args, basetool.WithUserID(42), basetool.WithTenant("session-1"))
	if err != nil {
		t.Fatalf("deterministic approval should not error: %v", err)
	}
	if out != `{"status":"applied"}` {
		t.Errorf("inner result not propagated: %q", out)
	}
	if spawner.calls != 0 {
		t.Errorf("alert_rule/create apply must not spawn reviewer; got %d spawns", spawner.calls)
	}
	if int(inner.calls) != 1 {
		t.Errorf("inner should run once, got %d", inner.calls)
	}
	if len(sink.inserts) != 1 {
		t.Fatalf("expected one deterministic proposal insert, got %d", len(sink.inserts))
	}
	insert := sink.inserts[0]
	if insert.ReviewerAgent != deterministicPolicyReviewerAgent {
		t.Errorf("ReviewerAgent = %q, want %q", insert.ReviewerAgent, deterministicPolicyReviewerAgent)
	}
	if insert.OperatorUserID != 42 {
		t.Errorf("OperatorUserID = %d, want 42", insert.OperatorUserID)
	}
	if insert.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want session-1", insert.SessionID)
	}
	if len(sink.updates) != 1 || sink.updates[0].Decision != "approve" {
		t.Fatalf("expected one deterministic approve update, got %+v", sink.updates)
	}
	if !strings.Contains(sink.updates[0].Reason, "draft_hash") {
		t.Errorf("approval reason should mention deterministic checks: %q", sink.updates[0].Reason)
	}
	if len(sink.executed) != 1 {
		t.Errorf("expected MarkExecuted after deterministic approval, got %v", sink.executed)
	}
}

func TestReviewGate_ApplyConfigChangeNonAlertRuleStillUsesReviewer(t *testing.T) {
	inner := &fakeTool{name: "apply_config_change", class: "write", result: `{"status":"applied"}`}
	spawner := &fakeReviewSpawner{
		plannedRes: &ReviewSpawnResult{Result: "Decision: reject\n\nOnly alert_rule/create is deterministic-approved."},
	}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{})

	_, err := wrapped.InvokableRun(context.Background(), `{"domain":"llm","action":"create","confirmed":true}`)
	if err == nil {
		t.Fatalf("non-alert config apply should still be review-gated")
	}
	if !errors.Is(err, ErrReviewRejected) {
		t.Errorf("error should be ErrReviewRejected: %v", err)
	}
	if spawner.calls != 1 {
		t.Errorf("expected reviewer spawn for non-alert config apply, got %d", spawner.calls)
	}
	if int(inner.calls) != 0 {
		t.Errorf("inner must not run after reviewer reject; got %d calls", inner.calls)
	}
}

func TestReviewGate_DestructiveClassApprove(t *testing.T) {
	// "destructive" must be gated identically to "write".
	inner := &fakeTool{name: "drop_silence", class: "destructive", result: `{"dropped":true}`}
	spawner := &fakeReviewSpawner{
		plannedRes: &ReviewSpawnResult{Result: "Decision: approve\n\nlooks fine\n"},
	}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{})
	if _, err := wrapped.InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatalf("destructive approve failed: %v", err)
	}
	if spawner.calls != 1 {
		t.Errorf("destructive class must spawn reviewer; got %d spawns", spawner.calls)
	}
}

func TestReviewGate_WriteClassReject(t *testing.T) {
	inner := &fakeTool{name: "host_restart_service", class: "write", result: "should-not-run"}
	spawner := &fakeReviewSpawner{
		plannedRes: &ReviewSpawnResult{
			Result: "Decision: reject\n\nNo SOP for this action; refusing.\n",
		},
	}
	sink := &fakeProposalSink{}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{Sink: sink})

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("rejected call must return error")
	}
	if !errors.Is(err, ErrReviewRejected) {
		t.Errorf("error should be ErrReviewRejected: %v", err)
	}
	if !strings.Contains(err.Error(), "No SOP") {
		t.Errorf("error should carry reviewer's reason: %v", err)
	}
	if int(inner.calls) != 0 {
		t.Errorf("inner tool MUST NOT run after reject; got %d calls", inner.calls)
	}
	if len(sink.updates) != 1 || sink.updates[0].Decision != "reject" {
		t.Errorf("expected one reject update, got %+v", sink.updates)
	}
	if len(sink.executed) != 0 {
		t.Errorf("rejected call must NOT mark executed; got %v", sink.executed)
	}
}

func TestReviewGate_UndecidedTreatedAsReject(t *testing.T) {
	inner := &fakeTool{name: "host_restart_service", class: "write", result: "should-not-run"}
	spawner := &fakeReviewSpawner{
		plannedRes: &ReviewSpawnResult{Result: "I'm not sure. The situation is complex."},
	}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{})

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("undecided reviewer must reject")
	}
	if !errors.Is(err, ErrReviewUndecided) {
		t.Errorf("error should be ErrReviewUndecided: %v", err)
	}
	if int(inner.calls) != 0 {
		t.Errorf("undecided reviewer MUST NOT run inner: %d calls", inner.calls)
	}
}

func TestReviewGate_SpawnerError(t *testing.T) {
	inner := &fakeTool{name: "host_restart_service", class: "write", result: "should-not-run"}
	spawner := &fakeReviewSpawner{plannedErr: errors.New("agent registry down")}
	sink := &fakeProposalSink{}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{Sink: sink})

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("spawner error must surface as gate error")
	}
	if !errors.Is(err, ErrReviewerSpawn) {
		t.Errorf("error should be ErrReviewerSpawn: %v", err)
	}
	if int(inner.calls) != 0 {
		t.Errorf("inner MUST NOT run on spawner error: %d calls", inner.calls)
	}
	// Audit row should still record the rejection so an outage is
	// visible in the proposals table.
	if len(sink.updates) != 1 || sink.updates[0].Decision != "reject" {
		t.Errorf("spawner error should record reject; got %+v", sink.updates)
	}
}

func TestReviewGate_NilSpawner(t *testing.T) {
	inner := &fakeTool{name: "host_restart_service", class: "write"}
	wrapped := WithReviewGate(inner, nil, ReviewGateConfig{})
	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("nil spawner must reject mutating call")
	}
	if !errors.Is(err, ErrReviewerSpawn) {
		t.Errorf("nil spawner should return ErrReviewerSpawn: %v", err)
	}
}

func TestReviewGate_CustomReviewerAgent(t *testing.T) {
	inner := &fakeTool{name: "drop_table", class: "destructive", result: "ok"}
	spawner := &fakeReviewSpawner{plannedRes: &ReviewSpawnResult{Result: "Decision: approve"}}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{ReviewerAgent: "db-reviewer"})

	if _, err := wrapped.InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if spawner.gotAgent != "db-reviewer" {
		t.Errorf("custom reviewer agent ignored: got %q", spawner.gotAgent)
	}
}

func TestReviewGate_TimeoutBoundsReviewer(t *testing.T) {
	// Reviewer takes longer than the gate timeout; ctx should fire.
	inner := &fakeTool{name: "host_restart_service", class: "write"}
	spawner := &fakeReviewSpawner{
		delay:      50 * time.Millisecond,
		respectCtx: true,
	}
	wrapped := WithReviewGate(inner, spawner, ReviewGateConfig{Timeout: 5 * time.Millisecond})

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("expected timeout-style error")
	}
	if !errors.Is(err, ErrReviewerSpawn) {
		t.Errorf("timeout should surface as ErrReviewerSpawn (spawner returned ctx err): %v", err)
	}
	if int(inner.calls) != 0 {
		t.Errorf("inner must not run after timeout: %d calls", inner.calls)
	}
}

// =====================================================================
// parseReviewerDecision — exercise the line-scan parser independently
// =====================================================================

func TestParseReviewerDecision_VariousShapes(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		decision string
	}{
		{
			name:     "plain approve",
			input:    "Decision: approve\nGates passed.",
			decision: "approve",
		},
		{
			name:     "plain reject",
			input:    "Decision: reject\nNo SOP.",
			decision: "reject",
		},
		{
			name:     "markdown bold approve (reviewer.md format)",
			input:    "**Decision: approve**\n\n**Gates**\n- ✓ SOP found\n",
			decision: "approve",
		},
		{
			name:     "markdown bold reject (reviewer.md format)",
			input:    "**Decision: reject**\n\n**Missing gates**\n- no SOP\n- no rollback\n",
			decision: "reject",
		},
		{
			name:     "case insensitive",
			input:    "decision: APPROVE\n",
			decision: "approve",
		},
		{
			name:     "no marker → undecided",
			input:    "I'm not sure. Something is wrong.",
			decision: "",
		},
		{
			name:     "empty",
			input:    "",
			decision: "",
		},
		{
			name:     "decision: pending → keep scanning, none found → undecided",
			input:    "Decision: pending\nWill think more.",
			decision: "",
		},
		{
			name:     "decision: pending then approve → second wins",
			input:    "Decision: pending\nMore thinking...\nDecision: approve\nLooks ok.",
			decision: "approve",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := parseReviewerDecision(c.input)
			if got != c.decision {
				t.Errorf("decision = %q, want %q (input=%q)", got, c.decision, c.input)
			}
		})
	}
}

func TestParseReviewerDecision_ExtractsReason(t *testing.T) {
	in := "**Decision: reject**\n\n**Missing**\n- no SOP\n- no rollback\n"
	dec, reason := parseReviewerDecision(in)
	if dec != "reject" {
		t.Fatalf("decision = %q", dec)
	}
	if !strings.Contains(reason, "no SOP") || !strings.Contains(reason, "no rollback") {
		t.Errorf("reason should carry rejection rationale: %q", reason)
	}
}

// =====================================================================
// chain.Wrap integration — ReviewGate is in the chain only when
// ReviewSpawner is wired
// =====================================================================

func TestChainWrap_InstallsReviewGateOnlyWithSpawner(t *testing.T) {
	inner := &fakeTool{name: "host_restart_service", class: "write", result: `{"ok":true}`}

	// Without spawner: chain doesn't gate, so the call goes straight
	// through. (TenantBind / Audit / Timeout / RateLimit / Metric still
	// apply, but they don't reject mutating tools.)
	wrappedNoGate := Wrap(inner, Deps{})
	if _, err := wrappedNoGate.InvokableRun(context.Background(), `{}`); err != nil {
		t.Errorf("without spawner the chain should pass through, got: %v", err)
	}
	if int(inner.calls) != 1 {
		t.Errorf("inner should run once without gate: %d", inner.calls)
	}

	// With spawner that rejects: chain blocks the call.
	inner.calls = 0
	spawner := &fakeReviewSpawner{
		plannedRes: &ReviewSpawnResult{Result: "Decision: reject\nNot now."},
	}
	wrappedGated := Wrap(inner, Deps{ReviewSpawner: spawner})
	_, err := wrappedGated.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("expected reject through Wrap chain")
	}
	if !errors.Is(err, ErrReviewRejected) {
		t.Errorf("error should be ErrReviewRejected: %v", err)
	}
	if int(inner.calls) != 0 {
		t.Errorf("inner must NOT run after reject through chain: %d", inner.calls)
	}
}

// Make sure basetool import is exercised even on a pure decorator
// test (avoids unused-import shenanigans across refactors).
var _ basetool.BaseTool = (*fakeTool)(nil)
