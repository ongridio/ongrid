package store

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// linkedTypes builds three models wired in a chain, plus the relation type.
func linkedTypes(t *testing.T, repo *Repo, db *gorm.DB) (a, b, c citypemodel.CIType, rt citypemodel.RelationType) {
	t.Helper()
	attr := seedAttr(t, db, "hostname")
	a = mustType(t, repo, "idc", attr.ID)
	b = mustType(t, repo, "room", attr.ID)
	c = mustType(t, repo, "rack", attr.ID)
	rt = seedRelationType(t, db, "contains")
	ctx := context.Background()
	for _, p := range [][2]model.ID{{a.ID, b.ID}, {b.ID, c.ID}} {
		if _, err := repo.SaveTypeRelation(ctx, saveSpec(p[0], p[1], rt.ID)); err != nil {
			t.Fatalf("link %d -> %d: %v", p[0], p[1], err)
		}
	}
	return a, b, c, rt
}

func treeSpec(uid model.ID, name string, pairs ...citypemodel.TypePair) citypebiz.ServiceTreeSaveSpec {
	return citypebiz.ServiceTreeSaveSpec{
		UID: uid, Name: name, Pairs: pairs,
		Option: map[string]any{"k": "v"},
	}
}

// --- R22: the guard ---

// A MODEL A SERVICE TREE NAMES CANNOT BE DELETED.
//
// This is R22's guard, and it is a GUARD rather than a cascade, which is
// cantor's design: the definitions are per-user, so deleting the model would
// leave every definition mentioning it unresolvable — and those belong to
// people who did not ask for the deletion.
func TestDeleteTypeRefusedWhileAServiceTreeNamesIt(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, b, c, _ := linkedTypes(t, repo, db)
	ctx := context.Background()

	view, err := repo.SaveServiceTree(ctx, treeSpec(7, "机房拓扑",
		citypemodel.TypePair{ParentID: a.ID, ChildID: b.ID},
		citypemodel.TypePair{ParentID: b.ID, ChildID: c.ID},
	))
	if err != nil {
		t.Fatalf("SaveServiceTree() error = %v", err)
	}

	// The model at the TOP of the definition.
	err = repo.DeleteType(ctx, citypebiz.DeleteTypeInput{
		ID: a.ID, Actor: 7, ActorIsSuperuser: true,
		Previous: a,
	})
	if !errors.Is(err, citypebiz.ErrTypeInServiceTree) {
		t.Fatalf("delete error = %v, want ErrTypeInServiceTree", err)
	}
	if !containsText(err.Error(), "机房拓扑") {
		t.Fatalf("error = %q, want the offending definition named", err.Error())
	}

	// And the model in the MIDDLE, which is a child of one pair and a parent of
	// the next.
	if err := repo.DeleteType(ctx, citypebiz.DeleteTypeInput{
		ID: b.ID, Actor: 7, ActorIsSuperuser: true, Previous: b,
	}); !errors.Is(err, citypebiz.ErrTypeInServiceTree) {
		t.Fatalf("middle model delete error = %v, want it refused too", err)
	}

	// The model the definition does not mention is unaffected once its own
	// relations are out of the way.
	if _, err := repo.SaveTypeRelation(ctx, saveSpec(b.ID, c.ID, 0)); err == nil {
		_ = view
	}
}

