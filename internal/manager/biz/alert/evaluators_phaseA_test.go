package alert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestModelNormalizeKindAliases(t *testing.T) {
	// Phase-3 collapse: every legacy / deleted kind normalises to
	// metric_raw so a row that escapes the SQLite migration still
	// resolves to a working evaluator (with the wrong conditions_json
	// shape — the evaluator just no-ops or warns, which is preferable
	// to crashing).
	cases := []struct{ in, want string }{
		// Phase-3-final collapse: empty kind is metric_raw (the canonical
		// shape after metric_threshold became UI-only).
		{"", model.RuleKindMetricRaw},
		{"edge_offline", model.RuleKindMetricRaw},
		{"prom_query", model.RuleKindMetricRaw},
		{"ingest_health", model.RuleKindMetricRaw},
		{"edge_absence", model.RuleKindMetricRaw},
		{"health_ingest", model.RuleKindMetricRaw},
		{"event_internal", model.RuleKindMetricRaw},
		{"metric_anomaly", model.RuleKindMetricAnomaly},
		{"unknown_thing", "unknown_thing"},
	}
	for _, c := range cases {
		if got := model.NormalizeKind(c.in); got != c.want {
			t.Errorf("NormalizeKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModelKindCategoryGates(t *testing.T) {
	for _, kind := range []string{"log_match", "log_volume", "trace_latency", "trace_error_rate"} {
		if !model.IsKnownKind(kind) {
			t.Errorf("%s should be known", kind)
		}
		// Phase-B: 2026-05-08 — log_* / trace_* now have working
		// evaluators (Loki + Prom spanmetrics).
		if !model.IsEvaluableKind(kind) {
			t.Errorf("%s must be evaluable now Phase-B is wired", kind)
		}
	}
	if !model.IsEvaluableKind("metric_anomaly") {
		t.Errorf("metric_anomaly must be evaluable")
	}
	if model.IsEvaluableKind("does_not_exist") {
		t.Errorf("unknown kind must not be evaluable")
	}
}

func TestCompileMetricAnomalyDefaultsAndValidation(t *testing.T) {
	spec, _ := json.Marshal(map[string]any{"metric": "cpu_pct"})
	row := &model.Rule{ID: 1, RuleKey: "cpu_anom", ConditionsJSON: string(spec)}
	r, err := compileMetricAnomalyRule(row)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if r.Method != "zscore" || r.BaselineWindow != "1h" || r.BaselineStep != "5m" || r.Deviation != 3 {
		t.Errorf("defaults wrong: %+v", r)
	}

	bad, _ := json.Marshal(map[string]any{"metric": "cpu_pct", "method": "bogus"})
	if _, err := compileMetricAnomalyRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(bad)}); err == nil {
		t.Errorf("compile should reject method=bogus")
	}

	missing, _ := json.Marshal(map[string]any{"metric": ""})
	if _, err := compileMetricAnomalyRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(missing)}); err == nil {
		t.Errorf("compile should reject empty metric")
	}
}

func TestCompileMetricForecastRequiresPredictWindow(t *testing.T) {
	spec, _ := json.Marshal(map[string]any{
		"metric":   "disk_avail_bytes",
		"operator": "<=",
	})
	if _, err := compileMetricForecastRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(spec)}); err == nil {
		t.Errorf("compile should reject predict_seconds<=0")
	}
}

func TestCompileMetricBurnRateValidatesBurns(t *testing.T) {
	noWindows, _ := json.Marshal(map[string]any{"sli": "sum(rate(http_requests_total[$window]))", "slo": 99.9})
	if _, err := compileMetricBurnRateRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(noWindows)}); err == nil {
		t.Errorf("compile should reject empty burns")
	}
	noWindowSLI, _ := json.Marshal(map[string]any{
		"sli":   "http_success_ratio",
		"slo":   99.9,
		"burns": []any{map[string]any{"window": "1h", "multiplier": 14.4}},
	})
	if _, err := compileMetricBurnRateRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(noWindowSLI)}); err == nil {
		t.Errorf("compile should reject SLI without $window or range selector")
	}
	bad, _ := json.Marshal(map[string]any{
		"sli":   `sum(rate(http_requests_total{code!~"5.."}[5m])) / sum(rate(http_requests_total[5m]))`,
		"slo":   99.9,
		"burns": []any{map[string]any{"window": "1h", "multiplier": 14.4}},
	})
	r, err := compileMetricBurnRateRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(bad)})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(r.Burns) != 1 || r.Burns[0].Window != "1h" || r.Burns[0].Multiplier != 14.4 {
		t.Errorf("burns parse wrong: %+v", r.Burns)
	}
	if strings.Contains(r.SLI, "[5m]") || strings.Count(r.SLI, "[$window]") != 2 {
		t.Errorf("SLI should normalize fixed ranges to $window, got %q", r.SLI)
	}

	ratioSLO, _ := json.Marshal(map[string]any{
		"sli":   `sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))`,
		"slo":   0.999,
		"burns": []any{map[string]any{"window": "1h", "multiplier": 14.4}},
	})
	r, err = compileMetricBurnRateRule(&model.Rule{RuleKey: "x", ConditionsJSON: string(ratioSLO)})
	if err != nil {
		t.Fatalf("compile ratio SLO: %v", err)
	}
	if r.SLO != 99.9 {
		t.Fatalf("SLO = %v, want 99.9 percent", r.SLO)
	}
}

