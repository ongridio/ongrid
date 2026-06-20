package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewRepo(db)
}

func TestIncidentEventRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	labels := `{"device_id":"2","rule":"cpu_high"}`
	incident := &model.Incident{
		DeviceID:     ptrUint64(2),
		Scope:        model.RuleScopeHost,
		Rule:         "cpu_high",
		RuleName:     "CPU High",
		Severity:     "warning",
		Status:       model.IncidentStatusOpen,
		Summary:      "edge 2 cpu high",
		DedupeKey:    "2:cpu_high",
		LabelsJSON:   labels,
		EventCount:   1,
		FirstFiredAt: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
		LastFiredAt:  time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
		SourceType:   model.RuleSourceBuiltin,
		RunbookURL:   "https://example.invalid/runbooks/cpu_high",
	}
	if err := repo.CreateAlertIncident(ctx, incident); err != nil {
		t.Fatalf("CreateAlertIncident: %v", err)
	}

	got, err := repo.GetIncidentByDedupeKey(ctx, incident.DedupeKey)
	if err != nil {
		t.Fatalf("GetIncidentByDedupeKey: %v", err)
	}
	if got.ID == 0 || got.Rule != incident.Rule {
		t.Fatalf("incident mismatch: %+v", got)
	}
	parsedLabels, err := got.Labels()
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if parsedLabels["rule"] != "cpu_high" {
		t.Fatalf("Labels rule = %q", parsedLabels["rule"])
	}

	ev := &model.Event{
		IncidentID: got.ID,
		EventType:  model.EventTypeFiring,
		Reason:     "threshold hit",
		CreatedAt:  incident.FirstFiredAt,
	}
	if err := repo.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if err := repo.BumpIncidentFiring(ctx, got.ID, incident.FirstFiredAt.Add(5*time.Minute)); err != nil {
		t.Fatalf("BumpIncidentFiring: %v", err)
	}
	if err := repo.UpdateIncidentStatus(ctx, got.ID, model.IncidentStatusAcknowledged, ptrUint64(99), incident.FirstFiredAt.Add(6*time.Minute)); err != nil {
		t.Fatalf("UpdateIncidentStatus: %v", err)
	}

	gotAfter, err := repo.GetIncidentByID(ctx, got.ID)
	if err != nil {
		t.Fatalf("GetIncidentByID: %v", err)
	}
	if gotAfter.Status != model.IncidentStatusAcknowledged {
		t.Fatalf("status = %q", gotAfter.Status)
	}
	if gotAfter.AcknowledgedBy == nil || *gotAfter.AcknowledgedBy != 99 {
		t.Fatalf("acknowledged_by = %v", gotAfter.AcknowledgedBy)
	}
	if gotAfter.EventCount != 2 {
		t.Fatalf("event_count = %d", gotAfter.EventCount)
	}

	list, err := repo.ListEventsByIncident(ctx, got.ID, 10)
	if err != nil {
		t.Fatalf("ListEventsByIncident: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("events len = %d", len(list))
	}
}

func TestSilenceRuleChannelDeliveryRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	silence := &model.Silence{
		Name:         "self-loop cpu maintenance",
		Scope:        model.RuleScopeHost,
		DeviceID:     ptrUint64(2),
		Rule:         "cpu_high",
		Status:       model.SilenceStatusActive,
		MatchersJSON: `[{"field":"device_id","operator":"=","value":"2"}]`,
		Reason:       ptrString("maintenance"),
		StartsAt:     time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
		EndsAt:       time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
	}
	if err := repo.CreateSilence(ctx, silence); err != nil {
		t.Fatalf("CreateSilence: %v", err)
	}
	activeAt := time.Date(2026, 5, 2, 11, 0, 0, 0, time.UTC)
	silences, err := repo.ListSilenceRows(ctx, SilenceFilter{Status: model.SilenceStatusActive, ActiveAt: &activeAt})
	if err != nil {
		t.Fatalf("ListSilences: %v", err)
	}
	if len(silences) != 1 {
		t.Fatalf("silences len = %d", len(silences))
	}
	matchers, err := silences[0].Matchers()
	if err != nil {
		t.Fatalf("Matchers: %v", err)
	}
	if len(matchers) != 1 || matchers[0].Value != "2" {
		t.Fatalf("matchers = %+v", matchers)
	}

	rule := &model.Rule{
		Name:           "host cpu and mem high",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       "warning",
		Enabled:        true,
		ConditionsJSON: `[{"metric":"cpu_pct","operator":">","threshold":90,"for":"10m"},{"metric":"mem_pct","operator":">","threshold":85,"for":"10m"}]`,
	}
	if err := repo.CreateRule(ctx, rule); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	gotRule, err := repo.GetRuleByID(ctx, rule.ID)
	if err != nil {
		t.Fatalf("GetRuleByID: %v", err)
	}
	conds, err := gotRule.Conditions()
	if err != nil {
		t.Fatalf("Conditions: %v", err)
	}
	if len(conds) != 2 || conds[0].Metric != "cpu_pct" {
		t.Fatalf("conditions = %+v", conds)
	}
	if err := repo.UpdateRuleEnabled(ctx, gotRule.ID, false); err != nil {
		t.Fatalf("UpdateRuleEnabled: %v", err)
	}

	channel := &model.Channel{
		Name:        "feishu-default",
		ChannelType: model.ChannelTypeFeishu,
		Enabled:     true,
		ConfigJSON:  `{"webhook_url":"https://example.invalid/hook"}`,
	}
	if err := repo.CreateChannel(ctx, channel); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	cfg, err := channel.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg["webhook_url"] == "" {
		t.Fatalf("channel config empty")
	}

	delivery := &model.Delivery{
		IncidentID:   ptrUint64(1),
		ChannelID:    channel.ID,
		Status:       model.DeliveryStatusPending,
		AttemptCount: 0,
	}
	if err := repo.CreateDelivery(ctx, delivery); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	now := time.Date(2026, 5, 2, 10, 10, 0, 0, time.UTC)
	msgID := "feishu-msg-1"
	resp := `{"code":0}`
	if err := repo.UpdateDeliveryStatus(ctx, delivery.ID, model.DeliveryStatusSuccess, 1, &msgID, &resp, nil, &now, &now); err != nil {
		t.Fatalf("UpdateDeliveryStatus: %v", err)
	}
	deliveries, err := repo.ListDeliveryRows(ctx, DeliveryFilter{IncidentID: ptrUint64(1)})
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != model.DeliveryStatusSuccess {
		t.Fatalf("deliveries = %+v", deliveries)
	}
}

func TestRepoNotFoundAndValidation(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.GetIncidentByID(ctx, 42); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("GetIncidentByID err = %v", err)
	}
	if err := repo.UpdateIncidentStatus(ctx, 42, model.IncidentStatusOpen, nil, time.Time{}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("UpdateIncidentStatus err = %v", err)
	}
	if err := repo.UpdateIncidentStatus(ctx, 1, "bogus", nil, time.Time{}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid incident status err = %v", err)
	}
	if err := repo.UpdateSilenceStatus(ctx, 1, "bogus", nil, nil); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid silence status err = %v", err)
	}
	if err := repo.UpdateDeliveryStatus(ctx, 1, "bogus", 0, nil, nil, nil, nil, nil); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid delivery status err = %v", err)
	}
}

func TestDeleteRuleHardDeletesAndFreesRuleKey(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	key := "custom_disk_pressure"

	first := &model.Rule{
		RuleKey:        key,
		Kind:           model.RuleKindMetricRaw,
		Name:           "Custom Disk Pressure",
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       "warning",
		Enabled:        true,
		ConditionsJSON: `{"expr":"up == 0"}`,
	}
	if err := repo.CreateRule(ctx, first); err != nil {
		t.Fatalf("CreateRule first: %v", err)
	}
	if err := repo.DeleteRule(ctx, first.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if _, err := repo.GetRuleByKey(ctx, key); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("GetRuleByKey after delete err = %v, want ErrNotFound", err)
	}

	second := &model.Rule{
		RuleKey:        key,
		Kind:           model.RuleKindMetricRaw,
		Name:           "Custom Disk Pressure Recreated",
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       "warning",
		Enabled:        true,
		ConditionsJSON: `{"expr":"up == 1"}`,
	}
	if err := repo.CreateRule(ctx, second); err != nil {
		t.Fatalf("CreateRule second with same key: %v", err)
	}
}

