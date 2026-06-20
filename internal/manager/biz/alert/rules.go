package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// effectiveScope returns the rule's stored scope_type, defaulting per
// kind when empty. Empty values appear on (a) older rule rows written
// before scope_type was a required field, and (b) test fixtures that
// only set the fields under test. Keeping the default in one place
// means evaluator code can trust rule.ScopeType to be non-empty.
func effectiveScope(scope, kind string) string {
	if scope != "" {
		return scope
	}
	return defaultScopeForKind(kind)
}

// MetricRawRule is the compiled form of a kind=metric_raw rule (the
// rename of prom_query). The evaluator runs Query(Expr) every
// tick and fires for every series with at least one returned sample.
//
// Phase-3 collapse note: Operator+Threshold/ForSeconds were removed —
// PromQL's own comparison operators (`up == 0`, `cpu_pct > 90`) already
// return 0/1 (or absence) and are the canonical predicate. Splitting
// them client-side just duplicated work.
type MetricRawRule struct {
	ID         uint64
	RuleKey    string
	Name       string
	Severity   string
	ScopeType  string // host / global / monitoring_pipeline
	RunbookURL string
	Labels     map[string]string
	Expr       string
}

// MetricAnomalyRule is the compiled form of a kind=metric_anomaly rule.
// The evaluator turns the spec into a PromQL expression that compares the
// current value of Metric against the rolling baseline (mean ± Deviation
// × stddev) over BaselineWindow, in BaselineStep buckets. When Method is
// "mad" the evaluator substitutes median + MAD for the baseline.
//
// Labels propagated from rule.Labels are merged onto the firing payload
// alongside any labels returned by the underlying instant query.
type MetricAnomalyRule struct {
	ID             uint64
	RuleKey        string
	Name           string
	Severity       string
	ScopeType      string
	RunbookURL     string
	Labels         map[string]string
	Metric         string  // e.g. "cpu_pct" — resolved to the canonical PromQL via metricExprFor
	Selector       string  // optional Prometheus selector merged into the closed-set metric expression
	Method         string  // "zscore" | "mad" (default zscore)
	BaselineWindow string  // PromQL duration ("1h")
	BaselineStep   string  // sub-query step ("5m"); defaults to 5m
	Deviation      float64 // ≥ this many σ (or MAD multiples) triggers
	ForSeconds     int     // optional dwell time
}

// MetricForecastRule is the compiled form of a kind=metric_forecast
// rule. It generates predict_linear(metric[FitWindow], PredictSeconds)
// [Operator] Threshold and fires on any returned vector entry. Useful
// for "disk fills in the next 6h" style alarms.
type MetricForecastRule struct {
	ID             uint64
	RuleKey        string
	Name           string
	Severity       string
	ScopeType      string
	RunbookURL     string
	Labels         map[string]string
	Metric         string  // canonical metric (cpu_pct / disk_avail_bytes / ...)
	Selector       string  // optional Prometheus selector merged into the closed-set metric expression
	FitWindow      string  // PromQL duration the linear fit looks back over
	PredictSeconds int     // how far into the future to extrapolate
	Operator       string  // ">", ">=", "<", "<=", "==", "!="
	Threshold      float64 // value to compare predicted point against
	ForSeconds     int
}

// LogMatchRule is the compiled form of a kind=log_match rule (
// Phase-B). The evaluator runs `count_over_time(<stream> |~ <filter>
// [window])` against Loki every tick and fires for each label-set whose
// count satisfies Operator+Threshold. LineFilter is optional — when
// empty, the rule counts every line in the stream.
type LogMatchRule struct {
	ID             uint64
	RuleKey        string
	Name           string
	Severity       string
	ScopeType      string
	RunbookURL     string
	Labels         map[string]string
	StreamSelector string
	LineFilter     string
	Window         string
	Operator       string  // ">", ">=", "<", "<=", "==", "!="
	Threshold      float64 // count threshold
}

