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

func field(attrID model.ID, fixed bool) citypebiz.SubscribedFieldSpec {
	id := attrID
	return citypebiz.SubscribedFieldSpec{AttrID: &id, IsFixed: fixed}
}

func builtinField(name string) citypebiz.SubscribedFieldSpec {
	return citypebiz.SubscribedFieldSpec{Builtin: name}
}

func showRows(t *testing.T, db *gorm.DB, uid, typeID model.ID) []citypemodel.PreferenceShowAttributes {
	t.Helper()
	var rows []citypemodel.PreferenceShowAttributes
	if err := db.Where("uid = ? AND type_id = ?", uid, typeID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// --- subscriptions ---

// The write is one transaction and REPLACES the set, rather than adding to it.
//
// cantor wrote each field with its own commit and decided afterwards which old
// rows to drop, so a failure in that second pass left the new fields added to
// the old ones — a subscription the console cannot produce intentionally.
func TestReplaceShowAttributesIsAWholesaleReplace(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	second := seedAttr(t, db, "cpu")
	ctx := context.Background()

	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID,
		Fields: []citypebiz.SubscribedFieldSpec{
			field(unique.ID, true), field(second.ID, false),
		},
	}); err != nil {
		t.Fatalf("ReplaceShowAttributes() error = %v", err)
	}
	if got := showRows(t, db, 7, ct.ID); len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}

	// The second write drops cpu and keeps hostname, in one position.
	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID,
		Fields: []citypebiz.SubscribedFieldSpec{field(unique.ID, false), builtinField("_updated_at")},
	}); err != nil {
		t.Fatalf("second ReplaceShowAttributes() error = %v", err)
	}

	rows := showRows(t, db, 7, ct.ID)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 — the dropped field must be gone", len(rows))
	}
	if rows[0].AttrID == nil || *rows[0].AttrID != unique.ID || rows[0].Order != 0 {
		t.Fatalf("first row = %+v, want the attribute at position 0", rows[0])
	}
	if rows[0].IsFixed {
		t.Fatal("is_fixed was not updated back to false")
	}
	if rows[1].BuiltinAttr == nil || *rows[1].BuiltinAttr != "_updated_at" || rows[1].Order != 1 {
		t.Fatalf("second row = %+v, want the built-in column at position 1", rows[1])
	}
}

