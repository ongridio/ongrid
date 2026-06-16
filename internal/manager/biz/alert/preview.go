package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

// PreviewInput is the parameter object PreviewRule consumes. It mirrors
// the shape of biz.RuleInput plus a lookback window in seconds — the
// preview never persists, so RuleKey may be empty / scratch.
type PreviewInput struct {
	Input           RuleInput
	LookbackSeconds int
}

// PreviewSample is one fire instant the preview detected, with the
// summary string the live evaluator would have built.
type PreviewSample struct {
	Timestamp time.Time         `json:"ts"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Summary   string            `json:"summary"`
}

// PreviewSeriesPoint is one (ts, value) pair in the chart-friendly
// time series the editor renders. Time-ordered ascending. Surfaced via
// PreviewResult.Series so the UI can draw the metric line, threshold
// overlay and fire-region wash without re-querying Prom/Loki.
type PreviewSeriesPoint struct {
	Timestamp time.Time `json:"ts"`
	Value     float64   `json:"value"`
}

// PreviewResult is the response the HTTP handler renders to JSON.
type PreviewResult struct {
	FireCount     int             `json:"fire_count"`
	FirstFireAt   *time.Time      `json:"first_fire_at,omitempty"`
	LastFireAt    *time.Time      `json:"last_fire_at,omitempty"`
	Samples       []PreviewSample `json:"samples,omitempty"`
	SkippedReason string          `json:"skipped_reason,omitempty"`
	// Series is a time-ordered ascending sequence of points the editor
	// renders as a line chart. May be empty when the kind has no
	// time-series source (event_internal / edge_absence / health_ingest)
	// or when the underlying range query returned no data.
	Series []PreviewSeriesPoint `json:"series,omitempty"`
	// Threshold is the single horizontal reference value the UI overlays
	// on the chart. nil when the rule's threshold is per-series or
	// dynamic (anomaly z-score, forecast linear, burn_rate per-window).
	Threshold *float64 `json:"threshold,omitempty"`
	// Unit is the heuristic Y-axis suffix ("%", "ms", "rps", "bps") —
	// "" when the unit is unknown (raw PromQL, custom expressions).
	Unit string `json:"unit,omitempty"`
}

// maxPreviewSeriesPoints is the upper bound on points returned to the
// UI. A 24h window at 60s step yields 1440 points so 1500 leaves
// headroom; larger ranges get downsampled by skipping samples evenly.
const (
	maxPreviewSeriesPoints        = 1500
	defaultPreviewLookbackSeconds = 86400
	minPreviewLookbackSeconds     = 60
	maxPreviewLookbackSeconds     = 604800
)

// PreviewPromQuerier is the narrow PromQL range surface PreviewRule
// needs. *promquery.Client satisfies it.
type PreviewPromQuerier interface {
	QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error)
}

// PreviewLogQuerier is the narrow LogQL surface PreviewRule needs.
type PreviewLogQuerier interface {
	QueryRange(ctx context.Context, opts logquery.QueryRangeOptions) (*logquery.QueryRangeResult, error)
}

// PreviewDeps bundles the optional clients PreviewRule fans out to. Any
// nil dep makes the corresponding kind return a skipped_reason instead
// of crashing.
type PreviewDeps struct {
	Prom PreviewPromQuerier
	Log  PreviewLogQuerier
	Now  func() time.Time
}

// PreviewRule is the read-only side-channel for the rule editor's
// "试算" button. It validates+compiles the input rule (reusing the
// same code path as CreateRule), then dispatches to a per-kind
// previewer. Persists nothing.
func PreviewRule(ctx context.Context, in PreviewInput, deps PreviewDeps) (*PreviewResult, error) {
	row, err := buildRuleRow(in.Input, false)
	if err != nil {
		return nil, err
	}
	if !model.IsKnownKind(row.Kind) {
		return nil, fmt.Errorf("preview: unknown kind %q", row.Kind)
	}
	// Preview is a read-only side-channel — none of the per-kind compile
	// functions need a real rule_key / name / severity (those identify
	// the rule for storage / dedupe / notification, not for PromQL).
	// Plug in placeholders so compile* doesn't trip on them and the user
	// can iterate on the query before naming the rule.
	if row.RuleKey == "" {
		row.RuleKey = "__preview__"
	}
	if row.Name == "" {
		row.Name = "preview"
	}
	if row.Severity == "" {
		row.Severity = "info"
	}
	now := time.Now().UTC()
	if deps.Now != nil {
		now = deps.Now()
	}
	lookback := normalizePreviewLookbackSeconds(in.LookbackSeconds)
	start := now.Add(-time.Duration(lookback) * time.Second)

	// metric_threshold is a UI-only entry form: buildRuleRow has already
	// rewritten Kind to metric_raw and ConditionsJSON to {"expr": "<compiled>"}.
	// We do NOT see metric_threshold here in normal flows, but legacy rows
	// (or a UI that hasn't been redeployed) might still arrive — the
	// NormalizeKind alias path keeps them working.
	switch row.Kind {
	case model.RuleKindMetricAnomaly:
		return previewMetricAnomaly(ctx, row, start, now, deps)
	case model.RuleKindMetricForecast:
		return previewMetricForecast(ctx, row, start, now, deps)
	case model.RuleKindMetricBurnRate:
		return previewMetricBurnRate(ctx, row, start, now, deps)
	case model.RuleKindMetricRaw:
		return previewMetricRaw(ctx, row, start, now, deps)
	case model.RuleKindLogMatch:
		return previewLogMatch(ctx, row, start, now, deps)
	case model.RuleKindLogVolume:
		return previewLogVolume(ctx, row, start, now, deps)
	case model.RuleKindTraceLatency:
		return previewTraceLatency(ctx, row, start, now, deps)
	case model.RuleKindTraceErrorRate:
		return previewTraceErrorRate(ctx, row, start, now, deps)
	}
	return &PreviewResult{SkippedReason: "kind not supported by preview"}, nil
}

func normalizePreviewLookbackSeconds(lookback int) int {
	switch {
	case lookback <= 0:
		return defaultPreviewLookbackSeconds
	case lookback < minPreviewLookbackSeconds:
		return minPreviewLookbackSeconds
	case lookback > maxPreviewLookbackSeconds:
		return maxPreviewLookbackSeconds
	default:
		return lookback
	}
}

// ---- Prom matrix walker -----------------------------------------------------

type matrixSeries struct {
	Metric map[string]string   `json:"metric"`
	Values [][]json.RawMessage `json:"values"`
}

// walkPromMatrix runs a Prom range query and invokes hit() for every
// (timestamp, labels, value) tuple where keep returns true. Caller-built
// keep / fmtSummary keep the fan-out lean.
//
// In addition to fire detection, walkPromMatrix selects a single
// representative series for the chart preview: the series with the
// most points (ties broken by highest average value). That's the
// "biggest" line in the matrix — what an operator would naturally
// look at first. Multi-series matrices get a single line; v1 doesn't
// stack overlays.
func walkPromMatrix(
	ctx context.Context,
	prom PreviewPromQuerier,
	expr string,
	start, end time.Time,
	step time.Duration,
	keep func(value float64) bool,
	fmtSummary func(labels map[string]string, value float64, ts time.Time) string,
) (*PreviewResult, error) {
	if prom == nil {
		return &PreviewResult{SkippedReason: "Prometheus client 未配置 — 无法试算"}, nil
	}
	res, err := prom.QueryRange(ctx, expr, start, end, step)
	if err != nil {
		return nil, fmt.Errorf("preview prom query: %w", err)
	}
	if res == nil {
		return &PreviewResult{}, nil
	}
	if res.ResultType != "matrix" {
		return &PreviewResult{SkippedReason: fmt.Sprintf("expected matrix result, got %q", res.ResultType)}, nil
	}
	var series []matrixSeries
	if err := json.Unmarshal(res.Result, &series); err != nil {
		return nil, fmt.Errorf("preview decode matrix: %w", err)
	}
	out := &PreviewResult{}
	type fired struct {
		ts      time.Time
		labels  map[string]string
		value   float64
		summary string
	}
	var hits []fired
	// Collect every series's full point list so we can later pick the
	// representative line for the chart (largest series wins).
	all := make([]previewSeries, 0, len(series))
	for _, s := range series {
		var pts []PreviewSeriesPoint
		for _, v := range s.Values {
			ts, value, ok := decodeMatrixSample(v)
			if !ok {
				continue
			}
			pts = append(pts, PreviewSeriesPoint{Timestamp: ts, Value: value})
			if keep != nil && !keep(value) {
				continue
			}
			summary := ""
			if fmtSummary != nil {
				summary = fmtSummary(s.Metric, value, ts)
			}
			hits = append(hits, fired{
				ts:      ts,
				labels:  s.Metric,
				value:   value,
				summary: summary,
			})
		}
		if len(pts) > 0 {
			all = append(all, previewSeries{labels: s.Metric, points: pts})
		}
	}
	out.Series = pickRepresentativeSeries(all)
	if len(hits) == 0 {
		return out, nil
	}
	// Sort ascending; oldest first / newest last so first/last are well-defined.
	sort.Slice(hits, func(i, j int) bool { return hits[i].ts.Before(hits[j].ts) })
	out.FireCount = len(hits)
	first := hits[0].ts
	last := hits[len(hits)-1].ts
	out.FirstFireAt = &first
	out.LastFireAt = &last
	// Sample the most-recent 5 (newest first).
	tail := hits
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	for i := len(tail) - 1; i >= 0; i-- {
		h := tail[i]
		out.Samples = append(out.Samples, PreviewSample{
			Timestamp: h.ts,
			Labels:    h.labels,
			Value:     h.value,
			Summary:   h.summary,
		})
	}
	return out, nil
}

// previewSeries bundles one matrix series's labels with its decoded
// points so we can pick a representative line for the chart preview.
type previewSeries struct {
	labels map[string]string
	points []PreviewSeriesPoint
}

// pickRepresentativeSeries selects one series from a multi-series
// matrix to feed the chart. Largest point count wins; ties are broken
// by highest mean value (the "loudest" line). The returned slice is
// time-sorted and capped at maxPreviewSeriesPoints via even-step
// downsampling.
func pickRepresentativeSeries(all []previewSeries) []PreviewSeriesPoint {
	if len(all) == 0 {
		return nil
	}
	bestIdx := 0
	bestLen := len(all[0].points)
	bestMean := meanOf(all[0].points)
	for i := 1; i < len(all); i++ {
		l := len(all[i].points)
		if l > bestLen {
			bestIdx = i
			bestLen = l
			bestMean = meanOf(all[i].points)
			continue
		}
		if l == bestLen {
			m := meanOf(all[i].points)
			if m > bestMean {
				bestIdx = i
				bestMean = m
			}
		}
	}
	pts := append([]PreviewSeriesPoint(nil), all[bestIdx].points...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].Timestamp.Before(pts[j].Timestamp) })
	return downsampleSeries(pts, maxPreviewSeriesPoints)
}

func meanOf(pts []PreviewSeriesPoint) float64 {
	if len(pts) == 0 {
		return 0
	}
	var sum float64
	for _, p := range pts {
		sum += p.Value
	}
	return sum / float64(len(pts))
}

// downsampleSeries thins a series down to at most cap points by
// keeping every Nth sample. A naive stride avoids the cost of
// LTTB/min-max while still preserving overall shape for editor preview.
func downsampleSeries(pts []PreviewSeriesPoint, cap int) []PreviewSeriesPoint {
	if cap <= 0 || len(pts) <= cap {
		return pts
	}
	stride := (len(pts) + cap - 1) / cap // ceil
	out := make([]PreviewSeriesPoint, 0, cap+1)
	for i := 0; i < len(pts); i += stride {
		out = append(out, pts[i])
	}
	// Always keep the last point so the chart's right edge is anchored.
	if last := pts[len(pts)-1]; len(out) == 0 || !out[len(out)-1].Timestamp.Equal(last.Timestamp) {
		out = append(out, last)
	}
	return out
}

// decodeMatrixSample reads a [<unix_seconds>, "<float>"] pair.
func decodeMatrixSample(v []json.RawMessage) (time.Time, float64, bool) {
	if len(v) < 2 {
		return time.Time{}, 0, false
	}
	var ts float64
	if err := json.Unmarshal(v[0], &ts); err != nil {
		return time.Time{}, 0, false
	}
	var s string
	if err := json.Unmarshal(v[1], &s); err != nil {
		return time.Time{}, 0, false
	}
	val, ok := parseFloat(s)
	if !ok {
		return time.Time{}, 0, false
	}
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC(), val, true
}

// ---- per-kind previewers ----------------------------------------------------

func previewMetricAnomaly(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	r, err := compileMetricAnomalyRule(row)
	if err != nil {
		return &PreviewResult{SkippedReason: "请补全规则字段：" + err.Error()}, nil
	}
	base, ok := metricExprFor(r.Metric)
	if !ok {
		return &PreviewResult{SkippedReason: fmt.Sprintf("metric %q 不在 closed-set", r.Metric)}, nil
	}
	base = applyClosedSetMetricSelector(base, r.Selector)
	var expr string
	switch r.Method {
	case "mad":
		med := fmt.Sprintf("quantile_over_time(0.5, (%s)[%s:%s])", base, r.BaselineWindow, r.BaselineStep)
		disp := fmt.Sprintf("avg_over_time((abs((%s) - (%s)))[%s:%s])", base, med, r.BaselineWindow, r.BaselineStep)
		expr = fmt.Sprintf("abs((%s) - (%s)) > %g * (%s)", base, med, r.Deviation, disp)
	default:
		mean := fmt.Sprintf("avg_over_time((%s)[%s:%s])", base, r.BaselineWindow, r.BaselineStep)
		std := fmt.Sprintf("stddev_over_time((%s)[%s:%s])", base, r.BaselineWindow, r.BaselineStep)
		expr = fmt.Sprintf("abs((%s) - (%s)) > %g * (%s)", base, mean, r.Deviation, std)
	}
	return walkPromMatrix(ctx, deps.Prom, expr, start, end, 60*time.Second, nil,
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: %s 偏离基线 ≥ %gσ (labels=%s)",
				r.RuleKey, r.Metric, r.Deviation, labelSetKey(labels))
		})
}

func previewMetricForecast(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	r, err := compileMetricForecastRule(row)
	if err != nil {
		return &PreviewResult{SkippedReason: "请补全规则字段：" + err.Error()}, nil
	}
	base, ok := metricExprFor(r.Metric)
	if !ok {
		return &PreviewResult{SkippedReason: fmt.Sprintf("metric %q 不在 closed-set", r.Metric)}, nil
	}
	base = applyClosedSetMetricSelector(base, r.Selector)
	expr := fmt.Sprintf("predict_linear((%s)[%s:5m], %d) %s %g",
		base, r.FitWindow, r.PredictSeconds, r.Operator, r.Threshold)
	return walkPromMatrix(ctx, deps.Prom, expr, start, end, 5*time.Minute, nil,
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: %s 预计 %ds 后 %s %g (labels=%s)",
				r.RuleKey, r.Metric, r.PredictSeconds, r.Operator, r.Threshold, labelSetKey(labels))
		})
}

func previewMetricBurnRate(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	r, err := compileMetricBurnRateRule(row)
	if err != nil {
		return &PreviewResult{SkippedReason: "请补全规则字段：" + err.Error()}, nil
	}
	if len(r.Burns) == 0 {
		return &PreviewResult{SkippedReason: "no burn windows"}, nil
	}
	// Preview the shortest-window burn — that's the leading indicator the
	// SRE Workbook recipe gates on. The fastest signal is the most useful
	// for tuning.
	b := r.Burns[0]
	for _, candidate := range r.Burns[1:] {
		if shorterWindow(candidate.Window, b.Window) {
			b = candidate
		}
	}
	budget := 1 - r.SLO/100
	expr := fmt.Sprintf("(1 - (%s)) >= %g", windowedSLI(r.SLI, b.Window), b.Multiplier*budget)
	return walkPromMatrix(ctx, deps.Prom, expr, start, end, 60*time.Second, nil,
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: SLO %.2f%% burn rate triggered (window=%s ×%g)",
				r.RuleKey, r.SLO, b.Window, b.Multiplier)
		})
}

// metricRawComparisonRE captures the trailing comparison in a
// metric_raw expression: `<lhs> <op> <number>`. When matched, the
// preview can split the expression into a base series query (for the
// chart line) plus a horizontal threshold reference. When unmatched
// (e.g. `up == 0` after Prom-level filtering, or compound `and`/`or`
// expressions), the chart skips the line and only renders fire_count
// + samples.
var metricRawComparisonRE = regexp.MustCompile(`^(.+?)\s*(==|!=|>=?|<=?)\s*([+-]?[0-9.]+(?:[eE][+-]?\d+)?)\s*$`)

func previewMetricRaw(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	r, err := compileMetricRawRule(row)
	if err != nil {
		return &PreviewResult{SkippedReason: "请补全规则字段：" + err.Error()}, nil
	}
	if deps.Prom == nil {
		return &PreviewResult{SkippedReason: "Prometheus client 未配置 — 无法试算"}, nil
	}
	// Phase-3 collapse: the expression IS the predicate. Run it as-is
	// and treat every returned matrix point as a fire. Then heuristically
	// extract the trailing `<lhs> <op> <number>` so the chart can show
	// the LHS as a line plus a horizontal threshold reference.
	res, err := walkPromMatrix(ctx, deps.Prom, r.Expr, start, end, 60*time.Second, nil,
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: %s ⇒ %g (labels=%s)",
				r.RuleKey, r.Expr, value, labelSetKey(labels))
		})
	if err != nil || res == nil {
		return res, err
	}
	// Try to upgrade the chart to "line + threshold reference" when the
	// trailing comparison is detectable. Failure to extract = ship the
	// fire counts but no chart line.
	if m := metricRawComparisonRE.FindStringSubmatch(strings.TrimSpace(r.Expr)); m != nil {
		lhs := strings.TrimSpace(m[1])
		thrStr := m[3]
		thr, perr := strconv.ParseFloat(thrStr, 64)
		if perr == nil && lhs != "" {
			// Re-query the LHS so the chart shows the underlying metric
			// curve. Fires already counted from the predicate query.
			lineRes, lerr := walkPromMatrix(ctx, deps.Prom, lhs, start, end, 60*time.Second, nil, nil)
			if lerr == nil && lineRes != nil && len(lineRes.Series) > 0 {
				res.Series = lineRes.Series
			}
			tcopy := thr
			res.Threshold = &tcopy
		}
	} else {
		// No comparison detected — chart line stays empty (fire_count
		// + samples still render).
		res.Series = nil
	}
	return res, nil
}

func previewLogMatch(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	if deps.Log == nil {
		return &PreviewResult{SkippedReason: "Loki client 未配置 — 无法试算 log_match"}, nil
	}
	var spec struct {
		StreamSelector string  `json:"stream_selector"`
		LineFilter     string  `json:"line_filter"`
		Window         string  `json:"window"`
		Operator       string  `json:"operator"`
		Threshold      float64 `json:"threshold"`
	}
	if err := json.Unmarshal([]byte(row.ConditionsJSON), &spec); err != nil {
		return nil, fmt.Errorf("preview log_match decode: %w", err)
	}
	if strings.TrimSpace(spec.StreamSelector) == "" {
		return &PreviewResult{SkippedReason: "stream_selector 为空"}, nil
	}
	win := spec.Window
	if win == "" {
		win = "5m"
	}
	op := spec.Operator
	if op == "" {
		op = ">="
	}
	expr := fmt.Sprintf("count_over_time(%s [%s])", spec.StreamSelector, win)
	if strings.TrimSpace(spec.LineFilter) != "" {
		expr = fmt.Sprintf("count_over_time(%s |~ %q [%s])", spec.StreamSelector, spec.LineFilter, win)
	}
	return walkLokiMatrix(ctx, deps.Log, expr, start, end, 5*time.Minute,
		func(v float64) bool { return compareFloat(v, op, spec.Threshold) },
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: log_match 命中 %g 条 %s %g (labels=%s)",
				row.RuleKey, value, op, spec.Threshold, labelSetKey(labels))
		})
}

func previewLogVolume(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	if deps.Log == nil {
		return &PreviewResult{SkippedReason: "Loki client 未配置 — 无法试算 log_volume"}, nil
	}
	var spec struct {
		StreamSelector string  `json:"stream_selector"`
		LineFilter     string  `json:"line_filter"`
		Window         string  `json:"window"`
		RatioOp        string  `json:"ratio_op"`
		RatioThreshold float64 `json:"ratio_threshold"`
	}
	if err := json.Unmarshal([]byte(row.ConditionsJSON), &spec); err != nil {
		return nil, fmt.Errorf("preview log_volume decode: %w", err)
	}
	if strings.TrimSpace(spec.StreamSelector) == "" {
		return &PreviewResult{SkippedReason: "stream_selector 为空"}, nil
	}
	win := spec.Window
	if win == "" {
		win = "5m"
	}
	// Approximation: count_over_time gives raw volume; we don't compute
	// the previous-window ratio in preview (would need a prom-style
	// offset). Treat any non-zero volume as a candidate fire and let the
	// operator tune the ratio against the absolute counts.
	expr := fmt.Sprintf("count_over_time(%s [%s])", spec.StreamSelector, win)
	if strings.TrimSpace(spec.LineFilter) != "" {
		expr = fmt.Sprintf("count_over_time(%s |~ %q [%s])", spec.StreamSelector, spec.LineFilter, win)
	}
	op := spec.RatioOp
	if op == "" {
		op = ">="
	}
	return walkLokiMatrix(ctx, deps.Log, expr, start, end, 5*time.Minute,
		// Preview can't backfill the ratio — surface the volume directly.
		func(v float64) bool { return compareFloat(v, op, spec.RatioThreshold) },
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: log_volume 当前窗口 %g 条 (labels=%s)", row.RuleKey, value, labelSetKey(labels))
		})
}

func traceSpanMetricsSelectorHasPoints(ctx context.Context, prom PreviewPromQuerier, metric, selector string, start, end time.Time) (bool, error) {
	if prom == nil {
		return true, nil
	}
	expr := fmt.Sprintf("count by (service_name) (%s{%s})", metric, selector)
	if strings.Contains(selector, "span_name=") {
		expr = fmt.Sprintf("count by (service_name, span_name) (%s{%s})", metric, selector)
	}
	res, err := prom.QueryRange(ctx, expr, start, end, 60*time.Second)
	if err != nil {
		return false, fmt.Errorf("preview trace spanmetrics lookup: %w", err)
	}
	if res == nil {
		return false, nil
	}
	if res.ResultType != "matrix" {
		return false, nil
	}
	var series []matrixSeries
	if err := json.Unmarshal(res.Result, &series); err != nil {
		return false, fmt.Errorf("preview trace spanmetrics decode: %w", err)
	}
	for _, s := range series {
		for _, v := range s.Values {
			if _, _, ok := decodeMatrixSample(v); ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func traceSpanMetricsMissingReason(metric, selector string) string {
	return fmt.Sprintf("当前 %s 未发现 %s", metric, selector)
}

func previewTraceLatency(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	var spec struct {
		Service     string  `json:"service"`
		Operation   string  `json:"operation"`
		Quantile    string  `json:"quantile"`
		Window      string  `json:"window"`
		ThresholdMs float64 `json:"threshold_ms"`
	}
	if err := json.Unmarshal([]byte(row.ConditionsJSON), &spec); err != nil {
		return nil, fmt.Errorf("preview trace_latency decode: %w", err)
	}
	if strings.TrimSpace(spec.Service) == "" {
		return &PreviewResult{SkippedReason: "service 为空"}, nil
	}
	q := 0.95
	switch spec.Quantile {
	case "p50":
		q = 0.5
	case "p95":
		q = 0.95
	case "p99":
		q = 0.99
	}
	win := spec.Window
	if win == "" {
		win = "5m"
	}
	selector := fmt.Sprintf(`service_name=%q`, spec.Service)
	if spec.Operation != "" {
		selector = fmt.Sprintf(`service_name=%q,span_name=%q`, spec.Service, spec.Operation)
	}
	exists, err := traceSpanMetricsSelectorHasPoints(ctx, deps.Prom, "traces_spanmetrics_latency_bucket", selector, start, end)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &PreviewResult{SkippedReason: traceSpanMetricsMissingReason("traces_spanmetrics_latency_bucket", selector)}, nil
	}
	// Tempo emits histogram buckets in seconds; threshold is given in ms.
	expr := fmt.Sprintf(
		"histogram_quantile(%g, sum by (le) (rate(traces_spanmetrics_latency_bucket{%s}[%s]))) * 1000 > %g",
		q, selector, win, spec.ThresholdMs)
	return walkPromMatrix(ctx, deps.Prom, expr, start, end, 60*time.Second, nil,
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: %s %s 延迟 %.1fms > %gms",
				row.RuleKey, spec.Service, spec.Quantile, value, spec.ThresholdMs)
		})
}

func previewTraceErrorRate(ctx context.Context, row *model.Rule, start, end time.Time, deps PreviewDeps) (*PreviewResult, error) {
	var spec struct {
		Service      string  `json:"service"`
		Window       string  `json:"window"`
		Operator     string  `json:"operator"`
		ThresholdPct float64 `json:"threshold_pct"`
	}
	if err := json.Unmarshal([]byte(row.ConditionsJSON), &spec); err != nil {
		return nil, fmt.Errorf("preview trace_error_rate decode: %w", err)
	}
	if strings.TrimSpace(spec.Service) == "" {
		return &PreviewResult{SkippedReason: "service 为空"}, nil
	}
	win := spec.Window
	if win == "" {
		win = "5m"
	}
	op := spec.Operator
	if op == "" {
		op = ">="
	}
	selector := fmt.Sprintf(`service_name=%q`, spec.Service)
	exists, err := traceSpanMetricsSelectorHasPoints(ctx, deps.Prom, "traces_spanmetrics_calls_total", selector, start, end)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &PreviewResult{SkippedReason: traceSpanMetricsMissingReason("traces_spanmetrics_calls_total", selector)}, nil
	}
	expr := fmt.Sprintf(
		"100 * (sum(rate(traces_spanmetrics_calls_total{service_name=%q,status_code=\"STATUS_CODE_ERROR\"}[%s])) / "+
			"sum(rate(traces_spanmetrics_calls_total{service_name=%q}[%s]))) %s %g",
		spec.Service, win, spec.Service, win, op, spec.ThresholdPct)
	return walkPromMatrix(ctx, deps.Prom, expr, start, end, 60*time.Second, nil,
		func(labels map[string]string, value float64, ts time.Time) string {
			return fmt.Sprintf("%s: %s 错误率 %.2f%% %s %g%%",
				row.RuleKey, spec.Service, value, op, spec.ThresholdPct)
		})
}

// ---- Loki matrix walker -----------------------------------------------------

func walkLokiMatrix(
	ctx context.Context,
	logc PreviewLogQuerier,
	expr string,
	start, end time.Time,
	step time.Duration,
	keep func(value float64) bool,
	fmtSummary func(labels map[string]string, value float64, ts time.Time) string,
) (*PreviewResult, error) {
	if logc == nil {
		return &PreviewResult{SkippedReason: "Loki client 未配置"}, nil
	}
	res, err := logc.QueryRange(ctx, logquery.QueryRangeOptions{
		Query: expr,
		Start: start,
		End:   end,
		Step:  step,
	})
	if err != nil {
		return nil, fmt.Errorf("preview loki query: %w", err)
	}
	if res == nil {
		return &PreviewResult{}, nil
	}
	if res.ResultType != "matrix" {
		return &PreviewResult{SkippedReason: fmt.Sprintf("expected matrix result, got %q", res.ResultType)}, nil
	}
	var series []matrixSeries
	if err := json.Unmarshal(res.Result, &series); err != nil {
		return nil, fmt.Errorf("preview decode loki matrix: %w", err)
	}
	out := &PreviewResult{}
	type fired struct {
		ts      time.Time
		labels  map[string]string
		value   float64
		summary string
	}
	var hits []fired
	for _, s := range series {
		for _, v := range s.Values {
			ts, value, ok := decodeMatrixSample(v)
			if !ok {
				continue
			}
			if keep != nil && !keep(value) {
				continue
			}
			hits = append(hits, fired{
				ts: ts, labels: s.Metric, value: value,
				summary: fmtSummary(s.Metric, value, ts),
			})
		}
	}
	if len(hits) == 0 {
		return out, nil
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].ts.Before(hits[j].ts) })
	out.FireCount = len(hits)
	first := hits[0].ts
	last := hits[len(hits)-1].ts
	out.FirstFireAt = &first
	out.LastFireAt = &last
	tail := hits
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	for i := len(tail) - 1; i >= 0; i-- {
		h := tail[i]
		out.Samples = append(out.Samples, PreviewSample{
			Timestamp: h.ts,
			Labels:    h.labels,
			Value:     h.value,
			Summary:   h.summary,
		})
	}
	return out, nil
}

// shorterWindow compares two PromQL duration strings ("5m", "1h"). A cheap
// scan is enough for the common burn-rate windows; on a parse failure we
// fall back to lexicographic compare so the function never panics.
func shorterWindow(a, b string) bool {
	da, oka := promDurationSeconds(a)
	db, okb := promDurationSeconds(b)
	if !oka || !okb {
		return a < b
	}
	return da < db
}

func promDurationSeconds(d string) (int, bool) {
	if d == "" {
		return 0, false
	}
	unit := d[len(d)-1]
	num := d[:len(d)-1]
	var n int
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil {
		return 0, false
	}
	switch unit {
	case 's':
		return n, true
	case 'm':
		return n * 60, true
	case 'h':
		return n * 3600, true
	case 'd':
		return n * 86400, true
	}
	return 0, false
}