// LogVolumeRule is the compiled form of a kind=log_volume rule. v1
// implementation: same engine as log_match (current-window count
// against an absolute threshold). The original "ratio vs previous
// window" semantics in the spec is left for a future refinement —
// would need two LogQL queries + Go-side division. The current shape
// already covers "log volume crossed N" alerts which is the common
// real-world ask.
type LogVolumeRule struct {
	ID             uint64
	RuleKey        string
	Name           string
	Severity       string
	ScopeType      string
	RunbookURL     string
	Labels         map[string]string
	StreamSelector string
	LineFilter     string
	Window         string
	Operator       string
	Threshold      float64
}

// TraceLatencyRule is the compiled form of a kind=trace_latency rule.
// We query Prom (NOT Tempo directly) — spanmetrics generator scrapes
// Tempo into traces_spanmetrics_latency_bucket. The compiled Expr is
// a histogram_quantile() comparison that lets Prom filter to only
// breaching series, matching the metric_raw evaluator pattern.
type TraceLatencyRule struct {
	ID         uint64
	RuleKey    string
	Name       string
	Severity   string
	ScopeType  string
	RunbookURL string
	Labels     map[string]string
	Expr       string // pre-built histogram_quantile(...) > threshold_ms expression
	Spec       traceLatencySpec
}

// TraceErrorRateRule is the compiled form of a kind=trace_error_rate
// rule. Like trace_latency, queries Prom (spanmetrics_calls_total split
// by status_code). Fires when error percentage crosses threshold_pct.
type TraceErrorRateRule struct {
	ID         uint64
	RuleKey    string
	Name       string
	Severity   string
	ScopeType  string
	RunbookURL string
	Labels     map[string]string
	Expr       string
	Spec       traceErrorRateSpec
}

// MetricBurnRateRule is the compiled form of a kind=metric_burn_rate
// rule. It implements Google SRE Workbook's multi-window multi-burn-rate
// alert for SLO error budgets: every Window in Burns must report
// (1 - SLI[window]) ≥ Multiplier × (1 - SLO/100). All windows must
// trigger together to fire — that is the whole point of the multi-burn
// pattern (it filters out brief blips while staying fast on real
// failures).
type MetricBurnRateRule struct {
	ID         uint64
	RuleKey    string
	Name       string
	Severity   string
	ScopeType  string
	RunbookURL string
	Labels     map[string]string
	SLI        string           // PromQL expression that yields a 0..1 success ratio over a duration
	SLO        float64          // target % (e.g. 99.9 → budget 0.001)
	Burns      []BurnRateWindow // windows + multipliers; OR-ed never, AND-ed always
}

// BurnRateWindow is one (window, multiplier) tuple in a burn-rate rule.
type BurnRateWindow struct {
	Window     string  // PromQL duration: "5m" / "1h" / "6h"
	Multiplier float64 // 14.4 / 6 / 3 / 1 (per SRE Workbook recipe)
}

// RuleSource is the narrow biz.Repo subset CachedRulesProvider needs.
type RuleSource interface {
	ListAllEnabledRules(ctx context.Context) ([]*model.Rule, error)
}

// RulesProvider hands the evaluator a snapshot of currently-enabled rules,
// bucketed by kind. Each accessor returns a slice the caller MUST treat as
// read-only — the cache reuses the underlying array across reads.
//
// Phase-3 final collapse: HostRules() was removed when metric_threshold
// became a UI-only entry form that compiles to metric_raw at save time.
// All host-scoped threshold alerts now flow through the metric_raw
// evaluator on the same Prom queries the friendly form would have built.
type RulesProvider interface {
	MetricRawRules() []MetricRawRule
	MetricAnomalyRules() []MetricAnomalyRule
	MetricForecastRules() []MetricForecastRule
	MetricBurnRateRules() []MetricBurnRateRule
	LogMatchRules() []LogMatchRule
	LogVolumeRules() []LogVolumeRule
	TraceLatencyRules() []TraceLatencyRule
	TraceErrorRateRules() []TraceErrorRateRule
}

