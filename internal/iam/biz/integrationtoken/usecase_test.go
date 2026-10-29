package integrationtoken_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	biz "github.com/ongridio/ongrid/internal/iam/biz/integrationtoken"
	store "github.com/ongridio/ongrid/internal/iam/data/integrationtoken"
	users "github.com/ongridio/ongrid/internal/iam/data/user/sqlite"
	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type auditor struct{ fail bool }

func (a *auditor) Record(context.Context, auditctx.Event) error {
	if a.fail {
		return errors.New("audit unavailable")
	}
	return nil
}

func TestLifecycleAndAuthority(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "iam.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, store.Migrate(db))
	require.NoError(t, store.Migrate(db))
	user := &model.User{Email: "admin@example.test", Role: model.RoleAdmin, Status: model.StatusActive}
	require.NoError(t, db.Create(user).Error)
	audit := &auditor{}
	repo := store.New(db)
	uc := biz.New(repo, users.NewRepo(db), audit)
	in := biz.CreateInput{Name: "WorkBuddy", Scopes: []string{"devices:read", "devices:write"}}
	token, raw, err := uc.Create(ctx, user.ID, in)
	require.NoError(t, err)
	require.Nil(t, token.ExpiresAt)
	require.Len(t, raw, 103)
	row, err := repo.Get(ctx, token.PublicID)
	require.NoError(t, err)
	require.Len(t, row.SecretHash, 32)
	require.NotContains(t, string(row.SecretHash), strings.Split(raw, ".")[1])
	identity, err := uc.Authenticate(ctx, raw)
	require.NoError(t, err)
	require.True(t, identity.Allows("devices:read", "devices:write"))
	require.False(t, identity.Allows("devices:delete"))
	_, err = uc.Authenticate(ctx, raw[:len(raw)-1]+"!")
	require.ErrorIs(t, err, errs.ErrUnauthorized)
	_, err = uc.Authenticate(ctx, "eyJ.login.jwt")
	require.ErrorIs(t, err, errs.ErrUnauthorized)
	_, err = uc.Authenticate(ctx, "ogmcp_missing")
	require.ErrorIs(t, err, errs.ErrUnauthorized)
	_, _, err = uc.List(ctx, user.ID, 20, 0)
	require.NoError(t, err)
	require.ErrorIs(t, uc.Delete(ctx, user.ID+1, token.PublicID), errs.ErrUnauthorized)
	other := &model.User{Email: "other@example.test", Role: model.RoleAdmin, Status: model.StatusActive}
	require.NoError(t, db.Create(other).Error)
	require.ErrorIs(t, uc.Delete(ctx, other.ID, token.PublicID), errs.ErrNotFound)
	_, err = uc.Update(ctx, other.ID, token.PublicID, in)
	require.ErrorIs(t, err, errs.ErrNotFound)
	_, err = uc.Update(ctx, user.ID, token.PublicID, biz.CreateInput{Name: "invalid", Scopes: []string{"devices:delete"}})
	require.ErrorIs(t, err, errs.ErrInvalid)
	expiry := time.Now().Add(time.Hour)
	updated, err := uc.Update(ctx, user.ID, token.PublicID, biz.CreateInput{Name: "Edited client", Scopes: []string{"devices:read", "devices:delete"}, ExpiresAt: &expiry})
	require.NoError(t, err)
	require.Equal(t, "Edited client", updated.Name)
	require.Equal(t, token.PublicID, updated.PublicID)
	require.NotNil(t, updated.ExpiresAt)
	current, err := uc.Authenticate(ctx, raw)
	require.NoError(t, err)
	require.True(t, current.Allows("devices:delete"))
	require.False(t, current.Allows("devices:write"))
	_, err = uc.Revalidate(ctx, identity)
	require.ErrorIs(t, err, errs.ErrForbidden)
	stored, err := repo.Get(ctx, token.PublicID)
	require.NoError(t, err)
	require.Equal(t, row.SecretHash, stored.SecretHash)
	_, err = uc.Update(ctx, user.ID, token.PublicID, in)
	require.NoError(t, err)
	stored, err = repo.Get(ctx, token.PublicID)
	require.NoError(t, err)
	require.Nil(t, stored.ExpiresAt)
	audit.fail = true
	_, err = uc.Update(ctx, user.ID, token.PublicID, biz.CreateInput{Name: "must roll back", Scopes: []string{"devices:read"}})
	require.Error(t, err)
	audit.fail = false
	stored, err = repo.Get(ctx, token.PublicID)
	require.NoError(t, err)
	require.Equal(t, in.Name, stored.Name)
	current, err = uc.Authenticate(ctx, raw)
	require.NoError(t, err)
	require.True(t, current.Allows("devices:write"))
	legacy, legacyRaw, err := uc.Create(ctx, user.ID, in)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.IntegrationToken{}).Where("public_id = ?", legacy.PublicID).Update("revoked_at", time.Now()).Error)
	items, total, err := uc.List(ctx, user.ID, 20, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 1, total)
	_, err = uc.Update(ctx, user.ID, legacy.PublicID, in)
	require.ErrorIs(t, err, errs.ErrNotFound)
	_, err = uc.Authenticate(ctx, legacyRaw)
	require.ErrorIs(t, err, errs.ErrUnauthorized)
	require.NoError(t, uc.Delete(ctx, user.ID, legacy.PublicID))
	require.NoError(t, db.Model(user).Update("role", model.RoleUser).Error)
	_, err = uc.Authenticate(ctx, raw)
	require.ErrorIs(t, err, errs.ErrForbidden)
	_, err = uc.Revalidate(ctx, identity)
	require.ErrorIs(t, err, errs.ErrForbidden)
	require.NoError(t, db.Model(user).Update("role", model.RoleAdmin).Error)
	require.NoError(t, db.Model(user).Update("status", model.StatusDisabled).Error)
	_, err = uc.Authenticate(ctx, raw)
	require.ErrorIs(t, err, errs.ErrForbidden)
	require.NoError(t, db.Model(user).Update("status", model.StatusActive).Error)
	require.NoError(t, uc.Delete(ctx, user.ID, token.PublicID))
	require.NoError(t, uc.Delete(ctx, user.ID, token.PublicID))
	_, err = repo.Get(ctx, token.PublicID)
	require.ErrorIs(t, err, errs.ErrNotFound)
	items, total, err = uc.List(ctx, user.ID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, total)
	_, err = uc.Authenticate(ctx, raw)
	require.ErrorIs(t, err, errs.ErrUnauthorized)
	_, err = uc.Revalidate(ctx, identity)
	require.ErrorIs(t, err, errs.ErrUnauthorized)

	for _, scope := range []string{"*", "devices:delete", "host:execute", "unknown:read"} {
		_, _, err := uc.Create(ctx, user.ID, biz.CreateInput{Name: "invalid", Scopes: []string{scope}})
		require.ErrorIs(t, err, errs.ErrInvalid)
	}
	past := time.Now().Add(-time.Hour)
	in.ExpiresAt = &past
	_, _, err = uc.Create(ctx, user.ID, in)
	require.ErrorIs(t, err, errs.ErrInvalid)
	future := time.Now().AddDate(2, 0, 0)
	in.ExpiresAt = &future
	token, raw, err = uc.Create(ctx, user.ID, in)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.IntegrationToken{}).Where("public_id = ?", token.PublicID).Update("expires_at", past).Error)
	_, err = uc.Authenticate(ctx, raw)
	require.ErrorIs(t, err, errs.ErrUnauthorized)
	audit.fail = true
	_, raw, err = uc.Create(ctx, user.ID, in)
	require.Error(t, err)
	require.Empty(t, raw)
}