// TestCountRulesReferencingChannel exercises the LIKE-based scan used
// by the DeleteChannel guard. The matcher must distinguish id 1 from
// id 11 inside JSON arrays of varying shape.
func TestCountRulesReferencingChannel(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	// Mix of rules whose notify_channel_ids_json embeds channel id 1
	// in different positions, plus one with id 11 (should NOT match)
	// and one with no override (should NOT match).
	cases := []struct {
		key string
		ids string
	}{
		{"rule_only_one", `[1]`},
		{"rule_first", `[1,2,3]`},
		{"rule_middle", `[5,1,8]`},
		{"rule_last", `[7,9,1]`},
		{"rule_eleven", `[11]`},           // must not match id 1
		{"rule_eleven_first", `[11,2,3]`}, // must not match id 1
	}
	for _, c := range cases {
		ids := c.ids
		rule := &model.Rule{
			RuleKey:              c.key,
			Name:                 c.key,
			SourceType:           model.RuleSourceBuiltin,
			ScopeType:            model.RuleScopeHost,
			JoinMode:             model.RuleJoinModeAll,
			Severity:             "warning",
			Enabled:              true,
			ConditionsJSON:       `[]`,
			NotifyChannelIDsJSON: &ids,
		}
		if err := repo.CreateRule(ctx, rule); err != nil {
			t.Fatalf("CreateRule %s: %v", c.key, err)
		}
	}
	// One more rule with no override at all.
	if err := repo.CreateRule(ctx, &model.Rule{
		RuleKey:        "rule_no_override",
		Name:           "rule_no_override",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       "warning",
		Enabled:        true,
		ConditionsJSON: `[]`,
	}); err != nil {
		t.Fatalf("CreateRule no-override: %v", err)
	}

	// Channel id 1 is referenced by exactly four rules.
	count, err := repo.CountRulesReferencingChannel(ctx, 1)
	if err != nil {
		t.Fatalf("Count(1): %v", err)
	}
	if count != 4 {
		t.Fatalf("Count(1) = %d, want 4", count)
	}

	// Channel id 11 by 2 rules.
	count, err = repo.CountRulesReferencingChannel(ctx, 11)
	if err != nil {
		t.Fatalf("Count(11): %v", err)
	}
	if count != 2 {
		t.Fatalf("Count(11) = %d, want 2", count)
	}

	// Channel id 999 by zero rules.
	count, err = repo.CountRulesReferencingChannel(ctx, 999)
	if err != nil {
		t.Fatalf("Count(999): %v", err)
	}
	if count != 0 {
		t.Fatalf("Count(999) = %d, want 0", count)
	}
}

// TestPurgeLegacyLogChannels verifies the boot-time idempotent
// cleanup soft-deletes rows whose channel_type = "log".
func TestPurgeLegacyLogChannels(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	keep := &model.Channel{
		Name:        "primary-feishu",
		ChannelType: model.ChannelTypeFeishu,
		Enabled:     true,
		ConfigJSON:  `{}`,
	}
	if err := repo.CreateChannel(ctx, keep); err != nil {
		t.Fatalf("CreateChannel keep: %v", err)
	}
	legacy := &model.Channel{
		Name:        "legacy-log",
		ChannelType: "log", // legacy type — no longer in the constants
		Enabled:     true,
		ConfigJSON:  `{}`,
	}
	if err := repo.CreateChannel(ctx, legacy); err != nil {
		t.Fatalf("CreateChannel legacy: %v", err)
	}

	if err := repo.PurgeLegacyLogChannels(ctx); err != nil {
		t.Fatalf("PurgeLegacyLogChannels: %v", err)
	}

	rows, err := repo.ListChannelRows(ctx, ChannelFilter{})
	if err != nil {
		t.Fatalf("ListChannelRows: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "primary-feishu" {
		t.Fatalf("after purge, rows = %+v; want only primary-feishu", rows)
	}

	// Idempotent: running twice doesn't error.
	if err := repo.PurgeLegacyLogChannels(ctx); err != nil {
		t.Fatalf("second PurgeLegacyLogChannels: %v", err)
	}
}

func ptrUint64(v uint64) *uint64 { return &v }

func ptrString(v string) *string { return &v }