// rulesSnapshot is the immutable bundle CachedRulesProvider swaps in
// atomically every refresh.
type rulesSnapshot struct {
	metricRaw      []MetricRawRule
	metricAnomaly  []MetricAnomalyRule
	metricForecast []MetricForecastRule
	metricBurnRate []MetricBurnRateRule
	logMatch       []LogMatchRule
	logVolume      []LogVolumeRule
	traceLatency   []TraceLatencyRule
	traceErrorRate []TraceErrorRateRule
}

// StaticRulesProvider serves a fixed snapshot. Used in tests and embedded
// deployments without a DB.
type StaticRulesProvider struct {
	snap rulesSnapshot
}

// NewStaticRulesProvider builds a provider from literal rule lists.
// Pass any combination of WithMetricRawRules / WithMetricAnomalyRules /
// WithMetricForecastRules / WithMetricBurnRateRules.
func NewStaticRulesProvider(opts ...StaticOption) *StaticRulesProvider {
	s := &StaticRulesProvider{}
	for _, opt := range opts {
		opt(&s.snap)
	}
	return s
}

// StaticOption is a functional option for NewStaticRulesProvider — keeps the
// constructor backward-compatible while letting tests inject other kinds.
type StaticOption func(*rulesSnapshot)

// WithMetricRawRules attaches metric_raw kind rules to the static provider.
func WithMetricRawRules(rs []MetricRawRule) StaticOption {
	return func(s *rulesSnapshot) { s.metricRaw = append([]MetricRawRule(nil), rs...) }
}

// WithMetricAnomalyRules attaches metric_anomaly rules to the static provider.
func WithMetricAnomalyRules(rs []MetricAnomalyRule) StaticOption {
	return func(s *rulesSnapshot) { s.metricAnomaly = append([]MetricAnomalyRule(nil), rs...) }
}

// WithMetricForecastRules attaches metric_forecast rules to the static provider.
func WithMetricForecastRules(rs []MetricForecastRule) StaticOption {
	return func(s *rulesSnapshot) { s.metricForecast = append([]MetricForecastRule(nil), rs...) }
}

// WithMetricBurnRateRules attaches metric_burn_rate rules to the static provider.
func WithMetricBurnRateRules(rs []MetricBurnRateRule) StaticOption {
	return func(s *rulesSnapshot) { s.metricBurnRate = append([]MetricBurnRateRule(nil), rs...) }
}

func (s *StaticRulesProvider) MetricRawRules() []MetricRawRule         { return s.snap.metricRaw }
func (s *StaticRulesProvider) MetricAnomalyRules() []MetricAnomalyRule { return s.snap.metricAnomaly }
func (s *StaticRulesProvider) MetricForecastRules() []MetricForecastRule {
	return s.snap.metricForecast
}
func (s *StaticRulesProvider) MetricBurnRateRules() []MetricBurnRateRule {
	return s.snap.metricBurnRate
}
func (s *StaticRulesProvider) LogMatchRules() []LogMatchRule   { return s.snap.logMatch }
func (s *StaticRulesProvider) LogVolumeRules() []LogVolumeRule { return s.snap.logVolume }
func (s *StaticRulesProvider) TraceLatencyRules() []TraceLatencyRule {
	return s.snap.traceLatency
}
func (s *StaticRulesProvider) TraceErrorRateRules() []TraceErrorRateRule {
	return s.snap.traceErrorRate
}

// WithLogMatchRules / WithLogVolumeRules / WithTraceLatencyRules /
// WithTraceErrorRateRules attach Phase-B kinds to the static provider —
// used by tests that want to exercise the Phase-B evaluators in
// isolation.
func WithLogMatchRules(rs []LogMatchRule) StaticOption {
	return func(s *rulesSnapshot) { s.logMatch = append([]LogMatchRule(nil), rs...) }
}
func WithLogVolumeRules(rs []LogVolumeRule) StaticOption {
	return func(s *rulesSnapshot) { s.logVolume = append([]LogVolumeRule(nil), rs...) }
}
func WithTraceLatencyRules(rs []TraceLatencyRule) StaticOption {
	return func(s *rulesSnapshot) { s.traceLatency = append([]TraceLatencyRule(nil), rs...) }
}
func WithTraceErrorRateRules(rs []TraceErrorRateRule) StaticOption {
	return func(s *rulesSnapshot) { s.traceErrorRate = append([]TraceErrorRateRule(nil), rs...) }
}

