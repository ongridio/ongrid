package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

// DeviceLister enumerates registered devices. *devicebiz.Usecase satisfies it.
// This is the canonical source for heartbeat staleness after the May 2026
// Edge ↔ Device split: Device.LastSeenAt is the denormalised presence view
// used by the UI and survives edge identity churn.
type DeviceLister interface {
	List(ctx context.Context, f devicebiz.ListFilter) ([]*devicemodel.Device, error)
}

// EdgeLister enumerates registered edges. *edgebiz.Usecase satisfies it.
// Kept as a fallback for tests/older wiring that has not supplied a
// DeviceLister yet.
type EdgeLister interface {
	List(ctx context.Context, f edgebiz.ListFilter) ([]*edgemodel.Edge, error)
}

// PromQuerier runs an instant PromQL query. *promquery.Client satisfies it.
type PromQuerier interface {
	Query(ctx context.Context, expr string, ts time.Time) (*promquery.InstantResult, error)
}

// LogQuerier runs a LogQL range query against Loki. *logquery.Client
// satisfies it via QueryRange. The Phase-B evaluator queries a tight
// `[now-5s, now]` range with a 5s step to get the latest count_over_time
// matrix entries — this is effectively an instant query expressed in
// LogQL's range API.
type LogQuerier interface {
	QueryRange(ctx context.Context, opts logquery.QueryRangeOptions) (*logquery.QueryRangeResult, error)
}

// PipelineEvaluatorOpts wires the evaluator. DeviceLister keeps the heartbeat
// gauges fresh; PromQuerier drives every metric_* rule kind. Both are optional;
// when nil the corresponding rule kinds are silently skipped each tick.
type PipelineEvaluatorOpts struct {
	Usecase         *Usecase
	Rules           RulesProvider
	Notifier        Notifier
	Resolver        ChannelResolver
	Inhibitor       Inhibitor
	DefaultChannels []string
	Cooldown        time.Duration
	Interval        time.Duration

	DeviceLister DeviceLister
	EdgeLister   EdgeLister
	PromQuerier  PromQuerier

	// LogQuerier is the Loki client used by Phase-B kinds log_match /
	// log_volume. nil means those kinds are silently skipped each tick
	// (the cache still loads the rows so the UI can list them).
	LogQuerier LogQuerier

	Log *slog.Logger
	Now func() time.Time
}

// PipelineEvaluator runs the pipeline-class rules on a tick.
// Driven by the rules table: every enabled rule (metric_raw,
// metric_anomaly, metric_forecast, metric_burn_rate — ) is
// evaluated each tick. Phase-3-final collapse: host-metric thresholds
// also flow through here as metric_raw rules; the legacy real-time
// HostMetricDecorator on push_host_metrics is gone, host alerts now
// run on the same 30s Prom tick as everything else.
type PipelineEvaluator struct {
	uc        *Usecase
	rules     RulesProvider
	notifier  Notifier
	resolver  ChannelResolver
	inhibitor Inhibitor
	channels  []string
	cooldown  time.Duration
	interval  time.Duration

	devices DeviceLister
	edges   EdgeLister
	prom    PromQuerier
	logq    LogQuerier

	// gaugeSnapshot is the previous tick's (device_id, device_name) set
	// used by refreshDeviceStalenessGauge to garbage-collect series for
	// devices that fell out of inventory. Guarded by gaugeMu — Loop is
	// single-goroutine but tests call EvaluateOnce concurrently.
	gaugeMu       sync.Mutex
	gaugeSnapshot map[string]string

	// firingSnapshot maps rule_key → set of dedupe keys we fired on
	// the previous tick. Used by evaluatePromQuery to detect recovery:
	// a key present last tick but absent this tick means PromQL's
	// comparison filter dropped the series ⇒ predicate cleared ⇒ we
	// resolve the incident. Only touched from evaluatePromQuery so no
	// extra mutex is needed (Loop is single-goroutine, EvaluateOnce in
	// tests is too).
	firingSnapshot map[string]map[string]struct{}

	log *slog.Logger
	now func() time.Time
}