func TestBuildConditionsJSONNormalizesBurnRateRatioSLOToPercent(t *testing.T) {
	conditions, err := buildConditionsJSON(model.RuleKindMetricBurnRate, RuleInput{
		Spec: map[string]any{
			"sli":   `sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))`,
			"slo":   0.999,
			"burns": []any{map[string]any{"window": "1h", "multiplier": 14.4}},
		},
	})
	if err != nil {
		t.Fatalf("buildConditionsJSON: %v", err)
	}
	var got struct {
		SLO float64 `json:"slo"`
	}
	if err := json.Unmarshal([]byte(conditions), &got); err != nil {
		t.Fatalf("decode conditions: %v", err)
	}
	if got.SLO != 99.9 {
		t.Fatalf("stored SLO = %v, want 99.9 percent", got.SLO)
	}
}

func TestBuildConditionsJSONPreservesMetricForecastSelector(t *testing.T) {
	conditions, err := buildConditionsJSON(model.RuleKindMetricForecast, RuleInput{
		Spec: map[string]any{
			"metric":          "disk_avail_bytes",
			"selector":        `{mountpoint="/var"}`,
			"fit_window":      "1h",
			"predict_seconds": 21600,
			"operator":        "<",
			"threshold":       float64(10 * 1024 * 1024 * 1024),
		},
	})
	if err != nil {
		t.Fatalf("buildConditionsJSON: %v", err)
	}
	var got struct {
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal([]byte(conditions), &got); err != nil {
		t.Fatalf("decode conditions: %v", err)
	}
	if got.Selector != `{mountpoint="/var"}` {
		t.Fatalf("selector = %q, want mountpoint selector", got.Selector)
	}
}

func TestMetricExprForKnownMetrics(t *testing.T) {
	for _, m := range []string{"cpu_pct", "mem_pct", "disk_used_pct", "disk_avail_bytes", "load1", "net_rx_bps"} {
		if _, ok := metricExprFor(m); !ok {
			t.Errorf("metricExprFor(%q) returned !ok", m)
		}
	}
	if _, ok := metricExprFor("not_a_metric"); ok {
		t.Errorf("metricExprFor unknown should return false")
	}
}

func TestMetricAnomalyExprMentionsBaselineAndStddev(t *testing.T) {
	// Drive evaluator through fake prom and verify the query string the
	// evaluator constructs contains the pieces operators expect — the
	// baseline window, the deviation multiplier, and the stddev call.
	captured := ""
	prom := capturingPromQuerier{capture: &captured, result: emptyVector()}
	repo := newFakeRepo()
	notifier := &fakeNotifier{}
	rules := NewStaticRulesProvider(WithMetricAnomalyRules([]MetricAnomalyRule{
		{ID: 1, RuleKey: "cpu_anom", Name: "CPU Anomaly", Severity: "warning",
			Metric: "cpu_pct", Method: "zscore", BaselineWindow: "1h", BaselineStep: "5m", Deviation: 3},
	}))
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	eval := newPipelineEvaluator(t, repo, notifier, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return now },
	})
	eval.EvaluateOnce(context.Background())
	if !strings.Contains(captured, "stddev_over_time") {
		t.Errorf("anomaly expr missing stddev_over_time: %s", captured)
	}
	if !strings.Contains(captured, "[1h:5m]") {
		t.Errorf("anomaly expr missing baseline subquery [1h:5m]: %s", captured)
	}
	if !strings.Contains(captured, "3 *") {
		t.Errorf("anomaly expr missing deviation multiplier: %s", captured)
	}
}