// Positions come from the list index, so two fields cannot share one and a
// reorder is expressible without touching the fields themselves.
func TestReplaceShowAttributesAssignsPositionsFromTheList(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	second := seedAttr(t, db, "cpu")
	ctx := context.Background()

	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID,
		Fields: []citypebiz.SubscribedFieldSpec{field(unique.ID, false), field(second.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID,
		Fields: []citypebiz.SubscribedFieldSpec{field(second.ID, false), field(unique.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}

	rows := showRows(t, db, 7, ct.ID)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the same two", len(rows))
	}
	byAttr := map[model.ID]model.SmallInt{}
	for _, r := range rows {
		byAttr[*r.AttrID] = r.Order
	}
	if byAttr[second.ID] != 0 || byAttr[unique.ID] != 1 {
		t.Fatalf("positions = %v, want the reordered list", byAttr)
	}
}

func TestReplaceShowAttributesRefusesAMissingModel(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")

	err := repo.ReplaceShowAttributes(context.Background(), citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: 9999,
		Fields: []citypebiz.SubscribedFieldSpec{field(attr.ID, false)},
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want not-found: cantor's write never checked and so could leave a "+
			"subscription pointing at a model that does not exist", err)
	}
}

// A subscription is per-user: one person's write must not touch another's.
func TestReplaceShowAttributesIsScopedToTheUser(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	second := seedAttr(t, db, "cpu")
	ctx := context.Background()

	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID, Fields: []citypebiz.SubscribedFieldSpec{field(unique.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 8, TypeID: ct.ID, Fields: []citypebiz.SubscribedFieldSpec{field(second.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}

	if got := showRows(t, db, 7, ct.ID); len(got) != 1 || *got[0].AttrID != unique.ID {
		t.Fatalf("user 7 rows = %+v, want their own subscription", got)
	}
	if got := showRows(t, db, 8, ct.ID); len(got) != 1 || *got[0].AttrID != second.ID {
		t.Fatalf("user 8 rows = %+v", got)
	}
}

// cantor cleaned these on unbind and the port must too: a subscription row
// naming an attribute the model no longer carries renders as a column with no
// data behind it.
func TestDeleteShowAttributesForAttrRemovesTheField(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	second := seedAttr(t, db, "cpu")
	ctx := context.Background()

	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID,
		Fields: []citypebiz.SubscribedFieldSpec{field(unique.ID, false), field(second.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteShowAttributesForAttr(ctx, []model.ID{ct.ID}, second.ID); err != nil {
		t.Fatalf("DeleteShowAttributesForAttr() error = %v", err)
	}

	rows := showRows(t, db, 7, ct.ID)
	if len(rows) != 1 || *rows[0].AttrID != unique.ID {
		t.Fatalf("rows = %+v, want only the untouched field", rows)
	}
}

// --- model ordering ---

func TestUpsertTypeOrderReplacesTheOrdering(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	a := mustType(t, repo, "a", attr.ID)
	b := mustType(t, repo, "b", attr.ID)
	c := mustType(t, repo, "c", attr.ID)
	ctx := context.Background()

	if err := repo.UpsertTypeOrder(ctx, citypebiz.TypeOrderSpec{UID: 7, TypeIDs: []model.ID{c.ID, a.ID}}); err != nil {
		t.Fatalf("UpsertTypeOrder() error = %v", err)
	}
	if err := repo.UpsertTypeOrder(ctx, citypebiz.TypeOrderSpec{UID: 7, TypeIDs: []model.ID{b.ID, c.ID}}); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.TypeOrder(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	got := map[model.ID]model.SmallInt{}
	for _, r := range rows {
		if r.TypeID != nil {
			got[*r.TypeID] = r.Order
		}
	}
	if len(got) != 2 || got[b.ID] != 0 || got[c.ID] != 1 {
		t.Fatalf("order = %v, want b then c and a removed", got)
	}
}

// The two orderings a user holds — resource and tree — are independent.
func TestTypeOrderKeepsTheTwoOrderingsApart(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	a := mustType(t, repo, "a", attr.ID)
	b := mustType(t, repo, "b", attr.ID)
	ctx := context.Background()

	if err := repo.UpsertTypeOrder(ctx, citypebiz.TypeOrderSpec{UID: 7, TypeIDs: []model.ID{a.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTypeOrder(ctx, citypebiz.TypeOrderSpec{UID: 7, TypeIDs: []model.ID{b.ID}, IsTree: true}); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.TypeOrder(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want one per ordering", len(rows))
	}
	for _, r := range rows {
		if r.TypeID == nil {
			continue
		}
		if r.IsTree && *r.TypeID != b.ID {
			t.Fatalf("tree order = %d, want b", *r.TypeID)
		}
		if !r.IsTree && *r.TypeID != a.ID {
			t.Fatalf("resource order = %d, want a", *r.TypeID)
		}
	}
}

// A subscription keeps the ordering entry in step, and dropping the last field
// removes it — otherwise a model the user unsubscribed from keeps its place in
// the sidebar ordering.
func TestSubscriptionKeepsTheOrderingEntryInStep(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	ctx := context.Background()

	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID, Fields: []citypebiz.SubscribedFieldSpec{field(unique.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.TypeOrder(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("order rows = %d, want the subscription to create one", len(rows))
	}

	if err := repo.ClearShowAttributes(ctx, 7, ct.ID); err != nil {
		t.Fatalf("ClearShowAttributes() error = %v", err)
	}
	rows, err = repo.TypeOrder(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("order rows = %d, want the entry removed with the subscription", len(rows))
	}
}

// --- auto-subscription ---

func TestAutoSubscriptionUpsertsOnUID(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	ct := mustType(t, repo, "host", attr.ID)
	ctx := context.Background()

	first, err := repo.SaveAutoSubscription(ctx, citypebiz.AutoSubscriptionSpec{
		UID: 7, Strategy: citypebiz.SubscribeNone, GroupIDs: []model.ID{1}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveAutoSubscription() error = %v", err)
	}
	second, err := repo.SaveAutoSubscription(ctx, citypebiz.AutoSubscriptionSpec{
		UID: 7, Strategy: citypebiz.SubscribeAll, TypeIDs: []model.ID{ct.ID}, Enabled: false,
		Description: "changed",
	})
	if err != nil {
		t.Fatalf("second SaveAutoSubscription() error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids %d and %d differ, want the same row edited: uid is unique", first.ID, second.ID)
	}

	loaded, err := repo.AutoSubscription(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BaseStrategy != string(citypebiz.SubscribeAll) {
		t.Fatalf("strategy = %q", loaded.BaseStrategy)
	}
	if loaded.Enabled {
		t.Fatal("enabled was not updated")
	}
	// The JSON id lists round-trip as arrays.
	if len(loaded.TypeIDs) != 1 || loaded.TypeIDs[0] != ct.ID {
		t.Fatalf("type_ids = %v", loaded.TypeIDs)
	}
	if len(loaded.GroupIDs) != 0 {
		t.Fatalf("group_ids = %v, want cleared by the second write", loaded.GroupIDs)
	}
	if loaded.Description == nil || *loaded.Description != "changed" {
		t.Fatalf("description = %v", loaded.Description)
	}
}

func TestAutoSubscriptionIsPerUser(t *testing.T) {
	repo, _ := newAttrRepo(t)
	ctx := context.Background()

	if _, err := repo.SaveAutoSubscription(ctx, citypebiz.AutoSubscriptionSpec{
		UID: 7, Strategy: citypebiz.SubscribeAll, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	other, err := repo.AutoSubscription(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Fatalf("user 8 has a rule: %+v", other)
	}
	if err := repo.DeleteAutoSubscription(ctx, 7); err != nil {
		t.Fatal(err)
	}
	gone, err := repo.AutoSubscription(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if gone != nil {
		t.Fatalf("rule survived deletion: %+v", gone)
	}
}

// --- the model-delete cascade ---

// cantor cascaded these, and leaving them behind is what makes a sidebar fail
// to render: the subscription outlives the model it points at.
func TestDeleteTypeCascadesPreferences(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	ctx := context.Background()

	if err := repo.ReplaceShowAttributes(ctx, citypebiz.ShowAttributesSpec{
		UID: 7, TypeID: ct.ID, Fields: []citypebiz.SubscribedFieldSpec{field(unique.ID, false)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveAutoSubscription(ctx, citypebiz.AutoSubscriptionSpec{
		UID: 7, Strategy: citypebiz.SubscribeNone, TypeIDs: []model.ID{ct.ID}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteType(ctx, citypebiz.DeleteTypeInput{
		ID: ct.ID, Actor: 7, ActorIsSuperuser: true, Previous: ct,
	}); err != nil {
		t.Fatalf("DeleteType() error = %v", err)
	}

	if got := showRows(t, db, 7, ct.ID); len(got) != 0 {
		t.Fatalf("subscription rows = %d, want the model's cascade to remove them", len(got))
	}
	rows, err := repo.TypeOrder(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("order rows = %d, want them cascaded too", len(rows))
	}

	// The auto-subscription RULE survives, and that is deliberate: it is a
	// statement about sets, and a stale id inside it is inert because the rule
	// is always intersected with the models that still exist.
	rule, err := repo.AutoSubscription(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if rule == nil {
		t.Fatal("the auto-subscription rule was removed; it is a set rule, not a row about the model")
	}
}