// NewPipelineEvaluator builds the evaluator with sensible defaults applied.
func NewPipelineEvaluator(opts PipelineEvaluatorOpts) *PipelineEvaluator {
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Minute
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = 10 * time.Minute
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PipelineEvaluator{
		uc:        opts.Usecase,
		rules:     opts.Rules,
		notifier:  opts.Notifier,
		resolver:  opts.Resolver,
		inhibitor: opts.Inhibitor,
		channels:  append([]string(nil), opts.DefaultChannels...),
		cooldown:  opts.Cooldown,
		interval:  opts.Interval,
		devices:   opts.DeviceLister,
		edges:     opts.EdgeLister,
		prom:      opts.PromQuerier,
		logq:      opts.LogQuerier,
		log:       opts.Log,
		now:       opts.Now,
	}
}

// Loop runs the evaluator until ctx is cancelled.
func (e *PipelineEvaluator) Loop(ctx context.Context) error {
	if e.uc == nil || e.rules == nil {
		return nil
	}
	tick := time.NewTicker(e.interval)
	defer tick.Stop()
	e.evaluate(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			e.evaluate(ctx)
		}
	}
}

// EvaluateOnce runs one tick — exposed for tests.
func (e *PipelineEvaluator) EvaluateOnce(ctx context.Context) {
	e.evaluate(ctx)
}

func (e *PipelineEvaluator) evaluate(ctx context.Context) {
	now := e.now()
	if e.devices != nil || e.edges != nil {
		// Refresh the heartbeat compatibility gauges first so any
		// metric_raw rule scraped this cycle sees fresh values. The
		// canonical host-staleness signal now calculates age from the
		// heartbeat timestamp:
		//
		//   time() - max by (device_id) (device_last_seen_timestamp_seconds) > 90
		e.refreshDeviceStalenessGauge(ctx, now)
	}
	if e.prom != nil {
		e.evaluatePromQuery(ctx, now)
		e.evaluateMetricAnomaly(ctx, now)
		e.evaluateMetricForecast(ctx, now)
		e.evaluateMetricBurnRate(ctx, now)
		// Trace kinds query Prom (spanmetrics generator scrapes Tempo
		// into traces_spanmetrics_*) so they share the prom != nil gate.
		e.evaluateTraceLatency(ctx, now)
		e.evaluateTraceErrorRate(ctx, now)
	}
	if e.logq != nil {
		e.evaluateLogMatch(ctx, now)
		e.evaluateLogVolume(ctx, now)
	}
}

// refreshDeviceStalenessGauge updates the device heartbeat Prom gauges with
// one series per registered device. Source-of-truth is Device.LastSeenAt; the
// edge view is only a fallback for older tests/wiring. Series for devices that
// fall out of inventory are deleted so the metric_raw evaluator doesn't keep
// firing on a removed device.
//
// Called every evaluator tick. Errors here are logged and skipped — gauge
// staleness for one tick is preferable to a panic in the alert loop. Real-time
// offline detection should use device_last_seen_timestamp_seconds, which is
// also updated directly from the heartbeat path.
func (e *PipelineEvaluator) refreshDeviceStalenessGauge(ctx context.Context, now time.Time) {
	current, ok := e.refreshDeviceGaugesFromDevices(ctx, now)
	if !ok {
		current = e.refreshDeviceGaugesFromEdges(ctx, now)
	}
	if current == nil {
		return
	}
	e.gaugeMu.Lock()
	prev := e.gaugeSnapshot
	e.gaugeSnapshot = current
	e.gaugeMu.Unlock()
	for id, name := range prev {
		if _, ok := current[id]; ok {
			continue
		}
		prom.DeleteDeviceLastSeenSecondsAgo(id, name)
		prom.DeleteDeviceLastSeenTimestampSeconds(id)
	}
}