func TestMetricForecastExprUsesPredictLinear(t *testing.T) {
	captured := ""
	prom := capturingPromQuerier{capture: &captured, result: emptyVector()}
	repo := newFakeRepo()
	rules := NewStaticRulesProvider(WithMetricForecastRules([]MetricForecastRule{
		{ID: 1, RuleKey: "disk_fill", Name: "Disk Fills", Severity: "warning",
			Metric: "disk_avail_bytes", Selector: `{mountpoint="/var"}`, FitWindow: "1h", PredictSeconds: 21600,
			Operator: "<=", Threshold: 0},
	}))
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	eval := newPipelineEvaluator(t, repo, &fakeNotifier{}, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return now },
	})
	eval.EvaluateOnce(context.Background())
	if !strings.Contains(captured, "predict_linear") {
		t.Errorf("forecast expr missing predict_linear: %s", captured)
	}
	if !strings.Contains(captured, "21600") {
		t.Errorf("forecast expr missing predict seconds: %s", captured)
	}
	if !strings.Contains(captured, `node_filesystem_avail_bytes{mountpoint="/var"}`) {
		t.Errorf("forecast expr missing selector override: %s", captured)
	}
}

func TestMetricForecastHostScopeUsesDeviceIDLabel(t *testing.T) {
	repo := newFakeRepo()
	rules := NewStaticRulesProvider(WithMetricForecastRules([]MetricForecastRule{
		{ID: 1, RuleKey: "disk_fill", Name: "Disk Fills", Severity: "warning",
			ScopeType: "host", Metric: "disk_used_pct", FitWindow: "1h", PredictSeconds: 21600,
			Operator: "<=", Threshold: 100},
	}))
	prom := &scriptedProm{results: []*promquery.InstantResult{
		vectorInstantEntry(map[string]string{"device_id": "2", "mountpoint": "/"}, "1"),
	}}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	eval := newPipelineEvaluator(t, repo, &fakeNotifier{}, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return now },
	})

	eval.EvaluateOnce(context.Background())

	if len(repo.incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(repo.incidents))
	}
	for _, inc := range repo.incidents {
		if inc.DeviceID == nil || *inc.DeviceID != 2 {
			t.Fatalf("DeviceID = %v, want 2", inc.DeviceID)
		}
	}
}

func TestMetricBurnRateRequiresAllWindowsToFire(t *testing.T) {
	// First window fires (vector with one entry), second is empty → no
	// firing, all-AND semantics. Then both fire → incident created.
	repo := newFakeRepo()
	notifier := &fakeNotifier{}
	rules := NewStaticRulesProvider(WithMetricBurnRateRules([]MetricBurnRateRule{
		{ID: 1, RuleKey: "slo_burn", Name: "SLO Burn", Severity: "critical",
			SLI: `1 - (sum(rate(errors[$window])) / sum(rate(reqs[$window])))`, SLO: 99.9,
			Burns: []BurnRateWindow{
				{Window: "1h", Multiplier: 14.4},
				{Window: "6h", Multiplier: 6},
			}},
	}))
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	// One window fires, the other is empty.
	prom := &scriptedProm{results: []*promquery.InstantResult{singleEntry(), emptyVector()}}
	eval := newPipelineEvaluator(t, repo, notifier, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return now },
	})
	eval.EvaluateOnce(context.Background())
	if len(repo.incidents) != 0 {
		t.Errorf("partial burn must not fire, got %d incidents", len(repo.incidents))
	}

	// All windows fire.
	repo2 := newFakeRepo()
	prom2 := &scriptedProm{results: []*promquery.InstantResult{singleEntry(), singleEntry()}}
	eval2 := newPipelineEvaluator(t, repo2, notifier, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom2,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return now },
	})
	eval2.EvaluateOnce(context.Background())
	if len(repo2.incidents) != 1 {
		t.Errorf("full burn must fire one incident, got %d", len(repo2.incidents))
	}
}

// ------------ test doubles ------------

type capturingPromQuerier struct {
	capture *string
	result  *promquery.InstantResult
}

func (c capturingPromQuerier) Query(_ context.Context, expr string, _ time.Time) (*promquery.InstantResult, error) {
	if c.capture != nil {
		*c.capture = expr
	}
	return c.result, nil
}

type scriptedProm struct {
	idx     int
	results []*promquery.InstantResult
	err     error
}

func (s *scriptedProm) Query(_ context.Context, _ string, _ time.Time) (*promquery.InstantResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.idx >= len(s.results) {
		return nil, errors.New("scriptedProm: out of scripted results")
	}
	r := s.results[s.idx]
	s.idx++
	return r, nil
}

func emptyVector() *promquery.InstantResult {
	return &promquery.InstantResult{ResultType: "vector", Result: json.RawMessage(`[]`)}
}

func singleEntry() *promquery.InstantResult {
	return vectorInstantEntry(map[string]string{"__name__": "burn"}, "1")
}

func vectorInstantEntry(labels map[string]string, value string) *promquery.InstantResult {
	body := []map[string]any{{
		"metric": labels,
		"value":  []any{float64(time.Now().Unix()), value},
	}}
	raw, _ := json.Marshal(body)
	return &promquery.InstantResult{ResultType: "vector", Result: raw}
}
