package alert

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// RetryWorkerOpts wires the retry worker. MaxAttempts caps the per-delivery
// attempt budget (default 5); BackoffPerAttempt scales the linear minimum
// gap between retries (default 1m, so retry N happens at least N*backoff
// after the first attempt). Tick is the loop frequency (default 30s).
type RetryWorkerOpts struct {
	Repo              Repo
	Notifier          Notifier
	Resolver          ChannelResolver
	Usecase           *Usecase
	MaxAttempts       uint32
	BackoffPerAttempt time.Duration
	Tick              time.Duration
	Log               *slog.Logger
	Now               func() time.Time
}

// RetryWorker drains failed notification_deliveries, re-runs the underlying
// channel send, and updates the row to success / further-failed. It is the
// "delivery_tracking" loop.
type RetryWorker struct {
	repo              Repo
	notifier          Notifier
	resolver          ChannelResolver
	uc                *Usecase
	maxAttempts       uint32
	backoffPerAttempt time.Duration
	tick              time.Duration
	log               *slog.Logger
	now               func() time.Time
}

// NewRetryWorker builds the worker with sensible defaults applied.
func NewRetryWorker(opts RetryWorkerOpts) *RetryWorker {
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 5
	}
	if opts.BackoffPerAttempt <= 0 {
		opts.BackoffPerAttempt = time.Minute
	}
	if opts.Tick <= 0 {
		opts.Tick = 30 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &RetryWorker{
		repo:              opts.Repo,
		notifier:          opts.Notifier,
		resolver:          opts.Resolver,
		uc:                opts.Usecase,
		maxAttempts:       opts.MaxAttempts,
		backoffPerAttempt: opts.BackoffPerAttempt,
		tick:              opts.Tick,
		log:               opts.Log,
		now:               opts.Now,
	}
}

// Loop runs the worker until ctx is cancelled. Per-cycle errors are logged
// but never propagated — the loop only returns on ctx.Done().
func (w *RetryWorker) Loop(ctx context.Context) error {
	if w.repo == nil || w.notifier == nil {
		return nil
	}
	tick := time.NewTicker(w.tick)
	defer tick.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			w.runOnce(ctx)
		}
	}
}

// RunOnce drains all retriable deliveries in a single pass — exposed for
// tests so they don't need to drive a real ticker.
func (w *RetryWorker) RunOnce(ctx context.Context) { w.runOnce(ctx) }

func (w *RetryWorker) runOnce(ctx context.Context) {
	now := w.now()
	rows, err := w.repo.ListRetriableDeliveries(ctx, w.maxAttempts, now.Add(-w.backoffPerAttempt), 200)
	if err != nil {
		w.log.Warn("retry: list failed deliveries", slog.Any("err", err))
		return
	}
	for _, d := range rows {
		w.retryOne(ctx, d, now)
	}
}

