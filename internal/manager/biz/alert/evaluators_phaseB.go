// evaluators_phaseB.go contains the Phase-B evaluators —
// log_match / log_volume against Loki, trace_latency / trace_error_rate
// against Prom (spanmetrics).
//
// All four follow the metric_raw recovery pattern: track which dedupe
// keys fired this tick per rule, and resolve any incident from the
// previous tick whose key fell out of the result. firingSnapshot[key]
// is the per-rule "last tick" set; the same map is shared with
// evaluatePromQuery (rule_keys are unique across kinds, so collisions
// can't happen).

package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

// evaluateLogMatch runs every enabled log_match rule's count_over_time
// query and fires for each label-set whose count satisfies operator+threshold.
// Logs that disappear from one tick to the next auto-resolve.
func (e *PipelineEvaluator) evaluateLogMatch(ctx context.Context, now time.Time) {
	rules := e.rules.LogMatchRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindLogMatch, &evalErr)
		expr := buildLogMatchQuery(rule.StreamSelector, rule.LineFilter, rule.Window)
		entries, err := runLokiInstant(ctx, e.logq, expr, now)
		if err != nil {
			e.log.Warn("alert: loki query failed",
				slog.String("rule", rule.RuleKey),
				slog.String("expr", expr),
				slog.Any("err", err))
			evalErr = err
			done()
			continue
		}
		scope := effectiveScope(rule.ScopeType, model.RuleKindLogMatch)
		fired := make(map[string]struct{}, len(entries))
		for _, ent := range entries {
			if !compareFloat(ent.Value, rule.Operator, rule.Threshold) {
				continue
			}
			dedupeKey := fmt.Sprintf("pipeline:%s:%s", rule.RuleKey, labelSetKey(ent.Labels))
			fired[dedupeKey] = struct{}{}
			summary := fmt.Sprintf("%s: log_match %g %s %g (labels=%s)",
				rule.RuleKey, ent.Value, rule.Operator, rule.Threshold, labelSetKey(ent.Labels))
			val := ent.Value
			var devID *uint64
			if scope == model.RuleScopeHost {
				devID = deviceIDFromLabels(ent.Labels)
			}
			input := FiringInput{
				ScopeType:  scope,
				Scope:      scope,
				Rule:       rule.RuleKey,
				RuleName:   rule.Name,
				Severity:   ruleSev(rule.Severity, notify.SeverityWarning),
				DeviceID:   devID,
				DedupeKey:  dedupeKey,
				OccurredAt: now,
				Title:      summary,
				Summary:    summary,
				RunbookURL: rule.RunbookURL,
				Value:      &val,
				Labels:     mergeLabels(rule.Labels, ent.Labels, map[string]string{"rule": rule.RuleKey, "trigger": "ticker"}),
			}
			res, err := e.uc.RecordFiring(ctx, input)
			if err != nil {
				e.log.Warn("alert: record firing log_match failed",
					slog.String("rule", rule.RuleKey),
					slog.Any("err", err))
				continue
			}
			e.notify(ctx, res, summary, scope, now)
		}
		e.sweepRecovery(ctx, rule.RuleKey, fired, "log_match condition cleared", now)
		done()
		_ = evalErr
	}
}

// evaluateLogVolume — v1 implementation reuses the log_match shape
// (current-window count vs absolute threshold). The "ratio vs previous
// window" semantics in the original spec is left for a future pass; the
// current shape already covers "log volume crossed N" alerts which is
// the common ask. The per-kind rule type keeps the schema gate so we
// can tighten semantics later without a UI break.
func (e *PipelineEvaluator) evaluateLogVolume(ctx context.Context, now time.Time) {
	rules := e.rules.LogVolumeRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindLogVolume, &evalErr)
		expr := buildLogMatchQuery(rule.StreamSelector, rule.LineFilter, rule.Window)
		entries, err := runLokiInstant(ctx, e.logq, expr, now)
		if err != nil {
			e.log.Warn("alert: loki query failed",
				slog.String("rule", rule.RuleKey),
				slog.String("expr", expr),
				slog.Any("err", err))
			evalErr = err
			done()
			continue
		}
		scope := effectiveScope(rule.ScopeType, model.RuleKindLogVolume)
		fired := make(map[string]struct{}, len(entries))
		for _, ent := range entries {
			if !compareFloat(ent.Value, rule.Operator, rule.Threshold) {
				continue
			}
			dedupeKey := fmt.Sprintf("pipeline:%s:%s", rule.RuleKey, labelSetKey(ent.Labels))
			fired[dedupeKey] = struct{}{}
			summary := fmt.Sprintf("%s: log_volume %g %s %g (labels=%s)",
				rule.RuleKey, ent.Value, rule.Operator, rule.Threshold, labelSetKey(ent.Labels))
			val := ent.Value
			var devID *uint64
			if scope == model.RuleScopeHost {
				devID = deviceIDFromLabels(ent.Labels)
			}
			input := FiringInput{
				ScopeType:  scope,
				Scope:      scope,
				Rule:       rule.RuleKey,
				RuleName:   rule.Name,
				Severity:   ruleSev(rule.Severity, notify.SeverityWarning),
				DeviceID:   devID,
				DedupeKey:  dedupeKey,
				OccurredAt: now,
				Title:      summary,
				Summary:    summary,
				RunbookURL: rule.RunbookURL,
				Value:      &val,
				Labels:     mergeLabels(rule.Labels, ent.Labels, map[string]string{"rule": rule.RuleKey, "trigger": "ticker"}),
			}
			res, err := e.uc.RecordFiring(ctx, input)
			if err != nil {
				e.log.Warn("alert: record firing log_volume failed",
					slog.String("rule", rule.RuleKey),
					slog.Any("err", err))
				continue
			}
			e.notify(ctx, res, summary, scope, now)
		}
		e.sweepRecovery(ctx, rule.RuleKey, fired, "log_volume condition cleared", now)
		done()
		_ = evalErr
	}
}

