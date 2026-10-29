package externalmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	operation "github.com/ongridio/ongrid/internal/manager/model/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct{ db *gorm.DB }

func New(db *gorm.DB) *Store    { return &Store{db: db} }
func Migrate(db *gorm.DB) error { return db.AutoMigrate(&model.ExecutionLock{}, &model.Preflight{}) }
func (s *Store) Transaction(ctx context.Context, fn func(context.Context) error) error {
	return dbx.Transaction(ctx, s.db, fn)
}

func (s *Store) Accept(ctx context.Context, op *operation.Operation) (*operation.Operation, bool, error) {
	if op.Source != biz.Source || op.TokenID == nil || op.IdempotencyKey == nil || op.CreatedBy == 0 {
		return nil, false, errs.ErrInvalid
	}
	tx := dbx.WithContext(ctx, s.db)
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(op)
	if res.Error != nil {
		return nil, false, res.Error
	}
	var existing operation.Operation
	err := tx.Where("token_id = ? AND idempotency_key = ?", *op.TokenID, *op.IdempotencyKey).First(&existing).Error
	return &existing, existing.ID == op.ID, err
}

func (s *Store) Get(ctx context.Context, id string) (*operation.Operation, error) {
	var op operation.Operation
	err := dbx.WithContext(ctx, s.db).Where("id = ? AND source = ?", id, biz.Source).First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	return &op, err
}

func (s *Store) Update(ctx context.Context, id string, states []string, updates map[string]any) error {
	res := dbx.WithContext(ctx, s.db).Model(&operation.Operation{}).Where("id = ? AND source = ? AND state IN ?", id, biz.Source, states).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrConflict
	}
	return nil
}

func (s *Store) Claim(ctx context.Context, op *operation.Operation, target biz.Target) (bool, error) {
	claimed := false
	err := s.Transaction(ctx, func(txctx context.Context) error {
		tx := dbx.WithContext(txctx, s.db)
		now := time.Now().UTC()
		expiry := now.Add(12 * time.Minute)
		// Expired locks only guard attempts; interrupted writes become unknown
		// and are never automatically replayed.
		if err := tx.Where("expires_at < ?", now).Delete(&model.ExecutionLock{}).Error; err != nil {
			return err
		}
		targetHash := sha256.Sum256([]byte(target.Key))
		for _, key := range []string{"token:" + *op.TokenID, "target:" + hex.EncodeToString(targetHash[:])} {
			lock := model.ExecutionLock{Key: key, OperationID: op.ID, ExpiresAt: expiry}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&lock)
			if res.Error != nil {
				return res.Error
			}
			var current model.ExecutionLock
			if err := tx.Where("`key` = ?", key).First(&current).Error; err != nil {
				return err
			}
			if current.OperationID != op.ID {
				return errs.ErrConflict
			}
		}
		if err := s.Update(txctx, op.ID, []string{operation.OperationStateQueued}, map[string]any{"state": operation.OperationStateRunning, "lease_until": expiry}); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if errors.Is(err, errs.ErrConflict) {
		return false, nil
	}
	return claimed, err
}

func (s *Store) Release(ctx context.Context, id string) error {
	return dbx.WithContext(ctx, s.db).Where("operation_id = ?", id).Delete(&model.ExecutionLock{}).Error
}

func (s *Store) Pending(ctx context.Context) ([]*operation.Operation, error) {
	var ops []*operation.Operation
	err := s.db.WithContext(ctx).Where("source = ? AND (audit_pending = ? OR state IN ? OR (state = ? AND lease_until < ?))", biz.Source, true, []string{operation.OperationStateQueued, operation.OperationStateAwaitingApproval}, operation.OperationStateRunning, time.Now().UTC()).Order("updated_at ASC").Limit(20).Find(&ops).Error
	return ops, err
}

// Keep unresolved/unaudited work. Completed idempotency records last 30 days.
func (s *Store) Sweep(ctx context.Context, now time.Time) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		tx := dbx.WithContext(ctx, s.db)
		var ids []string
		q := tx.Model(&operation.Operation{}).Where("source = ? AND audit_pending = ? AND terminal_at < ? AND state IN ?", biz.Source, false, now.Add(-30*24*time.Hour), []string{operation.OperationStateSucceeded, operation.OperationStateFailed, operation.OperationStateCancelled, operation.OperationStatePartial})
		if err := q.Limit(1000).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := tx.Unscoped().Where("id IN ? AND source = ?", ids, biz.Source).Delete(&operation.Operation{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("expires_at < ?", now).Delete(&model.Preflight{}).Error; err != nil {
			return err
		}
		return tx.Where("expires_at < ?", now).Delete(&model.ExecutionLock{}).Error
	})
}

func (s *Store) IssuePreflight(ctx context.Context, token string, fingerprint []byte, expires time.Time) error {
	i, ok := integrationauth.From(ctx)
	if !ok {
		return errs.ErrForbidden
	}
	hash := sha256.Sum256([]byte(token))
	tx := dbx.WithContext(ctx, s.db)
	if err := tx.Where("expires_at < ?", time.Now().UTC()).Delete(&model.Preflight{}).Error; err != nil {
		return err
	}
	return tx.Create(&model.Preflight{ID: hex.EncodeToString(hash[:]), Fingerprint: fingerprint, OwnerID: i.Owner.UserID, TokenID: i.TokenID, ExpiresAt: expires.UTC()}).Error
}

func (s *Store) ConsumePreflight(ctx context.Context, token string, fingerprint []byte) error {
	i, ok := integrationauth.From(ctx)
	if !ok {
		return errs.ErrForbidden
	}
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	res := dbx.WithContext(ctx, s.db).Model(&model.Preflight{}).Where("id = ? AND fingerprint = ? AND owner_id = ? AND token_id = ? AND expires_at > ? AND consumed_at IS NULL", hex.EncodeToString(hash[:]), fingerprint, i.Owner.UserID, i.TokenID, now).Update("consumed_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errs.ErrInvalid
	}
	return nil
}

func (s *Store) LockTarget(ctx context.Context, target biz.Target) error {
	if target.ID == 0 {
		return nil
	}
	// Table names are selected here; arguments can never supply SQL identifiers.
	table := ""
	switch target.Type {
	case "device":
		table = "devices"
	case "incident":
		table = "alert_incidents"
	case "alert_rule":
		table = "alert_rules"
	case "monitor_panel":
		table = "monitor_panels"
	default:
		return errs.ErrInvalid
	}
	// A no-op UPDATE obtains the write lock on both MySQL and SQLite before
	// business reads validate expected_revision. Concurrent REST writes wait.
	res := dbx.WithContext(ctx, s.db).Table(table).Where("id = ?", target.ID).UpdateColumn("id", gorm.Expr("id"))
	return res.Error
}
