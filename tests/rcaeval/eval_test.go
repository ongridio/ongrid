package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func testCases(t *testing.T) []evalCase {
	t.Helper()
	cases, err := loadCases("cases", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 8 {
		t.Fatalf("want 8 cases, got %d", len(cases))
	}
	return cases
}

func TestReplayRespectsObjectTimeAndExpression(t *testing.T) {
	c := testCases(t)[0]
	r, err := newReplay(c, 20)
	if err != nil {
		t.Fatal(err)
	}
	bag, err := r.tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var target basetool.BaseTool
	for _, tool := range bag {
		info, _ := tool.Info(context.Background())
		if info.Name == "query_promql" {
			target = tool
		}
	}
	var declared map[string]any
	if err := json.Unmarshal(fixtureByID(c, "metrics_5m").Args[0], &declared); err != nil {
		t.Fatal(err)
	}
	expr := declared["expr"].(string)
	args := func(expr string, extra map[string]any) string {
		m := map[string]any{"expr": expr}
		for key, value := range extra {
			m[key] = value
		}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for _, test := range []struct {
		args string
		ok   bool
	}{
		{args(expr, nil), true},
		{args(expr, map[string]any{"lookback_seconds": 300}), true},
		{args(strings.ReplaceAll(expr, `"42"`, `"43"`), nil), false},
		{args(expr, map[string]any{"lookback_seconds": 86400}), false},
		{`{"expr":"memory_usage_percent{device_id=\"42\"}"}`, false},
		{args(expr, map[string]any{"start": "yesterday"}), false},
		{`null`, false},
		{`{`, false},
	} {
		out, err := target.InvokableRun(context.Background(), test.args)
		if (err == nil) != test.ok || (!test.ok && out != "") {
			t.Fatalf("args=%s result=%s err=%v", test.args, out, err)
		}
	}
}

func TestCaseRejectsAmbiguousFixture(t *testing.T) {
	c := testCases(t)[0]
	f := c.Fixtures[0]
	f.ID = "different-result"
	c.Fixtures = append(c.Fixtures, f)
	if err := c.validate(); err == nil {
		t.Fatal("ambiguous fixture accepted")
	}
}

func TestSmokeProductionPipeline(t *testing.T) {
	for _, c := range testCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			c.Rubric.Forbidden = append(c.Rubric.Forbidden, "RUBRIC_SECRET_SENTINEL")
			dir := t.TempDir()
			cfg := runConfig{Mode: "smoke", Model: "scripted-smoke", MaxModelCalls: 16, MaxToolCalls: 24, MaxTokens: 10000, Timeout: time.Minute, Temperature: 0.1}
			r, err := runCase(context.Background(), "../..", dir, c, 1, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != "awaiting_review" || r.Report.Status != "ready" || len(r.MissingObservations) != 0 {
				t.Fatalf("pipeline result: %+v", r)
			}
			if r.ModelCalls < 4 || r.ToolCalls < 2 || r.UsageComplete {
				t.Fatalf("bad accounting: %+v", r)
			}
			var calls []modelCall
			if err := readJSON(filepath.Join(dir, "model_calls.json"), &calls); err != nil {
				t.Fatal(err)
			}
			extractions := 0
			for _, call := range calls {
				if call.SessionID == "" {
					t.Fatal("model call not linked to a session")
				}
				if call.Phase == "extraction" {
					extractions++
				}
				input, _ := json.Marshal(call.Request)
				if strings.Contains(string(input), "RUBRIC_SECRET_SENTINEL") || strings.Contains(string(input), `"acceptable"`) || strings.Contains(string(input), `"forbidden"`) {
					t.Fatal("rubric leaked to model")
				}
			}
			if extractions != 1 {
				t.Fatalf("extractions=%d", extractions)
			}
			var check alertmodel.EvidenceCheck
			if err := json.Unmarshal([]byte(*r.Report.EvidenceCheckJSON), &check); err != nil {
				t.Fatal(err)
			}
			assertScenarioReferences(t, c, check)
			if c.ID == "downstream_dependency" {
				if r.Sessions != 2 {
					t.Fatalf("specialist session missing: %d", r.Sessions)
				}
				childEvidence := false
				for _, s := range check.Sources {
					if s.SessionID != *r.Report.AuditSessionID {
						childEvidence = true
					}
				}
				if !childEvidence {
					t.Fatal("specialist evidence missing from final report")
				}
			}
			if c.ID == "query_timeout" {
				if check.RootCause.Status != "partial" {
					t.Fatal("timeout cited as verified")
				}
				found := false
				for _, s := range check.Sources {
					if s.State == "timeout" || (s.Tool == "query_logql" && s.State == "error") {
						found = true
					}
				}
				if !found {
					t.Fatal("timeout state lost")
				}
			}
			if c.ID == "mixed_devices" {
				found := false
				for _, s := range check.Sources {
					if strings.Contains(s.Target, "43") && s.State == "error" {
						found = true
					}
				}
				if !found {
					t.Fatalf("failed device lost: %+v", check.Sources)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "sessions.sqlite")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueryKeyPreservesLargeDeviceIDs(t *testing.T) {
	a, err := queryKey("get_host_load", json.RawMessage(`{"device_ids":[9007199254740992]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := queryKey("get_host_load", json.RawMessage(`{"device_ids":[9007199254740993]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("different device IDs collapse to the same fixture")
	}
}

func TestMetricFixtureNamesAndQueryWindows(t *testing.T) {
	validName := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	for _, c := range testCases(t) {
		for _, f := range c.Fixtures {
			if f.Tool != "query_promql" || f.Error != "" {
				continue
			}
			var result struct {
				Result []struct {
					Metric map[string]string    `json:"metric"`
					Values [][2]json.RawMessage `json:"values"`
				} `json:"result"`
			}
			if err := json.Unmarshal(f.Result, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Result) == 0 {
				t.Fatalf("%s fixture has no metric series", f.ID)
			}
			for _, args := range f.Args {
				var a struct {
					Lookback int64 `json:"lookback_seconds"`
				}
				if err := json.Unmarshal(args, &a); err != nil {
					t.Fatal(err)
				}
				if a.Lookback == 0 {
					a.Lookback = 300
				}
				for _, series := range result.Result {
					if name := series.Metric["__name__"]; name != "" && !validName.MatchString(name) {
						t.Fatalf("%s/%s invalid metric name: %v", c.ID, f.ID, series.Metric)
					}
					for _, sample := range series.Values {
						var at int64
						if err := json.Unmarshal(sample[0], &at); err != nil {
							t.Fatal(err)
						}
						if at < c.Incident.At.Unix()-a.Lookback || at > c.Incident.At.Unix() {
							t.Fatalf("%s/%s sample outside declared query window", c.ID, f.ID)
						}
					}
				}
			}
		}
	}
}

type clientFunc func(context.Context, llm.ChatReq) (*llm.ChatResp, error)

func (f clientFunc) Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	return f(ctx, req)
}

func TestModelBudgetIncludesConcurrentSpecialistsAndExtraction(t *testing.T) {
	m := &meter{root: context.Background(), gate: make(chan struct{}, 1), maxCalls: 2, maxTokens: 100,
		inner: clientFunc(func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return &llm.ChatResp{Usage: llm.Usage{TotalTokens: 60}}, nil
		})}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = (meteredClient{m, "investigation"}).Chat(context.Background(), llm.ChatReq{})
		}()
	}
	wg.Wait()
	if len(m.calls) != 2 || m.tokens != 120 || !m.exceeded {
		t.Fatalf("budget failed: calls=%d tokens=%d", len(m.calls), m.tokens)
	}
	if _, err := (meteredClient{m, "extraction"}).Chat(context.Background(), llm.ChatReq{}); !errors.Is(err, errBudget) {
		t.Fatal(err)
	}
}

func TestModelHonorsRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &meter{root: ctx, gate: make(chan struct{}, 1), maxCalls: 5, maxTokens: 100, inner: clientFunc(func(ctx context.Context, _ llm.ChatReq) (*llm.ChatResp, error) { <-ctx.Done(); return nil, ctx.Err() })}
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (meteredClient{m, "investigation"}).Chat(context.Background(), llm.ChatReq{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("model request did not cancel")
	}
}

func TestReviewCannotTurnCoverageGapIntoPass(t *testing.T) {
	r := runResult{ID: "run", CaseHash: "hash", Status: "fixture_gap"}
	v := review{RunID: "run", CaseHash: "hash", Verdict: "pass", Reviewer: "reviewer", Notes: "reviewed", RootCauseSupported: "yes", UncertaintyHandled: "yes", TargetAndTimeCorrect: "yes", NextStepUseful: "yes"}
	v.ResultHash = hashValue(r)
	if err := validateReview(r, v); err == nil {
		t.Fatal("coverage gap became a pass")
	}
	r.Status = "awaiting_review"
	v.ResultHash = hashValue(r)
	if err := validateReview(r, v); err != nil {
		t.Fatal(err)
	}
	v.CaseHash = "changed"
	if err := validateReview(r, v); err == nil {
		t.Fatal("stale review accepted")
	}
}

func TestRunClassifiesModelFailureAndBudget(t *testing.T) {
	c := testCases(t)[0]
	for _, tc := range []struct {
		name, status   string
		max            int
		failExtraction bool
		coverageGap    bool
	}{
		{"extraction", "model_error", 16, true, false},
		{"extraction_and_gap", "model_error", 16, true, true},
		{"model_budget", "budget_exceeded", 2, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := &smokeClient{caseData: c, positions: map[string]int{}}
			client := clientFunc(func(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
				if len(req.Tools) == 0 && tc.failExtraction {
					return nil, errors.New("provider unavailable")
				}
				resp, err := script.Chat(ctx, req)
				if tc.coverageGap && resp != nil && len(resp.Assistant.ToolCalls) > 0 {
					resp.Assistant.ToolCalls[0].Args = json.RawMessage(`{"uncovered":true}`)
				}
				return resp, err
			})
			cfg := runConfig{Mode: "live", Model: "fake", MaxModelCalls: tc.max, MaxToolCalls: 24, MaxTokens: 10000, Timeout: time.Minute}
			r, err := runCase(context.Background(), "../..", t.TempDir(), c, 1, cfg, client)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != tc.status || r.ModelCalls > tc.max {
				t.Fatalf("status=%s calls=%d", r.Status, r.ModelCalls)
			}
			if tc.failExtraction && r.Report.FindingsMD == "" {
				t.Fatal("model failure lost investigation narrative")
			}
			if tc.coverageGap && (len(r.Queries) == 0 || r.Queries[0].Error != "fixture_coverage_gap") {
				t.Fatal("fixture did not exercise simultaneous model failure and coverage gap")
			}
		})
	}
}

func TestRequestedCallsIncludeMemoizedQueries(t *testing.T) {
	c := testCases(t)[0]
	c.Smoke.Steps = append([]smokeStep{{Fixture: "correlation"}}, c.Smoke.Steps...)
	cfg := runConfig{Mode: "smoke", Model: "scripted-smoke", MaxModelCalls: 16, MaxToolCalls: 24, MaxTokens: 10000, Timeout: time.Minute}
	r, err := runCase(context.Background(), "../..", t.TempDir(), c, 1, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.ToolCalls != 5 || r.ToolExecutions != 4 || r.RepeatedQueries != 1 {
		t.Fatalf("requested=%d executed=%d repeats=%d", r.ToolCalls, r.ToolExecutions, r.RepeatedQueries)
	}
}

func TestSmokeRejectsUnavailableTool(t *testing.T) {
	c := testCases(t)[0]
	c.Smoke.Steps = []smokeStep{{Tool: "query_traceql", Args: json.RawMessage(`{"trace_id":"0123456789abcdef0123456789abcdef"}`)}}
	script := &smokeClient{caseData: c, positions: map[string]int{}}
	_, err := script.Chat(context.Background(), llm.ChatReq{Tools: []llm.ToolSchema{{Name: "query_logql"}}})
	if err == nil || !strings.Contains(err.Error(), "unavailable tool query_traceql") {
		t.Fatalf("unauthorized smoke tool was accepted: %v", err)
	}
}

func TestReportAndComparisonDoNotAssignAutomaticPass(t *testing.T) {
	c := testCases(t)[0]
	base := t.TempDir()
	dir := filepath.Join(base, "cpu_process-01")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{Mode: "smoke", Model: "scripted-smoke", MaxModelCalls: 16, MaxToolCalls: 24, MaxTokens: 10000, Timeout: time.Minute}
	if _, err := runCase(context.Background(), "../..", dir, c, 1, cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(base, "manifest.json"), manifest{Config: cfg, Cases: []string{c.ID}, Repeat: 1, Completed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := renderReport(base, base); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(base, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "pending → pending") || !strings.Contains(string(report), "unknown") {
		t.Fatal(string(report))
	}
}