// retryOne performs a single retry attempt. The strategy is conservative:
//  1. Resolve incident + channel rows.
//  2. If the incident is already resolved, mark the delivery success
//     (operator no longer needs the notify).
//  3. Otherwise rebuild the notify.Message and the typed sender from the
//     persisted channel, then deliver through notifier.SendVia. This mirrors
//     the initial delivery path and avoids resolving DB channels against the
//     environment-configured router by name.
//  4. Update the delivery with attempt_count++ and the new outcome.
func (w *RetryWorker) retryOne(ctx context.Context, d *model.Delivery, now time.Time) {
	if d == nil || d.IncidentID == nil {
		return
	}
	// Linear backoff: skip rows whose finished_at is younger than
	// attempt_count * backoffPerAttempt.
	if d.FinishedAt != nil {
		minWait := time.Duration(d.AttemptCount) * w.backoffPerAttempt
		if now.Sub(*d.FinishedAt) < minWait {
			return
		}
	}
	incident, err := w.repo.GetIncidentByID(ctx, *d.IncidentID)
	if err != nil {
		w.log.Warn("retry: incident lookup failed",
			slog.Uint64("delivery_id", d.ID),
			slog.Any("err", err),
		)
		return
	}
	if incident.Status == model.IncidentStatusResolved {
		// No point retrying — operator's question is moot.
		finished := now
		if err := w.repo.UpdateDeliveryStatus(ctx, d.ID, model.DeliveryStatusSuccess, d.AttemptCount+1, nil, nil, ptrString("incident resolved before retry"), &finished, &finished); err != nil {
			w.log.Warn("retry: mark resolved delivery finished",
				slog.Uint64("delivery_id", d.ID),
				slog.Any("err", err),
			)
		}
		return
	}
	channel, err := w.repo.GetChannelByID(ctx, d.ChannelID)
	if err != nil {
		w.log.Warn("retry: channel lookup failed",
			slog.Uint64("delivery_id", d.ID),
			slog.Uint64("channel_id", d.ChannelID),
			slog.Any("err", err),
		)
		return
	}
	if !channel.Enabled {
		// Operator disabled the channel — keep the row failed but stop
		// retrying by burning the budget.
		finished := now
		errMsg := fmt.Sprintf("channel %q disabled", channel.Name)
		if err := w.repo.UpdateDeliveryStatus(ctx, d.ID, model.DeliveryStatusFailed, w.maxAttempts, nil, nil, &errMsg, d.SentAt, &finished); err != nil {
			w.log.Warn("retry: exhaust disabled channel delivery",
				slog.Uint64("delivery_id", d.ID),
				slog.Any("err", err),
			)
		}
		return
	}

	msg := buildIncidentMessage(incident, now)
	sentAt := now
	finished := now
	sender, buildErr := BuildSenderFromChannel(channel)
	var sendErr error
	if buildErr != nil {
		sendErr = buildErr
	} else {
		sendErr = w.notifier.SendVia(ctx, msg, sender)
	}
	status := model.DeliveryStatusSuccess
	var errMsg *string
	eventType := model.EventTypeNotificationSent
	if sendErr != nil {
		status = model.DeliveryStatusFailed
		s := sendErr.Error()
		errMsg = &s
		eventType = model.EventTypeNotificationFailed
	}
	if err := w.repo.UpdateDeliveryStatus(ctx, d.ID, status, d.AttemptCount+1, nil, nil, errMsg, &sentAt, &finished); err != nil {
		w.log.Warn("retry: update delivery failed", slog.Uint64("delivery_id", d.ID), slog.Any("err", err))
	}
	reason := channel.Name
	if sendErr != nil {
		reason = fmt.Sprintf("%s: retry attempt %d: %s", channel.Name, d.AttemptCount+1, sendErr.Error())
	} else {
		reason = fmt.Sprintf("%s: retry attempt %d", channel.Name, d.AttemptCount+1)
	}
	if err := w.repo.CreateEvent(ctx, &model.Event{
		IncidentID:  *d.IncidentID,
		EventType:   eventType,
		StatusAfter: incident.Status,
		Severity:    incident.Severity,
		Title:       incident.Title,
		ActorType:   model.ActorTypeSystem,
		Reason:      reason,
		OccurredAt:  now,
	}); err == nil {
		// Mirror Usecase.createEvent: feed alert_events_total so retry-
		// path notification_sent / notification_failed events show up
		// in the same counter the first-attempt path does.
		prom.IncAlertEvent(eventType, incident.Severity, incident.Rule)
	}
	if sendErr == nil {
		_ = w.repo.MarkIncidentNotified(ctx, *d.IncidentID, now)
	}
}

func buildIncidentMessage(incident *model.Incident, now time.Time) notify.Message {
	severity := notify.Severity(incident.Severity)
	if severity == "" {
		severity = notify.SeverityWarning
	}
	labels := map[string]string{
		"rule":        incident.Rule,
		"incident_id": fmt.Sprintf("%d", incident.ID),
	}
	if incident.DeviceID != nil {
		labels["device_id"] = fmt.Sprintf("%d", *incident.DeviceID)
	}
	subject := incident.Summary
	if subject == "" {
		subject = incident.Title
	}
	occurredAt := incident.LastFiredAt
	if occurredAt.IsZero() {
		occurredAt = now
	}
	return notify.Message{
		Subject:    subject,
		Severity:   severity,
		Source:     incident.ScopeType,
		DedupeKey:  incident.DedupeKey,
		OccurredAt: occurredAt,
		Labels:     labels,
	}
}

func ptrString(s string) *string { return &s }
