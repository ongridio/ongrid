package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func userTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "iam.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.OrgMembership{}))
	return db
}

func TestDeleteRemovesOnlyUsersMemberships(t *testing.T) {
	ctx := context.Background()
	db := userTestDB(t)
	repo := NewRepo(db)
	deleted := &model.User{Email: "deleted@example.test"}
	retained := &model.User{Email: "retained@example.test"}
	require.NoError(t, repo.Create(ctx, deleted))
	require.NoError(t, repo.Create(ctx, retained))
	memberships := []model.OrgMembership{
		{UserID: deleted.ID, OrgID: 1, Role: model.MembershipRoleMember},
		{UserID: deleted.ID, OrgID: 2, Role: model.MembershipRoleViewer},
		{UserID: retained.ID, OrgID: 1, Role: model.MembershipRoleAdmin},
	}
	require.NoError(t, db.Create(&memberships).Error)

	require.NoError(t, repo.Delete(ctx, deleted.ID))
	_, err := repo.GetByID(ctx, deleted.ID)
	require.ErrorIs(t, err, errs.ErrNotFound)
	_, err = repo.GetByID(ctx, retained.ID)
	require.NoError(t, err)
	var remaining []model.OrgMembership
	require.NoError(t, db.Order("id").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	require.Equal(t, memberships[2].ID, remaining[0].ID)
	require.Equal(t, retained.ID, remaining[0].UserID)
	require.Equal(t, model.MembershipRoleAdmin, remaining[0].Role)
	require.ErrorIs(t, repo.Delete(ctx, deleted.ID), errs.ErrNotFound)

	withoutMemberships := &model.User{Email: "unassigned@example.test"}
	require.NoError(t, repo.Create(ctx, withoutMemberships))
	require.NoError(t, repo.Delete(ctx, withoutMemberships.ID))
}

func TestDeleteRollsBackWhenMembershipCleanupFails(t *testing.T) {
	ctx := context.Background()
	db := userTestDB(t)
	repo := NewRepo(db)
	user := &model.User{Email: "retained@example.test"}
	require.NoError(t, repo.Create(ctx, user))
	membership := &model.OrgMembership{UserID: user.ID, OrgID: 1, Role: model.MembershipRoleMember}
	require.NoError(t, db.Create(membership).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_membership_delete BEFORE DELETE ON org_memberships
		BEGIN SELECT RAISE(ABORT, 'membership cleanup failed'); END`).Error)

	require.ErrorContains(t, repo.Delete(ctx, user.ID), "membership cleanup failed")
	_, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Model(&model.OrgMembership{}).Where("user_id = ?", user.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestDeleteParticipatesInCallerTransaction(t *testing.T) {
	ctx := context.Background()
	db := userTestDB(t)
	repo := NewRepo(db)
	user := &model.User{Email: "retained@example.test"}
	require.NoError(t, repo.Create(ctx, user))
	require.NoError(t, db.Create(&model.OrgMembership{UserID: user.ID, OrgID: 1, Role: model.MembershipRoleMember}).Error)
	failure := errors.New("later operation failed")

	err := dbx.Transaction(ctx, db, func(tx context.Context) error {
		if err := repo.Delete(tx, user.ID); err != nil {
			return err
		}
		var count int64
		require.NoError(t, dbx.WithContext(tx, db).Model(&model.OrgMembership{}).Where("user_id = ?", user.ID).Count(&count).Error)
		require.Zero(t, count)
		return failure
	})
	require.ErrorIs(t, err, failure)
	_, err = repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Model(&model.OrgMembership{}).Where("user_id = ?", user.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
