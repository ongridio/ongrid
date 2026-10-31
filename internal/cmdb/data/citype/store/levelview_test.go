package store

import (
	"context"
	"testing"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

func treeOrderRows(t *testing.T, db *gorm.DB, uid model.ID) []citypemodel.PreferenceCITypeOrder {
	t.Helper()
	var rows []citypemodel.PreferenceCITypeOrder
	if err := db.Where("uid = ? AND is_tree = ?", uid, true).Order("`order`").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func countLevelViews(t *testing.T, db *gorm.DB, uid model.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&citypemodel.PreferenceTreeView{}).Where("uid = ?", uid).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// A FIRST WRITE CREATES BOTH ROWS. The level list is what the page renders and
// the ordering entry is what makes the model reachable on the tree page at all,
// so a view without one is a view the user cannot see.
func TestSaveLevelViewCreatesTheViewAndItsOrdering(t *testing.T) {
	repo, db := newTestRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)
	ctx := context.Background()

	saved, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{11, 12}, AppendOrder: true,
	})
	if err != nil {
		t.Fatalf("SaveLevelView() error = %v", err)
	}
	if saved == nil || len(saved.Levels) != 2 || saved.Levels[0] != 11 {
		t.Fatalf("saved = %+v, want the level list", saved)
	}

	rows := treeOrderRows(t, db, 7)
	if len(rows) != 1 || rows[0].TypeID == nil || *rows[0].TypeID != ct.ID || rows[0].Order != 1 {
		t.Fatalf("tree order = %+v, want one entry for the model at position 1", rows)
	}

	got, err := repo.LevelView(ctx, 7, ct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Levels) != 2 {
		t.Fatalf("LevelView = %+v", got)
	}
}

// EDITING REWRITES THE LIST AND LEAVES THE ORDER ALONE. The position belongs to
// the sidebar and has its own endpoint; moving a model because someone changed
// which column a level shows would be a surprising side effect.
func TestSaveLevelViewEditsWithoutTouchingTheOrder(t *testing.T) {
	repo, db := newTestRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)
	ctx := context.Background()

	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{12, 13}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}

	rows := treeOrderRows(t, db, 7)
	if len(rows) != 1 {
		t.Fatalf("tree order = %+v, want the model to hold ONE position", rows)
	}
	got, err := repo.LevelView(ctx, 7, ct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Levels) != 2 || got.Levels[0] != 12 {
		t.Fatalf("levels = %v, want the replacement list", got.Levels)
	}
}

// The ordering entry is APPENDED after the highest existing one, so a newly
// subscribed model appears last. A constant would put it alongside the first
// ordered model instead of after all of them.
func TestSaveLevelViewAppendsToTheEndOfTheOrder(t *testing.T) {
	repo, db := newTestRepo(t)
	first := seedType(t, db, "host", "主机", 1, nil)
	second := seedType(t, db, "switch", "交换机", 1, nil)
	ctx := context.Background()

	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: first.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: second.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}

	rows := treeOrderRows(t, db, 7)
	if len(rows) != 2 || rows[0].Order >= rows[1].Order {
		t.Fatalf("tree order = %+v, want increasing positions", rows)
	}
	if rows[1].TypeID == nil || *rows[1].TypeID != second.ID {
		t.Fatalf("last entry = %+v, want the model just subscribed", rows[1])
	}
}

// AN EMPTY LEVEL LIST REMOVES BOTH ROWS. That is how the console expresses
// "stop customising this model": the page falls back to the model's defaults,
// and an ordering entry pointing at a model with no view would leave an entry
// on the tree page that opens nothing.
func TestSaveLevelViewWithNoLevelsClearsBoth(t *testing.T) {
	repo, db := newTestRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)
	ctx := context.Background()

	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: nil,
	}); err != nil {
		t.Fatalf("clearing error = %v", err)
	}

	if n := countLevelViews(t, db, 7); n != 0 {
		t.Fatalf("level views = %d, want the row removed", n)
	}
	if rows := treeOrderRows(t, db, 7); len(rows) != 0 {
		t.Fatalf("tree order = %+v, want the entry removed", rows)
	}
}

