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

// seedRelationType creates a relation-type dictionary entry.
func seedRelationType(t *testing.T, db *gorm.DB, name string) citypemodel.RelationType {
	t.Helper()
	rt := citypemodel.RelationType{Name: name, Color: "#1890ff"}
	if err := db.Create(&rt).Error; err != nil {
		t.Fatalf("create relation type %s: %v", name, err)
	}
	return rt
}

// relationFixture builds two models and a relation type.
func relationFixture(t *testing.T, repo *Repo, db *gorm.DB) (parent, child citypemodel.CIType, rt citypemodel.RelationType) {
	t.Helper()
	attr := seedAttr(t, db, "hostname")
	parent = mustType(t, repo, "host", attr.ID)
	child = mustType(t, repo, "idc", attr.ID)
	rt = seedRelationType(t, db, "runs_on")
	return parent, child, rt
}

func saveSpec(parent, child, rtID model.ID, attrs ...model.ID) citypebiz.TypeRelationSaveSpec {
	return citypebiz.TypeRelationSaveSpec{
		ParentID: parent, ChildID: child, RelationTypeID: rtID,
		Constraint:    string(model.ConstraintOne2Many),
		ParentAttrIDs: attrs,
		Actor:         7,
	}
}

// THE CYCLE GUARD, and the witness that makes it usable.
//
// cantor ran a topological sort over the WHOLE graph and answered "circular
// dependency" without saying which models were involved. The scan covers the
// whole graph, so the offending cycle may be one nobody in this request
// touched — which is exactly when a witness is the difference between an
// operator fixing their edit and hunting their data.
func TestSaveTypeRelationRejectsACycle(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, rt := relationFixture(t, repo, db)
	ctx := context.Background()

	if _, err := repo.SaveTypeRelation(ctx, saveSpec(parent.ID, child.ID, rt.ID)); err != nil {
		t.Fatalf("first edge error = %v", err)
	}

	// idc -> host would close a two-cycle.
	_, err := repo.SaveTypeRelation(ctx, saveSpec(child.ID, parent.ID, rt.ID))
	if !errors.Is(err, citypebiz.ErrRelationCycle) {
		t.Fatalf("reversed edge error = %v, want ErrRelationCycle", err)
	}

	var cycleErr *citypebiz.RelationCycleError
	if !errors.As(err, &cycleErr) {
		t.Fatalf("error = %v, want it to carry the cycle", err)
	}
	if len(cycleErr.Path) != 3 {
		t.Fatalf("cycle = %v, want the two models and the closing repeat", cycleErr.Path)
	}
	seen := map[model.ID]bool{}
	for _, id := range cycleErr.Path {
		seen[id] = true
	}
	if !seen[parent.ID] || !seen[child.ID] {
		t.Fatalf("cycle = %v, want it to name both models", cycleErr.Path)
	}

	// And nothing was written.
	var rows int64
	if err := db.Model(&citypemodel.CITypeRelation{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("relations = %d, want the refused edge not stored", rows)
	}
}

// The refusal names a cycle nobody in this request touched, which is the case
// cantor's message could not distinguish from the caller's own edit.
func TestSaveTypeRelationRejectsAnUnrelatedCycle(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	a := mustType(t, repo, "a", attr.ID)
	b := mustType(t, repo, "b", attr.ID)
	c := mustType(t, repo, "c", attr.ID)
	x := mustType(t, repo, "x", attr.ID)
	y := mustType(t, repo, "y", attr.ID)
	rt := seedRelationType(t, db, "runs_on")
	ctx := context.Background()

	if _, err := repo.SaveTypeRelation(ctx, saveSpec(a.ID, b.ID, rt.ID)); err != nil {
		t.Fatal(err)
	}
	// A cycle between two models the next request has nothing to do with.
	// Written directly, as a direct SQL insert or an older cantor release
	// could have.
	if err := db.Create(&citypemodel.CITypeRelation{ParentID: x.ID, ChildID: y.ID, RelationTypeID: rt.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&citypemodel.CITypeRelation{ParentID: y.ID, ChildID: x.ID, RelationTypeID: rt.ID}).Error; err != nil {
		t.Fatal(err)
	}

	_, err := repo.SaveTypeRelation(ctx, saveSpec(b.ID, c.ID, rt.ID))
	if !errors.Is(err, citypebiz.ErrRelationCycle) {
		t.Fatalf("error = %v, want the pre-existing cycle to refuse the write", err)
	}
	var cycleErr *citypebiz.RelationCycleError
	if !errors.As(err, &cycleErr) {
		t.Fatalf("error = %v", err)
	}
	for _, id := range cycleErr.Path {
		if id == b.ID || id == c.ID {
			t.Fatalf("cycle = %v, want the x/y pair rather than the models of this request", cycleErr.Path)
		}
	}
}

func TestSaveTypeRelationRefusesASelfLoop(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, _, rt := relationFixture(t, repo, db)

	_, err := repo.SaveTypeRelation(context.Background(), saveSpec(parent.ID, parent.ID, rt.ID))
	if !errors.Is(err, citypebiz.ErrRelationSelfLoop) {
		t.Fatalf("error = %v, want ErrRelationSelfLoop", err)
	}
}

func TestSaveTypeRelationRefusesAnUnknownRelationType(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, _ := relationFixture(t, repo, db)

	_, err := repo.SaveTypeRelation(context.Background(), saveSpec(parent.ID, child.ID, 9999))
	if !errors.Is(err, citypebiz.ErrRelationTypeNotFound) {
		t.Fatalf("error = %v, want an unknown relation type refused", err)
	}
}

// A model pair has at most one relation, so posting the pair again edits it
// rather than failing — which is what lets the console change a cardinality.
func TestSaveTypeRelationUpsertsThePair(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, rt := relationFixture(t, repo, db)
	other := seedRelationType(t, db, "belongs_to")
	ctx := context.Background()

	first, err := repo.SaveTypeRelation(ctx, saveSpec(parent.ID, child.ID, rt.ID))
	if err != nil {
		t.Fatal(err)
	}

	second := saveSpec(parent.ID, child.ID, other.ID)
	second.Constraint = string(model.ConstraintMany2Many)
	saved, err := repo.SaveTypeRelation(ctx, second)
	if err != nil {
		t.Fatalf("second SaveTypeRelation() error = %v", err)
	}
	if saved.ID != first.ID {
		t.Fatalf("ids %d and %d differ, want the same row edited", first.ID, saved.ID)
	}
	if saved.RelationTypeID != other.ID {
		t.Fatalf("relation type = %d, want the new one", saved.RelationTypeID)
	}
	if saved.Constraint == nil || *saved.Constraint != string(model.ConstraintMany2Many) {
		t.Fatalf("constraint = %v, want the new cardinality", saved.Constraint)
	}

	var rows int64
	if err := db.Model(&citypemodel.CITypeRelation{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("relations = %d, want one row for the pair", rows)
	}
}

// A relation's attribute pairs are REPLACED, including being cleared when the
// request omits them: this endpoint sets a relation to exactly what it is
// given.
func TestSaveTypeRelationReplacesTheAttributePairs(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, rt := relationFixture(t, repo, db)
	left := seedAttr(t, db, "serial")
	right := seedAttr(t, db, "asset_no")
	ctx := context.Background()

	first := saveSpec(parent.ID, child.ID, rt.ID, left.ID)
	first.ChildAttrIDs = []model.ID{right.ID}
	if _, err := repo.SaveTypeRelation(ctx, first); err != nil {
		t.Fatal(err)
	}

	// The JSON columns are arrays, not maps: a map declaration would fail to
	// read every row cantor wrote.
	var loaded citypemodel.CITypeRelation
	if err := db.Where("parent_id = ? AND child_id = ?", parent.ID, child.ID).First(&loaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded.ParentAttrIDs) != 1 || loaded.ParentAttrIDs[0] != left.ID {
		t.Fatalf("parent attrs = %v", loaded.ParentAttrIDs)
	}
	if len(loaded.ChildAttrIDs) != 1 || loaded.ChildAttrIDs[0] != right.ID {
		t.Fatalf("child attrs = %v", loaded.ChildAttrIDs)
	}

	// Now post the same pair with no attribute pairs at all.
	if _, err := repo.SaveTypeRelation(ctx, saveSpec(parent.ID, child.ID, rt.ID)); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("parent_id = ? AND child_id = ?", parent.ID, child.ID).First(&loaded).Error; err != nil {
		t.Fatal(err)
	}
	if len(loaded.ParentAttrIDs) != 0 || len(loaded.ChildAttrIDs) != 0 {
		t.Fatalf("attrs = %v/%v, want the omitted pairs cleared", loaded.ParentAttrIDs, loaded.ChildAttrIDs)
	}
}

// cantor committed the relation and then committed the history row separately.
func TestSaveTypeRelationRollsBackWithItsHistory(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, rt := relationFixture(t, repo, db)

	if err := db.Exec(`CREATE TRIGGER fail_relation_history BEFORE INSERT ON c_ci_type_histories
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatalf("create fault-injection trigger: %v", err)
	}

	_, err := repo.SaveTypeRelation(context.Background(), saveSpec(parent.ID, child.ID, rt.ID))
	if err == nil {
		t.Fatal("SaveTypeRelation() with an injected failure = nil error, want error")
	}

	var rows int64
	if err := db.Model(&citypemodel.CITypeRelation{}).Where("parent_id = ?", parent.ID).
		Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("relations = %d after a rolled-back save, want 0 — the edge survived while its "+
			"audit row failed", rows)
	}
}

func TestDeleteTypeRelationWritesHistory(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, rt := relationFixture(t, repo, db)
	ctx := context.Background()

	saved, err := repo.SaveTypeRelation(ctx, saveSpec(parent.ID, child.ID, rt.ID))
	if err != nil {
		t.Fatal(err)
	}
	before := typeHistoryCount(t, db, parent.ID)

	if err := repo.DeleteTypeRelation(ctx, citypebiz.TypeRelationDeleteSpec{
		ID: saved.ID, Previous: *saved, Actor: 7,
	}); err != nil {
		t.Fatalf("DeleteTypeRelation() error = %v", err)
	}
	// cantor recorded a relation against the PARENT model, on both the add and
	// the delete: a relation is written from the parent's side.
	if after := typeHistoryCount(t, db, parent.ID); after <= before {
		t.Fatalf("history rows %d -> %d, want the deletion recorded against the parent", before, after)
	}
	// A second delete is a not-found rather than an error about a missing row.
	if err := repo.DeleteTypeRelation(ctx, citypebiz.TypeRelationDeleteSpec{
		ID: saved.ID, Previous: *saved, Actor: 7,
	}); !errors.Is(err, citypebiz.ErrRelationNotFound) {
		t.Fatalf("second delete error = %v, want ErrRelationNotFound", err)
	}
}

func TestListTypeRelationsFiltersByEitherEnd(t *testing.T) {
	repo, db := newAttrRepo(t)
	parent, child, rt := relationFixture(t, repo, db)
	third := mustType(t, repo, "switch", parent.UniqueID)
	ctx := context.Background()

	if _, err := repo.SaveTypeRelation(ctx, saveSpec(parent.ID, child.ID, rt.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveTypeRelation(ctx, saveSpec(third.ID, parent.ID, rt.ID)); err != nil {
		t.Fatal(err)
	}

	all, err := repo.ListTypeRelations(ctx, citypebiz.TypeRelationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("relations = %d, want both", len(all))
	}
	// Ordered by id, which is what makes the graph walks deterministic.
	if all[0].ID > all[1].ID {
		t.Fatalf("relations = %+v, want id order", all)
	}

	touching, err := repo.ListTypeRelations(ctx, citypebiz.TypeRelationFilter{
		TypeIDs: []model.ID{parent.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(touching) != 2 {
		t.Fatalf("relations touching the parent = %d, want both sides counted", len(touching))
	}

	onlyChild, err := repo.ListTypeRelations(ctx, citypebiz.TypeRelationFilter{
		TypeIDs: []model.ID{child.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyChild) != 1 || onlyChild[0].ChildID != child.ID {
		t.Fatalf("relations = %+v, want just the one reaching the child", onlyChild)
	}
}

func TestRelationTypesByIDRoundTrips(t *testing.T) {
	repo, db := newAttrRepo(t)
	rt := seedRelationType(t, db, "runs_on")

	got, err := repo.RelationTypesByID(context.Background(), []model.ID{rt.ID, 9999})
	if err != nil {
		t.Fatalf("RelationTypesByID() error = %v", err)
	}
	if len(got) != 1 || got[rt.ID].Name != "runs_on" {
		t.Fatalf("got = %+v, want just the existing entry", got)
	}

	empty, err := repo.RelationTypesByID(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("got = %+v, want nothing for an empty request", empty)
	}
}
