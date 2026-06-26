package alert

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

// fakeNotifier is the in-memory notify.Notifier stub used by every alert
// test. Records every message + channel set so assertions can read back
// what the firing path attempted.
type fakeNotifier struct {
	msgs     []notify.Message
	channels [][]string
	fail     bool
}

func (f *fakeNotifier) Send(_ context.Context, msg notify.Message, channels ...string) error {
	f.msgs = append(f.msgs, msg)
	cp := make([]string, len(channels))
	copy(cp, channels)
	f.channels = append(f.channels, cp)
	if f.fail {
		return errors.New("synthetic notify failure")
	}
	return nil
}

// SendVia records the same way as Send (keyed by the sender's name) so
// assertions on msgs/channels still work for the persisted-channel
// dispatch path (MaybeNotify builds a typed sender then calls SendVia).
func (f *fakeNotifier) SendVia(_ context.Context, msg notify.Message, sender notify.Sender) error {
	f.msgs = append(f.msgs, msg)
	name := ""
	if sender != nil {
		name = sender.Name()
	}
	f.channels = append(f.channels, []string{name})
	if f.fail {
		return errors.New("synthetic notify failure")
	}
	return nil
}

// fakeRepo is an in-memory implementation of biz.Repo sufficient to drive
// every alert test. Only the subset RecordFiring + maybeNotify touches is
// implemented; ListIncidents et al. return empty.
type fakeRepo struct {
	now func() time.Time

	incidents     map[uint64]*model.Incident
	byDedupe      map[string]*model.Incident
	nextID        uint64
	silences      []*model.Silence
	channels      map[string]*model.Channel
	rules         map[string]*model.Rule
	deliveries    []*model.Delivery
	events        []*model.Event
	createIncErr  error
	bumpCalls     int
	reopenCalls   int
	notifiedCalls map[uint64]time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		incidents:     map[uint64]*model.Incident{},
		byDedupe:      map[string]*model.Incident{},
		channels:      map[string]*model.Channel{},
		rules:         map[string]*model.Rule{},
		notifiedCalls: map[uint64]time.Time{},
	}
}

func (r *fakeRepo) ListEnabledRulesByScope(_ context.Context, scopeType string) ([]*model.Rule, error) {
	out := make([]*model.Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if !rule.Enabled {
			continue
		}
		if rule.ScopeType != "" && rule.ScopeType != scopeType {
			continue
		}
		out = append(out, rule)
	}
	return out, nil
}

func (r *fakeRepo) ListAllEnabledRules(_ context.Context) ([]*model.Rule, error) {
	out := make([]*model.Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if !rule.Enabled {
			continue
		}
		out = append(out, rule)
	}
	return out, nil
}