// evaluateTraceLatency runs each trace_latency rule's pre-built
// histogram_quantile() Prom expression. The expression includes the
// `> threshold_ms` comparison so Prom returns only breaching series —
// same pattern as evaluatePromQuery.
func (e *PipelineEvaluator) evaluateTraceLatency(ctx context.Context, now time.Time) {
	rules := e.rules.TraceLatencyRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindTraceLatency, &evalErr)
		entries, err := runPromInstant(ctx, e.prom, rule.Expr, now)
		if err != nil {
			e.log.Warn("alert: trace_latency prom query failed",
				slog.String("rule", rule.RuleKey),
				slog.String("expr", rule.Expr),
				slog.Any("err", err))
			evalErr = err
			done()
			continue
		}
		scope := effectiveScope(rule.ScopeType, model.RuleKindTraceLatency)
		fired := make(map[string]struct{}, len(entries))
		for _, ent := range entries {
			dedupeKey := fmt.Sprintf("pipeline:%s:%s", rule.RuleKey, labelSetKey(ent.Labels))
			fired[dedupeKey] = struct{}{}
			summary := fmt.Sprintf("%s: %s %s 延迟 %.1fms > %gms",
				rule.RuleKey, rule.Spec.Service, rule.Spec.Quantile, ent.Value, rule.Spec.ThresholdMs)
			val := ent.Value
			input := FiringInput{
				ScopeType:  scope,
				Scope:      scope,
				Rule:       rule.RuleKey,
				RuleName:   rule.Name,
				Severity:   ruleSev(rule.Severity, notify.SeverityWarning),
				DedupeKey:  dedupeKey,
				OccurredAt: now,
				Title:      summary,
				Summary:    summary,
				RunbookURL: rule.RunbookURL,
				Value:      &val,
				Labels:     mergeLabels(rule.Labels, ent.Labels, map[string]string{"rule": rule.RuleKey, "trigger": "ticker"}),
			}
			res, err := e.uc.RecordFiring(ctx, input)
			if err != nil {
				e.log.Warn("alert: record firing trace_latency failed",
					slog.String("rule", rule.RuleKey),
					slog.Any("err", err))
				continue
			}
			e.notify(ctx, res, summary, scope, now)
		}
		e.sweepRecovery(ctx, rule.RuleKey, fired, "trace_latency condition cleared", now)
		done()
		_ = evalErr
	}
}

// evaluateTraceErrorRate is the symmetric trace_error_rate evaluator.
func (e *PipelineEvaluator) evaluateTraceErrorRate(ctx context.Context, now time.Time) {
	rules := e.rules.TraceErrorRateRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindTraceErrorRate, &evalErr)
		entries, err := runPromInstant(ctx, e.prom, rule.Expr, now)
		if err != nil {
			e.log.Warn("alert: trace_error_rate prom query failed",
				slog.String("rule", rule.RuleKey),
				slog.String("expr", rule.Expr),
				slog.Any("err", err))
			evalErr = err
			done()
			continue
		}
		scope := effectiveScope(rule.ScopeType, model.RuleKindTraceErrorRate)
		fired := make(map[string]struct{}, len(entries))
		for _, ent := range entries {
			dedupeKey := fmt.Sprintf("pipeline:%s:%s", rule.RuleKey, labelSetKey(ent.Labels))
			fired[dedupeKey] = struct{}{}
			summary := fmt.Sprintf("%s: %s 错误率 %.2f%% %s %g%%",
				rule.RuleKey, rule.Spec.Service, ent.Value, rule.Spec.Operator, rule.Spec.ThresholdPct)
			val := ent.Value
			input := FiringInput{
				ScopeType:  scope,
				Scope:      scope,
				Rule:       rule.RuleKey,
				RuleName:   rule.Name,
				Severity:   ruleSev(rule.Severity, notify.SeverityWarning),
				DedupeKey:  dedupeKey,
				OccurredAt: now,
				Title:      summary,
				Summary:    summary,
				RunbookURL: rule.RunbookURL,
				Value:      &val,
				Labels:     mergeLabels(rule.Labels, ent.Labels, map[string]string{"rule": rule.RuleKey, "trigger": "ticker"}),
			}
			res, err := e.uc.RecordFiring(ctx, input)
			if err != nil {
				e.log.Warn("alert: record firing trace_error_rate failed",
					slog.String("rule", rule.RuleKey),
					slog.Any("err", err))
				continue
			}
			e.notify(ctx, res, summary, scope, now)
		}
		e.sweepRecovery(ctx, rule.RuleKey, fired, "trace_error_rate condition cleared", now)
		done()
		_ = evalErr
	}
}

