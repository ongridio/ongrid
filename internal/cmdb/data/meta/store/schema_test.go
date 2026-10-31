package store

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestLatestWithNoRowsReportsNotFound(t *testing.T) {
	repo := NewRepo(newTestDB(t))

	if _, ok, err := repo.Latest(context.Background()); err != nil || ok {
		t.Fatalf("Latest() = ok %v, err %v; want ok=false, err=nil", ok, err)
	}
}

func TestRecordThenLatest(t *testing.T) {
	repo, ctx := NewRepo(newTestDB(t)), context.Background()

	if err := repo.Record(ctx, meta.SchemaMeta{Generation: meta.GenerationFresh, Origin: "sqlite", AppliedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	got, ok, err := repo.Latest(ctx)
	if err != nil || !ok {
		t.Fatalf("Latest() = ok %v, err %v; want ok=true", ok, err)
	}
	if got.Generation != meta.GenerationFresh || got.Origin != "sqlite" {
		t.Fatalf("Latest() = %+v, want fresh/sqlite", got)
	}
}

// Rows are append-only, so Latest must pick the newest by AppliedAt, not the
// first inserted. A fixed AppliedAt forces the id tie-break.
func TestLatestPrefersNewestAndTieBreaksOnID(t *testing.T) {
	repo, ctx := NewRepo(newTestDB(t)), context.Background()
	same := time.Now().UTC()

	if err := repo.Record(ctx, meta.SchemaMeta{Generation: meta.GenerationFresh, AppliedAt: same.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, meta.SchemaMeta{Generation: meta.GenerationAdoptedCantor, AppliedAt: same}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, meta.SchemaMeta{Generation: meta.GenerationFresh, AppliedAt: same}); err != nil {
		t.Fatal(err)
	}

	got, _, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got.Generation != meta.GenerationFresh {
		t.Fatalf("Latest().Generation = %q, want the last-written fresh row", got.Generation)
	}
}

func TestRecordRejectsEmptyGeneration(t *testing.T) {
	repo := NewRepo(newTestDB(t))

	if err := repo.Record(context.Background(), meta.SchemaMeta{}); err == nil {
		t.Fatal("Record() with empty generation = nil error, want error")
	}
}
