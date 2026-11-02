// Package store is the gorm-backed repo for the IM bridge tables.
// Mirrors the (data) → (biz interface) split used by every other
// manager subsystem.
package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type Repo struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repo { return &Repo{db: db} }

// ---- ImApp -----------------------------------------------------------------

func (r *Repo) ListApps(ctx context.Context, provider string) ([]*model.ImApp, error) {
	tx := r.db.WithContext(ctx).Model(&model.ImApp{})
	if provider != "" {
		tx = tx.Where("provider = ?", provider)
	}
	var out []*model.ImApp
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListEnabledStreamApps returns every (enabled, stream-mode) ImApp
// row, regardless of provider. The StreamSupervisor calls this at
// boot and on every reconcile tick.
func (r *Repo) ListEnabledStreamApps(ctx context.Context) ([]*model.ImApp, error) {
	var out []*model.ImApp
	err := r.db.WithContext(ctx).
		Where("enabled = ? AND mode = ?", true, model.ModeStream).
		Order("id ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) GetApp(ctx context.Context, id uint64) (*model.ImApp, error) {
	var row model.ImApp
	if err := r.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// GetAppByAppID looks up the registered ImApp for a given platform
// app_id — used at the webhook entry point to resolve which app's
// secret should verify the incoming signature.
func (r *Repo) GetAppByAppID(ctx context.Context, provider, appID string) (*model.ImApp, error) {
	var row model.ImApp
	if err := r.db.WithContext(ctx).Where("provider = ? AND app_id = ?", provider, appID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repo) CreateApp(ctx context.Context, app *model.ImApp) error {
	return r.db.WithContext(ctx).Create(app).Error
}

func (r *Repo) UpdateApp(ctx context.Context, app *model.ImApp) error {
	return r.db.WithContext(ctx).Save(app).Error
}

func (r *Repo) DeleteApp(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&model.ImApp{}, id).Error
}

// ---- ImThread --------------------------------------------------------------

// GetOrCreateThread looks up the thread mapping for (im_app_id,
// im_chat_id, im_thread_id). Returns (row, isNew, err). isNew=true
// means the caller should also create a fresh chat_session and
// then set OngridSessionID; we don't do that here because the
// session creation belongs to the aiops biz layer (encapsulates
// owner_user_id resolution, model defaults, etc.).
// FindThread looks up the per-chat session mapping. One session is
// shared by every user in a chat (group / DM); the only differentiator
// past chat_id is the Feishu reply thread_id when a user replies
// inside a thread. ImSenderID is recorded on the row but isn't part
// of the lookup key.
func (r *Repo) FindThread(ctx context.Context, imAppID uint64, imChatID, imThreadID string) (*model.ImThread, error) {
	var row model.ImThread
	q := r.db.WithContext(ctx).Where(
		"im_app_id = ? AND im_chat_id = ? AND im_thread_id = ?",
		imAppID, imChatID, imThreadID,
	)
	if err := q.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repo) CreateThread(ctx context.Context, t *model.ImThread) error {
	return r.db.WithContext(ctx).Create(t).Error
}

func (r *Repo) TouchThread(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.ImThread{}).Where("id = ?", id).Update("last_seen_at", time.Now().UTC()).Error
}

// RotateThreadSession overwrites ongrid_session_id on an existing
// thread mapping — used when the previous session aged out
// (LastSeenAt > IdleTimeout) or the user explicitly asked for a new
// one via /new. Also bumps last_seen_at so the new session doesn't
// immediately re-rotate.
func (r *Repo) RotateThreadSession(ctx context.Context, threadID uint64, newSessionID string) error {
	return r.db.WithContext(ctx).Model(&model.ImThread{}).
		Where("id = ?", threadID).
		Updates(map[string]any{
			"ongrid_session_id": newSessionID,
			"last_seen_at":      time.Now().UTC(),
		}).Error
}
