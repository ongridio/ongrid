package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	biz "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (r *Repo) SaveApprovalTarget(ctx context.Context, target *model.ApprovalTarget) error {
	// Freeze target identity. A later message may renew the same target's expiring
	// credential, but may never retarget an old session to another conversation.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(target).Error; err != nil {
			return err
		}
		var stored model.ApprovalTarget
		if err := tx.First(&stored, "session_id = ?", target.SessionID).Error; err != nil {
			return err
		}
		if stored.ConnectionIdentity != target.ConnectionIdentity || stored.AppID != target.AppID || stored.ThreadID != target.ThreadID || stored.ChatID != target.ChatID || stored.Provider != target.Provider || stored.AppIdentifier != target.AppIdentifier || stored.PlatformThreadID != target.PlatformThreadID || stored.DeliverySpace != target.DeliverySpace || stored.CallbackChatID != target.CallbackChatID {
			return errs.ErrConflict
		}
		return tx.Model(&model.ApprovalTarget{}).Where("session_id = ?", target.SessionID).Updates(map[string]any{"secret_context": target.SecretContext, "expires_at": target.ExpiresAt}).Error
	})
}
func (r *Repo) GetApprovalTarget(ctx context.Context, sessionID string) (*model.ApprovalTarget, error) {
	var row model.ApprovalTarget
	if err := r.db.WithContext(ctx).First(&row, "session_id = ?", sessionID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// SyncApprovalNotifications repairs an event-to-projection crash window. The
// durable target is saved before the run, so old/foreign sessions never fan out.
func (r *Repo) SyncApprovalNotifications(ctx context.Context, owner uint64) error {
	var rows []struct{ ID, SessionID, Kind string }
	err := r.db.WithContext(ctx).Table("approvals a").Select("a.id, a.session_id, a.kind").
		Joins("JOIN im_approval_targets t ON t.session_id = a.session_id").
		Joins("LEFT JOIN im_approval_notifications n ON n.approval_id = a.id").
		Where("a.proposed_by = ? AND a.created_at >= t.created_at AND n.approval_id IS NULL", owner).
		Order("a.created_at ASC").Limit(100).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("find missing IM approval deliveries: %w", err)
	}
	for _, row := range rows {
		n := model.ApprovalNotification{ApprovalID: row.ID, SessionID: row.SessionID, ActionKind: row.Kind, NextAt: time.Now().UTC()}
		if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&n).Error; err != nil {
			return err
		}
	}
	return nil
}
func (r *Repo) ClaimApprovalNotifications(ctx context.Context, now time.Time) ([]model.ApprovalNotification, error) {
	now = now.UTC()
	var candidates []model.ApprovalNotification
	if err := r.db.WithContext(ctx).Where("done = ? AND next_at <= ? AND (lease_until IS NULL OR lease_until < ?)", false, now, now).Order("next_at ASC").Limit(5).Find(&candidates).Error; err != nil {
		return nil, err
	}
	out := make([]model.ApprovalNotification, 0, len(candidates))
	for _, n := range candidates {
		n.LeaseOwner = uuid.NewString()
		leaseUntil := now.UTC().Add(2 * time.Minute)
		n.LeaseUntil = &leaseUntil
		res := r.db.WithContext(ctx).Model(&model.ApprovalNotification{}).Where("approval_id = ? AND done = ? AND next_at <= ? AND (lease_until IS NULL OR lease_until < ?)", n.ApprovalID, false, now, now).Updates(map[string]any{"lease_owner": n.LeaseOwner, "lease_until": n.LeaseUntil})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			var claimed model.ApprovalNotification
			if err := r.db.WithContext(ctx).Where("approval_id = ? AND lease_owner = ?", n.ApprovalID, n.LeaseOwner).First(&claimed).Error; err != nil {
				return nil, err
			}
			out = append(out, claimed)
		}
	}
	return out, nil
}
func (r *Repo) FinishApprovalNotification(ctx context.Context, n model.ApprovalNotification) error {
	res := r.db.WithContext(ctx).Model(&model.ApprovalNotification{}).Where("approval_id = ? AND lease_owner = ?", n.ApprovalID, n.LeaseOwner).
		Updates(map[string]any{"native_card": n.NativeCard, "refresh_at": n.RefreshAt, "message_id": n.MessageID, "delivered_status": n.DeliveredStatus, "attempts": n.Attempts, "next_at": n.NextAt.UTC(), "done": n.Done, "last_error": n.LastError, "lease_owner": "", "lease_until": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errs.ErrConflict
	}
	return nil
}

// Retain small terminal tombstones to prevent the repair scan replaying history.
// Message references are no longer needed after 30 days; expired reply secrets
// are cleared independently, without touching the approvals audit table.
func (r *Repo) CompactApprovalNotifications(ctx context.Context, now time.Time) error {
	if err := r.db.WithContext(ctx).Model(&model.ApprovalTarget{}).Where("expires_at <= ? AND secret_context <> ?", now, "").UpdateColumn("secret_context", "").Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.ApprovalNotification{}).Where("done = ? AND updated_at < ? AND message_id <> ?", true, now.Add(-30*24*time.Hour), "").UpdateColumn("message_id", "").Error
}

func (r *Repo) DeliveryNotBefore(ctx context.Context, app uint64) (time.Time, error) {
	var gate model.DeliveryGate
	err := r.db.WithContext(ctx).Where("app_id = ?", app).Take(&gate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, nil
	}
	return gate.NotBefore, err
}
func (r *Repo) DelayAppDelivery(ctx context.Context, app uint64, until time.Time) error {
	gate := model.DeliveryGate{AppID: app, NotBefore: until.UTC()}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&gate).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.DeliveryGate{}).Where("app_id = ? AND not_before < ?", app, until.UTC()).Update("not_before", until.UTC()).Error
}

func (r *Repo) ApprovalDeliveryStats(ctx context.Context) ([]biz.DeliveryStats, error) {
	var rows []biz.DeliveryStats
	err := r.db.WithContext(ctx).Table("im_approval_notifications n").Select("t.provider, COUNT(*) AS backlog").Joins("JOIN im_approval_targets t ON t.session_id = n.session_id").Where("n.done = ? AND (n.delivered_status = ? OR n.last_error <> ?)", false, "", "").Group("t.provider").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for j := range rows {
		var oldest model.ApprovalNotification
		err = r.db.WithContext(ctx).Table("im_approval_notifications n").Select("n.created_at").Joins("JOIN im_approval_targets t ON t.session_id = n.session_id").Where("t.provider = ? AND n.done = ? AND (n.delivered_status = ? OR n.last_error <> ?)", rows[j].Provider, false, "", "").Order("n.created_at ASC").Limit(1).Scan(&oldest).Error
		if err != nil {
			return nil, err
		}
		if !oldest.CreatedAt.IsZero() {
			rows[j].Oldest = &oldest.CreatedAt
		}
	}
	return rows, nil
}