// CachedRulesProvider periodically refreshes the rule snapshot from a
// RuleSource. Snapshots are immutable and swapped atomically so concurrent
// readers (the evaluators) never see torn state.
type CachedRulesProvider struct {
	src      RuleSource
	interval time.Duration
	log      *slog.Logger

	snap   atomic.Pointer[rulesSnapshot]
	mu     sync.Mutex
	loaded bool
}

// NewCachedRulesProvider builds a cache. Refresh interval defaults to 30s.
func NewCachedRulesProvider(src RuleSource, interval time.Duration, log *slog.Logger) *CachedRulesProvider {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &CachedRulesProvider{src: src, interval: interval, log: log}
}

func (c *CachedRulesProvider) load() *rulesSnapshot {
	if p := c.snap.Load(); p != nil {
		return p
	}
	return &rulesSnapshot{}
}

func (c *CachedRulesProvider) MetricRawRules() []MetricRawRule { return c.load().metricRaw }
func (c *CachedRulesProvider) MetricAnomalyRules() []MetricAnomalyRule {
	return c.load().metricAnomaly
}
func (c *CachedRulesProvider) MetricForecastRules() []MetricForecastRule {
	return c.load().metricForecast
}
func (c *CachedRulesProvider) MetricBurnRateRules() []MetricBurnRateRule {
	return c.load().metricBurnRate
}
func (c *CachedRulesProvider) LogMatchRules() []LogMatchRule   { return c.load().logMatch }
func (c *CachedRulesProvider) LogVolumeRules() []LogVolumeRule { return c.load().logVolume }
func (c *CachedRulesProvider) TraceLatencyRules() []TraceLatencyRule {
	return c.load().traceLatency
}
func (c *CachedRulesProvider) TraceErrorRateRules() []TraceErrorRateRule {
	return c.load().traceErrorRate
}

