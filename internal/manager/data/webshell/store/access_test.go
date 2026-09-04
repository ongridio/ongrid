package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	bizwebshell "github.com/ongridio/ongrid/internal/manager/biz/webshell"
	webshellstore "github.com/ongridio/ongrid/internal/manager/data/webshell/store"
	wsmodel "github.com/ongridio/ongrid/internal/manager/model/webshell"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestCredentialIsolationEncryptionAndHostPin(t *testing.T) {
	t.Setenv("ONGRID_SECRET_KEY", "")
	t.Setenv("ONGRID_JWT_SECRET", "test-persisted-random-jwt-secret")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := webshellstore.Migrate(db); err != nil {
		t.Fatal(err)
	}
	repo := webshellstore.NewRepo(db)
	access := bizwebshell.NewAccess(repo)
	ctx := context.Background()

	view, err := access.CreateCredential(ctx, 1, 10, "root", "top-secret", 2222)
	if err != nil {
		t.Fatal(err)
	}
	var stored wsmodel.Credential
	if err := db.First(&stored, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PasswordCiphertext == "top-secret" || stored.PasswordCiphertext == "" {
		t.Fatalf("password was not encrypted: %q", stored.PasswordCiphertext)
	}
	if _, err := access.ResolveCredential(ctx, view.ID, 2, 10); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("other user resolved credential: %v", err)
	}
	resolved, err := access.ResolveCredential(ctx, view.ID, 1, 10)
	if err != nil || resolved.Password != "top-secret" || resolved.SSHPort != 2222 {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}

	err = access.VerifyHostKey(ctx, 1, 10, 2222, "SHA256:first", "")
	var hostErr *bizwebshell.HostKeyError
	if !errors.As(err, &hostErr) || hostErr.Kind != "unknown" {
		t.Fatalf("first host key = %v", err)
	}
	if err := access.VerifyHostKey(ctx, 1, 10, 2222, "SHA256:first", "SHA256:first"); err != nil {
		t.Fatal(err)
	}
	err = access.VerifyHostKey(ctx, 1, 10, 2222, "SHA256:changed", "")
	if !errors.As(err, &hostErr) || hostErr.Kind != "changed" {
		t.Fatalf("changed host key = %v", err)
	}
}
