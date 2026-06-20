package alert

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

type fakePromRange struct {
	lastExpr  string
	lastStep  time.Duration
	lastStart time.Time
	lastEnd   time.Time
	queries   []string
	res       *promquery.InstantResult
	err       error
	responses []*promquery.InstantResult
	errs      []error
}

func (f *fakePromRange) QueryRange(_ context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error) {
	f.lastExpr = expr
	f.lastStep = step
	f.lastStart = start
	f.lastEnd = end
	f.queries = append(f.queries, expr)
	idx := len(f.queries) - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.responses) {
		return f.responses[idx], nil
	}
	return f.res, f.err
}

func TestPreviewRule_ClampsLookbackWindow(t *testing.T) {
	now := time.Unix(1714003600, 0).UTC()
	prom := &fakePromRange{
		res: matrix(t, []struct {
			Ts    int64
			Value string
			Label map[string]string
		}{
			{Ts: 1714000000, Value: "92.3", Label: map[string]string{"device_id": "1"}},
		}),
	}
	in := PreviewInput{
		Input: RuleInput{
			Kind:      "metric_raw",
			Name:      "test",
			Severity:  "warning",
			Enabled:   true,
			ScopeType: "global",
			Spec:      map[string]interface{}{"expr": `up > 0`},
		},
		LookbackSeconds: maxPreviewLookbackSeconds * 10,
	}

	_, err := PreviewRule(context.Background(), in, PreviewDeps{
		Prom: prom,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if got, want := prom.lastEnd.Sub(prom.lastStart), time.Duration(maxPreviewLookbackSeconds)*time.Second; got != want {
		t.Fatalf("lookback range = %s, want %s", got, want)
	}
}

type fakeLogRange struct {
	lastQuery string
	res       *logquery.QueryRangeResult
	err       error
}

func (f *fakeLogRange) QueryRange(_ context.Context, opts logquery.QueryRangeOptions) (*logquery.QueryRangeResult, error) {
	f.lastQuery = opts.Query
	return f.res, f.err
}

type fakeEventCounter struct {
	// rolling[t] = total events with created_at >= t. Used to mimic the
	// monotone behavior of CountEventsByType.
	answers []int64
	calls   int
}

func (f *fakeEventCounter) CountEventsByType(_ context.Context, _ string, _ time.Time, _, _ string) (int64, error) {
	if f.calls >= len(f.answers) {
		f.calls++
		return 0, nil
	}
	v := f.answers[f.calls]
	f.calls++
	return v, nil
}

// matrix builds a synthetic Prom matrix payload with one series per
// metric label set + a list of sample (ts, value) pairs.
func matrix(t *testing.T, points []struct {
	Ts    int64
	Value string
	Label map[string]string
}) *promquery.InstantResult {
	t.Helper()
	type entry struct {
		Metric map[string]string `json:"metric"`
		Values [][]any           `json:"values"`
	}
	byKey := map[string]*entry{}
	for _, p := range points {
		key := ""
		for k, v := range p.Label {
			key += k + "=" + v + ","
		}
		e, ok := byKey[key]
		if !ok {
			e = &entry{Metric: p.Label}
			byKey[key] = e
		}
		e.Values = append(e.Values, []any{p.Ts, p.Value})
	}
	var ms []entry
	for _, e := range byKey {
		ms = append(ms, *e)
	}
	raw, err := json.Marshal(ms)
	if err != nil {
		t.Fatalf("marshal matrix: %v", err)
	}
	return &promquery.InstantResult{ResultType: "matrix", Result: raw}
}

// TestPreviewMetricThreshold_CompilesToMetricRaw confirms the
// Phase-3-final collapse: a kind=metric_threshold submission still
// works as the editor's friendly preview entry point, but now flows
// through buildRuleRow's metric_threshold→metric_raw compile path
// before reaching the previewer. The previewer detects the trailing
// comparison and re-queries the LHS so the chart still gets a line.
func TestPreviewMetricThreshold_CompilesToMetricRaw(t *testing.T) {
	prom := &fakePromRange{
		res: matrix(t, []struct {
			Ts    int64
			Value string
			Label map[string]string
		}{
			{Ts: 1714000000, Value: "92.3", Label: map[string]string{"device_id": "1"}},
			{Ts: 1714000060, Value: "95.1", Label: map[string]string{"device_id": "1"}},
			{Ts: 1714000120, Value: "94.0", Label: map[string]string{"device_id": "1"}},
		}),
	}
	deps := PreviewDeps{Prom: prom, Now: func() time.Time { return time.Unix(1714003600, 0).UTC() }}
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:   "cpu_high",
			Kind:      "metric_threshold",
			Name:      "test",
			Severity:  "warning",
			Enabled:   true,
			ScopeType: "host",
			Conditions: []model.RuleCondition{
				{Metric: "cpu_pct", Operator: ">=", Threshold: 90},
			},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, deps)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if res.FireCount != 3 {
		t.Fatalf("fire_count = %d, want 3", res.FireCount)
	}
	if res.FirstFireAt == nil || res.LastFireAt == nil {
		t.Fatalf("expected first/last fire timestamps populated")
	}
	if res.FirstFireAt.Unix() != 1714000000 {
		t.Errorf("first_fire_at = %s, want unix 1714000000", res.FirstFireAt)
	}
	if res.LastFireAt.Unix() != 1714000120 {
		t.Errorf("last_fire_at = %s, want unix 1714000120", res.LastFireAt)
	}
	if len(res.Samples) != 3 {
		t.Errorf("samples len = %d, want 3", len(res.Samples))
	}
	// fakePromRange.lastExpr captures the final call. Phase-3-final
	// flow: the predicate (compiled metric_threshold) runs first, then
	// the regex split re-queries the LHS — both contain the cpu base.
	if !strings.Contains(prom.lastExpr, "node_cpu_seconds_total") {
		t.Errorf("expr should contain cpu base expr, got %s", prom.lastExpr)
	}
	if res.Threshold == nil || *res.Threshold != 90 {
		t.Errorf("threshold should be 90 on result, got %v", res.Threshold)
	}
	if len(res.Series) == 0 {
		t.Errorf("series should be populated for chart preview")
	}
}

// TestPreviewLegacyKindNormalisesToMetricRaw confirms that a row whose
// kind escaped the SQLite migration (edge_absence / health_ingest /
// event_internal) gets normalised by NormalizeKind into metric_raw
// before previewing — the preview then falls through to the metric_raw
// previewer, which surfaces a "rule field" error because the legacy
// conditions_json shape is wrong. This is the safety net that keeps a
// stale UI form from crashing when the user opens it for migration.
func TestPreviewLegacyKindNormalisesToMetricRaw(t *testing.T) {
	cases := []string{"edge_absence", "health_ingest", "event_internal"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			in := RuleInput{
				RuleKey:  "legacy",
				Kind:     kind,
				Name:     "x",
				Severity: "warning",
				Enabled:  true,
				Spec:     map[string]any{},
			}
			res, err := PreviewRule(context.Background(),
				PreviewInput{Input: in, LookbackSeconds: 3600}, PreviewDeps{})
			// The conditions_json built from an empty Spec doesn't satisfy
			// the metric_raw compile contract, so we expect either an
			// invalid-input error from buildRuleRow or a skipped_reason
			// with "请补全规则字段" — both are valid post-migration paths.
			if err != nil {
				if !strings.Contains(err.Error(), "metric_raw") &&
					!strings.Contains(err.Error(), "expr") {
					t.Errorf("unexpected error for legacy %q: %v", kind, err)
				}
				return
			}
			if res.SkippedReason == "" {
				t.Errorf("expected error or skipped_reason for legacy %q, got nil", kind)
			}
		})
	}
}

