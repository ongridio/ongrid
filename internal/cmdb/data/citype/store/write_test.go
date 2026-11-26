package store

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// newWriteRepo sets up a database with both the model-definition and the
// CI-side tables, since the delete guards consult c_cis.
func newWriteRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	// The model-definition tables, plus the CI-side ones the delete guards
	// consult (c_cis for live instances, c_ci_type_histories for the audit
	// trail the cascade must not touch).
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate model tables: %v", err)
	}
	if err := db.AutoMigrate(&cimodel.CI{}, &cimodel.TypeHistory{}); err != nil {
		t.Fatalf("migrate ci tables: %v", err)
	}
	return NewRepo(db), db
}

func mustType(t *testing.T, repo *Repo, name string, uniqueAttr model.ID, parents ...model.ID) citypemodel.CIType {
	t.Helper()
	ct := citypemodel.CIType{Name: name, Alias: name, UniqueID: uniqueAttr, Enabled: true}
	created, err := repo.CreateType(context.Background(), citypebiz.CreateTypeInput{
		Type: ct, UniqueAttributeID: uniqueAttr, ParentIDs: parents, Actor: 7,
	})
	if err != nil {
		t.Fatalf("CreateType(%s) error = %v", name, err)
	}
	return *created
}

func TestCreateTypeBindsUniqueAttributeAsRequired(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")

	ct := mustType(t, repo, "host", attr.ID)

	var binding citypemodel.CITypeAttribute
	if err := db.Where("type_id = ? AND attr_id = ?", ct.ID, attr.ID).First(&binding).Error; err != nil {
		t.Fatalf("the unique attribute was not bound: %v", err)
	}
	if !binding.IsRequired {
		t.Fatal("the unique attribute binding must be required, or the model cannot be instantiated")
	}
	if !binding.DefaultShow {
		t.Fatal("the binding should default to shown")
	}
}

func TestCreateTypeCreatesInheritanceEdges(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	parent := mustType(t, repo, "parent", attr.ID)
	child := mustType(t, repo, "child", attr.ID, parent.ID)

	var edges []citypemodel.CITypeInheritance
	if err := db.Where("child_id = ?", child.ID).Find(&edges).Error; err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].ParentID != parent.ID {
		t.Fatalf("edges = %+v, want one edge to %d", edges, parent.ID)
	}
}

func TestCreateTypeWritesHistory(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	mustType(t, repo, "host", attr.ID)

	var rows []cimodel.TypeHistory
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("history rows = %d, want 1", len(rows))
	}
	if rows[0].OperateType == nil || *rows[0].OperateType != string(model.CITypeOpAdd) {
		t.Fatalf("operate_type = %v, want ADD", rows[0].OperateType)
	}
	if rows[0].UID == nil || *rows[0].UID != 7 {
		t.Fatalf("history uid = %v, want 7", rows[0].UID)
	}
	if rows[0].Change["new"] == nil {
		t.Fatalf("history change = %#v, want a `new` snapshot", rows[0].Change)
	}
}

// The CMDB schema declares NO database-level foreign keys.
//
// That is a deliberate adoption decision (see internal/cmdb/model/const.go):
// cantor's tables carry FKs, GORM would want to recreate them, and a
// type-changing ALTER on an FK child column fails with MySQL 3780. The
// consequence is that referential integrity is the APPLICATION's job, and this
// test pins that consequence so it is a known property rather than a surprise:
// a dangling parent id inserts fine at this layer, and it is
// citypebiz.Usecase.validateParents that refuses it.
func TestNoForeignKeysSoDanglingReferencesInsert(t *testing.T) {
	_, db := newWriteRepo(t)

	err := db.Create(&citypemodel.CITypeInheritance{ParentID: 99999, ChildID: 1}).Error
	if err != nil {
		t.Fatalf("insert of a dangling inheritance edge failed (%v); if CMDB has gained foreign "+
			"keys, the adoption story in const.go needs revisiting and the usecase guards are "+
			"no longer the only defence", err)
	}
}

