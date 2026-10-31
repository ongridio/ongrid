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
	valuemodel "github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// bindAttr attaches an attribute to a type directly, bypassing the usecase.
func bindAttr(t *testing.T, db *gorm.DB, typeID, attrID model.ID) {
	t.Helper()
	row := citypemodel.CITypeAttribute{TypeID: typeID, AttrID: attrID, DefaultShow: true}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("bind attribute %d to type %d: %v", attrID, typeID, err)
	}
}

// valueText writes one text value for a CI and attribute.
func valueText(t *testing.T, db *gorm.DB, ciID, attrID model.ID, v string) {
	t.Helper()
	if err := db.Create(&valuemodel.ValueText{CIID: ciID, AttrID: attrID, Value: v}).Error; err != nil {
		t.Fatalf("write value: %v", err)
	}
}

func liveBindings(t *testing.T, db *gorm.DB, typeID, attrID model.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&citypemodel.CITypeAttribute{}).
		Where("type_id = ? AND attr_id = ?", typeID, attrID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func unbind(t *testing.T, repo *Repo, typeID model.ID, attrIDs ...model.ID) (citypebiz.BindingsDeleteResult, error) {
	t.Helper()
	return repo.DeleteBindings(context.Background(), citypebiz.BindingsDeleteSpec{
		TypeID: typeID, AttrIDs: attrIDs, Actor: 7,
	})
}

// THE HEADLINE DATA-LOSS FIX.
//
// cantor's unbind walked the descendant tree and deleted the attribute's values
// on every descendant, without asking whether the descendant bound the
// attribute itself. A descendant that did kept the attribute on its form — its
// own binding row was never touched and the read path surfaces it — while every
// value it held had been deleted. One request about the parent silently emptied
// a model the request never mentioned.
func TestUnbindKeepsValuesOnADescendantThatBindsTheAttributeItself(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	shared := seedTypedAttr(t, db, citypemodel.Attribute{Name: "cpu_count", ValueType: "2"})

	parent := mustType(t, repo, "parent", uniqueAttr.ID)
	child := mustType(t, repo, "child", uniqueAttr.ID, parent.ID)
	// The child binds the attribute ITSELF, on top of inheriting it.
	bindAttr(t, db, parent.ID, shared.ID)
	bindAttr(t, db, child.ID, shared.ID)

	parentCI := seedCI(t, db, parent.ID)
	childCI := seedCI(t, db, child.ID)
	valueText(t, db, parentCI, shared.ID, "8")
	valueText(t, db, childCI, shared.ID, "16")

	res, err := unbind(t, repo, parent.ID, shared.ID)
	if err != nil {
		t.Fatalf("DeleteBindings() error = %v", err)
	}

	// The parent loses the attribute and its value.
	if got := liveBindings(t, db, parent.ID, shared.ID); got != 0 {
		t.Fatalf("parent bindings = %d, want 0", got)
	}
	if got := countRawValues(t, db, valuemodel.TableTexts, shared.ID); got != 1 {
		t.Fatalf("values remaining = %d, want 1 — the child keeps the attribute, so it keeps its data", got)
	}
	// The child keeps both.
	if got := liveBindings(t, db, child.ID, shared.ID); got != 1 {
		t.Fatalf("child bindings = %d, want 1 — its own binding was never the subject of the request", got)
	}
	for _, id := range res.AffectedTypes {
		if id == child.ID {
			t.Fatalf("AffectedTypes = %v, want the child excluded: it does not lose the attribute", res.AffectedTypes)
		}
	}
	if res.DeletedValues != 1 {
		t.Fatalf("DeletedValues = %d, want exactly the parent's one value", res.DeletedValues)
	}
}

// The other half: a descendant that only INHERITS the attribute does lose it,
// and its values go with it. That is the defined semantics of unbinding, and it
// is the case cantor's loop was written for.
func TestUnbindErasesValuesOnDescendantsThatInheritIt(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	shared := seedTypedAttr(t, db, citypemodel.Attribute{Name: "cpu_count", ValueType: "2"})

	parent := mustType(t, repo, "parent", uniqueAttr.ID)
	child := mustType(t, repo, "child", uniqueAttr.ID, parent.ID)
	bindAttr(t, db, parent.ID, shared.ID)

	parentCI := seedCI(t, db, parent.ID)
	childCI := seedCI(t, db, child.ID)
	valueText(t, db, parentCI, shared.ID, "8")
	valueText(t, db, childCI, shared.ID, "16")

	res, err := unbind(t, repo, parent.ID, shared.ID)
	if err != nil {
		t.Fatalf("DeleteBindings() error = %v", err)
	}
	if got := countRawValues(t, db, valuemodel.TableTexts, shared.ID); got != 0 {
		t.Fatalf("values remaining = %d, want 0: the child inherited the attribute and loses it", got)
	}
	if res.DeletedValues != 2 {
		t.Fatalf("DeletedValues = %d, want 2 (parent and child)", res.DeletedValues)
	}
	found := false
	for _, id := range res.AffectedTypes {
		found = found || id == child.ID
	}
	if !found {
		t.Fatalf("AffectedTypes = %v, want the child included", res.AffectedTypes)
	}
}

// THE ATOMICITY FIX. cantor evaluated its guards inside the per-attribute loop
// and committed once per iteration, so `attr_id=A,U` — U being the model's
// unique attribute — deleted A, committed, and then answered 400. The caller
// was told nothing had happened.
//
// WHAT THIS TEST PINS, precisely: that a refused multi-attribute unbind changes
// nothing. That is the property cantor violated, and it is the property worth
// pinning.
//
// It does NOT distinguish the port's two structural changes from each other.
// Ordering the guards before all mutations and keeping them inside the loop are
// equivalent while the whole request is one transaction — the transaction is
// what actually defeats cantor's failure, and the phase split only becomes
// load-bearing if that boundary is ever relaxed. Verified by mutation: moving
// the guards back inside the loop leaves this test green, removing the guard
// turns it red.
func TestUnbindIsAtomicAcrossAttributes(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	doomed := seedTypedAttr(t, db, citypemodel.Attribute{Name: "cpu_count", ValueType: "2"})

	// mustType binds uniqueAttr as required, so it is genuinely the type's
	// unique attribute AND a live binding.
	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, doomed.ID)
	ci := seedCI(t, db, ct.ID)
	valueText(t, db, ci, doomed.ID, "8")

	_, err := unbind(t, repo, ct.ID, doomed.ID, uniqueAttr.ID)
	if !errors.Is(err, citypebiz.ErrBindingUniqueAttr) {
		t.Fatalf("DeleteBindings() error = %v, want ErrBindingUniqueAttr", err)
	}

	// Nothing at all may have happened. The doomed attribute is listed FIRST,
	// which is exactly the ordering cantor corrupted.
	if got := liveBindings(t, db, ct.ID, doomed.ID); got != 1 {
		t.Fatalf("bindings for the first attribute = %d, want 1: the request was refused, so no part of it ran", got)
	}
	if got := countRawValues(t, db, valuemodel.TableTexts, doomed.ID); got != 1 {
		t.Fatalf("values for the first attribute = %d, want 1: refusing must not delete anything", got)
	}
}