func TestPreviewMetricRaw_NoPromClientReturnsSkipped(t *testing.T) {
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:   "rawq",
			Kind:      "metric_raw",
			Name:      "raw",
			Severity:  "warning",
			Enabled:   true,
			ScopeType: "global",
			Spec: map[string]any{
				"expr": "up == 0",
			},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, PreviewDeps{}) // no Prom client
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if !strings.Contains(res.SkippedReason, "Prometheus") {
		t.Errorf("expected skipped reason to mention Prometheus, got %q", res.SkippedReason)
	}
}

// TestPreviewMetricRaw_ExtractsThresholdFromComparison verifies the
// Phase-3 collapse: when the expr ends in `<lhs> <op> <number>`, the
// previewer extracts the number as the chart's horizontal threshold
// reference and re-queries the LHS for the chart's metric line. The
// fire timestamps still come from the predicate result.
func TestPreviewMetricRaw_ExtractsThresholdFromComparison(t *testing.T) {
	prom := &fakePromRange{
		res: matrix(t, []struct {
			Ts    int64
			Value string
			Label map[string]string
		}{
			{Ts: 1714000000, Value: "92.3", Label: map[string]string{"device_id": "1"}},
			{Ts: 1714000060, Value: "95.1", Label: map[string]string{"device_id": "1"}},
		}),
	}
	deps := PreviewDeps{Prom: prom, Now: func() time.Time { return time.Unix(1714003600, 0).UTC() }}
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:   "cpu_high",
			Kind:      "metric_raw",
			Name:      "raw",
			Severity:  "warning",
			Enabled:   true,
			ScopeType: "global",
			Spec:      map[string]any{"expr": "cpu_pct > 90"},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, deps)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if res.FireCount != 2 {
		t.Errorf("fire_count = %d, want 2", res.FireCount)
	}
	if res.Threshold == nil || *res.Threshold != 90 {
		t.Errorf("expected threshold extracted as 90, got %v", res.Threshold)
	}
	if len(res.Series) == 0 {
		t.Errorf("series should be populated from LHS query")
	}
	// fakePromRange.lastExpr captures the second call (the LHS line
	// query), confirming the re-query path ran.
	if !strings.Contains(prom.lastExpr, "cpu_pct") {
		t.Errorf("LHS line query expected to contain cpu_pct, got %q", prom.lastExpr)
	}
}