func (e *PipelineEvaluator) refreshDeviceGaugesFromDevices(ctx context.Context, now time.Time) (map[string]string, bool) {
	if e.devices == nil {
		return nil, false
	}
	devices, err := e.devices.List(ctx, devicebiz.ListFilter{Limit: 10000})
	if err != nil {
		e.log.Warn("alert: list devices for staleness gauge failed", slog.Any("err", err))
		return nil, false
	}
	current := make(map[string]string, len(devices))
	for _, device := range devices {
		lastSeen := device.CreatedAt
		if device.LastSeenAt != nil {
			lastSeen = *device.LastSeenAt
		}
		name := device.Name
		if name == "" {
			name = device.Hostname
		}
		idStr := fmt.Sprintf("%d", device.ID)
		setDeviceLastSeenGauges(idStr, name, lastSeen, now)
		current[idStr] = name
	}
	return current, true
}

func (e *PipelineEvaluator) refreshDeviceGaugesFromEdges(ctx context.Context, now time.Time) map[string]string {
	if e.edges == nil {
		return nil
	}
	edges, err := e.edges.List(ctx, edgebiz.ListFilter{Limit: 10000})
	if err != nil {
		e.log.Warn("alert: list edges for staleness gauge failed", slog.Any("err", err))
		return nil
	}
	// Re-build the per-tick view of which (device_id, device_name) tuples
	// we still own. Anything in the previous snapshot but not in this
	// one gets deleted from the gauge so reuse-after-removal of
	// device_id values doesn't double-up the series.
	current := make(map[string]string, len(edges))
	for _, edge := range edges {
		var lastSeen time.Time
		if edge.LastSeenAt != nil {
			lastSeen = *edge.LastSeenAt
		} else {
			lastSeen = edge.CreatedAt
		}
		// Numeric device_id: prefer Edge.DeviceID (the host device's id);
		// fall back to edge.ID before the register flow has linked them
		// (idempotent because the backfill makes the values match).
		var deviceID uint64 = edge.ID
		if edge.DeviceID != nil && *edge.DeviceID != 0 {
			deviceID = *edge.DeviceID
		}
		idStr := fmt.Sprintf("%d", deviceID)
		setDeviceLastSeenGauges(idStr, edge.Name, lastSeen, now)
		current[idStr] = edge.Name
	}
	return current
}

func setDeviceLastSeenGauges(deviceID string, deviceName string, lastSeen time.Time, now time.Time) {
	secs := now.Sub(lastSeen).Seconds()
	if secs < 0 {
		secs = 0
	}
	prom.SetDeviceLastSeenSecondsAgo(deviceID, deviceName, secs)
	prom.SetDeviceLastSeenTimestampSeconds(deviceID, float64(lastSeen.Unix()))
}

