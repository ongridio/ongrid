package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed membership repository.
type Repo struct {
	db *gorm.DB
}

// NewRepo wraps a *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Upsert creates or updates a membership; returns the resulting row.
// Idempotent so the boot seeder can call it safely.
func (r *Repo) Upsert(ctx context.Context, userID, orgID uint64, role string) (*model.OrgMembership, error) {
	var existing model.OrgMembership
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND org_id = ?", userID, orgID).
		First(&existing).Error
	if err == nil {
		// Update if role changed.
		if existing.Role != role {
			if err := r.db.WithContext(ctx).Model(&existing).Update("role", role).Error; err != nil {
				return nil, err
			}
			existing.Role = role
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Insert.
	row := &model.OrgMembership{UserID: userID, OrgID: orgID, Role: role}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// Delete removes the membership by (user, org). ErrNotFound when no row.
func (r *Repo) Delete(ctx context.Context, userID, orgID uint64) error {
	res := r.db.WithContext(ctx).
		Where("user_id = ? AND org_id = ?", userID, orgID).
		Delete(&model.OrgMembership{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// DeleteByOrg removes every membership in an org. Used when the org
// itself is deleted.
func (r *Repo) DeleteByOrg(ctx context.Context, orgID uint64) error {
	return r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Delete(&model.OrgMembership{}).Error
}

// DeleteByUser removes every membership a user has. Used when the
// user is deleted.
func (r *Repo) DeleteByUser(ctx context.Context, userID uint64) error {
	return r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Delete(&model.OrgMembership{}).Error
}

// ListByOrg returns every membership in the given org with the
// embedded user pre-joined.
type MembershipWithUser = model.MembershipWithUser

// ListByOrg lists memberships joined to user rows.
func (r *Repo) ListByOrg(ctx context.Context, orgID uint64) ([]MembershipWithUser, error) {
	var ms []model.OrgMembership
	if err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Order("id asc").
		Find(&ms).Error; err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, nil
	}
	userIDs := make([]uint64, 0, len(ms))
	for _, m := range ms {
		userIDs = append(userIDs, m.UserID)
	}
	var users []model.User
	if err := r.db.WithContext(ctx).Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint64]model.User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	out := make([]MembershipWithUser, 0, len(ms))
	for _, m := range ms {
		out = append(out, MembershipWithUser{OrgMembership: m, User: byID[m.UserID]})
	}
	return out, nil
}

// ListByUser returns every org the user is a member of, with the
// embedded org row.
type MembershipWithOrg = model.MembershipWithOrg

// ListByUser lists memberships joined to org rows.
func (r *Repo) ListByUser(ctx context.Context, userID uint64) ([]MembershipWithOrg, error) {
	var ms []model.OrgMembership
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("id asc").
		Find(&ms).Error; err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, nil
	}
	orgIDs := make([]uint64, 0, len(ms))
	for _, m := range ms {
		orgIDs = append(orgIDs, m.OrgID)
	}
	var orgs []model.Org
	if err := r.db.WithContext(ctx).Where("id IN ?", orgIDs).Find(&orgs).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint64]model.Org, len(orgs))
	for _, o := range orgs {
		byID[o.ID] = o
	}
	out := make([]MembershipWithOrg, 0, len(ms))
	for _, m := range ms {
		out = append(out, MembershipWithOrg{OrgMembership: m, Org: byID[m.OrgID]})
	}
	return out, nil
}

// All returns every membership row. Used by the Authorizer at boot to
// hydrate casbin g policies from the truth table.
func (r *Repo) All(ctx context.Context) ([]model.OrgMembership, error) {
	var out []model.OrgMembership
	if err := r.db.WithContext(ctx).Order("id asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
