package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// observeEval is the per-rule latency timer used by every Phase-A
// evaluator (and pipeline.go). Returned closure should be deferred at the
// top of each per-rule iteration; capturing evalErr by reference lets the
// loop body record the data-source error (PromQL / DB) so result=error
// on the histogram lines up with the WARN log.
func observeEval(kind string, evalErr *error) func() {
	start := time.Now()
	return func() {
		var e error
		if evalErr != nil {
			e = *evalErr
		}
		prom.ObserveAlertEvaluator(kind, time.Since(start).Seconds(), e)
	}
}

// metricExprFor maps the closed-set canonical metric name (cpu_pct,
// mem_pct, …) to the PromQL expression that yields its current value
// across edges. This is the same vocabulary the host evaluator uses for
// metric_threshold rules. Returns ("", false) when the name is not in
// the closed set — callers should reject the rule at compile time, but
// the evaluator double-checks at run time too.
func metricExprFor(metric string) (string, bool) {
	switch metric {
	case "cpu_pct":
		return `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))`, true
	case "mem_pct":
		return `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`, true
	case "disk_used_pct":
		return `100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})`, true
	case "disk_avail_bytes":
		return `node_filesystem_avail_bytes{mountpoint="/"}`, true
	case "load1":
		return `node_load1`, true
	case "load5":
		return `node_load5`, true
	case "load15":
		return `node_load15`, true
	case "net_rx_bps":
		return `sum by (device_id) (rate(node_network_receive_bytes_total[1m]))`, true
	case "net_tx_bps":
		return `sum by (device_id) (rate(node_network_transmit_bytes_total[1m]))`, true
	}
	return "", false
}

var metricExprNodeSelectorRE = regexp.MustCompile(`\b(node_[a-zA-Z0-9_:]+)(\{[^}]*\})?`)

func applyClosedSetMetricSelector(expr, selector string) string {
	selector = normalizePromSelectorFragment(selector)
	if selector == "" {
		return expr
	}
	return metricExprNodeSelectorRE.ReplaceAllStringFunc(expr, func(match string) string {
		parts := metricExprNodeSelectorRE.FindStringSubmatch(match)
		if len(parts) < 3 || parts[1] == "" {
			return match
		}
		return parts[1] + mergePromSelectorFragments(parts[2], selector)
	})
}

func normalizePromSelectorFragment(selector string) string {
	selector = strings.TrimSpace(selector)
	selector = strings.TrimPrefix(selector, "{")
	selector = strings.TrimSuffix(selector, "}")
	return strings.TrimSpace(selector)
}

func mergePromSelectorFragments(existing, add string) string {
	add = normalizePromSelectorFragment(add)
	if add == "" {
		if strings.TrimSpace(existing) == "" {
			return ""
		}
		return "{" + normalizePromSelectorFragment(existing) + "}"
	}
	existingParts := splitPromSelectorFragment(normalizePromSelectorFragment(existing))
	addParts := splitPromSelectorFragment(add)
	addKeys := make(map[string]struct{}, len(addParts))
	for _, part := range addParts {
		if key := promMatcherKey(part); key != "" {
			addKeys[key] = struct{}{}
		}
	}
	merged := make([]string, 0, len(existingParts)+len(addParts))
	for _, part := range existingParts {
		if key := promMatcherKey(part); key != "" {
			if _, replaced := addKeys[key]; replaced {
				continue
			}
		}
		merged = append(merged, part)
	}
	merged = append(merged, addParts...)
	if len(merged) == 0 {
		return ""
	}
	return "{" + strings.Join(merged, ",") + "}"
}