// TestPreviewMetricRaw_NoComparisonShipsFireCountWithoutChart verifies
// the fallback: a compound predicate with no trailing comparison
// returns fire_count + samples but the chart line is empty (UI then
// hides the line and just shows the matched timestamps).
func TestPreviewMetricRaw_NoComparisonShipsFireCountWithoutChart(t *testing.T) {
	prom := &fakePromRange{
		res: matrix(t, []struct {
			Ts    int64
			Value string
			Label map[string]string
		}{
			{Ts: 1714000000, Value: "1", Label: map[string]string{"job": "x"}},
		}),
	}
	deps := PreviewDeps{Prom: prom, Now: func() time.Time { return time.Unix(1714003600, 0).UTC() }}
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:   "compound",
			Kind:      "metric_raw",
			Name:      "raw",
			Severity:  "warning",
			Enabled:   true,
			ScopeType: "global",
			// "and" / "or" expressions don't terminate in <op> <num>;
			// regex extraction skips and we ship fire_count alone.
			Spec: map[string]any{"expr": "up == 0 and on() vector(1)"},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, deps)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if res.FireCount == 0 {
		t.Errorf("fire_count = 0, want > 0")
	}
	if res.Threshold != nil {
		t.Errorf("threshold should be nil for compound expr, got %v", *res.Threshold)
	}
	if len(res.Series) != 0 {
		t.Errorf("series should be empty for compound expr (no trailing comparison)")
	}
}

