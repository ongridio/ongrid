package imbridge

import (
	"context"
	"errors"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func (r *deliveryRepo) CompactApprovalNotifications(context.Context, time.Time) error { return nil }

type deliveryRepo struct {
	target model.ApprovalTarget
	app    model.ImApp
}

func (r *deliveryRepo) SaveApprovalTarget(context.Context, *model.ApprovalTarget) error { return nil }
func (r *deliveryRepo) GetApprovalTarget(context.Context, string) (*model.ApprovalTarget, error) {
	return &r.target, nil
}
func (r *deliveryRepo) GetApp(context.Context, uint64) (*model.ImApp, error)    { return &r.app, nil }
func (r *deliveryRepo) SyncApprovalNotifications(context.Context, uint64) error { return nil }
func (r *deliveryRepo) ClaimApprovalNotifications(context.Context, time.Time) ([]model.ApprovalNotification, error) {
	return nil, nil
}
func (r *deliveryRepo) FinishApprovalNotification(context.Context, model.ApprovalNotification) error {
	return nil
}
func TestApprovalDeliveryRetriesAndFollowsTerminalStatus(t *testing.T) {
	for _, provider := range []string{"slack", "telegram", "feishu", "dingtalk"} {
		t.Run(provider, func(t *testing.T) {
			repo := &deliveryRepo{target: model.ApprovalTarget{ChatID: "original", Locale: "en", Provider: provider}, app: model.ImApp{Enabled: true, Provider: provider}}
			reader := &noticeStatus{value: "pending"}
			sender := &failingDeliverySender{fail: true}
			factory := func(*model.ImApp, *model.ApprovalTarget, string) (Sender, error) { return sender, nil }
			d := NewApprovalDelivery(repo, reader, 1, "https://ongrid.example", map[string]ApprovalSenderFactory{provider: factory}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			job := model.ApprovalNotification{ApprovalID: "a1", SessionID: "session"}
			d.deliver(context.Background(), &job)
			if job.Attempts != 1 || job.DeliveredStatus != "" || !job.NextAt.After(time.Now()) {
				t.Fatalf("retry not scheduled: %+v", job)
			}
			sender.fail = false
			d.deliver(context.Background(), &job)
			if job.DeliveredStatus != "pending" || job.Done || job.Attempts != 0 {
				t.Fatalf("bad pending state: %+v", job)
			}
			d.deliver(context.Background(), &job)
			if sender.sent != 2 {
				t.Fatal("unchanged status resent")
			}
			reader.value = "executed"
			// Fresh worker simulates restart: existing message/status are supplied from DB.
			d = NewApprovalDelivery(repo, reader, 1, "https://ongrid.example", map[string]ApprovalSenderFactory{provider: factory}, d.log)
			d.deliver(context.Background(), &job)
			if !job.Done || job.DeliveredStatus != "executed" || !strings.Contains(sender.text, "Executed") {
				t.Fatalf("no terminal delivery: %+v", job)
			}
		})
	}
}

type failingDeliverySender struct {
	fail bool
	sent int
	text string
}

func (s *failingDeliverySender) SendText(_ context.Context, chatID, _, text string) (string, error) {
	if chatID != "original" {
		return "", errors.New("wrong conversation")
	}
	s.sent++
	s.text = text
	if s.fail {
		return "", errors.New("platform rejected https://credential-must-not-be-logged")
	}
	return "message", nil
}
func TestApprovalDeliveryDisabledAndExpiredTargetsNeverSend(t *testing.T) {
	repo := &deliveryRepo{target: model.ApprovalTarget{ChatID: "original", Provider: "dingtalk"}, app: model.ImApp{Provider: "dingtalk"}}
	sender := &failingDeliverySender{}
	d := NewApprovalDelivery(repo, &noticeStatus{value: "pending"}, 1, "", map[string]ApprovalSenderFactory{"dingtalk": func(*model.ImApp, *model.ApprovalTarget, string) (Sender, error) { return sender, nil }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	job := model.ApprovalNotification{ApprovalID: "a1"}
	d.deliver(context.Background(), &job)
	if job.LastError != "app_disabled" || sender.sent != 0 {
		t.Fatal("disabled app sent notification")
	}
	repo.app.Enabled = true
	expired := time.Now().Add(-time.Second)
	repo.target.ExpiresAt = &expired
	d.deliver(context.Background(), &job)
	if job.LastError != "reply_context_expired" || sender.sent != 0 {
		t.Fatal("expired webhook used")
	}
}

type editFailureSender struct {
	failingDeliverySender
	err error
}

func (s *editFailureSender) EditText(context.Context, string, string) error { return s.err }
func TestApprovalDeliveryHonorsRateLimitAndReplacesMissingMessage(t *testing.T) {
	for _, kind := range []string{"rate", "missing", "permanent"} {
		t.Run(kind, func(t *testing.T) {
			repo := &deliveryRepo{target: model.ApprovalTarget{ChatID: "original", Provider: "slack"}, app: model.ImApp{Provider: "slack", Enabled: true}}
			e := &DeliveryError{Code: "rate_limited", RetryAfter: 3 * time.Minute}
			if kind == "missing" {
				e = &DeliveryError{Code: "cannot_edit", CannotEdit: true}
			}
			if kind == "permanent" {
				e = &DeliveryError{Code: "forbidden", Permanent: true}
			}
			sender := &editFailureSender{err: e}
			d := NewApprovalDelivery(repo, &noticeStatus{value: "executed"}, 1, "", map[string]ApprovalSenderFactory{"slack": func(*model.ImApp, *model.ApprovalTarget, string) (Sender, error) { return sender, nil }}, nil)
			n := model.ApprovalNotification{ApprovalID: "a", MessageID: "old", DeliveredStatus: "pending"}
			d.deliver(context.Background(), &n)
			if n.Done {
				t.Fatal("failed update marked delivered")
			}
			if kind == "missing" {
				if n.MessageID != "" {
					t.Fatal("missing message retained")
				}
				d.deliver(context.Background(), &n)
				if !n.Done || n.MessageID != "message" {
					t.Fatal("replacement not sent")
				}
			} else {
				if kind == "rate" && time.Until(n.NextAt) < 170*time.Second {
					t.Fatal("Retry-After ignored")
				}
				if kind == "permanent" && time.Until(n.NextAt) < 59*time.Minute {
					t.Fatal("permanent error hot loop")
				}
			}
		})
	}
}

// 已失效审批只更新原卡片，不在最新消息处重新发一张，也不再刷新待审批 token。
func TestApprovalExpiryEditsOriginalNativeCard(t *testing.T) {
	for _, provider := range []string{"feishu", "slack", "telegram", "dingtalk"} {
		t.Run(provider, func(t *testing.T) {
			repo := &deliveryRepo{target: model.ApprovalTarget{ChatID: "original", Provider: provider}, app: model.ImApp{Enabled: true, Provider: provider}}
			sender := &terminalCardSender{t: t}
			d := NewApprovalDelivery(repo, &noticeStatus{value: "failed"}, 1, "", map[string]ApprovalSenderFactory{provider: func(*model.ImApp, *model.ApprovalTarget, string) (Sender, error) { return sender, nil }}, nil)
			job := model.ApprovalNotification{ApprovalID: "expired", MessageID: "original-card", NativeCard: true, DeliveredStatus: "pending"}
			d.deliver(context.Background(), &job)
			if !job.Done || job.DeliveredStatus != "failed" || job.MessageID != "original-card" || sender.edits != 1 {
				t.Fatalf("expiry delivery: %+v edits=%d", job, sender.edits)
			}
			d.deliver(context.Background(), &job)
			if sender.edits != 1 {
				t.Fatal("terminal card refreshed")
			}
		})
	}
}

type terminalCardSender struct {
	t     *testing.T
	edits int
}

func (s *terminalCardSender) SendText(context.Context, string, string, string) (string, error) {
	s.t.Error("unexpected new text message")
	return "", errors.New("unexpected new message")
}
func (s *terminalCardSender) SendApproval(context.Context, ApprovalCard) (string, error) {
	s.t.Error("unexpected new approval card")
	return "", errors.New("unexpected new card")
}
func (s *terminalCardSender) EditApproval(_ context.Context, id string, card ApprovalCard) error {
	if id != "original-card" || card.Buttons.Approve != "" || card.Buttons.Reject != "" {
		s.t.Errorf("invalid terminal card: %s %+v", id, card)
	}
	s.edits++
	return nil
}
