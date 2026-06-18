package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/marketplace"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewRepo(db)
}

func samplePack(packID string) *model.InstalledPack {
	return &model.InstalledPack{
		TenantID:         0,
		PackID:           packID,
		DisplayName:      packID + " display",
		Version:          "0.1.0",
		Source:           "local",
		SourceURL:        "/tmp/" + packID,
		InstallPath:      "/var/lib/ongrid/system/skills/" + packID,
		ManifestSHA256:   "abcd",
		SignatureState:   model.SigStateUnsigned,
		CapabilitiesJSON: `{"skills":[]}`,
		InstalledBy:      1,
		InstalledAt:      time.Now().UTC(),
	}
}

func TestRepoCreateAndGet(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.GetByPackID(ctx, 0, "etcd-tools"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on empty repo, got %v", err)
	}

	pack := samplePack("etcd-tools")
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByPackID(ctx, 0, "etcd-tools")
	if err != nil {
		t.Fatalf("GetByPackID: %v", err)
	}
	if got.PackID != "etcd-tools" || got.DisplayName != "etcd-tools display" {
		t.Fatalf("unexpected row: %+v", got)
	}
}

func TestRepoCreateDuplicateRejected(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Create(ctx, samplePack("etcd-tools")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := repo.Create(ctx, samplePack("etcd-tools"))
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("duplicate Create error = %v, want ErrConflict", err)
	}
}

func TestRepoGetByManifestSHA(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	p := samplePack("etcd-tools")
	p.ManifestSHA256 = "deadbeef"
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByManifestSHA(ctx, 0, "deadbeef")
	if err != nil {
		t.Fatalf("GetByManifestSHA: %v", err)
	}
	if got.PackID != "etcd-tools" {
		t.Fatalf("got pack_id %q want etcd-tools", got.PackID)
	}
	if _, err := repo.GetByManifestSHA(ctx, 0, "nope"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing sha: err = %v, want ErrNotFound", err)
	}
}

func TestRepoListOrderedByInstalledAtDesc(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	older := samplePack("a-pack")
	older.InstalledAt = time.Now().UTC().Add(-2 * time.Hour)
	if err := repo.Create(ctx, older); err != nil {
		t.Fatalf("Create older: %v", err)
	}
	newer := samplePack("b-pack")
	newer.InstalledAt = time.Now().UTC()
	if err := repo.Create(ctx, newer); err != nil {
		t.Fatalf("Create newer: %v", err)
	}

	items, err := repo.List(ctx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List len = %d, want 2", len(items))
	}
	if items[0].PackID != "b-pack" {
		t.Fatalf("first row should be the newer one (b-pack); got %q", items[0].PackID)
	}
}

func TestRepoDeleteSoft(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Create(ctx, samplePack("etcd-tools")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.DeleteSoft(ctx, 0, "etcd-tools"); err != nil {
		t.Fatalf("DeleteSoft: %v", err)
	}
	if _, err := repo.GetByPackID(ctx, 0, "etcd-tools"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("after delete: GetByPackID err = %v, want ErrNotFound", err)
	}

	// Second delete returns ErrNotFound (not silently OK at this layer —
	// the biz layer is responsible for translating that into the
	// "idempotent uninstall" envelope).
	err := repo.DeleteSoft(ctx, 0, "etcd-tools")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("re-delete err = %v, want ErrNotFound", err)
	}

	// List filters out deleted rows.
	items, err := repo.List(ctx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List after delete = %d, want 0", len(items))
	}

	reinstalled := samplePack("etcd-tools")
	reinstalled.DisplayName = "etcd-tools reinstalled"
	if err := repo.Create(ctx, reinstalled); err != nil {
		t.Fatalf("Create after DeleteSoft: %v", err)
	}
	got, err := repo.GetByPackID(ctx, 0, "etcd-tools")
	if err != nil {
		t.Fatalf("GetByPackID after reinstall: %v", err)
	}
	if got.ID != reinstalled.ID || got.DisplayName != "etcd-tools reinstalled" {
		t.Fatalf("unexpected reinstalled row: %+v", got)
	}
}
