package imbridge

import (
	"context"
	"errors"
	"fmt"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
	"log/slog"
	"math/rand/v2"
	"time"
)

// ApprovalDeliveryRepo owns only durable routing and delivery state.
type ApprovalDeliveryRepo interface {
	SaveApprovalTarget(context.Context, *model.ApprovalTarget) error
	GetApprovalTarget(context.Context, string) (*model.ApprovalTarget, error)
	SyncApprovalNotifications(context.Context, uint64) error
	CompactApprovalNotifications(context.Context, time.Time) error
	ClaimApprovalNotifications(context.Context, time.Time) ([]model.ApprovalNotification, error)
	FinishApprovalNotification(context.Context, model.ApprovalNotification) error
	GetApp(context.Context, uint64) (*model.ImApp, error)
}

// ApprovalSenderContext is optional for expiring provider reply credentials.
type ApprovalDestination interface {
	ApprovalDestination() (space, callbackChat string)
}
type ApprovalSenderContext interface{ ApprovalReplyContext() (string, time.Time) }
type ApprovalSenderFactory func(*model.ImApp, *model.ApprovalTarget, string) (Sender, error)

type ApprovalRateGate interface {
	DeliveryNotBefore(context.Context, uint64) (time.Time, error)
	DelayAppDelivery(context.Context, uint64, time.Time) error
}

type ApprovalDelivery struct {
	metrics     *DeliveryMetrics
	interaction *Interaction
	repo        ApprovalDeliveryRepo
	reader      ApprovalStatusReader
	factories   map[string]ApprovalSenderFactory // immutable after construction
	owner       uint64
	publicURL   string
	log         *slog.Logger
}

func NewApprovalDelivery(repo ApprovalDeliveryRepo, reader ApprovalStatusReader, owner uint64, publicURL string, factories map[string]ApprovalSenderFactory, log *slog.Logger) *ApprovalDelivery {
	if log == nil {
		log = slog.Default()
	}
	copied := make(map[string]ApprovalSenderFactory, len(factories))
	for k, v := range factories {
		copied[k] = v
	}
	return &ApprovalDelivery{repo: repo, reader: reader, owner: owner, publicURL: publicURL, factories: copied, log: log}
}
func WithApprovalDelivery(delivery *ApprovalDelivery) BridgeOption {
	return func(b *Bridge) { b.approvalDelivery = delivery }
}

func (d *ApprovalDelivery) register(ctx context.Context, app *model.ImApp, thread *model.ImThread, sender Sender, receiveIDType string) error {
	target := &model.ApprovalTarget{ConnectionIdentity: ConnectionIdentity(app), SessionID: thread.OngridSessionID, Provider: app.Provider, AppIdentifier: app.AppID, AppID: app.ID, ThreadID: thread.ID, ChatID: thread.ImChatID, ReceiveIDType: receiveIDType, Locale: app.DefaultLocale}
	target.PlatformThreadID = thread.ImThreadID
	if destination, ok := sender.(ApprovalDestination); ok {
		target.DeliverySpace, target.CallbackChatID = destination.ApprovalDestination()
	}
	if source, ok := sender.(ApprovalSenderContext); ok {
		raw, expires := source.ApprovalReplyContext()
		if secretbox.KeyIsWeak() {
			return fmt.Errorf("durable IM reply requires a configured encryption key")
		}
		if raw == "" || !expires.After(time.Now()) {
			return fmt.Errorf("IM reply context is missing or expired")
		}
		encrypted, err := secretbox.Encrypt(raw)
		if err != nil {
			return fmt.Errorf("encrypt IM reply context: %w", err)
		}
		target.SecretContext = encrypted
		target.ExpiresAt = &expires
	}
	return d.repo.SaveApprovalTarget(ctx, target)
}
func (d *ApprovalDelivery) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		d.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Keep a malformed provider response from terminating the Manager process.
func (d *ApprovalDelivery) reconcile(ctx context.Context) {
	defer func() {
		if recover() != nil {
			d.log.Error("IM approval delivery panic; unfinished leases will expire")
		}
	}()
	if err := d.tick(ctx); err != nil && ctx.Err() == nil {
		d.log.Warn("IM approval delivery reconciliation failed", slog.Any("err", err))
	}
}