func splitPromSelectorFragment(selector string) []string {
	selector = normalizePromSelectorFragment(selector)
	if selector == "" {
		return nil
	}
	parts := strings.Split(selector, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func promMatcherKey(matcher string) string {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" {
		return ""
	}
	for _, op := range []string{"=~", "!~", "!=", "="} {
		if idx := strings.Index(matcher, op); idx > 0 {
			return strings.TrimSpace(matcher[:idx])
		}
	}
	return ""
}

// evaluateMetricAnomaly turns each metric_anomaly rule into a PromQL
// query of the shape
//
//	abs(<expr> - avg_over_time((<expr>)[<bw>:<step>])) > <dev> * stddev_over_time((<expr>)[<bw>:<step>])
//
// (or median + MAD when method=mad), runs it, and fires for every
// returned vector entry. Each entry's label set forms the dedupe key
// suffix so anomalies on different edges become independent incidents.
func (e *PipelineEvaluator) evaluateMetricAnomaly(ctx context.Context, now time.Time) {
	rules := e.rules.MetricAnomalyRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindMetricAnomaly, &evalErr)
		base, ok := metricExprFor(rule.Metric)
		if !ok {
			e.log.Warn("alert: metric_anomaly metric outside closed set",
				slog.String("rule", rule.RuleKey),
				slog.String("metric", rule.Metric))
			done()
			continue
		}
		base = applyClosedSetMetricSelector(base, rule.Selector)
		var expr string
		switch rule.Method {
		case "mad":
			// MAD requires the user-defined functions Prom doesn't ship;
			// we approximate with quantile_over_time(0.5) as the median
			// and avg_over_time(abs(x - median)) as the dispersion.
			med := fmt.Sprintf("quantile_over_time(0.5, (%s)[%s:%s])", base, rule.BaselineWindow, rule.BaselineStep)
			disp := fmt.Sprintf("avg_over_time((abs((%s) - (%s)))[%s:%s])", base, med, rule.BaselineWindow, rule.BaselineStep)
			expr = fmt.Sprintf("abs((%s) - (%s)) > %g * (%s)", base, med, rule.Deviation, disp)
		default: // zscore
			mean := fmt.Sprintf("avg_over_time((%s)[%s:%s])", base, rule.BaselineWindow, rule.BaselineStep)
			std := fmt.Sprintf("stddev_over_time((%s)[%s:%s])", base, rule.BaselineWindow, rule.BaselineStep)
			expr = fmt.Sprintf("abs((%s) - (%s)) > %g * (%s)", base, mean, rule.Deviation, std)
		}
		evalErr = e.runVectorRule(ctx, vectorRule{
			ruleKey:   rule.RuleKey,
			ruleName:  rule.Name,
			severity:  rule.Severity,
			scopeType: effectiveScope(rule.ScopeType, model.RuleKindMetricAnomaly),
			runbook:   rule.RunbookURL,
			labels:    rule.Labels,
			expr:      expr,
			fmtSummary: func(labels map[string]string, value float64) string {
				return fmt.Sprintf("%s: %s 偏离基线 ≥ %gσ (labels=%s)", rule.RuleKey, rule.Metric, rule.Deviation, labelSetKey(labels))
			},
			resolveReason: "anomaly cleared",
		}, now)
		done()
	}
}

// evaluateMetricForecast renders predict_linear((expr)[fit:step], predict_seconds)
// then compares against the user threshold.
func (e *PipelineEvaluator) evaluateMetricForecast(ctx context.Context, now time.Time) {
	rules := e.rules.MetricForecastRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindMetricForecast, &evalErr)
		base, ok := metricExprFor(rule.Metric)
		if !ok {
			e.log.Warn("alert: metric_forecast metric outside closed set",
				slog.String("rule", rule.RuleKey),
				slog.String("metric", rule.Metric))
			done()
			continue
		}
		base = applyClosedSetMetricSelector(base, rule.Selector)
		// predict_linear over a sub-query: use 5m step regardless — the
		// fit window still controls how much history influences the
		// slope, the step only affects sampling resolution.
		expr := fmt.Sprintf("predict_linear((%s)[%s:5m], %d) %s %g",
			base, rule.FitWindow, rule.PredictSeconds, rule.Operator, rule.Threshold)
		evalErr = e.runVectorRule(ctx, vectorRule{
			ruleKey:   rule.RuleKey,
			ruleName:  rule.Name,
			severity:  rule.Severity,
			scopeType: effectiveScope(rule.ScopeType, model.RuleKindMetricForecast),
			runbook:   rule.RunbookURL,
			labels:    rule.Labels,
			expr:      expr,
			fmtSummary: func(labels map[string]string, value float64) string {
				return fmt.Sprintf("%s: %s 预计 %ds 后 %s %g (labels=%s)",
					rule.RuleKey, rule.Metric, rule.PredictSeconds, rule.Operator, rule.Threshold, labelSetKey(labels))
			},
			resolveReason: "forecast cleared",
		}, now)
		done()
	}
}

