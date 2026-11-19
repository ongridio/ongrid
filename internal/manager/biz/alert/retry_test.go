package alert

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

func TestRetryWorkerSucceedsOnRecover(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	// Seed an incident + a failed delivery that has aged past backoff.
	edge := uint64(7)
	incident := &model.Incident{
		DeviceID:       &edge,
		Title:        "edge 7 cpu_high",
		ScopeType:    model.RuleScopeHost,
		Rule:         "cpu_high",
		Severity:     "warning",
		Status:       model.IncidentStatusOpen,
		DedupeKey:    "host:7:cpu_high",
		Summary:      "CPU spike",
		FirstFiredAt: now.Add(-10 * time.Minute),
		LastFiredAt:  now.Add(-time.Minute),
	}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	repo.channels["log"] = &model.Channel{ID: 7, Name: "log", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"endpoint":"https://hooks.example.test/ongrid"}`}
	finished := now.Add(-5 * time.Minute)
	failedAt := now.Add(-5 * time.Minute)
	repo.deliveries = append(repo.deliveries, &model.Delivery{
		ID:           1,
		IncidentID:   &incident.ID,
		ChannelID:    7,
		Status:       model.DeliveryStatusFailed,
		AttemptCount: 1,
		SentAt:       &failedAt,
		FinishedAt:   &finished,
	})

	notifier := &fakeNotifier{}
	worker := NewRetryWorker(RetryWorkerOpts{
		Repo:              repo,
		Notifier:          notifier,
		Usecase:           NewUsecase(repo, nil),
		MaxAttempts:       5,
		BackoffPerAttempt: time.Minute,
		Tick:              time.Hour, // unused
		Now:               func() time.Time { return now },
	})
	worker.RunOnce(context.Background())

	if len(notifier.msgs) != 1 {
		t.Fatalf("retry should have sent once, got %d", len(notifier.msgs))
	}
	if repo.deliveries[0].Status != model.DeliveryStatusSuccess {
		t.Errorf("delivery status = %q, want success", repo.deliveries[0].Status)
	}
	if repo.deliveries[0].AttemptCount != 2 {
		t.Errorf("attempt_count = %d, want 2", repo.deliveries[0].AttemptCount)
	}
	if !hasEventType(repo.events, model.EventTypeNotificationSent) {
		t.Errorf("notification_sent event missing on retry")
	}
}

func TestRetryWorkerSendsThroughPersistedChannelConfig(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		channelType string
		withURL     bool
		// staleEnvSender registers a router sender with the channel's name
		// pointing elsewhere, like a seeded channel edited after boot.
		staleEnvSender bool
		wantStatus     string
		wantHits       int32
		wantErr        string
	}{
		{name: "ui webhook", channelType: model.ChannelTypeWebhook, withURL: true, wantStatus: model.DeliveryStatusSuccess, wantHits: 1},
		{name: "ui wecom", channelType: model.ChannelTypeWeCom, withURL: true, wantStatus: model.DeliveryStatusSuccess, wantHits: 1},
		{name: "edited seeded channel", channelType: model.ChannelTypeWebhook, withURL: true, staleEnvSender: true, wantStatus: model.DeliveryStatusSuccess, wantHits: 1},
		{name: "no endpoint", channelType: model.ChannelTypeWebhook, wantStatus: model.DeliveryStatusFailed, wantErr: "has no endpoint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits, staleHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
			}))
			defer srv.Close()
			stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				staleHits.Add(1)
			}))
			defer stale.Close()

			repo := newFakeRepo()
			incident := &model.Incident{
				Title:       "disk full",
				ScopeType:   model.RuleScopeGlobal,
				Rule:        "disk_full",
				Severity:    "critical",
				Status:      model.IncidentStatusOpen,
				DedupeKey:   "global:disk_full",
				Summary:     "disk full",
				LastFiredAt: now.Add(-time.Minute),
			}
			if err := repo.CreateIncident(context.Background(), incident); err != nil {
				t.Fatalf("seed incident: %v", err)
			}
			configJSON := `{}`
			if tc.withURL {
				configJSON = fmt.Sprintf(`{"endpoint":%q}`, srv.URL)
			}
			repo.channels["ops"] = &model.Channel{ID: 9, Name: "ops", ChannelType: tc.channelType, Enabled: true, ConfigJSON: configJSON}
			finished := now.Add(-5 * time.Minute)
			repo.deliveries = append(repo.deliveries, &model.Delivery{
				ID:           1,
				IncidentID:   &incident.ID,
				ChannelID:    9,
				Status:       model.DeliveryStatusFailed,
				AttemptCount: 1,
				SentAt:       &finished,
				FinishedAt:   &finished,
			})

			var envSenders []notify.Sender
			if tc.staleEnvSender {
				envSenders = append(envSenders, notify.NewGenericWebhookSender("ops", stale.URL, "", nil))
			}
			worker := NewRetryWorker(RetryWorkerOpts{
				Repo:              repo,
				Notifier:          notify.NewRouter(true, time.Second, nil, envSenders...),
				Usecase:           NewUsecase(repo, nil),
				MaxAttempts:       5,
				BackoffPerAttempt: time.Minute,
				Now:               func() time.Time { return now },
			})
			worker.RunOnce(context.Background())

			d := repo.deliveries[0]
			gotErr := ""
			if d.ErrorMessage != nil {
				gotErr = *d.ErrorMessage
			}
			if d.Status != tc.wantStatus {
				t.Fatalf("delivery status = %q (error %q), want %q", d.Status, gotErr, tc.wantStatus)
			}
			if d.AttemptCount != 2 {
				t.Errorf("attempt_count = %d, want 2", d.AttemptCount)
			}
			if tc.wantErr != "" && !strings.Contains(gotErr, tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", gotErr, tc.wantErr)
			}
			if got := hits.Load(); got != tc.wantHits {
				t.Errorf("channel endpoint hits = %d, want %d", got, tc.wantHits)
			}
			if got := staleHits.Load(); got != 0 {
				t.Errorf("stale env-config endpoint hits = %d, want 0", got)
			}
		})
	}
}

func TestRetryWorkerHonorsBackoff(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	edge := uint64(7)
	incident := &model.Incident{
		DeviceID:    &edge,
		Title:     "edge 7 cpu_high",
		ScopeType: model.RuleScopeHost,
		Rule:      "cpu_high",
		Severity:  "warning",
		Status:    model.IncidentStatusOpen,
		DedupeKey: "host:7:cpu_high",
	}
	_ = repo.CreateIncident(context.Background(), incident)
	repo.channels["log"] = &model.Channel{ID: 7, Name: "log", ChannelType: model.ChannelTypeWebhook, Enabled: true}
	finished := now.Add(-30 * time.Second)
	failedAt := now.Add(-30 * time.Second)
	repo.deliveries = append(repo.deliveries, &model.Delivery{
		ID:           1,
		IncidentID:   &incident.ID,
		ChannelID:    7,
		Status:       model.DeliveryStatusFailed,
		AttemptCount: 1,
		SentAt:       &failedAt,
		FinishedAt:   &finished,
	})
	notifier := &fakeNotifier{}
	worker := NewRetryWorker(RetryWorkerOpts{
		Repo:              repo,
		Notifier:          notifier,
		Usecase:           NewUsecase(repo, nil),
		MaxAttempts:       5,
		BackoffPerAttempt: time.Minute,
		Now:               func() time.Time { return now },
	})

	// finished 30s ago < 1*backoff(60s) -> the global query already filters
	// by the upper bound; but even when the row sneaks through (older
	// schemas), retryOne's per-row backoff guard rejects it.
	worker.RunOnce(context.Background())
	if len(notifier.msgs) != 0 {
		t.Errorf("backoff should suppress retry, got %d sends", len(notifier.msgs))
	}
}

func TestRetryWorkerStopsAtMaxAttempts(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	edge := uint64(7)
	incident := &model.Incident{
		DeviceID:    &edge,
		ScopeType: model.RuleScopeHost,
		Rule:      "cpu_high",
		Status:    model.IncidentStatusOpen,
		DedupeKey: "host:7:cpu_high",
	}
	_ = repo.CreateIncident(context.Background(), incident)
	repo.channels["log"] = &model.Channel{ID: 7, Name: "log", Enabled: true}
	finished := now.Add(-time.Hour)
	repo.deliveries = append(repo.deliveries, &model.Delivery{
		ID:           1,
		IncidentID:   &incident.ID,
		ChannelID:    7,
		Status:       model.DeliveryStatusFailed,
		AttemptCount: 5, // already at max
		FinishedAt:   &finished,
	})
	notifier := &fakeNotifier{fail: true}
	worker := NewRetryWorker(RetryWorkerOpts{
		Repo:              repo,
		Notifier:          notifier,
		Usecase:           NewUsecase(repo, nil),
		MaxAttempts:       5,
		BackoffPerAttempt: time.Minute,
		Now:               func() time.Time { return now },
	})
	worker.RunOnce(context.Background())
	if len(notifier.msgs) != 0 {
		t.Errorf("worker must not retry beyond MaxAttempts")
	}
	if repo.deliveries[0].AttemptCount != 5 {
		t.Errorf("attempt_count should stay at 5, got %d", repo.deliveries[0].AttemptCount)
	}
}

func TestRetryWorkerSkipsResolvedIncident(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	resolved := now.Add(-2 * time.Minute)
	edge := uint64(7)
	incident := &model.Incident{
		DeviceID:     &edge,
		ScopeType:  model.RuleScopeHost,
		Rule:       "cpu_high",
		Status:     model.IncidentStatusResolved,
		DedupeKey:  "host:7:cpu_high",
		ResolvedAt: &resolved,
	}
	_ = repo.CreateIncident(context.Background(), incident)
	repo.channels["log"] = &model.Channel{ID: 7, Name: "log", Enabled: true}
	finished := now.Add(-5 * time.Minute)
	repo.deliveries = append(repo.deliveries, &model.Delivery{
		ID:           1,
		IncidentID:   &incident.ID,
		ChannelID:    7,
		Status:       model.DeliveryStatusFailed,
		AttemptCount: 1,
		FinishedAt:   &finished,
	})
	notifier := &fakeNotifier{}
	worker := NewRetryWorker(RetryWorkerOpts{
		Repo:              repo,
		Notifier:          notifier,
		Usecase:           NewUsecase(repo, nil),
		MaxAttempts:       5,
		BackoffPerAttempt: time.Minute,
		Now:               func() time.Time { return now },
	})
	worker.RunOnce(context.Background())
	if len(notifier.msgs) != 0 {
		t.Errorf("resolved incident should not trigger notify on retry")
	}
	if repo.deliveries[0].Status != model.DeliveryStatusSuccess {
		t.Errorf("delivery should be marked success when incident resolved, got %q", repo.deliveries[0].Status)
	}
}
