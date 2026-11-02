package store

import (
	"context"
	"errors"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (r *Repo) CreateBindingChallenge(ctx context.Context, c *model.BindingChallenge) error {
	c.ExpiresAt = c.ExpiresAt.UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 同一用户/应用仅保留一个有效 challenge，避免无限制造待绑定记录。
		if err := tx.Where("user_id = ? AND app_id = ?", c.UserID, c.AppID).Delete(&model.BindingChallenge{}).Error; err != nil {
			return err
		}
		return tx.Create(c).Error
	})
}
func (r *Repo) GetBindingChallenge(ctx context.Context, id string, user uint64) (*model.BindingChallenge, error) {
	var c model.BindingChallenge
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ? AND consumed = ? AND expires_at > ?", id, user, false, time.Now().UTC()).First(&c).Error
	return &c, interactionError(err)
}
func (r *Repo) ClaimBindingChallenge(ctx context.Context, hash string, app uint64, identity, subjectHash, cipher string) error {
	q := r.db.WithContext(ctx).Model(&model.BindingChallenge{}).Where("code_hash = ? AND app_id = ? AND app_identity = ? AND consumed = ? AND subject_hash = ? AND expires_at > ?", hash, app, identity, false, "", time.Now().UTC()).Updates(map[string]any{"subject_hash": subjectHash, "subject_cipher": cipher})
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected != 1 {
		return errs.ErrNotFound
	}
	return nil
}
func (r *Repo) ConfirmIdentityBinding(ctx context.Context, c *model.BindingChallenge, b *model.IdentityBinding) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.BindingChallenge{}).Where("id = ? AND user_id = ? AND consumed = ? AND subject_hash = ? AND subject_hash <> ? AND expires_at > ?", c.ID, c.UserID, false, c.SubjectHash, "", time.Now().UTC()).Update("consumed", true)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return errs.ErrConflict
		}
		// 不允许覆盖别人的绑定；撤销后才可以重新绑定。
		return tx.Create(b).Error
	})
}
func (r *Repo) FindIdentityBinding(ctx context.Context, app uint64, subject string) (*model.IdentityBinding, error) {
	var b model.IdentityBinding
	err := r.db.WithContext(ctx).Where("app_id = ? AND subject_hash = ?", app, subject).First(&b).Error
	return &b, interactionError(err)
}
func (r *Repo) GetIdentityBinding(ctx context.Context, id string) (*model.IdentityBinding, error) {
	var b model.IdentityBinding
	err := r.db.WithContext(ctx).First(&b, "id = ?", id).Error
	return &b, interactionError(err)
}
func (r *Repo) ListIdentityBindings(ctx context.Context, user uint64) ([]model.IdentityBinding, error) {
	var rows []model.IdentityBinding
	err := r.db.WithContext(ctx).Where("user_id = ?", user).Order("created_at DESC").Limit(100).Find(&rows).Error
	return rows, err
}
func (r *Repo) DeleteIdentityBinding(ctx context.Context, id string, user uint64) error {
	q := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, user).Delete(&model.IdentityBinding{})
	return q.Error
}
func (r *Repo) SaveActionTokens(ctx context.Context, tokens []model.ActionToken) error {
	for n := range tokens {
		tokens[n].ExpiresAt = tokens[n].ExpiresAt.UTC()
	}
	return r.db.WithContext(ctx).Create(&tokens).Error
}
func (r *Repo) BindActionMessage(ctx context.Context, hashes []string, messageID string) error {
	return r.db.WithContext(ctx).Model(&model.ActionToken{}).Where("hash IN ? AND message_id = ?", hashes, "").Update("message_id", messageID).Error
}
func (r *Repo) GetActionToken(ctx context.Context, hash string) (*model.ActionToken, error) {
	var t model.ActionToken
	err := r.db.WithContext(ctx).First(&t, "hash = ? AND expires_at > ?", hash, time.Now().UTC()).Error
	return &t, interactionError(err)
}
func (r *Repo) QueueDecision(ctx context.Context, c *model.DecisionCommand) error {
	q := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(c)
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected == 0 {
		return errs.ErrConflict
	}
	return nil
}
func (r *Repo) ClaimDecision(ctx context.Context) (*model.DecisionCommand, error) {
	var c model.DecisionCommand
	if err := r.db.WithContext(ctx).Where("state = ?", "queued").Order("created_at").First(&c).Error; err != nil {
		return nil, interactionError(err)
	}
	q := r.db.WithContext(ctx).Model(&model.DecisionCommand{}).Where("approval_id = ? AND state = ?", c.ApprovalID, "queued").Update("state", "running")
	if q.Error != nil {
		return nil, q.Error
	}
	if q.RowsAffected != 1 {
		return nil, errs.ErrConflict
	}
	return &c, nil
}
func (r *Repo) FinishDecision(ctx context.Context, id, outcome string) error {
	return r.db.WithContext(ctx).Model(&model.DecisionCommand{}).Where("approval_id = ? AND state = ?", id, "running").Updates(map[string]any{"state": "done", "outcome": outcome}).Error
}
func (r *Repo) CompactInteractions(ctx context.Context) error {
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.BindingChallenge{}).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.ActionToken{}).Error
}
func interactionError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.ErrNotFound
	}
	return err
}