// The two writes are ONE transaction. A failure while writing the ordering must
// not leave the level list behind, because the level list's entire purpose is
// to be reachable from the tree page.
func TestSaveLevelViewIsAtomic(t *testing.T) {
	repo, db := newTestRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)

	// A trigger that refuses every ordering insert, so the second half of the
	// transaction fails after the first half has written.
	if err := db.Exec(`CREATE TRIGGER refuse_order BEFORE INSERT ON c_pcto
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatalf("create fault-injection trigger: %v", err)
	}

	_, err := repo.SaveLevelView(context.Background(), citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{11}, AppendOrder: true,
	})
	if err == nil {
		t.Fatal("SaveLevelView() with an injected failure = nil error, want an error")
	}
	if n := countLevelViews(t, db, 7); n != 0 {
		t.Fatalf("level views = %d, want the row rolled back with the ordering", n)
	}
}

// A view the user does not have can be deleted without complaint: the caller
// asked for the state and the state is what they get.
func TestDeleteLevelViewIsIdempotent(t *testing.T) {
	repo, db := newTestRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)
	ctx := context.Background()

	if err := repo.DeleteLevelView(ctx, 7, ct.ID); err != nil {
		t.Fatalf("DeleteLevelView() on an absent view error = %v", err)
	}
	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteLevelView(ctx, 7, ct.ID); err != nil {
		t.Fatalf("DeleteLevelView() error = %v", err)
	}
	if n := countLevelViews(t, db, 7); n != 0 {
		t.Fatalf("level views = %d", n)
	}
	if rows := treeOrderRows(t, db, 7); len(rows) != 0 {
		t.Fatalf("tree order = %+v, want the entry removed", rows)
	}
}

// One user's write does not touch another's, and the resource ordering on the
// same table is left alone: the tree and the resource list are two lists.
func TestLevelViewsArePerUserAndPerOrdering(t *testing.T) {
	repo, db := newTestRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)
	ctx := context.Background()

	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: ct.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTypeOrder(ctx, citypebiz.TypeOrderSpec{
		UID: 7, TypeIDs: []model.ID{ct.ID}, IsTree: false,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteLevelView(ctx, 7, ct.ID); err != nil {
		t.Fatal(err)
	}
	var resourceOrder int64
	if err := db.Model(&citypemodel.PreferenceCITypeOrder{}).
		Where("uid = ? AND is_tree = ?", 7, false).Count(&resourceOrder).Error; err != nil {
		t.Fatal(err)
	}
	if resourceOrder != 1 {
		t.Fatalf("the resource ordering was removed with the tree one: %d rows", resourceOrder)
	}

	other, err := repo.LevelView(ctx, 8, ct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Fatalf("user 8 has a view they never wrote: %+v", other)
	}
}

// DELETING A MODEL TAKES EVERY USER'S VIEW WITH IT, through the same cascade
// that removes the other preferences. cantor cascaded this table too; a
// surviving row names a model that no longer exists, and the read path that
// resolves that model renders an entry with no name.
//
// The test goes through DeleteType rather than calling a cascade helper,
// because the helper is not the thing that has to work — the cascade list is.
func TestDeleteTypeCascadesLevelViews(t *testing.T) {
	// The full model+CI schema: DeleteType guards on instances of the model,
	// which live in c_cis, so the cascade cannot be exercised against the model
	// tables alone.
	repo, db := newAttrRepo(t)
	ct := seedType(t, db, "host", "主机", 1, nil)
	other := seedType(t, db, "switch", "交换机", 1, nil)
	ctx := context.Background()

	for _, uid := range []model.ID{7, 8} {
		if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
			UID: uid, TypeID: ct.ID, Levels: []model.ID{11}, AppendOrder: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.SaveLevelView(ctx, citypebiz.LevelViewSaveSpec{
		UID: 7, TypeID: other.ID, Levels: []model.ID{11}, AppendOrder: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteType(ctx, citypebiz.DeleteTypeInput{
		ID: ct.ID, Actor: 7, ActorIsSuperuser: true, Previous: ct,
	}); err != nil {
		t.Fatalf("DeleteType() error = %v", err)
	}

	if n := countLevelViews(t, db, 7); n != 1 {
		t.Fatalf("user 7 has %d views, want only the surviving model's", n)
	}
	if n := countLevelViews(t, db, 8); n != 0 {
		t.Fatalf("user 8 has %d views, want the deleted model's cascade to remove them", n)
	}
}