func TestUnbindRefusesTheModelsDefaultSortAttribute(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	sortAttr := seedTypedAttr(t, db, citypemodel.Attribute{Name: "cpu_count", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, sortAttr.ID)
	// cantor's column holds the attribute NAME, optionally with a '-' prefix
	// for descending, which is why the guard is a name comparison.
	desc := "-cpu_count"
	if err := db.Model(&citypemodel.CIType{}).Where("id = ?", ct.ID).
		Update("default_order_attr", desc).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := unbind(t, repo, ct.ID, sortAttr.ID); !errors.Is(err, citypebiz.ErrBindingDefaultOrderAttr) {
		t.Fatalf("DeleteBindings() error = %v, want ErrBindingDefaultOrderAttr", err)
	}
}

// The guard cantor did not have: the cascade reaches descendants, and a
// descendant's sort key is its own. Deleting the attribute from the parent
// would leave the child sorting by an attribute it no longer carries.
func TestUnbindRefusesADescendantsDefaultSortAttribute(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	sortAttr := seedTypedAttr(t, db, citypemodel.Attribute{Name: "cpu_count", ValueType: "2"})

	parent := mustType(t, repo, "parent", uniqueAttr.ID)
	child := mustType(t, repo, "child", uniqueAttr.ID, parent.ID)
	bindAttr(t, db, parent.ID, sortAttr.ID)
	if err := db.Model(&citypemodel.CIType{}).Where("id = ?", child.ID).
		Update("default_order_attr", "cpu_count").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := unbind(t, repo, parent.ID, sortAttr.ID); !errors.Is(err, citypebiz.ErrBindingDefaultOrderAttr) {
		t.Fatalf("DeleteBindings() error = %v, want the descendant's sort key to guard the request", err)
	}
}

func TestUnbindClosesGroupItemGaps(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	first := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	middle := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})
	last := seedTypedAttr(t, db, citypemodel.Attribute{Name: "c", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	for _, a := range []model.ID{first.ID, middle.ID, last.ID} {
		bindAttr(t, db, ct.ID, a)
	}
	group := citypemodel.CITypeAttributeGroup{Name: "硬件", TypeID: ct.ID}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	for i, a := range []model.ID{first.ID, middle.ID, last.ID} {
		item := citypemodel.CITypeAttributeGroupItem{
			GroupID: group.ID, AttrID: a, Order: model.SmallInt(i),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}

	if _, err := unbind(t, repo, ct.ID, middle.ID); err != nil {
		t.Fatalf("DeleteBindings() error = %v", err)
	}

	var items []citypemodel.CITypeAttributeGroupItem
	if err := db.Where("group_id = ?", group.ID).Order("id").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("group items = %d, want 2", len(items))
	}
	// cantor's delete_item shifted the later item down, so the remaining
	// positions stay contiguous.
	if items[1].Order != 1 {
		t.Fatalf("last item order = %d, want 1 after the gap was closed", items[1].Order)
	}
}

func TestUnbindStripsUniqueConstraints(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	b := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, a.ID)
	bindAttr(t, db, ct.ID, b.ID)

	// One constraint with a spare attribute, one that will become empty.
	keep := citypemodel.CITypeUniqueConstraint{TypeID: ct.ID, AttrIDs: citypemodel.IDList{a.ID, b.ID}}
	drop := citypemodel.CITypeUniqueConstraint{TypeID: ct.ID, AttrIDs: citypemodel.IDList{a.ID}}
	if err := db.Create(&keep).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&drop).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := unbind(t, repo, ct.ID, a.ID); err != nil {
		t.Fatalf("DeleteBindings() error = %v", err)
	}

	var gotKeep citypemodel.CITypeUniqueConstraint
	if err := db.First(&gotKeep, keep.ID).Error; err != nil {
		t.Fatalf("the constraint with a spare attribute was removed: %v", err)
	}
	if len(gotKeep.AttrIDs) != 1 || gotKeep.AttrIDs[0] != b.ID {
		t.Fatalf("constraint attributes = %v, want just b", gotKeep.AttrIDs)
	}
	var liveDropped int64
	if err := db.Model(&citypemodel.CITypeUniqueConstraint{}).Where("id = ?", drop.ID).
		Count(&liveDropped).Error; err != nil {
		t.Fatal(err)
	}
	if liveDropped != 0 {
		t.Fatal("a constraint left with no attributes must be removed, not kept as a constraint over nothing")
	}
}