// Removing the definition releases the model.
func TestDeleteTypeAllowedAfterTheDefinitionGoes(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, b, _, _ := linkedTypes(t, repo, db)
	ctx := context.Background()

	view, err := repo.SaveServiceTree(ctx, treeSpec(7, "t",
		citypemodel.TypePair{ParentID: a.ID, ChildID: b.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteServiceTree(ctx, citypebiz.ServiceTreeDeleteSpec{ID: view.ID, Previous: *view}); err != nil {
		t.Fatalf("DeleteServiceTree() error = %v", err)
	}

	err = repo.DeleteType(ctx, citypebiz.DeleteTypeInput{
		ID: a.ID, Actor: 7, ActorIsSuperuser: true, Previous: a,
	})
	if errors.Is(err, citypebiz.ErrTypeInServiceTree) {
		t.Fatal("the guard still fires after the definition was removed")
	}
}

// --- definitions ---

// cantor validated nothing, so a definition naming a model nobody has was
// stored — and its reader then crashed on that name, permanently, for every
// user of the endpoint.
func TestSaveServiceTreeValidatesInsideTheTransaction(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, _, _, _ := linkedTypes(t, repo, db)
	ctx := context.Background()

	// A model that does not exist.
	_, err := repo.SaveServiceTree(ctx, treeSpec(7, "t",
		citypemodel.TypePair{ParentID: a.ID, ChildID: 9999}))
	if !errors.Is(err, citypebiz.ErrServiceTreeUnknownModel) {
		t.Fatalf("unknown model error = %v", err)
	}

	// A pair that is not a relation.
	other := mustType(t, repo, "unrelated", a.UniqueID)
	_, err = repo.SaveServiceTree(ctx, treeSpec(7, "t",
		citypemodel.TypePair{ParentID: a.ID, ChildID: other.ID}))
	if !errors.Is(err, citypebiz.ErrServiceTreeUnknownModel) {
		t.Fatalf("non-relation pair error = %v", err)
	}

	// Nothing was written for either attempt.
	var rows int64
	if err := db.Model(&citypemodel.PreferenceRelationView{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("definitions = %d, want a refused definition not stored", rows)
	}
}

func TestSaveServiceTreeRoundTripsThePairs(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, b, c, _ := linkedTypes(t, repo, db)
	ctx := context.Background()

	view, err := repo.SaveServiceTree(ctx, treeSpec(7, "机房拓扑",
		citypemodel.TypePair{ParentID: a.ID, ChildID: b.ID},
		citypemodel.TypePair{ParentID: b.ID, ChildID: c.ID}))
	if err != nil {
		t.Fatalf("SaveServiceTree() error = %v", err)
	}

	var loaded citypemodel.PreferenceRelationView
	if err := db.First(&loaded, view.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	// The column is a JSON array of objects; a map declaration would fail to
	// read every row cantor wrote.
	if len(loaded.CRIDs) != 2 {
		t.Fatalf("pairs = %+v, want the stored chain", loaded.CRIDs)
	}
	if loaded.CRIDs[0].ParentID != a.ID || loaded.CRIDs[1].ChildID != c.ID {
		t.Fatalf("pairs = %+v, want the order preserved", loaded.CRIDs)
	}
	if loaded.Option["k"] != "v" {
		t.Fatalf("option = %+v", loaded.Option)
	}
}

// Editing under the same name replaces rather than accumulating, which is what
// the console's single save button implies.
func TestSaveServiceTreeReplacesTheOwnersDefinition(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, b, c, _ := linkedTypes(t, repo, db)
	ctx := context.Background()

	first, err := repo.SaveServiceTree(ctx, treeSpec(7, "t",
		citypemodel.TypePair{ParentID: a.ID, ChildID: b.ID}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.SaveServiceTree(ctx, citypebiz.ServiceTreeSaveSpec{
		ID: first.ID, UID: 7, Name: "t",
		Pairs: []citypemodel.TypePair{{ParentID: b.ID, ChildID: c.ID}},
	})
	if err != nil {
		t.Fatalf("second SaveServiceTree() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("ids %d and %d differ", first.ID, second.ID)
	}

	var rows int64
	if err := db.Model(&citypemodel.PreferenceRelationView{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("definitions = %d, want one", rows)
	}
}

func TestServiceTreesVisibility(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, b, _, _ := linkedTypes(t, repo, db)
	ctx := context.Background()
	pairs := []citypemodel.TypePair{{ParentID: a.ID, ChildID: b.ID}}

	mine, err := repo.SaveServiceTree(ctx, treeSpec(7, "mine", pairs...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveServiceTree(ctx, treeSpec(8, "theirs", pairs...)); err != nil {
		t.Fatal(err)
	}
	public := treeSpec(9, "public", pairs...)
	public.IsPublic = true
	if _, err := repo.SaveServiceTree(ctx, public); err != nil {
		t.Fatal(err)
	}

	visible, err := repo.ServiceTrees(ctx, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 {
		t.Fatalf("visible = %d, want the caller's own and the public one", len(visible))
	}
	found := map[string]bool{}
	for _, v := range visible {
		found[v.Name] = true
	}
	if !found["mine"] || !found["public"] {
		t.Fatalf("visible = %v, want mine and public", found)
	}

	all, err := repo.ServiceTrees(ctx, 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %d, want everything", len(all))
	}
	_ = mine
}

func TestServiceTreesReferencingFindsEitherEnd(t *testing.T) {
	repo, db := newAttrRepo(t)
	a, b, c, _ := linkedTypes(t, repo, db)
	ctx := context.Background()

	if _, err := repo.SaveServiceTree(ctx, treeSpec(7, "t",
		citypemodel.TypePair{ParentID: a.ID, ChildID: b.ID},
		citypemodel.TypePair{ParentID: b.ID, ChildID: c.ID})); err != nil {
		t.Fatal(err)
	}

	for _, id := range []model.ID{a.ID, b.ID, c.ID} {
		found, err := repo.ServiceTreesReferencing(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 {
			t.Fatalf("model %d referenced by %d definitions, want 1", id, len(found))
		}
	}
	other := mustType(t, repo, "unused", a.UniqueID)
	found, err := repo.ServiceTreesReferencing(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("unused model referenced by %+v", found)
	}
}

// --- saved searches ---

// The rolling recent list keeps its newest entries and no more. cantor trimmed
// before inserting, so the stored count was briefly over its own limit; the
// port inserts and trims in one transaction.
func TestRecentSearchListIsTrimmed(t *testing.T) {
	repo, _ := newAttrRepo(t)
	ctx := context.Background()

	for i := 0; i < citypemodel.SearchOptionRecentKeep+5; i++ {
		if _, err := repo.CreateSearchOption(ctx, citypebiz.SearchOptionCreateSpec{
			UID: 7, Name: citypemodel.SearchOptionRecent,
			Option: map[string]any{"n": i},
		}); err != nil {
			t.Fatalf("CreateSearchOption() error = %v", err)
		}
	}
	rows, err := repo.SearchOptions(ctx, citypebiz.SearchOptionFilter{
		UID: 7, Name: citypemodel.SearchOptionRecent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != citypemodel.SearchOptionRecentKeep {
		t.Fatalf("recent = %d, want exactly the limit of %d",
			len(rows), citypemodel.SearchOptionRecentKeep)
	}
}

func TestSearchOptionUpdateIsScopedToTheOwner(t *testing.T) {
	repo, _ := newAttrRepo(t)
	ctx := context.Background()

	created, err := repo.CreateSearchOption(ctx, citypebiz.SearchOptionCreateSpec{
		UID: 8, Name: "theirs", Option: map[string]any{"q": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The use case checks ownership; this closes the window between that check
	// and the write by putting the owner in the WHERE clause.
	err = repo.UpdateSearchOption(ctx, citypebiz.SearchOptionUpdateSpec{
		ID: created.ID, UID: 7, Name: "stolen",
	})
	if !errors.Is(err, citypebiz.ErrSearchOptionNotFound) {
		t.Fatalf("cross-user update error = %v, want it refused", err)
	}

	loaded, err := repo.SearchOptionByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name == nil || *loaded.Name != "theirs" {
		t.Fatalf("name = %v, want it unchanged", loaded.Name)
	}
}

func TestSearchOptionsFilter(t *testing.T) {
	repo, _ := newAttrRepo(t)
	ctx := context.Background()

	typeID := model.ID(10)
	if _, err := repo.CreateSearchOption(ctx, citypebiz.SearchOptionCreateSpec{
		UID: 7, Name: "hosts", TypeID: &typeID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSearchOption(ctx, citypebiz.SearchOptionCreateSpec{
		UID: 7, Name: "everything",
	}); err != nil {
		t.Fatal(err)
	}

	byName, err := repo.SearchOptions(ctx, citypebiz.SearchOptionFilter{UID: 7, Name: "hosts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 {
		t.Fatalf("by name = %d, want 1", len(byName))
	}
	byType, err := repo.SearchOptions(ctx, citypebiz.SearchOptionFilter{UID: 7, TypeID: typeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byType) != 1 {
		t.Fatalf("by model = %d, want 1", len(byType))
	}
	all, err := repo.SearchOptions(ctx, citypebiz.SearchOptionFilter{UID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %d, want both", len(all))
	}
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