// Refresh loads the latest rule set into the cache. Errors compile-failed
// rows individually (logs + skips) so a malformed row never disables the
// whole alerting subsystem.
func (c *CachedRulesProvider) Refresh(ctx context.Context) error {
	rows, err := c.src.ListAllEnabledRules(ctx)
	if err != nil {
		return fmt.Errorf("list enabled rules: %w", err)
	}
	snap := rulesSnapshot{}
	for _, row := range rows {
		switch model.NormalizeKind(row.Kind) {
		case model.RuleKindMetricRaw:
			pr, err := compileMetricRawRule(row)
			if err != nil {
				c.log.Warn("alert: metric_raw compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.metricRaw = append(snap.metricRaw, pr)
		case model.RuleKindMetricAnomaly:
			ar, err := compileMetricAnomalyRule(row)
			if err != nil {
				c.log.Warn("alert: metric_anomaly compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.metricAnomaly = append(snap.metricAnomaly, ar)
		case model.RuleKindMetricForecast:
			fr, err := compileMetricForecastRule(row)
			if err != nil {
				c.log.Warn("alert: metric_forecast compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.metricForecast = append(snap.metricForecast, fr)
		case model.RuleKindMetricBurnRate:
			br, err := compileMetricBurnRateRule(row)
			if err != nil {
				c.log.Warn("alert: metric_burn_rate compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.metricBurnRate = append(snap.metricBurnRate, br)
		case model.RuleKindLogMatch:
			lm, err := compileLogMatchRule(row)
			if err != nil {
				c.log.Warn("alert: log_match compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.logMatch = append(snap.logMatch, lm)
		case model.RuleKindLogVolume:
			lv, err := compileLogVolumeRule(row)
			if err != nil {
				c.log.Warn("alert: log_volume compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.logVolume = append(snap.logVolume, lv)
		case model.RuleKindTraceLatency:
			tl, err := compileTraceLatencyRule(row)
			if err != nil {
				c.log.Warn("alert: trace_latency compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.traceLatency = append(snap.traceLatency, tl)
		case model.RuleKindTraceErrorRate:
			te, err := compileTraceErrorRateRule(row)
			if err != nil {
				c.log.Warn("alert: trace_error_rate compile failed",
					slog.Uint64("rule_id", row.ID),
					slog.String("rule_key", row.RuleKey),
					slog.Any("err", err))
				continue
			}
			snap.traceErrorRate = append(snap.traceErrorRate, te)
		default:
			c.log.Warn("alert: unknown rule kind — skipped",
				slog.Uint64("rule_id", row.ID),
				slog.String("rule_key", row.RuleKey),
				slog.String("kind", row.Kind))
		}
	}
	c.snap.Store(&snap)
	c.mu.Lock()
	c.loaded = true
	c.mu.Unlock()
	return nil
}

// Loop runs Refresh on a ticker until ctx is cancelled. The first refresh
// runs synchronously inside Loop so the snapshot is non-empty before
// metric ingestion arrives at the evaluator.
func (c *CachedRulesProvider) Loop(ctx context.Context) error {
	if err := c.Refresh(ctx); err != nil {
		c.log.Warn("alert: initial rules refresh failed", slog.Any("err", err))
	}
	tick := time.NewTicker(c.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := c.Refresh(ctx); err != nil {
				c.log.Warn("alert: rules refresh failed", slog.Any("err", err))
			}
		}
	}
}

// ----------------------------------------------------------------------------
// per-kind compile
// ----------------------------------------------------------------------------

type metricRawSpec struct {
	Expr string `json:"expr"`
}

// compileMetricRawRule turns a stored metric_raw rule row into the
// evaluator's runtime view. The contract is intentionally minimal: the
// expression itself IS the predicate. We don't validate operator /
// threshold / for_seconds because they no longer exist in the spec —
// PromQL's parser will reject malformed exprs at query time, and we
// want users to be able to write any valid predicate (`up == 0`,
// `cpu_pct > 90`, `abs(x) > 1 and y < 2`, ...).
func compileMetricRawRule(r *model.Rule) (MetricRawRule, error) {
	if r.RuleKey == "" {
		return MetricRawRule{}, fmt.Errorf("rule_key empty")
	}
	var spec metricRawSpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return MetricRawRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.Expr) == "" {
		return MetricRawRule{}, fmt.Errorf("expr required")
	}
	out := MetricRawRule{
		ID:        r.ID,
		RuleKey:   r.RuleKey,
		Name:      r.Name,
		Severity:  r.Severity,
		ScopeType: effectiveScope(r.ScopeType, r.Kind),
		Expr:      spec.Expr,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// new compile funcs
// ----------------------------------------------------------------------------

type metricAnomalySpec struct {
	Metric         string  `json:"metric"`
	Selector       string  `json:"selector,omitempty"`
	Method         string  `json:"method"`          // "zscore" (default) | "mad"
	BaselineWindow string  `json:"baseline_window"` // e.g. "1h"
	BaselineStep   string  `json:"baseline_step"`   // e.g. "5m"; default 5m
	Deviation      float64 `json:"deviation"`       // e.g. 3
	ForSeconds     int     `json:"for_seconds"`
}

func compileMetricAnomalyRule(r *model.Rule) (MetricAnomalyRule, error) {
	if r.RuleKey == "" {
		return MetricAnomalyRule{}, fmt.Errorf("rule_key empty")
	}
	var spec metricAnomalySpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return MetricAnomalyRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.Metric) == "" {
		return MetricAnomalyRule{}, fmt.Errorf("metric required")
	}
	method := spec.Method
	if method == "" {
		method = "zscore"
	}
	if method != "zscore" && method != "mad" {
		return MetricAnomalyRule{}, fmt.Errorf("method %q unsupported (zscore | mad)", method)
	}
	if spec.BaselineWindow == "" {
		spec.BaselineWindow = "1h"
	}
	if spec.BaselineStep == "" {
		spec.BaselineStep = "5m"
	}
	if spec.Deviation <= 0 {
		spec.Deviation = 3
	}
	out := MetricAnomalyRule{
		ID:             r.ID,
		RuleKey:        r.RuleKey,
		Name:           r.Name,
		Severity:       r.Severity,
		ScopeType:      effectiveScope(r.ScopeType, r.Kind),
		Metric:         spec.Metric,
		Selector:       spec.Selector,
		Method:         method,
		BaselineWindow: spec.BaselineWindow,
		BaselineStep:   spec.BaselineStep,
		Deviation:      spec.Deviation,
		ForSeconds:     spec.ForSeconds,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

type metricForecastSpec struct {
	Metric         string  `json:"metric"`
	Selector       string  `json:"selector,omitempty"`
	FitWindow      string  `json:"fit_window"`
	PredictSeconds int     `json:"predict_seconds"`
	Operator       string  `json:"operator"`
	Threshold      float64 `json:"threshold"`
	ForSeconds     int     `json:"for_seconds"`
}

func compileMetricForecastRule(r *model.Rule) (MetricForecastRule, error) {
	if r.RuleKey == "" {
		return MetricForecastRule{}, fmt.Errorf("rule_key empty")
	}
	var spec metricForecastSpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return MetricForecastRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.Metric) == "" {
		return MetricForecastRule{}, fmt.Errorf("metric required")
	}
	if spec.FitWindow == "" {
		spec.FitWindow = "1h"
	}
	if spec.PredictSeconds <= 0 {
		return MetricForecastRule{}, fmt.Errorf("predict_seconds must be > 0")
	}
	if !validHostOperator(spec.Operator) {
		return MetricForecastRule{}, fmt.Errorf("operator %q unsupported", spec.Operator)
	}
	out := MetricForecastRule{
		ID:             r.ID,
		RuleKey:        r.RuleKey,
		Name:           r.Name,
		Severity:       r.Severity,
		ScopeType:      effectiveScope(r.ScopeType, r.Kind),
		Metric:         spec.Metric,
		Selector:       spec.Selector,
		FitWindow:      spec.FitWindow,
		PredictSeconds: spec.PredictSeconds,
		Operator:       spec.Operator,
		Threshold:      spec.Threshold,
		ForSeconds:     spec.ForSeconds,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

type metricBurnRateSpec struct {
	SLI   string             `json:"sli"`
	SLO   float64            `json:"slo"`
	Burns []burnRateWindowJS `json:"burns"`
}

type burnRateWindowJS struct {
	Window     string  `json:"window"`
	Multiplier float64 `json:"multiplier"`
}

func compileMetricBurnRateRule(r *model.Rule) (MetricBurnRateRule, error) {
	if r.RuleKey == "" {
		return MetricBurnRateRule{}, fmt.Errorf("rule_key empty")
	}
	var spec metricBurnRateSpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return MetricBurnRateRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	spec.SLI = normalizeBurnRateSLIExpression(spec.SLI)
	if strings.TrimSpace(spec.SLI) == "" {
		return MetricBurnRateRule{}, fmt.Errorf("sli required")
	}
	if !burnRateSLIUsesWindow(spec.SLI) {
		return MetricBurnRateRule{}, fmt.Errorf("sli must use $window or a PromQL range selector")
	}
	spec.SLO = normalizeBurnRateSLOPercent(spec.SLO)
	if spec.SLO <= 0 || spec.SLO >= 100 {
		return MetricBurnRateRule{}, fmt.Errorf("slo must be in (0, 100)")
	}
	if len(spec.Burns) == 0 {
		return MetricBurnRateRule{}, fmt.Errorf("at least one burn window required")
	}
	burns := make([]BurnRateWindow, 0, len(spec.Burns))
	for i, b := range spec.Burns {
		if b.Window == "" {
			return MetricBurnRateRule{}, fmt.Errorf("burns[%d].window required", i)
		}
		if b.Multiplier <= 0 {
			return MetricBurnRateRule{}, fmt.Errorf("burns[%d].multiplier must be > 0", i)
		}
		burns = append(burns, BurnRateWindow{Window: b.Window, Multiplier: b.Multiplier})
	}
	out := MetricBurnRateRule{
		ID:        r.ID,
		RuleKey:   r.RuleKey,
		Name:      r.Name,
		Severity:  r.Severity,
		ScopeType: effectiveScope(r.ScopeType, r.Kind),
		SLI:       spec.SLI,
		SLO:       spec.SLO,
		Burns:     burns,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

func validHostOperator(op string) bool {
	switch op {
	case ">", ">=", "<", "<=", "==", "!=":
		return true
	}
	return false
}

// ----------------------------------------------------------------------------
// compile funcs
// ----------------------------------------------------------------------------

type logMatchSpec struct {
	StreamSelector string  `json:"stream_selector"`
	LineFilter     string  `json:"line_filter"`
	Window         string  `json:"window"`
	Operator       string  `json:"operator"`
	Threshold      float64 `json:"threshold"`
}

type logVolumeSpec struct {
	StreamSelector string  `json:"stream_selector"`
	LineFilter     string  `json:"line_filter"`
	Window         string  `json:"window"`
	RatioOp        string  `json:"ratio_op"`
	RatioThreshold float64 `json:"ratio_threshold"`
}

type traceLatencySpec struct {
	Service     string  `json:"service"`
	Operation   string  `json:"operation"`
	Quantile    string  `json:"quantile"`
	Window      string  `json:"window"`
	ThresholdMs float64 `json:"threshold_ms"`
}

type traceErrorRateSpec struct {
	Service      string  `json:"service"`
	Window       string  `json:"window"`
	Operator     string  `json:"operator"`
	ThresholdPct float64 `json:"threshold_pct"`
}

func compileLogMatchRule(r *model.Rule) (LogMatchRule, error) {
	if r.RuleKey == "" {
		return LogMatchRule{}, fmt.Errorf("rule_key empty")
	}
	var spec logMatchSpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return LogMatchRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.StreamSelector) == "" {
		return LogMatchRule{}, fmt.Errorf("stream_selector required")
	}
	if spec.Window == "" {
		spec.Window = "5m"
	}
	if spec.Operator == "" {
		spec.Operator = ">="
	}
	if !validHostOperator(spec.Operator) {
		return LogMatchRule{}, fmt.Errorf("operator %q invalid", spec.Operator)
	}
	out := LogMatchRule{
		ID:             r.ID,
		RuleKey:        r.RuleKey,
		Name:           r.Name,
		Severity:       r.Severity,
		ScopeType:      effectiveScope(r.ScopeType, r.Kind),
		StreamSelector: spec.StreamSelector,
		LineFilter:     spec.LineFilter,
		Window:         spec.Window,
		Operator:       spec.Operator,
		Threshold:      spec.Threshold,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

func compileLogVolumeRule(r *model.Rule) (LogVolumeRule, error) {
	if r.RuleKey == "" {
		return LogVolumeRule{}, fmt.Errorf("rule_key empty")
	}
	var spec logVolumeSpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return LogVolumeRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.StreamSelector) == "" {
		return LogVolumeRule{}, fmt.Errorf("stream_selector required")
	}
	if spec.Window == "" {
		spec.Window = "5m"
	}
	op := spec.RatioOp
	if op == "" {
		op = ">="
	}
	if !validHostOperator(op) {
		return LogVolumeRule{}, fmt.Errorf("ratio_op %q invalid", op)
	}
	out := LogVolumeRule{
		ID:             r.ID,
		RuleKey:        r.RuleKey,
		Name:           r.Name,
		Severity:       r.Severity,
		ScopeType:      effectiveScope(r.ScopeType, r.Kind),
		StreamSelector: spec.StreamSelector,
		LineFilter:     spec.LineFilter,
		Window:         spec.Window,
		Operator:       op,
		Threshold:      spec.RatioThreshold,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

// quantileFloat maps the user-friendly quantile string to its float
// representation for histogram_quantile().
func quantileFloat(q string) float64 {
	switch q {
	case "p50", "0.5":
		return 0.5
	case "p99", "0.99":
		return 0.99
	default:
		// Default + "p95" both fall here.
		return 0.95
	}
}

func compileTraceLatencyRule(r *model.Rule) (TraceLatencyRule, error) {
	if r.RuleKey == "" {
		return TraceLatencyRule{}, fmt.Errorf("rule_key empty")
	}
	var spec traceLatencySpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return TraceLatencyRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.Service) == "" {
		return TraceLatencyRule{}, fmt.Errorf("service required")
	}
	if spec.Window == "" {
		spec.Window = "5m"
	}
	if spec.ThresholdMs <= 0 {
		return TraceLatencyRule{}, fmt.Errorf("threshold_ms must be > 0")
	}
	q := quantileFloat(spec.Quantile)
	selector := fmt.Sprintf("service_name=%q", spec.Service)
	if spec.Operation != "" {
		selector = fmt.Sprintf("service_name=%q,span_name=%q", spec.Service, spec.Operation)
	}
	expr := fmt.Sprintf(
		"histogram_quantile(%g, sum by (le) (rate(traces_spanmetrics_latency_bucket{%s}[%s]))) * 1000 > %g",
		q, selector, spec.Window, spec.ThresholdMs)
	out := TraceLatencyRule{
		ID:        r.ID,
		RuleKey:   r.RuleKey,
		Name:      r.Name,
		Severity:  r.Severity,
		ScopeType: effectiveScope(r.ScopeType, r.Kind),
		Expr:      expr,
		Spec:      spec,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}

func compileTraceErrorRateRule(r *model.Rule) (TraceErrorRateRule, error) {
	if r.RuleKey == "" {
		return TraceErrorRateRule{}, fmt.Errorf("rule_key empty")
	}
	var spec traceErrorRateSpec
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
		return TraceErrorRateRule{}, fmt.Errorf("decode conditions: %w", err)
	}
	if strings.TrimSpace(spec.Service) == "" {
		return TraceErrorRateRule{}, fmt.Errorf("service required")
	}
	if spec.Window == "" {
		spec.Window = "5m"
	}
	op := spec.Operator
	if op == "" {
		op = ">"
	}
	if !validHostOperator(op) {
		return TraceErrorRateRule{}, fmt.Errorf("operator %q invalid", op)
	}
	if spec.ThresholdPct <= 0 {
		return TraceErrorRateRule{}, fmt.Errorf("threshold_pct must be > 0")
	}
	// error_rate = 100 * sum(rate(spanmetrics_calls_total{status_code="ERROR"}[w])) / sum(rate(spanmetrics_calls_total[w]))
	selector := fmt.Sprintf("service_name=%q", spec.Service)
	expr := fmt.Sprintf(
		"100 * (sum by (service_name) (rate(traces_spanmetrics_calls_total{%s,status_code=\"STATUS_CODE_ERROR\"}[%s])) / sum by (service_name) (rate(traces_spanmetrics_calls_total{%s}[%s]))) %s %g",
		selector, spec.Window, selector, spec.Window, op, spec.ThresholdPct)
	out := TraceErrorRateRule{
		ID:        r.ID,
		RuleKey:   r.RuleKey,
		Name:      r.Name,
		Severity:  r.Severity,
		ScopeType: effectiveScope(r.ScopeType, r.Kind),
		Expr:      expr,
		Spec:      spec,
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(*r.LabelsJSON), &out.Labels)
	}
	return out, nil
}