func TestUnbindClearsTheDisplayAttribute(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	show := seedTypedAttr(t, db, citypemodel.Attribute{Name: "display_name", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, show.ID)
	showID := show.ID
	if err := db.Model(&citypemodel.CIType{}).Where("id = ?", ct.ID).
		Update("show_id", showID).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := unbind(t, repo, ct.ID, show.ID); err != nil {
		t.Fatalf("DeleteBindings() error = %v", err)
	}

	var stored citypemodel.CIType
	if err := db.First(&stored, ct.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ShowID != nil {
		t.Fatalf("show_id = %d, want NULL: a model must not label its instances with a missing attribute", *stored.ShowID)
	}
}

func TestUnbindRecordsHistoryOnlyForRealUnbindings(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	bound := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	unbound := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, bound.ID)

	if _, err := unbind(t, repo, ct.ID, bound.ID, unbound.ID); err != nil {
		t.Fatalf("DeleteBindings() error = %v", err)
	}

	var rows []cimodel.TypeHistory
	if err := db.Where("type_id = ?", ct.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, h := range rows {
		if h.OperateType != nil && *h.OperateType == string(model.CITypeOpDeleteAttribute) {
			if h.AttrID == nil || *h.AttrID != bound.ID {
				t.Fatalf("history names attribute %v; cantor wrote a DELETE_ATTRIBUTE row even for an "+
					"attribute the model never carried", h.AttrID)
			}
		}
	}
}

func TestAddBindingsIsIdempotentAndSkipsExisting(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	b := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	ctx := context.Background()

	first, err := repo.AddBindings(ctx, citypebiz.BindingsAddSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{a.ID, b.ID}, DefaultShow: true, Actor: 7,
	})
	if err != nil {
		t.Fatalf("AddBindings() error = %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first call bound %v, want both", first)
	}

	second, err := repo.AddBindings(ctx, citypebiz.BindingsAddSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{a.ID, b.ID}, DefaultShow: true, Actor: 7,
	})
	if err != nil {
		t.Fatalf("AddBindings() second call error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second call bound %v, want nothing: binding is idempotent", second)
	}
	if got := liveBindings(t, db, ct.ID, a.ID); got != 1 {
		t.Fatalf("bindings for a = %d, want 1", got)
	}
}

