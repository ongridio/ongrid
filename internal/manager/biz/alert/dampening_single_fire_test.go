package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

type dampeningEventRepo struct {
	*fakeRepo
	failFiring bool
	countLimit int
}

func (r *dampeningEventRepo) CreateEvent(ctx context.Context, ev *model.Event) error {
	if r.failFiring && ev.EventType == model.EventTypeFiring {
		return errors.New("simulated firing event write failure")
	}
	ev.CreatedAt = ev.OccurredAt
	return r.fakeRepo.CreateEvent(ctx, ev)
}

func (r *dampeningEventRepo) CountEventsByType(ctx context.Context, eventType string, since time.Time, rule, severity string, limit int) (int64, error) {
	r.countLimit = limit
	return r.fakeRepo.CountEventsByType(ctx, eventType, since, rule, severity, limit)
}

func TestMaybeNotifyDampeningSingleFireSemantics(t *testing.T) {
	for _, tc := range []struct {
		name               string
		failFiring, reopen bool
		wantCount          int64
		wantMessages       int
	}{
		{name: "normal_firing", wantCount: 1, wantMessages: 1},
		{name: "failed_firing_write", failFiring: true},
		{name: "reopen_after_window", reopen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := &dampeningEventRepo{fakeRepo: newFakeRepo(), failFiring: tc.failFiring}
			uc := NewUsecase(repo, nil)
			repo.channels["log"] = &model.Channel{ID: 1, Name: "log", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"url":"http://test.local/hook"}`}
			_, err := uc.CreateRule(ctx, RuleInput{RuleKey: "probe", Name: "Probe", Kind: model.RuleKindMetricRaw, ScopeType: model.RuleScopeGlobal, JoinMode: model.RuleJoinModeAll, Severity: "warning", Enabled: true, Spec: map[string]any{"expr": "vector(1) > 0"}, NotifyWindowSeconds: 1800, NotifyMinFires: 1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
			input := FiringInput{ScopeType: model.RuleScopeGlobal, Rule: "probe", Severity: "warning", Summary: "probe", DedupeKey: "dampening:probe", OccurredAt: now}
			res, err := uc.RecordFiring(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if tc.reopen {
				resolved, err := uc.SystemResolveIncident(ctx, input.DedupeKey, "recovered", now.Add(time.Minute))
				if err != nil || !resolved {
					t.Fatalf("resolve = %v, %v", resolved, err)
				}
				now = now.Add(31 * time.Minute)
				input.OccurredAt = now
				res, err = uc.RecordFiring(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				if !res.IsReopen {
					t.Fatal("expected reopened incident")
				}
			}
			count, err := repo.CountEventsByType(ctx, model.EventTypeFiring, now.Add(-30*time.Minute), "probe", "", 100)
			if err != nil || count != tc.wantCount {
				t.Fatalf("count = %d, %v; want %d", count, err, tc.wantCount)
			}
			notifier := &fakeNotifier{}
			uc.MaybeNotify(ctx, res, notify.Message{Subject: "probe", Severity: notify.SeverityWarning, OccurredAt: now}, NotifyOpts{Notifier: notifier, DefaultChannels: []string{"log"}, Cooldown: 0})
			if repo.countLimit != 1 {
				t.Fatalf("count limit = %d, want 1", repo.countLimit)
			}
			if len(notifier.msgs) != tc.wantMessages {
				t.Fatalf("messages = %d, want %d", len(notifier.msgs), tc.wantMessages)
			}
			t.Logf("RecordFiring succeeded; counted firing=%d, notifications=%d", count, len(notifier.msgs))
		})
	}
}