func TestPreviewTraceLatency_WhenServiceMissingReturnsSkipped(t *testing.T) {
	prom := &fakePromRange{
		responses: []*promquery.InstantResult{
			matrix(t, nil),
		},
	}
	deps := PreviewDeps{Prom: prom, Now: func() time.Time { return time.Unix(1714003600, 0).UTC() }}
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:  "trace_latency_missing",
			Kind:     "trace_latency",
			Name:     "trace latency",
			Severity: "warning",
			Enabled:  true,
			Spec: map[string]any{
				"service":      "missing-service",
				"threshold_ms": 750,
			},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, deps)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if !strings.Contains(res.SkippedReason, `未发现 service_name="missing-service"`) {
		t.Fatalf("skipped_reason = %q, want missing service_name", res.SkippedReason)
	}
	if len(prom.queries) != 1 || !strings.Contains(prom.queries[0], "traces_spanmetrics_latency_bucket") {
		t.Fatalf("queries = %#v, want only latency spanmetrics lookup", prom.queries)
	}
}

func TestPreviewTraceLatency_WhenServiceExistsQueriesPredicate(t *testing.T) {
	prom := &fakePromRange{
		responses: []*promquery.InstantResult{
			matrix(t, []struct {
				Ts    int64
				Value string
				Label map[string]string
			}{
				{Ts: 1714000000, Value: "1", Label: map[string]string{"service_name": "checkout"}},
			}),
			matrix(t, []struct {
				Ts    int64
				Value string
				Label map[string]string
			}{
				{Ts: 1714000060, Value: "810", Label: map[string]string{"service_name": "checkout"}},
			}),
		},
	}
	deps := PreviewDeps{Prom: prom, Now: func() time.Time { return time.Unix(1714003600, 0).UTC() }}
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:  "trace_latency_checkout",
			Kind:     "trace_latency",
			Name:     "trace latency",
			Severity: "warning",
			Enabled:  true,
			Spec: map[string]any{
				"service":      "checkout",
				"operation":    "GET /api",
				"quantile":     "p95",
				"threshold_ms": 750,
			},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, deps)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if res.SkippedReason != "" {
		t.Fatalf("skipped_reason = %q, want empty", res.SkippedReason)
	}
	if len(prom.queries) != 2 {
		t.Fatalf("queries = %#v, want lookup and predicate", prom.queries)
	}
	if !strings.Contains(prom.queries[0], `service_name="checkout",span_name="GET /api"`) {
		t.Fatalf("lookup query = %q, want service and span_name selector", prom.queries[0])
	}
	if !strings.Contains(prom.queries[1], "histogram_quantile") {
		t.Fatalf("predicate query = %q, want latency predicate", prom.queries[1])
	}
}

func TestPreviewTraceErrorRate_WhenServiceMissingReturnsSkipped(t *testing.T) {
	prom := &fakePromRange{
		responses: []*promquery.InstantResult{
			matrix(t, nil),
		},
	}
	deps := PreviewDeps{Prom: prom, Now: func() time.Time { return time.Unix(1714003600, 0).UTC() }}
	in := PreviewInput{
		Input: RuleInput{
			RuleKey:  "trace_error_missing",
			Kind:     "trace_error_rate",
			Name:     "trace error rate",
			Severity: "warning",
			Enabled:  true,
			Spec: map[string]any{
				"service":       "missing-service",
				"threshold_pct": 5,
			},
		},
		LookbackSeconds: 3600,
	}
	res, err := PreviewRule(context.Background(), in, deps)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if !strings.Contains(res.SkippedReason, `未发现 service_name="missing-service"`) {
		t.Fatalf("skipped_reason = %q, want missing service_name", res.SkippedReason)
	}
	if len(prom.queries) != 1 || !strings.Contains(prom.queries[0], "traces_spanmetrics_calls_total") {
		t.Fatalf("queries = %#v, want only calls spanmetrics lookup", prom.queries)
	}
}