func TestUpdateBindingsReportsSkippedAttributes(t *testing.T) {
	repo, db := newAttrRepo(t)
	uniqueAttr := seedAttr(t, db, "hostname")
	bound := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	notBound := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, bound.ID)

	required := true
	applied, skipped, err := repo.UpdateBindings(context.Background(), citypebiz.BindingsUpdateSpec{
		TypeID: ct.ID,
		Changes: []citypebiz.BindingChange{
			{AttrID: bound.ID, IsRequired: &required},
			{AttrID: notBound.ID, IsRequired: &required},
		},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("UpdateBindings() error = %v", err)
	}
	if len(applied) != 1 || applied[0] != bound.ID {
		t.Fatalf("applied = %v, want just the bound attribute", applied)
	}
	if len(skipped) != 1 || skipped[0] != notBound.ID {
		t.Fatalf("skipped = %v, want the unbound attribute reported rather than silently ignored", skipped)
	}
}

// --- the inheritance graph ---

func TestAncestorIDsRootFirst(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	g := mustType(t, repo, "grandparent", attr.ID)
	p := mustType(t, repo, "parent", attr.ID, g.ID)
	c := mustType(t, repo, "child", attr.ID, p.ID)

	got, err := repo.AncestorIDs(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("AncestorIDs() error = %v", err)
	}
	// Root-most first, nearest parent last: the order the effective attribute
	// set's field ordering depends on.
	if len(got) != 2 || got[0] != g.ID || got[1] != p.ID {
		t.Fatalf("AncestorIDs() = %v, want [%d %d]", got, g.ID, p.ID)
	}
}

func TestDescendantIDsTerminateOnACycle(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	a := mustType(t, repo, "a", attr.ID)
	b := mustType(t, repo, "b", attr.ID, a.ID)
	// A cycle nothing prevents: cantor's walk had no visited set, so this hung
	// the request rather than failing.
	if err := db.Create(&citypemodel.CITypeInheritance{ParentID: b.ID, ChildID: a.ID}).Error; err != nil {
		t.Fatal(err)
	}

	got, err := repo.DescendantIDs(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("DescendantIDs() error = %v", err)
	}
	if len(got) != 1 || got[0] != b.ID {
		t.Fatalf("DescendantIDs() = %v, want just b with the cycle dropped", got)
	}
}

func TestBindingsForTypesReturnsNothingForAnEmptySet(t *testing.T) {
	repo, _ := newAttrRepo(t)
	got, err := repo.BindingsForTypes(context.Background(), nil)
	if err != nil {
		t.Fatalf("BindingsForTypes() error = %v", err)
	}
	if got != nil {
		t.Fatalf("BindingsForTypes(nil) = %+v, want nil", got)
	}
}
