package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const unboundAttachmentTTL = 24 * time.Hour

func (r *SessionRepo) CreateAttachment(ctx context.Context, sessionID string, userID uint64, name, mimeType string, size int64, src io.Reader) (*model.Attachment, error) {
	if sessionID == "" || userID == 0 || size <= 0 || src == nil {
		return nil, errs.ErrInvalid
	}
	a := &model.Attachment{SessionID: sessionID, UserID: userID, Name: name, MIMEType: mimeType, Size: size}
	_ = a.BeforeCreate(nil)
	dir := filepath.Join(r.attachmentRoot, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}
	a.StoragePath = filepath.Join(dir, a.ID)
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("create attachment temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, copyErr := io.CopyN(tmp, src, size+1)
	closeErr := tmp.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return nil, fmt.Errorf("write attachment: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close attachment: %w", closeErr)
	}
	if written != size {
		return nil, errs.ErrInvalid
	}
	if err := os.Rename(tmpName, a.StoragePath); err != nil {
		return nil, fmt.Errorf("store attachment: %w", err)
	}
	expires := time.Now().UTC().Add(unboundAttachmentTTL)
	a.ExpiresAt = &expires
	if err := r.db.WithContext(ctx).Create(a).Error; err != nil {
		_ = os.Remove(a.StoragePath)
		return nil, err
	}
	return a, nil
}

func (r *SessionRepo) GetAttachment(ctx context.Context, sessionID, attachmentID string, userID uint64, admin bool) (*model.Attachment, error) {
	var a model.Attachment
	tx := r.db.WithContext(ctx).Where("id = ? AND session_id = ?", attachmentID, sessionID)
	if !admin {
		tx = tx.Where("user_id = ?", userID)
	}
	if err := tx.First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	if a.MessageID == nil && a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now().UTC()) {
		return nil, errs.ErrNotFound
	}
	data, err := os.ReadFile(a.StoragePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	a.Data = data
	return &a, nil
}

func (r *SessionRepo) ResolveAttachments(ctx context.Context, sessionID string, ids []string, userID uint64, admin bool) ([]model.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]model.Attachment, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return nil, errs.ErrInvalid
		}
		seen[id] = struct{}{}
		a, err := r.GetAttachment(ctx, sessionID, id, userID, admin)
		if err != nil {
			return nil, err
		}
		if a.MessageID != nil || (a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now().UTC())) {
			return nil, errs.ErrInvalid
		}
		out = append(out, *a)
	}
	return out, nil
}

func (r *SessionRepo) hydrateAttachments(ctx context.Context, messages []*model.Message) error {
	byID := make(map[string]*model.Message, len(messages))
	ids := make([]string, 0, len(messages))
	for _, m := range messages {
		byID[m.ID] = m
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []model.Attachment
	if err := r.db.WithContext(ctx).Where("message_id IN ?", ids).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		a := rows[i]
		data, err := os.ReadFile(a.StoragePath)
		if err != nil {
			return fmt.Errorf("read attachment %s: %w", a.ID, err)
		}
		a.Data = data
		if a.MessageID != nil {
			if m := byID[*a.MessageID]; m != nil {
				m.Attachments = append(m.Attachments, a)
			}
		}
	}
	return nil
}

func (r *SessionRepo) DeleteSessionAttachments(ctx context.Context, sessionID string) error {
	var rows []model.Attachment
	if err := r.db.WithContext(ctx).Where("session_id = ?", sessionID).Find(&rows).Error; err != nil {
		return err
	}
	for _, a := range rows {
		if err := os.Remove(a.StoragePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete attachment %s: %w", a.ID, err)
		}
	}
	if err := r.db.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.Attachment{}).Error; err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(r.attachmentRoot, sessionID))
	return nil
}

func (r *SessionRepo) CleanupExpiredAttachments(ctx context.Context, now time.Time) (int, error) {
	_ = os.RemoveAll(filepath.Join(r.attachmentRoot, ".trash"))
	var rows []model.Attachment
	if err := r.db.WithContext(ctx).Where("message_id IS NULL AND expires_at <= ?", now).Find(&rows).Error; err != nil {
		return 0, err
	}
	removed := 0
	for _, a := range rows {
		if err := os.Remove(a.StoragePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := r.db.WithContext(ctx).Delete(&a).Error; err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
