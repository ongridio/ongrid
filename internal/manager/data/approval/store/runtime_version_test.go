package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"gorm.io/gorm"
)

func TestRuntimeVersionMigrationKeepsHistoricalApprovalsUnclassified(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "approvals.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(db)
	old := &model.Approval{Kind: "action", Title: "historical", PayloadJSON: "{}"}
	if err := repo.Create(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.Approval{}, "RuntimeVersion"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.Get(context.Background(), old.ID)
	if err != nil || got.RuntimeVersion != "" || got.Status != model.StatusPending {
		t.Fatalf("historical row changed: %+v %v", got, err)
	}
	pinned := &model.Approval{Kind: "action", Title: "new", PayloadJSON: "{}", RuntimeVersion: "v3"}
	if err := repo.Create(context.Background(), pinned); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(context.Background(), pinned.ID)
	if err != nil || got.RuntimeVersion != "v3" {
		t.Fatalf("version not durable: %+v %v", got, err)
	}
}