func (d *ApprovalDelivery) tick(ctx context.Context) error {
	scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if d.metrics != nil {
		if err := d.metrics.refresh(scanCtx, d.repo); err != nil {
			d.log.Warn("IM delivery metrics refresh failed", "err", err)
		}
	}
	err := d.repo.CompactApprovalNotifications(scanCtx, time.Now().UTC())
	if err == nil {
		err = d.repo.SyncApprovalNotifications(scanCtx, d.owner)
	}
	cancel()
	if err != nil {
		return err
	}
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	jobs, err := d.repo.ClaimApprovalNotifications(claimCtx, time.Now().UTC())
	cancel()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		jobCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		before := job.DeliveredStatus
		d.deliver(jobCtx, &job)
		if d.metrics != nil {
			provider := "unknown"
			if t, e := d.repo.GetApprovalTarget(jobCtx, job.SessionID); e == nil {
				switch t.Provider {
				case "slack", "telegram", "feishu", "dingtalk":
					provider = t.Provider
				}
			}
			if job.LastError != "" {
				d.metrics.attempts.WithLabelValues(provider, "deferred").Inc()
			} else if before != job.DeliveredStatus {
				d.metrics.attempts.WithLabelValues(provider, "delivered").Inc()
			}
		}
		cancel()
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := d.repo.FinishApprovalNotification(finishCtx, job)
		finishCancel()
		if err != nil {
			return fmt.Errorf("save IM delivery outcome: %w", err)
		}
	}
	return nil
}
func (d *ApprovalDelivery) deliver(ctx context.Context, n *model.ApprovalNotification) {
	now := time.Now().UTC()
	n.NextAt = now.Add(5 * time.Second)
	fail := func(code string) {
		n.Attempts++
		n.LastError = code
		delay := time.Duration(1<<min(n.Attempts, 9))*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
		if n.Attempts >= 20 {
			delay = time.Hour
		}
		n.NextAt = now.Add(delay)
		if n.Attempts == 1 || n.Attempts == 20 {
			d.log.Warn("IM approval notification delayed", slog.String("approval_id", n.ApprovalID), slog.String("reason", code))
		}
	}
	target, err := d.repo.GetApprovalTarget(ctx, n.SessionID)
	if err != nil {
		fail("target_unavailable")
		return
	}
	app, err := d.repo.GetApp(ctx, target.AppID)
	if err != nil {
		fail("app_unavailable")
		return
	}
	if app.Provider != target.Provider || app.AppID != target.AppIdentifier || (target.ConnectionIdentity != "" && target.ConnectionIdentity != ConnectionIdentity(app)) {
		fail("app_identity_changed")
		n.NextAt = now.Add(time.Hour)
		return
	}
	if !app.Enabled {
		fail("app_disabled")
		n.NextAt = now.Add(time.Minute)
		return
	}
	if gate, ok := d.repo.(ApprovalRateGate); ok {
		until, gateErr := gate.DeliveryNotBefore(ctx, app.ID)
		if gateErr != nil {
			fail("rate_gate_unavailable")
			return
		}
		if until.After(now) {
			n.NextAt = until
			return
		}
	}
	status, err := d.reader.ApprovalStatus(ctx, n.SessionID, n.ApprovalID)
	if err != nil {
		fail("approval_unavailable")
		return
	}
	switch status {
	case "pending", "approved", "executed", "rejected", "failed":
	default:
		fail("unknown_status")
		return
	}
	if status == n.DeliveredStatus && !(n.NativeCard && status == "pending" && (n.RefreshAt == nil || !n.RefreshAt.After(now))) {
		n.Attempts = 0
		n.LastError = ""
		return
	}
	if target.ExpiresAt != nil && !target.ExpiresAt.After(now) && !(app.Provider == "dingtalk" && app.ApprovalTemplateID != "" && target.DeliverySpace != "") {
		fail("reply_context_expired")
		n.NextAt = now.Add(time.Minute)
		return
	}
	raw := ""
	if target.SecretContext != "" {
		raw, err = secretbox.Decrypt(target.SecretContext)
		if err != nil {
			fail("reply_context_unavailable")
			return
		}
	}
	factory := d.factories[app.Provider]
	if factory == nil {
		fail("provider_unsupported")
		return
	}
	sender, err := factory(app, target, raw)
	if err != nil {
		fail("sender_unavailable")
		return
	}
	text := (&streamEditor{locale: target.Locale, publicURL: d.publicURL}).approvalText(approvalNotice{id: n.ApprovalID, status: status, kind: n.ActionKind})
	if cards, ok := sender.(ApprovalCardSender); ok && (n.NativeCard || (d.interaction != nil && d.interaction.Enabled(app))) {
		card := ApprovalCard{Locale: target.Locale, Text: text, ReviewURL: approvalLink(d.publicURL, n.ApprovalID)}
		if d.interaction != nil {
			card, err = d.interaction.ReviewCard(ctx, app, target, n.ApprovalID)
			if err != nil {
				fail("card_review_unavailable")
				return
			}
		}
		if n.NativeCard && n.MessageID != "" {
			err = cards.EditApproval(ctx, n.MessageID, card)
		} else {
			n.MessageID, err = cards.SendApproval(ctx, card)
		}
		if err == nil && d.interaction != nil {
			err = d.interaction.BindMessage(ctx, card.Buttons, n.MessageID)
		}
		if err == nil {
			n.NativeCard = true
			refreshAt := now.Add(25 * time.Minute)
			n.RefreshAt = &refreshAt
		}
	} else if editor, ok := sender.(MessageEditor); ok && n.MessageID != "" {
		err = editor.EditText(ctx, n.MessageID, text)
	} else {
		n.MessageID, err = sender.SendText(ctx, target.ChatID, target.ReceiveIDType, text)
	}
	var platformErr *DeliveryError
	if errors.As(err, &platformErr) && platformErr.CannotEdit && n.MessageID != "" {
		n.MessageID = ""
		n.NativeCard = false
		n.DeliveredStatus = ""
		n.LastError = "message_not_editable"
		n.NextAt = now.Add(5 * time.Second)
		return
	}
	if err != nil {
		fail("platform_delivery_failed")
		if errors.As(err, &platformErr) {
			n.LastError = platformErr.Code
			if platformErr.Permanent {
				n.NextAt = now.Add(time.Hour)
			}
			if platformErr.RetryAfter > 0 {
				n.NextAt = now.Add(platformErr.RetryAfter)
				if gate, ok := d.repo.(ApprovalRateGate); ok {
					if gateErr := gate.DelayAppDelivery(ctx, app.ID, n.NextAt); gateErr != nil {
						d.log.Warn("persist IM rate gate failed", "err", gateErr)
					}
				}
			}
		}
		return
	} // Provider errors can contain bearer webhook URLs; never log them.
	n.DeliveredStatus = status
	n.Attempts = 0
	n.LastError = ""
	n.Done = status == "executed" || status == "rejected" || status == "failed"
}

// SetInteraction 仅在启动投递器之前装配。
func (d *ApprovalDelivery) SetInteraction(i *Interaction) { d.interaction = i }
