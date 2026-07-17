package alert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

type fakeEdgeLister struct {
	edges []*edgemodel.Edge
	err   error
}

func (f *fakeEdgeLister) List(_ context.Context, _ edgebiz.ListFilter) ([]*edgemodel.Edge, error) {
	return f.edges, f.err
}

type fakePromQuerier struct {
	result *promquery.InstantResult
	err    error
}

func (f *fakePromQuerier) Query(_ context.Context, _ string, _ time.Time) (*promquery.InstantResult, error) {
	return f.result, f.err
}

func newPipelineEvaluator(t *testing.T, repo *fakeRepo, notifier Notifier, rules RulesProvider, opts PipelineEvaluatorOpts) *PipelineEvaluator {
	t.Helper()
	if opts.Usecase == nil {
		opts.Usecase = NewUsecase(repo, nil)
	}
	if opts.Notifier == nil {
		opts.Notifier = notifier
	}
	if opts.Rules == nil {
		opts.Rules = rules
	}
	if opts.DefaultChannels == nil {
		opts.DefaultChannels = []string{"log"}
	}
	if opts.Now == nil {
		fixed := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
		opts.Now = func() time.Time { return fixed }
	}
	repo.channels["log"] = &model.Channel{ID: 1, Name: "log", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"url":"http://test.local/hook"}`}
	return NewPipelineEvaluator(opts)
}

// TestPipelineEdgeOfflineMetricRawFiresAndResolves verifies the
// replacement path for edge_offline alerts: a
// metric_raw rule on the edge_last_seen_seconds_ago gauge fires when
// any edge crosses the threshold and resolves once the gauge drops
// back below.
func TestPipelineEdgeOfflineMetricRawFiresAndResolves(t *testing.T) {
	repo := newFakeRepo()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	// Phase-3 collapse: the expr IS the predicate. fakePromQuerier
	// simulates Prom-side filtering: when the predicate matches, the
	// vector entry is in the response; on recovery we swap to an empty
	// vector (Prom drops the series when comparison returns false).
	prom := &fakePromQuerier{result: vectorEdgeStaleness(map[string]string{
		"1|node-a": "180", // stale: 180s > 90s ⇒ Prom keeps the series
	})}
	rules := NewStaticRulesProvider(WithMetricRawRules([]MetricRawRule{
		{ID: 100, RuleKey: "edge_offline", Name: "Edge Offline", Severity: "critical",
			ScopeType: "global", Expr: "edge_last_seen_seconds_ago > 90"},
	}))
	clock := now
	eval := newPipelineEvaluator(t, repo, notifier, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return clock },
	})

	eval.EvaluateOnce(context.Background())

	if len(repo.incidents) != 1 {
		t.Fatalf("expect 1 incident for stale edge, got %d", len(repo.incidents))
	}
	var inc *model.Incident
	for _, i := range repo.incidents {
		inc = i
	}
	if inc.Rule != "edge_offline" {
		t.Errorf("rule = %q", inc.Rule)
	}
	if len(notifier.msgs) != 1 {
		t.Errorf("notifications = %d, want 1", len(notifier.msgs))
	}

	// Recovery: predicate clears ⇒ Prom drops the series ⇒ empty vector.
	prom.result = vectorEdgeStaleness(map[string]string{})
	clock = now.Add(2 * time.Second)
	eval.EvaluateOnce(context.Background())

	if inc.Status != model.IncidentStatusResolved {
		t.Errorf("after recovery status = %q, want resolved", inc.Status)
	}
}

func TestPipelinePromQueryFiresAndResolves(t *testing.T) {
	repo := newFakeRepo()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	prom := &fakePromQuerier{result: vectorUp(map[string]string{
		"localhost:9100|node": "0",
	})}
	rules := NewStaticRulesProvider(WithMetricRawRules([]MetricRawRule{
		{ID: 200, RuleKey: "scrape_down", Name: "Scrape Down", Severity: "warning",
			Expr: "up == 0"},
	}))
	clock := now
	eval := newPipelineEvaluator(t, repo, notifier, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return clock },
	})

	eval.EvaluateOnce(context.Background())

	if len(repo.incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(repo.incidents))
	}
	var inc *model.Incident
	for _, i := range repo.incidents {
		inc = i
	}
	if inc.Rule != "scrape_down" {
		t.Errorf("rule = %q", inc.Rule)
	}
	wantDedupe := "pipeline:scrape_down:instance=localhost:9100,job=node"
	if inc.DedupeKey != wantDedupe {
		t.Errorf("dedupe = %q, want %q", inc.DedupeKey, wantDedupe)
	}

	// Recovery: PromQL `up == 0` no longer matches when up=1 ⇒ empty
	// vector ⇒ evaluator resolves the prior incident via snapshot diff.
	prom.result = vectorUp(map[string]string{})
	clock = now.Add(time.Second)
	eval.EvaluateOnce(context.Background())

	if inc.Status != model.IncidentStatusResolved {
		t.Errorf("status after recovery = %q", inc.Status)
	}
}

func TestPipelinePromQueryErrorIsSafe(t *testing.T) {
	repo := newFakeRepo()
	prom := &fakePromQuerier{err: errors.New("connection refused")}
	rules := NewStaticRulesProvider(WithMetricRawRules([]MetricRawRule{
		{ID: 1, RuleKey: "scrape_down", Name: "Scrape Down", Severity: "warning",
			Expr: "up == 0"},
	}))
	eval := newPipelineEvaluator(t, repo, &fakeNotifier{}, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
	})

	// Must not panic, must not resolve anything.
	eval.EvaluateOnce(context.Background())

	if len(repo.incidents) != 0 {
		t.Errorf("prom query failure must not create incidents, got %d", len(repo.incidents))
	}
}

func TestPipelineNotification_ResolvesDeviceIDToHostnameAndIP(t *testing.T) {
	repo := newFakeRepo()
	notifier := &fakeNotifier{}
	prom := &fakePromQuerier{result: vectorEdgeStaleness(map[string]string{
		"2|edge-197": "91",
	})}
	rules := NewStaticRulesProvider(WithMetricRawRules([]MetricRawRule{
		{ID: 201, RuleKey: "cpu_high_80", Name: "CPU High", Severity: "warning",
			ScopeType: model.RuleScopeHost, Expr: "device_cpu_usage_percent > 80"},
	}))
	eval := newPipelineEvaluator(t, repo, notifier, rules, PipelineEvaluatorOpts{
		PromQuerier: prom,
		DeviceIdentityResolver: func(_ context.Context, deviceID uint64) (DeviceIdentity, error) {
			if deviceID != 2 {
				t.Fatalf("deviceID = %d, want 2", deviceID)
			}
			return DeviceIdentity{Hostname: "VM-4-8-ubuntu", IPAddress: "10.2.4.8"}, nil
		},
	})

	eval.EvaluateOnce(context.Background())

	if len(notifier.msgs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifier.msgs))
	}
	msg := notifier.msgs[0]
	if strings.Contains(msg.Subject, "device_id=2") {
		t.Fatalf("subject still exposes raw device id: %q", msg.Subject)
	}
	if !strings.Contains(msg.Subject, "device=VM-4-8-ubuntu (10.2.4.8)") {
		t.Fatalf("subject = %q, want resolved device identity", msg.Subject)
	}
	if msg.Labels["device_id"] != "2" || msg.Labels["device_hostname"] != "VM-4-8-ubuntu" || msg.Labels["device_ip"] != "10.2.4.8" {
		t.Fatalf("labels = %#v", msg.Labels)
	}
}

func TestDeviceDisplay_UsesAvailableIdentityFields(t *testing.T) {
	cases := []struct {
		name     string
		identity DeviceIdentity
		want     string
	}{
		{name: "hostname and ip", identity: DeviceIdentity{Hostname: "host-a", IPAddress: "10.0.0.1"}, want: "host-a (10.0.0.1)"},
		{name: "name fallback", identity: DeviceIdentity{Name: "gateway-a"}, want: "gateway-a"},
		{name: "ip only", identity: DeviceIdentity{IPAddress: "10.0.0.2"}, want: "10.0.0.2"},
		{name: "empty", identity: DeviceIdentity{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceDisplay(tc.identity); got != tc.want {
				t.Fatalf("deviceDisplay() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPipelineEdgeOfflineMultipleMetricRawRules confirms two metric_raw
// rules with different thresholds against edge_last_seen_seconds_ago
// each create their own incident — the same multi-rule fan-out the
// deleted edge_absence path supported.
func TestPipelineEdgeOfflineMultipleMetricRawRules(t *testing.T) {
	repo := newFakeRepo()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	prom := &fakePromQuerier{result: vectorEdgeStaleness(map[string]string{
		"5|core": "120", // 120s stale: crosses both thresholds
	})}
	rules := NewStaticRulesProvider(WithMetricRawRules([]MetricRawRule{
		{ID: 100, RuleKey: "edge_offline", Name: "Edge Offline 90s", Severity: "warning",
			ScopeType: "global", Expr: "edge_last_seen_seconds_ago > 90"},
		{ID: 101, RuleKey: "edge_offline_strict", Name: "Edge Offline 30s", Severity: "critical",
			ScopeType: "global", Expr: "edge_last_seen_seconds_ago > 30"},
	}))
	eval := newPipelineEvaluator(t, repo, notifier, rules, PipelineEvaluatorOpts{
		EdgeLister:  &fakeEdgeLister{},
		PromQuerier: prom,
		Cooldown:    time.Minute,
		Now:         func() time.Time { return now },
	})

	eval.EvaluateOnce(context.Background())

	if len(repo.incidents) != 2 {
		t.Errorf("expect 2 incidents (one per rule), got %d", len(repo.incidents))
	}
}

// vectorEdgeStaleness builds a Prom-style vector response keyed
// "edge_id|edge_name" -> seconds-ago, mimicking the
// edge_last_seen_seconds_ago gauge PipelineEvaluator publishes.
func vectorEdgeStaleness(samples map[string]string) *promquery.InstantResult {
	type vEntry struct {
		Metric map[string]string `json:"metric"`
		Value  []json.RawMessage `json:"value"`
	}
	var entries []vEntry
	for k, v := range samples {
		edgeID := k
		edgeName := ""
		for i := range k {
			if k[i] == '|' {
				edgeID = k[:i]
				edgeName = k[i+1:]
				break
			}
		}
		ts, _ := json.Marshal(float64(time.Now().Unix()))
		val, _ := json.Marshal(v)
		entries = append(entries, vEntry{
			Metric: map[string]string{
				"__name__":  "edge_last_seen_seconds_ago",
				"device_id": edgeID,
				"edge_name": edgeName,
			},
			Value: []json.RawMessage{ts, val},
		})
	}
	raw, _ := json.Marshal(entries)
	return &promquery.InstantResult{ResultType: "vector", Result: raw}
}

// vectorUp formats a Prom-style vector response keyed "instance|job" -> value.
func vectorUp(samples map[string]string) *promquery.InstantResult {
	type vEntry struct {
		Metric map[string]string `json:"metric"`
		Value  []json.RawMessage `json:"value"`
	}
	var entries []vEntry
	for k, v := range samples {
		instance := k
		job := ""
		for i := range k {
			if k[i] == '|' {
				instance = k[:i]
				job = k[i+1:]
				break
			}
		}
		ts, _ := json.Marshal(float64(time.Now().Unix()))
		val, _ := json.Marshal(v)
		entries = append(entries, vEntry{
			Metric: map[string]string{
				"__name__": "up",
				"instance": instance,
				"job":      job,
			},
			Value: []json.RawMessage{ts, val},
		})
	}
	raw, _ := json.Marshal(entries)
	return &promquery.InstantResult{ResultType: "vector", Result: raw}
}
