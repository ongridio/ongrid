package store

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
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

func TestSettingRepoSetAndGet(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Get(ctx, "llm", "openai_api_key"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on empty store, got %v", err)
	}

	if _, err := repo.Set(ctx, "llm", "openai_api_key", "sk-aaaaaaaa", true); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := repo.Get(ctx, "llm", "openai_api_key")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got.Value != "sk-aaaaaaaa" {
		t.Fatalf("Value = %q, want %q", got.Value, "sk-aaaaaaaa")
	}
	if !got.Sensitive {
		t.Fatalf("Sensitive = false, want true")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt zero on freshly inserted row")
	}

	// Upsert path: same (cat, key) updates value & flips sensitive.
	if _, err := repo.Set(ctx, "llm", "openai_api_key", "sk-bbbbbbbb", false); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	got2, err := repo.Get(ctx, "llm", "openai_api_key")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if got2.Value != "sk-bbbbbbbb" {
		t.Fatalf("upsert Value = %q, want %q", got2.Value, "sk-bbbbbbbb")
	}
	if got2.Sensitive {
		t.Fatalf("upsert Sensitive should be false")
	}
}

func TestSettingRepoListByCategory(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Set(ctx, "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("Set llm.openai_model: %v", err)
	}
	if _, err := repo.Set(ctx, "llm", "openai_api_key", "sk-zzzzzzzz", true); err != nil {
		t.Fatalf("Set llm.openai_api_key: %v", err)
	}
	if _, err := repo.Set(ctx, "other", "feature_flag", "on", false); err != nil {
		t.Fatalf("Set other.feature_flag: %v", err)
	}

	llm, err := repo.List(ctx, "llm")
	if err != nil {
		t.Fatalf("List llm: %v", err)
	}
	if len(llm) != 2 {
		t.Fatalf("List llm len = %d, want 2", len(llm))
	}
	// Ordering is by key asc.
	if llm[0].Key != "openai_api_key" || llm[1].Key != "openai_model" {
		t.Fatalf("List order: %s, %s", llm[0].Key, llm[1].Key)
	}

	all, err := repo.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List all len = %d, want 3", len(all))
	}
}

func TestSettingRepoDelete(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Set(ctx, "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := repo.Delete(ctx, "llm", "openai_model"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, "llm", "openai_model"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Delete missing row: want ErrNotFound, got %v", err)
	}
}

func TestSettingRepoSetBatchRollsBackEveryRowOnFailure(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.db.Exec(`
		CREATE TRIGGER fail_setting_batch
		BEFORE INSERT ON system_settings
		WHEN NEW.key = 'fail_key'
		BEGIN
			SELECT RAISE(ABORT, 'forced batch failure');
		END;
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := repo.SetBatch(ctx, []settingmodel.Setting{
		{Category: "llm", Key: "first_key", Value: "first"},
		{Category: "llm", Key: "fail_key", Value: "second"},
	})
	if err == nil {
		t.Fatal("SetBatch unexpectedly succeeded")
	}
	if _, err := repo.Get(ctx, "llm", "first_key"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("first row survived failed transaction: %v", err)
	}
}
