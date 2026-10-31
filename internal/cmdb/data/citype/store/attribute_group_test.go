package store

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// groupOf loads a section by name, failing when absent.
func groupOf(t *testing.T, db *gorm.DB, typeID model.ID, name string) citypemodel.CITypeAttributeGroup {
	t.Helper()
	var g citypemodel.CITypeAttributeGroup
	if err := db.Where("type_id = ? AND name = ?", typeID, name).First(&g).Error; err != nil {
		t.Fatalf("group %q: %v", name, err)
	}
	return g
}

// itemOrders returns the section's attributes in stored position order.
func itemOrders(t *testing.T, db *gorm.DB, groupID model.ID) []model.ID {
	t.Helper()
	var items []citypemodel.CITypeAttributeGroupItem
	if err := db.Where("group_id = ?", groupID).Order("id").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	// Sort by position to make the assertion say what it means.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].Order > items[j].Order; j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
	out := make([]model.ID, 0, len(items))
	for _, it := range items {
		out = append(out, it.AttrID)
	}
	return out
}

func TestSaveAttributeGroupUpsertsByName(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	first, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "网络",
	})
	if err != nil {
		t.Fatalf("SaveAttributeGroup() error = %v", err)
	}

	// Saving the same name again updates rather than duplicating: cantor's
	// console has one save button that does not know whether the section is
	// new, and both of its endpoints landed on the same helper.
	order := model.SmallInt(3)
	second, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "网络", Order: &order,
	})
	if err != nil {
		t.Fatalf("SaveAttributeGroup() second call error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("group ids %d and %d differ, want the same section updated", first.ID, second.ID)
	}

	var count int64
	if err := db.Model(&citypemodel.CITypeAttributeGroup{}).Where("type_id = ?", ct.ID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("groups = %d, want 1", count)
	}
	if got := groupOf(t, db, ct.ID, "网络"); got.Order != 3 {
		t.Fatalf("order = %d, want the supplied 3", got.Order)
	}
}

// An omitted order must not move a section. cantor defaulted it to 0 and wrote
// it unconditionally, so a request that merely renamed a section also sent it
// to the front of the form.
func TestSaveAttributeGroupLeavesOrderAloneWhenOmitted(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	seven := model.SmallInt(7)
	if _, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "网络", Order: &seven,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "网络",
	}); err != nil {
		t.Fatal(err)
	}
	if got := groupOf(t, db, ct.ID, "网络"); got.Order != 7 {
		t.Fatalf("order = %d, want 7 preserved: an omitted order means unchanged", got.Order)
	}
}

func TestSaveAttributeGroupAssignsPositionsFromTheList(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	uniqueAttr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	b := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})
	c := seedTypedAttr(t, db, citypemodel.Attribute{Name: "c", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	for _, x := range []model.ID{a.ID, b.ID, c.ID} {
		bindAttr(t, db, ct.ID, x)
	}

	members := []model.ID{c.ID, a.ID, b.ID}
	group, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "硬件", AttrOrder: &members,
	})
	if err != nil {
		t.Fatalf("SaveAttributeGroup() error = %v", err)
	}
	if got := itemOrders(t, db, group.ID); len(got) != 3 || got[0] != c.ID || got[1] != a.ID || got[2] != b.ID {
		t.Fatalf("membership = %v, want the list order c,a,b", got)
	}

	// A second save with fewer members must drop the missing one AND renumber
	// the survivors, since positions come from the index.
	fewer := []model.ID{b.ID}
	if _, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, GroupID: &group.ID, Name: "硬件", AttrOrder: &fewer,
	}); err != nil {
		t.Fatal(err)
	}
	if got := itemOrders(t, db, group.ID); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("membership = %v, want just b", got)
	}

	// An explicitly EMPTY list clears the membership. cantor could not express
	// this: an empty list was indistinguishable from an omitted one.
	empty := []model.ID{}
	if _, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, GroupID: &group.ID, Name: "硬件", AttrOrder: &empty,
	}); err != nil {
		t.Fatal(err)
	}
	if got := itemOrders(t, db, group.ID); len(got) != 0 {
		t.Fatalf("membership = %v, want cleared", got)
	}
}

// An omitted membership must leave the items alone, which is cantor's
// behaviour: its item sync was guarded on a non-empty list.
func TestSaveAttributeGroupLeavesMembershipAloneWhenOmitted(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	uniqueAttr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, a.ID)

	members := []model.ID{a.ID}
	group, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "硬件", AttrOrder: &members,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, GroupID: &group.ID, Name: "硬件",
	}); err != nil {
		t.Fatal(err)
	}
	if got := itemOrders(t, db, group.ID); len(got) != 1 {
		t.Fatalf("membership = %v, want preserved", got)
	}
}

