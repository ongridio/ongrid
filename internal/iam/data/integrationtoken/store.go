package integrationtoken

import (
	"context"
	"errors"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

type Store struct{ db *gorm.DB }

func New(db *gorm.DB) *Store    { return &Store{db: db} }
func Migrate(db *gorm.DB) error { return db.AutoMigrate(&model.IntegrationToken{}) }
func (s *Store) Transaction(ctx context.Context, fn func(context.Context) error) error {
	return dbx.Transaction(ctx, s.db, fn)
}
func (s *Store) Create(ctx context.Context, token *model.IntegrationToken) error {
	return dbx.WithContext(ctx, s.db).Create(token).Error
}
func (s *Store) Get(ctx context.Context, publicID string) (*model.IntegrationToken, error) {
	var token model.IntegrationToken
	err := dbx.WithContext(ctx, s.db).Where("public_id = ?", publicID).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	return &token, err
}
func (s *Store) List(ctx context.Context, ownerID uint64, limit, offset int) ([]model.IntegrationToken, int64, error) {
	q := dbx.WithContext(ctx, s.db).Model(&model.IntegrationToken{}).Where("owner_user_id = ? AND revoked_at IS NULL", ownerID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.IntegrationToken
	err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}
func (s *Store) Update(ctx context.Context, ownerID uint64, publicID string, fields map[string]any) error {
	return dbx.WithContext(ctx, s.db).Model(&model.IntegrationToken{}).Where("owner_user_id = ? AND public_id = ? AND revoked_at IS NULL", ownerID, publicID).Updates(fields).Error
}
func (s *Store) Delete(ctx context.Context, ownerID uint64, publicID string) error {
	return dbx.WithContext(ctx, s.db).Where("owner_user_id = ? AND public_id = ?", ownerID, publicID).Delete(&model.IntegrationToken{}).Error
}