// Creation is one transaction, so a failure at ANY step must leave nothing
// behind — not just a failure of the first statement.
//
// The failure is injected with a SQLite trigger rather than by supplying bad
// input, because with no foreign keys and no unique constraints on these
// tables there is no input that fails partway through. The trigger aborts on
// the SECOND statement (binding the unique attribute), which is exactly the
// partial state the transaction must undo.
func TestCreateTypeRollsBackWhenALaterStatementFails(t *testing.T) {
	repo, db := newWriteRepo(t)

	if err := db.Exec(`CREATE TRIGGER fail_binding BEFORE INSERT ON c_ci_type_attributes
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatalf("create fault-injection trigger: %v", err)
	}

	_, err := repo.CreateType(context.Background(), citypebiz.CreateTypeInput{
		Type:              citypemodel.CIType{Name: "host", Alias: "host", UniqueID: 1},
		UniqueAttributeID: 1,
		Actor:             7,
	})
	if err == nil {
		t.Fatal("CreateType() with an injected statement failure = nil error, want error")
	}

	// The FIRST statement (the type row) must have been rolled back too.
	var types int64
	if err := db.Model(&citypemodel.CIType{}).Count(&types).Error; err != nil {
		t.Fatal(err)
	}
	if types != 0 {
		t.Fatalf("CI type rows = %d after a rolled-back create, want 0 — the type survived "+
			"while its binding failed", types)
	}
	var history int64
	if err := db.Model(&cimodel.TypeHistory{}).Count(&history).Error; err != nil {
		t.Fatal(err)
	}
	if history != 0 {
		t.Fatalf("history rows = %d after a rolled-back create, want 0", history)
	}
}

func TestUpdateTypeRebindsAndMarksRequired(t *testing.T) {
	repo, db := newWriteRepo(t)
	first := seedAttr(t, db, "hostname")
	second := seedAttr(t, db, "serial")
	ct := mustType(t, repo, "host", first.ID)

	// Pre-bind the second attribute as NOT required, and confirm the update
	// flips it rather than adding a duplicate binding.
	if err := db.Create(&citypemodel.CITypeAttribute{
		TypeID: ct.ID, AttrID: second.ID, IsRequired: false, DefaultShow: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	updated := ct
	updated.UniqueID = second.ID
	if err := repo.UpdateType(context.Background(), citypebiz.UpdateTypeInput{
		Type: updated, UniqueAttributeID: second.ID, Previous: ct, Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateType() error = %v", err)
	}

	var bindings []citypemodel.CITypeAttribute
	if err := db.Where("type_id = ? AND attr_id = ?", ct.ID, second.ID).Find(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings for the new unique attribute = %d, want exactly 1 (no duplicate)", len(bindings))
	}
	if !bindings[0].IsRequired {
		t.Fatal("the new unique attribute must be marked required")
	}
}

func TestUpdateTypeWritesHistoryWithOldAndNew(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	updated := ct
	updated.Alias = "新主机"
	if err := repo.UpdateType(context.Background(), citypebiz.UpdateTypeInput{
		Type: updated, Previous: ct, Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateType() error = %v", err)
	}

	var rows []cimodel.TypeHistory
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("history rows = %d, want 2 (create + update)", len(rows))
	}
	last := rows[len(rows)-1]
	if last.Change["old"] == nil || last.Change["new"] == nil {
		t.Fatalf("update history change = %#v, want both old and new", last.Change)
	}
}

func TestSetEnabledPersists(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)
	ctx := context.Background()

	if err := repo.SetEnabled(ctx, ct.ID, false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	got, err := repo.GetType(ctx, ct.ID)
	if err != nil || got == nil {
		t.Fatalf("GetType() = %+v, %v", got, err)
	}
	if got.Enabled {
		t.Fatal("enabled was not persisted")
	}
	if err := repo.SetEnabled(ctx, 99999, false); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("SetEnabled(missing) error = %v, want ErrNotFound", err)
	}
}

// --- delete guards ---

func TestDeleteRefusesWhenInstancesExist(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	if err := db.Create(&cimodel.CI{TypeID: ct.ID}).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.DeleteType(context.Background(), citypebiz.DeleteTypeInput{
		ID: ct.ID, Actor: 7, Previous: ct,
	})
	if !errors.Is(err, citypebiz.ErrTypeHasInstances) || !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("error = %v, want ErrTypeHasInstances wrapping ErrConflict", err)
	}

	// The refusal must leave the model fully intact.
	if got, err := repo.GetType(context.Background(), ct.ID); err != nil || got == nil {
		t.Fatalf("the model was removed despite the refusal: %+v, %v", got, err)
	}
}

// The refusal must be ATOMIC: a guard that fires after other cascade steps had
// run would leave the model half-deleted — cantor's exact failure mode, since
// it committed the cascade before deleting the type.
func TestDeleteRefusalLeavesEverythingIntact(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)
	if err := db.Create(&cimodel.CI{TypeID: ct.ID}).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteType(context.Background(), citypebiz.DeleteTypeInput{
		ID: ct.ID, Actor: 7, Previous: ct,
	}); err == nil {
		t.Fatal("DeleteType() with instances = nil error, want a refusal")
	}

	var bindings int64
	if err := db.Model(&citypemodel.CITypeAttribute{}).Where("type_id = ?", ct.ID).Count(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if bindings == 0 {
		t.Fatal("attribute bindings were deleted before the guard fired — the cascade is not atomic")
	}
	var history int64
	if err := db.Model(&cimodel.TypeHistory{}).Count(&history).Error; err != nil {
		t.Fatal(err)
	}
	if history != 1 {
		t.Fatalf("history rows = %d, want 1 (the create only)", history)
	}
}

func TestDeleteRefusesWhenModelIsAParent(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	parent := mustType(t, repo, "parent", attr.ID)
	mustType(t, repo, "child", attr.ID, parent.ID)

	err := repo.DeleteType(context.Background(), citypebiz.DeleteTypeInput{
		ID: parent.ID, Actor: 7, Previous: parent,
	})
	if !errors.Is(err, citypebiz.ErrTypeIsParent) {
		t.Fatalf("error = %v, want ErrTypeIsParent", err)
	}

	// A CHILD of another model is deletable, which is cantor's asymmetry:
	// those edges are cascaded instead.
	child, err2 := repo.GetTypeByName(context.Background(), "child")
	if err2 != nil || child == nil {
		t.Fatalf("GetTypeByName(child) = %+v, %v", child, err2)
	}
	if err := repo.DeleteType(context.Background(), citypebiz.DeleteTypeInput{
		ID: child.ID, Actor: 7, Previous: *child,
	}); err != nil {
		t.Fatalf("deleting a child model error = %v, want nil", err)
	}
}

func TestDeleteRefusesWhenAnAttributeReferencesTheModel(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	ref := seedAttr(t, db, "owner_host")
	if err := db.Model(&citypemodel.Attribute{}).Where("id = ?", ref.ID).
		Updates(map[string]any{"is_reference": true, "reference_type_id": ct.ID}).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.DeleteType(context.Background(), citypebiz.DeleteTypeInput{
		ID: ct.ID, Actor: 7, Previous: ct,
	})
	if !errors.Is(err, citypebiz.ErrTypeIsReferenced) {
		t.Fatalf("error = %v, want ErrTypeIsReferenced", err)
	}
}

// --- delete cascade ---

func TestDeleteCascadesDependantsAndPreservesHistory(t *testing.T) {
	repo, db := newWriteRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	// Everything that hangs off a model, so the cascade has work to do.
	group := citypemodel.CITypeAttributeGroup{Name: "g", TypeID: ct.ID}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&citypemodel.CITypeAttributeGroupItem{GroupID: group.ID, AttrID: attr.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&citypemodel.CITypeGroupItem{GroupID: 1, TypeID: ct.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&citypemodel.CITypeUniqueConstraint{TypeID: ct.ID, AttrIDs: citypemodel.IDList{attr.ID}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&citypemodel.CITypeTrigger{TypeID: ct.ID}).Error; err != nil {
		t.Fatal(err)
	}
	other := mustType(t, repo, "other", attr.ID)
	if err := db.Create(&citypemodel.CITypeRelation{
		ParentID: ct.ID, ChildID: other.ID, RelationTypeID: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteType(ctx, citypebiz.DeleteTypeInput{ID: ct.ID, Actor: 7, Previous: ct}); err != nil {
		t.Fatalf("DeleteType() error = %v", err)
	}

	// The model is gone from live reads...
	if got, err := repo.GetType(ctx, ct.ID); err != nil || got != nil {
		t.Fatalf("GetType() after delete = %+v, %v; want nil", got, err)
	}

	// ...and so is everything that hung off it.
	checks := []struct {
		name  string
		model any
		where string
		arg   any
	}{
		{"attribute bindings", &citypemodel.CITypeAttribute{}, "type_id = ?", ct.ID},
		{"attribute groups", &citypemodel.CITypeAttributeGroup{}, "type_id = ?", ct.ID},
		{"attribute group items", &citypemodel.CITypeAttributeGroupItem{}, "group_id = ?", group.ID},
		{"type group items", &citypemodel.CITypeGroupItem{}, "type_id = ?", ct.ID},
		{"unique constraints", &citypemodel.CITypeUniqueConstraint{}, "type_id = ?", ct.ID},
		{"triggers", &citypemodel.CITypeTrigger{}, "type_id = ?", ct.ID},
		{"type relations as parent", &citypemodel.CITypeRelation{}, "parent_id = ?", ct.ID},
		{"inheritance as child", &citypemodel.CITypeInheritance{}, "child_id = ?", ct.ID},
	}
	for _, c := range checks {
		var n int64
		if err := db.Model(c.model).Where(c.where, c.arg).Count(&n).Error; err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n != 0 {
			t.Errorf("%s: %d live row(s) survived the cascade", c.name, n)
		}
	}

	// The OTHER model must survive: the cascade is scoped to the deleted type.
	if got, err := repo.GetType(ctx, other.ID); err != nil || got == nil {
		t.Fatalf("the sibling model was collateral damage: %+v, %v", got, err)
	}

	// The audit trail must survive its subject, including the delete record.
	var history int64
	if err := db.Model(&cimodel.TypeHistory{}).Where("type_id = ?", ct.ID).Count(&history).Error; err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("history rows for the deleted model = %d, want 2 (create + delete)", history)
	}
}

func TestDeleteMissingReportsNotFound(t *testing.T) {
	repo, _ := newWriteRepo(t)

	err := repo.DeleteType(context.Background(), citypebiz.DeleteTypeInput{ID: 99999, Actor: 7})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestFindAttributeByIDAndName(t *testing.T) {
	repo, db := newWriteRepo(t)
	attr := seedAttr(t, db, "hostname")
	ctx := context.Background()

	byName, err := repo.FindAttribute(ctx, "hostname")
	if err != nil || byName == nil || byName.ID != attr.ID {
		t.Fatalf("FindAttribute(name) = %+v, %v", byName, err)
	}
	byID, err := repo.FindAttribute(ctx, "1")
	if err != nil || byID == nil || byID.ID != attr.ID {
		t.Fatalf("FindAttribute(id) = %+v, %v", byID, err)
	}
	if got, err := repo.FindAttribute(ctx, "nope"); err != nil || got != nil {
		t.Fatalf("FindAttribute(missing) = %+v, %v; want nil", got, err)
	}
	if got, err := repo.FindAttribute(ctx, ""); err != nil || got != nil {
		t.Fatalf("FindAttribute(\"\") = %+v, %v; want nil", got, err)
	}
}
