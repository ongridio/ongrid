package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestListByOrgOmitsOrphansAndKeepsExistingUsers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "iam.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.OrgMembership{}))
	users := []model.User{
		{ID: 1, Email: "admin@example.test", DisplayName: "admin"},
		{ID: 4, Email: "unnamed@example.test"},
		{ID: 5, Email: "disabled@example.test", Status: model.StatusDisabled},
	}
	require.NoError(t, db.Create(&users).Error)
	memberships := []model.OrgMembership{
		{UserID: 1, OrgID: 1, Role: model.MembershipRoleAdmin},
		{UserID: 2, OrgID: 1, Role: model.MembershipRoleMember},
		{UserID: 3, OrgID: 1, Role: model.MembershipRoleMember},
		{UserID: 4, OrgID: 1, Role: model.MembershipRoleMember},
		{UserID: 5, OrgID: 1, Role: model.MembershipRoleViewer},
		{UserID: 2, OrgID: 2, Role: model.MembershipRoleMember},
	}
	require.NoError(t, db.Create(&memberships).Error)

	repo := NewRepo(db)
	rows, err := repo.ListByOrg(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for i, expected := range users {
		require.Equal(t, expected.ID, rows[i].UserID)
		require.Equal(t, expected.Email, rows[i].User.Email)
		require.Equal(t, expected.Status, rows[i].User.Status)
	}
	rows, err = repo.ListByOrg(context.Background(), 2)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = repo.ListByOrg(context.Background(), 3)
	require.NoError(t, err)
	require.Empty(t, rows)
}
