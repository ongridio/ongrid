package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/iam/biz/user"
	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed biz/user.Repo. Construct via NewRepo.
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repo around an opened *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// compile-time interface check.
var _ biz.Repo = (*Repo)(nil)

// Create inserts u and writes back the generated ID / timestamps.
func (r *Repo) Create(ctx context.Context, u *model.User) error {
	if u == nil {
		return fmt.Errorf("%w: nil user", errs.ErrInvalid)
	}
	return dbx.WithContext(ctx, r.db).Create(u).Error
}

// GetByEmail returns the user matching email or errs.ErrNotFound.
func (r *Repo) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	if err := dbx.WithContext(ctx, r.db).Where("email = ?", email).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// GetByID returns the user matching id or errs.ErrNotFound.
func (r *Repo) GetByID(ctx context.Context, id uint64) (*model.User, error) {
	var u model.User
	if err := dbx.WithContext(ctx, r.db).First(&u, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// List returns every user ordered by id asc.
func (r *Repo) List(ctx context.Context) ([]*model.User, error) {
	var out []*model.User
	if err := dbx.WithContext(ctx, r.db).Order("id asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Count returns the total number of users.
func (r *Repo) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := dbx.WithContext(ctx, r.db).Model(&model.User{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// Delete removes a user by id. A missing row maps to errs.ErrNotFound.
func (r *Repo) Delete(ctx context.Context, id uint64) error {
	res := dbx.WithContext(ctx, r.db).Delete(&model.User{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateRole sets role on the row with id=id. Missing row -> ErrNotFound.
func (r *Repo) UpdateRole(ctx context.Context, id uint64, role string) error {
	res := dbx.WithContext(ctx, r.db).Model(&model.User{}).Where("id = ?", id).Update("role", role)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateProfile sets display_name and phone. Missing row -> ErrNotFound.
func (r *Repo) UpdateProfile(ctx context.Context, id uint64, displayName, phone string) error {
	res := dbx.WithContext(ctx, r.db).Model(&model.User{}).Where("id = ?", id).Updates(map[string]any{
		"display_name": displayName,
		"phone":        phone,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateStatus flips active/disabled. Missing row -> ErrNotFound.
func (r *Repo) UpdateStatus(ctx context.Context, id uint64, status string) error {
	res := dbx.WithContext(ctx, r.db).Model(&model.User{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateSuperuser toggles is_superuser. ErrNotFound on missing row.
func (r *Repo) UpdateSuperuser(ctx context.Context, id uint64, isSuperuser bool) error {
	res := dbx.WithContext(ctx, r.db).Model(&model.User{}).Where("id = ?", id).Update("is_superuser", isSuperuser)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdatePassHash sets the argon2id-encoded password hash. ErrNotFound
// on missing row.
func (r *Repo) UpdatePassHash(ctx context.Context, id uint64, passHash string) error {
	res := dbx.WithContext(ctx, r.db).Model(&model.User{}).Where("id = ?", id).Update("pass_hash", passHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