func TestDeleteAttributeGroupRemovesItems(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	uniqueAttr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	ct := mustType(t, repo, "host", uniqueAttr.ID)
	bindAttr(t, db, ct.ID, a.ID)

	members := []model.ID{a.ID}
	group, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "硬件", AttrOrder: &members,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAttributeGroup(ctx, group.ID); err != nil {
		t.Fatalf("DeleteAttributeGroup() error = %v", err)
	}

	var items int64
	if err := db.Model(&citypemodel.CITypeAttributeGroupItem{}).
		Where("group_id = ?", group.ID).Count(&items).Error; err != nil {
		t.Fatal(err)
	}
	if items != 0 {
		t.Fatalf("items = %d, want 0", items)
	}
	if err := repo.DeleteAttributeGroup(ctx, group.ID); !errors.Is(err, citypebiz.ErrGroupNotFound) {
		t.Fatalf("second delete error = %v, want ErrGroupNotFound", err)
	}
}

func TestMoveAttributeBetweenGroupsClosesAndOpensGaps(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	uniqueAttr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	b := seedTypedAttr(t, db, citypemodel.Attribute{Name: "b", ValueType: "2"})
	c := seedTypedAttr(t, db, citypemodel.Attribute{Name: "c", ValueType: "2"})

	ct := mustType(t, repo, "host", uniqueAttr.ID)
	for _, x := range []model.ID{a.ID, b.ID, c.ID} {
		bindAttr(t, db, ct.ID, x)
	}

	src, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "源", AttrOrder: &[]model.ID{a.ID, b.ID, c.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "目标",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Move the middle attribute out. The source's remaining positions must be
	// contiguous, which is what cantor's delete_item shift produced.
	if err := repo.MoveAttribute(ctx, citypebiz.AttributeMoveSpec{
		TypeID: ct.ID, AttrID: b.ID, FromGroupID: &src.ID, ToGroupID: &dst.ID, Order: 0,
	}); err != nil {
		t.Fatalf("MoveAttribute() error = %v", err)
	}
	if got := itemOrders(t, db, src.ID); len(got) != 2 || got[0] != a.ID || got[1] != c.ID {
		t.Fatalf("source membership = %v, want a,c", got)
	}
	if got := itemOrders(t, db, dst.ID); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("destination membership = %v, want b", got)
	}
	var moved citypemodel.CITypeAttributeGroupItem
	if err := db.Where("group_id = ? AND attr_id = ?", dst.ID, b.ID).First(&moved).Error; err != nil {
		t.Fatal(err)
	}
	if moved.Order != 0 {
		t.Fatalf("moved position = %d, want 0", moved.Order)
	}
}

func TestMoveAttributeRequiresAGroup(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	a := seedTypedAttr(t, db, citypemodel.Attribute{Name: "a", ValueType: "2"})
	ct := mustType(t, repo, "host", attr.ID)
	bindAttr(t, db, ct.ID, a.ID)

	err := repo.MoveAttribute(context.Background(), citypebiz.AttributeMoveSpec{
		TypeID: ct.ID, AttrID: a.ID, Order: 0,
	})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("MoveAttribute() error = %v, want ErrInvalid when neither end is a section", err)
	}
}

// cantor resolved the two sections by id without consulting the model, so a
// request addressed to one model could reorder another's form.
func TestSwapAttributeGroupsRefusesAnotherModelsSection(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	mine := mustType(t, repo, "mine", attr.ID)
	theirs := mustType(t, repo, "theirs", attr.ID)

	myGroup, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{TypeID: mine.ID, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	theirGroup, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{TypeID: theirs.ID, Name: "b"})
	if err != nil {
		t.Fatal(err)
	}

	err = repo.SwapAttributeGroups(ctx, citypebiz.AttributeGroupSwapSpec{
		TypeID: mine.ID, FromGroupID: myGroup.ID, ToGroupID: theirGroup.ID,
	})
	if !errors.Is(err, citypebiz.ErrGroupNotInType) {
		t.Fatalf("SwapAttributeGroups() error = %v, want ErrGroupNotInType", err)
	}
}

func TestSwapAttributeGroupsExchangesPositions(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)

	one := model.SmallInt(1)
	two := model.SmallInt(2)
	first, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "a", Order: &one,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.SaveAttributeGroup(ctx, citypebiz.AttributeGroupSaveSpec{
		TypeID: ct.ID, Name: "b", Order: &two,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.SwapAttributeGroups(ctx, citypebiz.AttributeGroupSwapSpec{
		TypeID: ct.ID, FromGroupID: first.ID, ToGroupID: second.ID,
	}); err != nil {
		t.Fatalf("SwapAttributeGroups() error = %v", err)
	}
	if got := groupOf(t, db, ct.ID, "a"); got.Order != 2 {
		t.Fatalf("a order = %d, want 2", got.Order)
	}
	if got := groupOf(t, db, ct.ID, "b"); got.Order != 1 {
		t.Fatalf("b order = %d, want 1", got.Order)
	}
}
