package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func newTestRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(testDSN(t)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&citypemodel.RelationType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRepo(db), db
}

// The default colour is a database default in cantor, so it must survive a
// write that does not set it explicitly.
func TestCreateAppliesDatabaseDefaultColour(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	rt := &citypemodel.RelationType{Name: "runs_on"}
	if err := repo.Create(ctx, rt); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if rt.ID == 0 {
		t.Fatal("Create() did not fill in the assigned id")
	}

	got, err := repo.Get(ctx, rt.ID)
	if err != nil || got == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if got.Color != citypemodelDefaultColor {
		t.Fatalf("color = %q, want the database default %q", got.Color, citypemodelDefaultColor)
	}
}

// The default must match cantor's server_default exactly, otherwise a row
// created here and a row created by cantor would render differently.
const citypemodelDefaultColor = "#1890ff"

func TestListOrdersByID(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	for _, name := range []string{"c_third", "a_first", "b_second"} {
		if err := repo.Create(ctx, &citypemodel.RelationType{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List() = %d rows, want 3", len(got))
	}
	// Ordered by id (insertion order), NOT by name — the dictionary is stable
	// in creation order, matching cantor.
	if got[0].Name != "c_third" || got[1].Name != "a_first" || got[2].Name != "b_second" {
		t.Fatalf("List() order = %s,%s,%s; want insertion order",
			got[0].Name, got[1].Name, got[2].Name)
	}
}

func TestGetMissingReturnsNilNotError(t *testing.T) {
	repo, _ := newTestRepo(t)

	got, err := repo.Get(context.Background(), 999)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil for a missing row", err)
	}
	if got != nil {
		t.Fatalf("Get() = %+v, want nil", got)
	}
}

// A soft-deleted row must vanish from List, Get and FindByName — otherwise a
// deleted dictionary entry would still be offered to the UI.
func TestSoftDeleteHidesTheRowEverywhere(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()

	rt := &citypemodel.RelationType{Name: "runs_on"}
	if err := repo.Create(ctx, rt); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, rt.ID); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	if rows, err := repo.List(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("List() after delete = %+v, %v; want empty", rows, err)
	}
	if got, err := repo.Get(ctx, rt.ID); err != nil || got != nil {
		t.Fatalf("Get() after delete = %+v, %v; want nil", got, err)
	}
	if got, err := repo.FindByName(ctx, "runs_on"); err != nil || got != nil {
		t.Fatalf("FindByName() after delete = %+v, %v; want nil (the name is free again)", got, err)
	}

	// The row itself must still exist unscoped: CMDB soft-deletes, and an audit
	// trail that referenced the dictionary entry must not dangle.
	var unscoped int64
	if err := db.Unscoped().Model(&citypemodel.RelationType{}).Count(&unscoped).Error; err != nil {
		t.Fatal(err)
	}
	if unscoped != 1 {
		t.Fatalf("unscoped rows = %d, want 1 (soft delete, not hard)", unscoped)
	}
}

// A name freed by a soft delete can be reused, which is what makes the
// dictionary operable after a rename-away-and-back.
func TestNameIsReusableAfterDelete(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	first := &citypemodel.RelationType{Name: "runs_on"}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second := &citypemodel.RelationType{Name: "runs_on"}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("reusing a freed name: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("reuse produced the same id as the deleted row")
	}
}

func TestFindByNameIsExact(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &citypemodel.RelationType{Name: "runs_on"}); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.FindByName(ctx, "runs"); err != nil || got != nil {
		t.Fatalf("FindByName(prefix) = %+v, %v; want nil (exact match only)", got, err)
	}
	if got, err := repo.FindByName(ctx, "RUNS_ON"); err != nil || got != nil {
		t.Fatalf("FindByName(uppercase) = %+v, %v; want nil (case-sensitive, matching cantor)", got, err)
	}
	if got, err := repo.FindByName(ctx, "runs_on"); err != nil || got == nil {
		t.Fatalf("FindByName(exact) = %+v, %v; want the row", got, err)
	}
}

func TestUpdateWritesNameAndColour(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	rt := &citypemodel.RelationType{Name: "runs_on", Color: "#1890ff"}
	if err := repo.Create(ctx, rt); err != nil {
		t.Fatal(err)
	}
	rt.Name = "hosted_on"
	rt.Color = "#ff0000"
	if err := repo.Update(ctx, rt); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := repo.Get(ctx, rt.ID)
	if err != nil || got == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if got.Name != "hosted_on" || got.Color != "#ff0000" {
		t.Fatalf("row = %+v, want the updated name and colour", got)
	}
}

// A repository miss must surface as errs.ErrNotFound, not as GORM's own
// sentinel: the data layer is where storage vocabulary stops.
func TestUpdateAndDeleteMissingReturnErrsNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	if err := repo.Update(ctx, &citypemodel.RelationType{Name: "ghost"}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Update() missing row error = %v, want errs.ErrNotFound", err)
	}
	// A row with a real id but which has since been deleted.
	rt := &citypemodel.RelationType{Name: "runs_on"}
	if err := repo.Create(ctx, rt); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, rt.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, rt.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("second SoftDelete() error = %v, want errs.ErrNotFound", err)
	}
}

func TestCreateRejectsNilRow(t *testing.T) {
	repo, _ := newTestRepo(t)

	if err := repo.Create(context.Background(), nil); err == nil {
		t.Fatal("Create(nil) = nil error, want error")
	}
}

// testDSN names an in-memory database that EVERY connection in this test
// shares.
//
// A BARE `:memory:` IS NOT ENOUGH, and the difference is not academic. With
// `:memory:` each CONNECTION gets its own private, empty database, and
// database/sql may open a second connection at any moment — for instance while
// a transaction holds the first one. A test that then asserts "nothing was
// written" reads through whichever connection it is handed and can pass because
// it is looking at an empty database rather than because the write was rolled
// back. Measured on glebarez/sqlite: a CREATE on one connection followed by a
// read from inside a transaction reported "no such table: probe", and the pool
// had grown from one connection to two.
//
// The unique name keeps this test's database to itself, so tests cannot bleed
// into each other through a shared cache. It is dropped when the last
// connection closes.
func testDSN(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "?", "_", "#", "_").Replace(t.Name())
	return "file:cmdb-" + name + "?mode=memory&cache=shared"
}