// evaluateMetricBurnRate runs the SRE Workbook multi-window multi-burn-
// rate alarm: for every (window, multiplier) tuple the evaluator checks
//
//	(1 - SLI[window]) >= multiplier * (1 - SLO/100)
//
// All windows must trigger together; that AND filters out brief blips
// and is the whole point of the multi-burn pattern. The dedupe key is
// per-rule (no labels appended) since burn-rate rules are normally one
// per SLO.
func (e *PipelineEvaluator) evaluateMetricBurnRate(ctx context.Context, now time.Time) {
	rules := e.rules.MetricBurnRateRules()
	if len(rules) == 0 {
		return
	}
	budget := func(slo float64) float64 { return 1 - slo/100 }
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindMetricBurnRate, &evalErr)
		fired := true
		var firstNonFiringReason string
		var maxBurn float64
		for _, b := range rule.Burns {
			expr := fmt.Sprintf("(1 - (%s)) >= %g", windowedSLI(rule.SLI, b.Window), b.Multiplier*budget(rule.SLO))
			res, err := e.prom.Query(ctx, expr, now)
			if err != nil {
				e.log.Warn("alert: burn_rate window query failed",
					slog.String("rule", rule.RuleKey),
					slog.String("window", b.Window),
					slog.Any("err", err))
				fired = false
				firstNonFiringReason = "burn_rate query failed"
				evalErr = err
				break
			}
			if res == nil || res.ResultType != "vector" {
				fired = false
				firstNonFiringReason = "burn_rate non-vector"
				break
			}
			var entries []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal(res.Result, &entries); err != nil || len(entries) == 0 {
				fired = false
				firstNonFiringReason = "burn_rate empty vector"
				break
			}
			// Track the max burn across windows for the firing summary.
			if v, ok := promFirstNumeric(entries[0].Value); ok && v > maxBurn {
				maxBurn = v
			}
		}
		scope := effectiveScope(rule.ScopeType, model.RuleKindMetricBurnRate)
		dedupeKey := fmt.Sprintf("pipeline:%s", rule.RuleKey)
		if fired {
			summary := fmt.Sprintf("%s: SLO %.2f%% burn rate triggered across %d windows", rule.RuleKey, rule.SLO, len(rule.Burns))
			thr := budget(rule.SLO)
			val := maxBurn
			res2, err := e.uc.RecordFiring(ctx, FiringInput{
				ScopeType:  scope,
				Scope:      scope,
				Rule:       rule.RuleKey,
				RuleName:   rule.Name,
				Severity:   ruleSev(rule.Severity, notify.SeverityCritical),
				DedupeKey:  dedupeKey,
				OccurredAt: now,
				Title:      summary,
				Summary:    summary,
				Value:      &val,
				Threshold:  &thr,
				RunbookURL: rule.RunbookURL,
				Labels:     mergeLabels(rule.Labels, map[string]string{"rule": rule.RuleKey, "trigger": "burn_rate"}),
			})
			if err != nil {
				e.log.Warn("alert: record firing burn_rate failed",
					slog.String("rule", rule.RuleKey),
					slog.Any("err", err))
				done()
				continue
			}
			e.notify(ctx, res2, summary, scope, now)
			done()
			continue
		}
		if _, err := e.uc.SystemResolveIncident(ctx, dedupeKey, "burn_rate cleared: "+firstNonFiringReason, now); err != nil {
			e.log.Warn("alert: resolve burn_rate failed",
				slog.String("rule", rule.RuleKey),
				slog.Any("err", err))
		}
		done()
	}
}

// vectorRule bundles the per-rule plumbing shared by metric_anomaly,
// metric_forecast, and (eventually) other vector-style evaluators.
type vectorRule struct {
	ruleKey       string
	ruleName      string
	severity      string
	scopeType     string // host / global / monitoring_pipeline
	runbook       string
	labels        map[string]string
	expr          string
	fmtSummary    func(labels map[string]string, value float64) string
	resolveReason string
}

