package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

type retryPathNotifier struct {
	byNameCalls int
	viaCalls    int
	senderName  string
}

func (n *retryPathNotifier) Send(context.Context, notify.Message, ...string) error {
	n.byNameCalls++
	return errors.New("notify: channel not configured")
}

func (n *retryPathNotifier) SendVia(_ context.Context, _ notify.Message, sender notify.Sender) error {
	n.viaCalls++
	if sender != nil {
		n.senderName = sender.Name()
	}
	return nil
}

func TestRetryWorkerSucceedsOnRecover(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	// Seed an incident + a failed delivery that has aged past backoff.
	edge := uint64(7)
	incident := &model.Incident{
		DeviceID:     &edge,
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
	repo.channels["log"] = &model.Channel{ID: 7, Name: "log", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"endpoint":"https://example.test/hook"}`}
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
	if len(notifier.channels) != 1 || len(notifier.channels[0]) != 1 || notifier.channels[0][0] != "log" {
		t.Fatalf("retry should use the persisted webhook sender, got channels %#v", notifier.channels)
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

func TestRetryWorkerRetriesDatabaseSMTPChannelViaSender(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	incident := &model.Incident{
		Title:        "host cpu high",
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

	channel := chWithConfig("ops-email", model.ChannelTypeSMTP, map[string]string{
		"smtp":   `{"host":"smtp.example.test","port":587,"from":"alerts@example.test","to":["ops@example.test"],"tls_mode":"starttls"}`,
		"secret": "test-password",
	})
	channel.ID = 7
	channel.Enabled = true
	repo.channels[channel.Name] = channel
	finished := now.Add(-5 * time.Minute)
	failedAt := now.Add(-5 * time.Minute)
	repo.deliveries = append(repo.deliveries, &model.Delivery{
		ID:           1,
		IncidentID:   &incident.ID,
		ChannelID:    channel.ID,
		Status:       model.DeliveryStatusFailed,
		AttemptCount: 1,
		SentAt:       &failedAt,
		FinishedAt:   &finished,
	})

	notifier := &retryPathNotifier{}
	worker := NewRetryWorker(RetryWorkerOpts{
		Repo:              repo,
		Notifier:          notifier,
		Usecase:           NewUsecase(repo, nil),
		MaxAttempts:       5,
		BackoffPerAttempt: time.Minute,
		Now:               func() time.Time { return now },
	})
	worker.RunOnce(context.Background())

	if notifier.byNameCalls != 0 {
		t.Fatalf("database SMTP retry used name lookup %d times", notifier.byNameCalls)
	}
	if notifier.viaCalls != 1 || notifier.senderName != channel.Name {
		t.Fatalf("database SMTP retry did not use its sender: calls=%d name=%q", notifier.viaCalls, notifier.senderName)
	}
	if repo.deliveries[0].Status != model.DeliveryStatusSuccess || repo.deliveries[0].AttemptCount != 2 {
		t.Fatalf("delivery after retry = status %q attempts %d", repo.deliveries[0].Status, repo.deliveries[0].AttemptCount)
	}
	if !hasEventType(repo.events, model.EventTypeNotificationSent) {
		t.Fatal("notification_sent event missing after SMTP retry")
	}
}

func TestRetryWorkerHonorsBackoff(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	edge := uint64(7)
	incident := &model.Incident{
		DeviceID:  &edge,
		Title:     "edge 7 cpu_high",
		ScopeType: model.RuleScopeHost,
		Rule:      "cpu_high",
		Severity:  "warning",
		Status:    model.IncidentStatusOpen,
		DedupeKey: "host:7:cpu_high",
	}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
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
		DeviceID:  &edge,
		ScopeType: model.RuleScopeHost,
		Rule:      "cpu_high",
		Status:    model.IncidentStatusOpen,
		DedupeKey: "host:7:cpu_high",
	}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
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
		DeviceID:   &edge,
		ScopeType:  model.RuleScopeHost,
		Rule:       "cpu_high",
		Status:     model.IncidentStatusResolved,
		DedupeKey:  "host:7:cpu_high",
		ResolvedAt: &resolved,
	}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
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
