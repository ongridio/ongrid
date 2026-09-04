// Package store backs the webshell audit table.
package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/ongridio/ongrid/internal/manager/model/webshell"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Migrate runs AutoMigrate for the webshell_sessions table.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&webshell.Session{}, &webshell.Credential{}, &webshell.KnownHost{})
}

func (r *Repo) CreateCredential(ctx context.Context, credential *webshell.Credential) error {
	return r.db.WithContext(ctx).Create(credential).Error
}

func (r *Repo) ListCredentials(ctx context.Context, userID, deviceID uint64) ([]*webshell.Credential, error) {
	var out []*webshell.Credential
	err := r.db.WithContext(ctx).
		Where("ongrid_user_id = ? AND device_id = ?", userID, deviceID).
		Order("label asc").Find(&out).Error
	return out, err
}

func (r *Repo) GetCredential(ctx context.Context, id, userID, deviceID uint64) (*webshell.Credential, error) {
	var out webshell.Credential
	err := r.db.WithContext(ctx).
		Where("id = ? AND ongrid_user_id = ? AND device_id = ?", id, userID, deviceID).
		First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	return &out, err
}

func (r *Repo) DeleteCredential(ctx context.Context, id, userID, deviceID uint64) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND ongrid_user_id = ? AND device_id = ?", id, userID, deviceID).
		Delete(&webshell.Credential{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) MarkCredentialUsed(ctx context.Context, id, userID, deviceID uint64, at time.Time) error {
	return r.db.WithContext(ctx).Model(&webshell.Credential{}).
		Where("id = ? AND ongrid_user_id = ? AND device_id = ?", id, userID, deviceID).
		Update("last_used_at", at).Error
}

func (r *Repo) GetKnownHost(ctx context.Context, userID, deviceID uint64, port uint16) (*webshell.KnownHost, error) {
	var out webshell.KnownHost
	err := r.db.WithContext(ctx).
		Where("ongrid_user_id = ? AND device_id = ? AND ssh_port = ?", userID, deviceID, port).
		First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	return &out, err
}

func (r *Repo) CreateKnownHost(ctx context.Context, host *webshell.KnownHost) error {
	return r.db.WithContext(ctx).Create(host).Error
}

func (r *Repo) DeleteKnownHost(ctx context.Context, userID, deviceID uint64, port uint16) error {
	return r.db.WithContext(ctx).
		Where("ongrid_user_id = ? AND device_id = ? AND ssh_port = ?", userID, deviceID, port).
		Delete(&webshell.KnownHost{}).Error
}

// Repo is the GORM-backed audit repo.
type Repo struct{ db *gorm.DB }

// NewRepo wraps a *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Insert persists a freshly-opened session. ID is the manager-generated
// uuid; StartedAt should be set by caller.
func (r *Repo) Insert(ctx context.Context, s *webshell.Session) error {
	return r.db.WithContext(ctx).Create(s).Error
}

// Close updates the audit row with the terminal stats.
func (r *Repo) Close(ctx context.Context, id string, in CloseInput) error {
	res := r.db.WithContext(ctx).Model(&webshell.Session{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"ended_at":      in.EndedAt,
			"bytes_stdin":   in.BytesStdin,
			"bytes_stdout":  in.BytesStdout,
			"exit_code":     in.ExitCode,
			"terminated_by": in.TerminatedBy,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) SetHostFingerprint(ctx context.Context, id, fingerprint string) error {
	return r.db.WithContext(ctx).Model(&webshell.Session{}).
		Where("id = ?", id).Update("host_fingerprint", fingerprint).Error
}

// CloseInput bundles the terminal counters.
type CloseInput struct {
	EndedAt      any
	BytesStdin   uint64
	BytesStdout  uint64
	ExitCode     int
	TerminatedBy string
}

// List returns the most recent N sessions, newest first.
func (r *Repo) List(ctx context.Context, limit int) ([]*webshell.Session, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []*webshell.Session
	if err := r.db.WithContext(ctx).
		Order("started_at desc").Limit(limit).
		Find(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}