// sweepRecovery resolves any incident from the previous tick whose
// dedupe key fell out of this tick's fired set. Mirrors the recovery
// path in evaluatePromQuery so all kinds clear via the same mechanism.
func (e *PipelineEvaluator) sweepRecovery(ctx context.Context, ruleKey string, fired map[string]struct{}, reason string, now time.Time) {
	if e.firingSnapshot == nil {
		e.firingSnapshot = map[string]map[string]struct{}{}
	}
	prev := e.firingSnapshot[ruleKey]
	for prevKey := range prev {
		if _, stillFiring := fired[prevKey]; stillFiring {
			continue
		}
		if _, err := e.uc.SystemResolveIncident(ctx, prevKey, reason, now); err != nil {
			e.log.Warn("alert: system resolve failed",
				slog.String("rule", ruleKey),
				slog.String("dedupe", prevKey),
				slog.Any("err", err))
		}
	}
	e.firingSnapshot[ruleKey] = fired
}

// vectorEntry is the local interpretation of one promquery / loki
// matrix sample: a label set + a single numeric value (the latest
// sample of a matrix series, or the only sample of an instant vector).
type vectorEntry struct {
	Labels map[string]string
	Value  float64
}

// runPromInstant runs a Prom Query at `now` and decodes vector results
// into vectorEntry. Empty / non-vector results return nil with no error.
func runPromInstant(ctx context.Context, p PromQuerier, expr string, now time.Time) ([]vectorEntry, error) {
	if p == nil {
		return nil, nil
	}
	res, err := p.Query(ctx, expr, now)
	if err != nil {
		return nil, err
	}
	if res == nil || res.ResultType != "vector" {
		return nil, nil
	}
	type promEntry struct {
		Metric map[string]string `json:"metric"`
		Value  []json.RawMessage `json:"value"`
	}
	var raw []promEntry
	if err := json.Unmarshal(res.Result, &raw); err != nil {
		return nil, fmt.Errorf("decode prom vector: %w", err)
	}
	out := make([]vectorEntry, 0, len(raw))
	for _, ent := range raw {
		valStr := ""
		if len(ent.Value) >= 2 {
			_ = json.Unmarshal(ent.Value[1], &valStr)
		}
		v, _ := strconv.ParseFloat(valStr, 64)
		out = append(out, vectorEntry{Labels: ent.Metric, Value: v})
	}
	return out, nil
}

// runLokiInstant queries Loki via QueryRange over a tight 60s window
// ending at `now` with a 30s step, then takes the latest sample of
// each matrix series — the closest LogQL approximation to "evaluate
// count_over_time as of now".
func runLokiInstant(ctx context.Context, l LogQuerier, expr string, now time.Time) ([]vectorEntry, error) {
	if l == nil {
		return nil, nil
	}
	res, err := l.QueryRange(ctx, logquery.QueryRangeOptions{
		Query: expr,
		Start: now.Add(-60 * time.Second),
		End:   now,
		Step:  30 * time.Second,
		Limit: 1000,
	})
	if err != nil {
		return nil, err
	}
	if res == nil || res.ResultType != "matrix" {
		return nil, nil
	}
	type lokiEntry struct {
		Metric map[string]string   `json:"metric"`
		Values [][]json.RawMessage `json:"values"`
	}
	var raw []lokiEntry
	if err := json.Unmarshal(res.Result, &raw); err != nil {
		return nil, fmt.Errorf("decode loki matrix: %w", err)
	}
	out := make([]vectorEntry, 0, len(raw))
	for _, s := range raw {
		if len(s.Values) == 0 {
			continue
		}
		// Loki samples are [timestamp_ns_string, value_string]. Take
		// the last sample (most recent) as the "current value".
		last := s.Values[len(s.Values)-1]
		if len(last) < 2 {
			continue
		}
		var valStr string
		_ = json.Unmarshal(last[1], &valStr)
		v, _ := strconv.ParseFloat(valStr, 64)
		out = append(out, vectorEntry{Labels: s.Metric, Value: v})
	}
	return out, nil
}

func deviceIDFromLabels(labels map[string]string) *uint64 {
	raw := strings.TrimSpace(labels["device_id"])
	if raw == "" {
		return nil
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return nil
	}
	return &id
}

// buildLogMatchQuery composes the LogQL query for log_match / log_volume.
// Empty filter yields raw count_over_time on the whole stream selector;
// non-empty filter wraps with `|~ <regex>`.
func buildLogMatchQuery(stream, filter, window string) string {
	if window == "" {
		window = "5m"
	}
	if filter == "" {
		return fmt.Sprintf("count_over_time(%s [%s])", stream, window)
	}
	return fmt.Sprintf("count_over_time(%s |~ %q [%s])", stream, filter, window)
}
