package store

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

func newIMBridgeTestRepo(t *testing.T) *Repo {
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
	return New(db)
}

func sampleApp() *model.ImApp {
	return &model.ImApp{
		Provider:      model.ProviderFeishu,
		Mode:          model.ModeStream,
		Name:          "ops bot",
		AppID:         "cli_a",
		AppSecret:     "secret",
		DefaultLocale: "zh",
		Enabled:       true,
	}
}

func TestImAppSoftDeleteAllowsProviderAppIDReuse(t *testing.T) {
	repo := newIMBridgeTestRepo(t)
	ctx := context.Background()

	first := sampleApp()
	if err := repo.CreateApp(ctx, first); err != nil {
		t.Fatalf("first CreateApp: %v", err)
	}
	if err := repo.CreateApp(ctx, sampleApp()); err == nil {
		t.Fatalf("active duplicate CreateApp succeeded")
	}
	if err := repo.DeleteApp(ctx, first.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}

	recreated := sampleApp()
	recreated.Name = "ops bot recreated"
	if err := repo.CreateApp(ctx, recreated); err != nil {
		t.Fatalf("recreate after soft delete: %v", err)
	}
	if recreated.ID == first.ID {
		t.Fatalf("recreated app reused soft-deleted id %d", first.ID)
	}
	got, err := repo.GetAppByAppID(ctx, model.ProviderFeishu, "cli_a")
	if err != nil {
		t.Fatalf("GetAppByAppID: %v", err)
	}
	if got.ID != recreated.ID {
		t.Fatalf("GetAppByAppID id = %d, want recreated id %d", got.ID, recreated.ID)
	}
}