func (r *fakeRepo) GetRuleByKey(_ context.Context, key string) (*model.Rule, error) {
	if rule, ok := r.rules[key]; ok {
		return rule, nil
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) UpsertBuiltinRule(_ context.Context, in *model.Rule) (*model.Rule, error) {
	if existing, ok := r.rules[in.RuleKey]; ok {
		return existing, nil
	}
	in.ID = uint64(len(r.rules) + 1)
	r.rules[in.RuleKey] = in
	return in, nil
}

func (r *fakeRepo) ListRules(_ context.Context, scopeType string) ([]*model.Rule, error) {
	out := make([]*model.Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if scopeType != "" && rule.ScopeType != scopeType {
			continue
		}
		out = append(out, rule)
	}
	return out, nil
}

func (r *fakeRepo) GetRuleByID(_ context.Context, id uint64) (*model.Rule, error) {
	for _, rule := range r.rules {
		if rule.ID == id {
			return rule, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) CreateRule(_ context.Context, in *model.Rule) error {
	if _, exists := r.rules[in.RuleKey]; exists {
		return errs.ErrConflict
	}
	in.ID = uint64(len(r.rules) + 1)
	r.rules[in.RuleKey] = in
	return nil
}

func (r *fakeRepo) UpdateRule(_ context.Context, id uint64, in *model.Rule) error {
	for k, rule := range r.rules {
		if rule.ID == id {
			in.ID = id
			in.RuleKey = rule.RuleKey
			r.rules[k] = in
			return nil
		}
	}
	return errs.ErrNotFound
}

func (r *fakeRepo) UpdateRuleEnabled(_ context.Context, id uint64, enabled bool) error {
	for _, rule := range r.rules {
		if rule.ID == id {
			rule.Enabled = enabled
			return nil
		}
	}
	return errs.ErrNotFound
}

func (r *fakeRepo) DeleteRule(_ context.Context, id uint64) error {
	for k, rule := range r.rules {
		if rule.ID == id {
			delete(r.rules, k)
			return nil
		}
	}
	return errs.ErrNotFound
}

// seedMetricRawRules helps tests get the canonical CPU/Mem/Disk/Load
// rules into the fake repo as metric_raw rows (post-Phase-3-collapse
// shape) so the rule-driven evaluator has something to fire.
func (r *fakeRepo) seedMetricRawRules(t *testing.T) {
	t.Helper()
	type seed struct {
		key  string
		name string
		expr string
	}
	defaults := []seed{
		{"cpu_high", "CPU High", `(100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))) >= 80`},
		{"mem_high", "Mem High", `(100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)) >= 80`},
		{"disk_high", "Disk High", `(100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})) >= 80`},
		{"load1_high", "Load1 High", `node_load1 >= 1`},
	}
	for _, s := range defaults {
		blob, _ := json.Marshal(map[string]any{"expr": s.expr})
		r.rules[s.key] = &model.Rule{
			ID:             uint64(len(r.rules) + 1),
			RuleKey:        s.key,
			Kind:           model.RuleKindMetricRaw,
			Name:           s.name,
			SourceType:     model.RuleSourceBuiltin,
			ScopeType:      model.RuleScopeHost,
			JoinMode:       model.RuleJoinModeAll,
			Severity:       string(notify.SeverityWarning),
			Enabled:        true,
			ConditionsJSON: string(blob),
		}
	}
}

func (r *fakeRepo) ListIncidents(context.Context, IncidentFilter) ([]*model.Incident, error) {
	return nil, nil
}

func (r *fakeRepo) CountIncidents(context.Context, IncidentFilter) (int64, error) {
	return int64(len(r.incidents)), nil
}

func (r *fakeRepo) GetIncidentByID(_ context.Context, id uint64) (*model.Incident, error) {
	if i, ok := r.incidents[id]; ok {
		return i, nil
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) UpdateIncidentStatus(_ context.Context, id uint64, status string, _ *uint64, _ time.Time) error {
	i, ok := r.incidents[id]
	if !ok {
		return errs.ErrNotFound
	}
	i.Status = status
	return nil
}

func (r *fakeRepo) CreateEvent(_ context.Context, ev *model.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func (r *fakeRepo) ListEventsByIncident(_ context.Context, _ uint64, _ int) ([]*model.Event, error) {
	return nil, nil
}

func (r *fakeRepo) CountEventsByType(_ context.Context, eventType string, since time.Time, filterRule, filterSeverity string) (int64, error) {
	var n int64
	for _, ev := range r.events {
		if ev.EventType != eventType {
			continue
		}
		if !ev.CreatedAt.IsZero() && ev.CreatedAt.Before(since) {
			continue
		}
		if filterSeverity != "" && ev.Severity != filterSeverity {
			continue
		}
		if filterRule != "" {
			inc, ok := r.incidents[ev.IncidentID]
			if !ok || inc.Rule != filterRule {
				continue
			}
		}
		n++
	}
	return n, nil
}

func (r *fakeRepo) CreateSilence(_ context.Context, s *model.Silence) error {
	if s.ID == 0 {
		s.ID = uint64(len(r.silences) + 1)
	}
	r.silences = append(r.silences, s)
	return nil
}

func (r *fakeRepo) GetIncidentByDedupeKey(_ context.Context, dedupeKey string) (*model.Incident, error) {
	if i, ok := r.byDedupe[dedupeKey]; ok {
		return i, nil
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) CreateIncident(_ context.Context, in *model.Incident) error {
	if r.createIncErr != nil {
		return r.createIncErr
	}
	r.nextID++
	in.ID = r.nextID
	r.incidents[in.ID] = in
	r.byDedupe[in.DedupeKey] = in
	return nil
}

func (r *fakeRepo) BumpIncidentFiring(_ context.Context, id uint64, firedAt time.Time, summary string, value, threshold *float64) error {
	r.bumpCalls++
	i, ok := r.incidents[id]
	if !ok {
		return errs.ErrNotFound
	}
	i.LastFiredAt = firedAt
	i.EventCount++
	i.Summary = summary
	i.Value = value
	i.Threshold = threshold
	return nil
}

func (r *fakeRepo) ReopenIncident(_ context.Context, id uint64, firedAt time.Time, summary string, value, threshold *float64) error {
	r.reopenCalls++
	i, ok := r.incidents[id]
	if !ok {
		return errs.ErrNotFound
	}
	i.Status = model.IncidentStatusOpen
	i.LastFiredAt = firedAt
	i.SilencedUntil = nil
	i.ResolvedAt = nil
	i.ResolvedBy = nil
	i.EventCount++
	i.Summary = summary
	i.Value = value
	i.Threshold = threshold
	return nil
}

func (r *fakeRepo) MarkIncidentNotified(_ context.Context, id uint64, at time.Time) error {
	r.notifiedCalls[id] = at
	if i, ok := r.incidents[id]; ok {
		t := at
		i.LastNotifiedAt = &t
	}
	return nil
}

func (r *fakeRepo) ListActiveSilences(_ context.Context, _ time.Time) ([]*model.Silence, error) {
	return r.silences, nil
}

func (r *fakeRepo) GetChannelByName(_ context.Context, name string) (*model.Channel, error) {
	if c, ok := r.channels[name]; ok {
		return c, nil
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) GetChannelByID(_ context.Context, id uint64) (*model.Channel, error) {
	for _, c := range r.channels {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) ListEnabledChannels(_ context.Context) ([]*model.Channel, error) {
	out := make([]*model.Channel, 0, len(r.channels))
	for _, c := range r.channels {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *fakeRepo) ListChannels(_ context.Context, filter ChannelFilter) ([]*model.Channel, error) {
	out := make([]*model.Channel, 0, len(r.channels))
	for _, c := range r.channels {
		if filter.Enabled != nil && c.Enabled != *filter.Enabled {
			continue
		}
		if filter.ChannelType != "" && c.ChannelType != filter.ChannelType {
			continue
		}
		out = append(out, c)
	}
	if filter.Offset > 0 {
		if filter.Offset >= len(out) {
			return []*model.Channel{}, nil
		}
		out = out[filter.Offset:]
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (r *fakeRepo) CreateChannel(_ context.Context, in *model.Channel) error {
	if in == nil {
		return errors.New("channel required")
	}
	if r.channels == nil {
		r.channels = map[string]*model.Channel{}
	}
	in.ID = uint64(len(r.channels) + 1)
	r.channels[in.Name] = in
	return nil
}

func (r *fakeRepo) UpdateChannel(_ context.Context, id uint64, in *model.Channel) error {
	if in == nil {
		return errors.New("channel required")
	}
	for k, c := range r.channels {
		if c.ID == id {
			merged := *in
			merged.ID = id
			merged.CreatedAt = c.CreatedAt
			delete(r.channels, k)
			r.channels[merged.Name] = &merged
			return nil
		}
	}
	return errors.New("not found")
}

func (r *fakeRepo) DeleteChannel(_ context.Context, id uint64) error {
	for k, c := range r.channels {
		if c.ID == id {
			delete(r.channels, k)
			return nil
		}
	}
	return errors.New("not found")
}

func (r *fakeRepo) CountRulesReferencingChannel(_ context.Context, channelID uint64) (int64, error) {
	if channelID == 0 {
		return 0, nil
	}
	var count int64
	for _, rule := range r.rules {
		if rule == nil || rule.NotifyChannelIDsJSON == nil {
			continue
		}
		var ids []uint64
		if err := json.Unmarshal([]byte(*rule.NotifyChannelIDsJSON), &ids); err != nil {
			continue
		}
		for _, id := range ids {
			if id == channelID {
				count++
				break
			}
		}
	}
	return count, nil
}

func (r *fakeRepo) ListRetriableDeliveries(_ context.Context, maxAttempts uint32, before time.Time, limit int) ([]*model.Delivery, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []*model.Delivery
	for _, d := range r.deliveries {
		if d.Status != model.DeliveryStatusFailed {
			continue
		}
		if d.AttemptCount >= maxAttempts {
			continue
		}
		if !before.IsZero() && d.FinishedAt != nil && d.FinishedAt.After(before) {
			continue
		}
		out = append(out, d)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeRepo) CreateDelivery(_ context.Context, d *model.Delivery) error {
	d.ID = uint64(len(r.deliveries) + 1)
	r.deliveries = append(r.deliveries, d)
	return nil
}

func (r *fakeRepo) UpdateDeliveryStatus(_ context.Context, id uint64, status string, attempt uint32, _ *string, _ *string, errMsg *string, sentAt, finishedAt *time.Time) error {
	for _, d := range r.deliveries {
		if d.ID == id {
			d.Status = status
			d.AttemptCount = attempt
			d.ErrorMessage = errMsg
			d.SentAt = sentAt
			d.FinishedAt = finishedAt
			return nil
		}
	}
	return errs.ErrNotFound
}

func hasEventType(events []*model.Event, t string) bool {
	for _, ev := range events {
		if ev.EventType == t {
			return true
		}
	}
	return false
}