// evaluatePromQuery runs every enabled metric_raw rule's expression.
// Phase-3 collapse: the expression IS the predicate. PromQL's own
// comparison operators (`up == 0`, `cpu_pct > 90`) cause Prom to drop
// non-matching series from the response, so the evaluator's job is
// simply: for each returned vector entry, fire one incident (per
// label-set dedupe key). The previous-tick incidents whose series are
// no longer in the result get system-resolved so recovery still works.
func (e *PipelineEvaluator) evaluatePromQuery(ctx context.Context, now time.Time) {
	rules := e.rules.MetricRawRules()
	if len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		var evalErr error
		done := observeEval(model.RuleKindMetricRaw, &evalErr)
		res, err := e.prom.Query(ctx, rule.Expr, now)
		if err != nil {
			e.log.Warn("alert: prom query failed",
				slog.String("rule", rule.RuleKey),
				slog.String("expr", rule.Expr),
				slog.Any("err", err))
			evalErr = err
			done()
			continue
		}
		if res == nil || res.ResultType != "vector" {
			done()
			continue
		}
		type vectorEntry struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		}
		var entries []vectorEntry
		if err := json.Unmarshal(res.Result, &entries); err != nil {
			e.log.Warn("alert: decode prom vector failed",
				slog.String("rule", rule.RuleKey),
				slog.Any("err", err))
			evalErr = err
			done()
			continue
		}
		scope := effectiveScope(rule.ScopeType, model.RuleKindMetricRaw)
		// Track which dedupe keys this tick "owns" so we can resolve
		// any incident from the previous tick whose series fell out of
		// the result (the recovery path — Prom's comparison filtered
		// the series out because the predicate is no longer true).
		fired := make(map[string]struct{}, len(entries))
		for _, ent := range entries {
			valStr := ""
			if len(ent.Value) >= 2 {
				_ = json.Unmarshal(ent.Value[1], &valStr)
			}
			// Keep the value when it parses as a float — used purely
			// for the incident's value field. Absent values are fine;
			// the very presence of the series in the result means
			// "predicate satisfied" under PromQL's filtering semantics.
			value, hasValue := parseFloat(valStr)
			dedupeKey := fmt.Sprintf("pipeline:%s:%s", rule.RuleKey, labelSetKey(ent.Metric))
			fired[dedupeKey] = struct{}{}
			summary := fmt.Sprintf("%s: %s ⇒ %s (value=%s)", rule.RuleKey, rule.Expr, labelSetKey(ent.Metric), valStr)
			// Extract device_id from result labels when present — host-scope
			// rules require it for FiringInput validation, and the new
			// device-aware queries (`by (device_id)`) carry it as a label
			// on every series. Best-effort: malformed values fall through
			// and validateFiring rejects with a clear message.
			var devID *uint64
			if scope == model.RuleScopeHost {
				if v, ok := ent.Metric["device_id"]; ok && v != "" {
					if id, err := strconv.ParseUint(v, 10, 64); err == nil && id > 0 {
						devID = &id
					}
				}
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
				Labels:     mergeLabels(rule.Labels, ent.Metric, map[string]string{"rule": rule.RuleKey, "trigger": "ticker"}),
			}
			if hasValue {
				val := value
				input.Value = &val
			}
			res2, err := e.uc.RecordFiring(ctx, input)
			if err != nil {
				e.log.Warn("alert: record firing prom_query failed",
					slog.String("rule", rule.RuleKey),
					slog.Any("err", err))
				continue
			}
			e.notify(ctx, res2, summary, scope, now)
		}
		// Recovery sweep: any dedupe key we fired last tick that is
		// missing from this tick's result means the PromQL predicate
		// stopped matching for that label set — Prom drops the series
		// from the response when the comparison fails. Resolve those
		// incidents now so the operator sees the alarm clear. After
		// the sweep, store the current `fired` set as the new snapshot.
		if e.firingSnapshot == nil {
			e.firingSnapshot = map[string]map[string]struct{}{}
		}
		prev := e.firingSnapshot[rule.RuleKey]
		for prevKey := range prev {
			if _, stillFiring := fired[prevKey]; stillFiring {
				continue
			}
			if _, err := e.uc.SystemResolveIncident(ctx, prevKey, "prom condition cleared", now); err != nil {
				e.log.Warn("alert: resolve prom_query failed",
					slog.String("rule", rule.RuleKey),
					slog.String("dedupe", prevKey),
					slog.Any("err", err))
			}
		}
		e.resolveRecoveredPromQueryIncidents(ctx, rule.RuleKey, fired, now)
		e.firingSnapshot[rule.RuleKey] = fired
		done()
		_ = evalErr
	}
}