// runVectorRule mirrors evaluatePromQuery's loop body but takes a pre-
// rendered PromQL expression and a per-rule summary builder. Used by the
// metric_anomaly and metric_forecast evaluators. Returns the
// data-source error (PromQL query / decode) so callers can label the
// alert_evaluator_latency histogram observation.
func (e *PipelineEvaluator) runVectorRule(ctx context.Context, vr vectorRule, now time.Time) error {
	res, err := e.prom.Query(ctx, vr.expr, now)
	if err != nil {
		e.log.Warn("alert: vector query failed",
			slog.String("rule", vr.ruleKey),
			slog.String("expr", vr.expr),
			slog.Any("err", err))
		return err
	}
	if res == nil || res.ResultType != "vector" {
		return nil
	}
	type vectorEntry struct {
		Metric map[string]string `json:"metric"`
		Value  []json.RawMessage `json:"value"`
	}
	var entries []vectorEntry
	if err := json.Unmarshal(res.Result, &entries); err != nil {
		e.log.Warn("alert: decode vector failed",
			slog.String("rule", vr.ruleKey),
			slog.Any("err", err))
		return err
	}
	seen := map[string]bool{}
	for _, ent := range entries {
		v, _ := promFirstNumeric(ent.Value)
		dedupeKey := fmt.Sprintf("pipeline:%s:%s", vr.ruleKey, labelSetKey(ent.Metric))
		seen[dedupeKey] = true
		summary := vr.fmtSummary(ent.Metric, v)
		val := v
		var devID *uint64
		if vr.scopeType == model.RuleScopeHost {
			if raw := strings.TrimSpace(ent.Metric["device_id"]); raw != "" {
				if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
					devID = &id
				}
			}
		}
		res2, err := e.uc.RecordFiring(ctx, FiringInput{
			ScopeType:  vr.scopeType,
			Scope:      vr.scopeType,
			Rule:       vr.ruleKey,
			RuleName:   vr.ruleName,
			Severity:   ruleSev(vr.severity, notify.SeverityWarning),
			DeviceID:   devID,
			DedupeKey:  dedupeKey,
			OccurredAt: now,
			Title:      summary,
			Summary:    summary,
			Value:      &val,
			RunbookURL: vr.runbook,
			Labels:     mergeLabels(vr.labels, ent.Metric, map[string]string{"rule": vr.ruleKey, "trigger": "ticker"}),
		})
		if err != nil {
			e.log.Warn("alert: record firing vector rule failed",
				slog.String("rule", vr.ruleKey),
				slog.Any("err", err))
			continue
		}
		e.notify(ctx, res2, summary, vr.scopeType, now)
	}
	// NOTE: For PR-A we do not auto-resolve vector-rule incidents the
	// way evaluatePromQuery does — the anomaly/forecast queries don't
	// emit a per-series "value cleared" signal cleanly when the value
	// drops below threshold. Operators acknowledge/resolve manually.
	// PR-A2 will add a dedicated "did fire last tick?" sweep.
	_ = seen
	return nil
}

// promFirstNumeric extracts the numeric value from a Prom vector entry.
// The value field is shaped [<unix_ts>, "<float string>"].
func promFirstNumeric(value []json.RawMessage) (float64, bool) {
	if len(value) < 2 {
		return 0, false
	}
	var s string
	if err := json.Unmarshal(value[1], &s); err != nil {
		return 0, false
	}
	return parseFloat(s)
}

// windowedSLI substitutes the user's SLI expression with a windowed
// equivalent. The convention is that the SLI is written as a function
// of $window, e.g. `sum(rate(http_requests_total{code!~"5.."}[$window]))
// / sum(rate(http_requests_total[$window]))`. If $window is absent we
// append `[<window>]` as a fallback (works for raw counters but will
// confuse users — document the $window convention in the rule editor).
func windowedSLI(sli, window string) string {
	if strings.Contains(sli, "$window") {
		return strings.ReplaceAll(sli, "$window", window)
	}
	return sli + "[" + window + "]"
}