// resolveRecoveredPromQueryIncidents closes active metric_raw incidents that no
// longer appear in the current PromQL result, even when this evaluator process
// did not observe their previous firing in memory. That matters after deploys
// and after query label-shape changes: the old dedupe_key can remain open in
// the DB while the current tick has already proven the predicate is clear.
func (e *PipelineEvaluator) resolveRecoveredPromQueryIncidents(ctx context.Context, ruleKey string, fired map[string]struct{}, now time.Time) {
	if e.uc == nil || ruleKey == "" {
		return
	}
	for _, status := range []string{model.IncidentStatusOpen, model.IncidentStatusAcknowledged, model.IncidentStatusSilenced} {
		incidents, err := e.uc.ListIncidents(ctx, IncidentFilter{
			Status:  status,
			RuleKey: ruleKey,
			Limit:   10000,
		})
		if err != nil {
			e.log.Warn("alert: list active prom_query incidents failed",
				slog.String("rule", ruleKey),
				slog.String("status", status),
				slog.Any("err", err))
			continue
		}
		for _, inc := range incidents {
			if inc == nil || inc.DedupeKey == "" {
				continue
			}
			if _, stillFiring := fired[inc.DedupeKey]; stillFiring {
				continue
			}
			if _, err := e.uc.SystemResolveIncident(ctx, inc.DedupeKey, "prom condition cleared", now); err != nil {
				e.log.Warn("alert: resolve recovered prom_query incident failed",
					slog.String("rule", ruleKey),
					slog.String("dedupe", inc.DedupeKey),
					slog.Any("err", err))
			}
		}
	}
}

func (e *PipelineEvaluator) notify(ctx context.Context, res *FiringResult, summary, source string, at time.Time) {
	if res == nil || res.Incident == nil {
		return
	}
	msg := notify.Message{
		Subject:    summary,
		Severity:   notify.Severity(res.Incident.Severity),
		Source:     source,
		DedupeKey:  res.Incident.DedupeKey,
		OccurredAt: at,
		Labels: map[string]string{
			"rule":        res.Incident.Rule,
			"incident_id": fmt.Sprintf("%d", res.Incident.ID),
		},
	}
	if res.Incident.DeviceID != nil {
		msg.Labels["device_id"] = fmt.Sprintf("%d", *res.Incident.DeviceID)
	}
	if msg.Severity == "" {
		msg.Severity = notify.SeverityWarning
	}
	e.uc.MaybeNotify(ctx, res, msg, NotifyOpts{
		Notifier:        e.notifier,
		Resolver:        e.resolver,
		DefaultChannels: e.channels,
		Cooldown:        e.cooldown,
		Inhibitor:       e.inhibitor,
	})

	// Auto root-cause investigation fan-out happens upstream in
	// Usecase.recordFire (existing Investigator interface, see
	// usecase.go:51) — fires only on the isNew transition so reopens /
	// follow-up notifies don't re-trigger. Pipeline doesn't need its
	// own hook.
}

// nonIdentityLabels are provenance/collector labels that must NOT split an
// alert's identity. The same subject (e.g. a host's disk) can be reported by
// both the embedded and the cloud collector — one tags
// ongrid_source=embedded — which otherwise produced two incidents for one
// real alert. Excluding them from the dedupe key merges those back into one.
var nonIdentityLabels = map[string]struct{}{
	"__name__":      {},
	"ongrid_source": {},
}

func labelSetKey(m map[string]string) string {
	if len(m) == 0 {
		return "_"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if _, skip := nonIdentityLabels[k]; skip {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return "_"
	}
	// Sort manually to avoid importing sort here for one call site.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func mergeLabels(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, l := range layers {
		for k, v := range l {
			if k == "__name__" {
				continue
			}
			out[k] = v
		}
	}
	return out
}

func parseFloat(s string) (float64, bool) {
	var v float64
	if s == "" {
		return 0, false
	}
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, false
	}
	return v, true
}

// ruleSev returns the rule's severity, falling back to def when unset.
func ruleSev(s string, def notify.Severity) string {
	if s != "" {
		return s
	}
	return string(def)
}

func compareFloat(v float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return v > threshold
	case ">=":
		return v >= threshold
	case "<":
		return v < threshold
	case "<=":
		return v <= threshold
	case "==":
		return v == threshold
	case "!=":
		return v != threshold
	}
	return false
}
