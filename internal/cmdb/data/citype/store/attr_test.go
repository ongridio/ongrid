package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	cistore "github.com/ongridio/ongrid/internal/cmdb/data/ci/store"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	valuemodel "github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// newAttrRepo sets up a database with the model-definition tables AND the
// CI-side value tables, because attribute management writes into both: the
// dictionary and its bindings on one side, the EAV value tables on the other.
func newAttrRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate model tables: %v", err)
	}
	if err := cistore.Migrate(db); err != nil {
		t.Fatalf("migrate ci tables: %v", err)
	}
	return NewRepo(db), db
}

// seedTypedAttr creates an attribute with an explicit value type and flags.
func seedTypedAttr(t *testing.T, db *gorm.DB, a citypemodel.Attribute) citypemodel.Attribute {
	t.Helper()
	if a.Alias == "" {
		a.Alias = a.Name
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("create attribute %s: %v", a.Name, err)
	}
	return a
}

// seedCI inserts a CI of a type, so value rows have something to hang off.
func seedCI(t *testing.T, db *gorm.DB, typeID model.ID) model.ID {
	t.Helper()
	ci := cimodel.CI{TypeID: typeID}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("create ci: %v", err)
	}
	return ci.ID
}

// --- attribute dictionary ---

func TestCreateAttributeWritesChoicesInTheSameTransaction(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()

	// An integer attribute with one unparsable choice: the insert of the
	// attribute happens first, so this also proves the rollback reaches it.
	_, err := repo.CreateAttribute(ctx, citypebiz.AttributeCreateSpec{
		Attribute: citypemodel.Attribute{Name: "port", Alias: "port", ValueType: "0", IsChoice: true},
		ChoiceValues: []citypebiz.ChoiceValue{
			{Value: 80}, {Value: "not-a-number"},
		},
	})
	if err == nil {
		t.Fatal("CreateAttribute() = nil error, want the bad choice value refused")
	}

	var count int64
	if err := db.Model(&citypemodel.Attribute{}).Where("name = ?", "port").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("attribute rows = %d, want 0: the attribute and its choices are one transaction", count)
	}
}

func TestCreateAttributeStoresChoices(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()

	created, err := repo.CreateAttribute(ctx, citypebiz.AttributeCreateSpec{
		Attribute: citypemodel.Attribute{Name: "env", Alias: "环境", ValueType: "2", IsChoice: true},
		ChoiceValues: []citypebiz.ChoiceValue{
			{Value: "prod", Option: map[string]any{"label": "生产"}},
			{Value: "test"},
		},
	})
	if err != nil {
		t.Fatalf("CreateAttribute() error = %v", err)
	}

	var rows []valuemodel.ChoiceText
	if err := db.Where("attr_id = ?", created.ID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Value != "prod" {
		t.Fatalf("choice rows = %+v, want prod then test", rows)
	}
	// An absent option defaults its label to the value, which is what cantor
	// did so the console never had an unlabelled entry.
	if rows[1].Option["label"] != "test" {
		t.Fatalf("default option = %#v, want a label equal to the value", rows[1].Option)
	}
}

func TestChoiceValuesSkipsNonChoiceAttributes(t *testing.T) {
	repo, db := newAttrRepo(t)
	plain := seedAttr(t, db, "hostname")

	got, err := repo.ChoiceValues(context.Background(), []model.ID{plain.ID})
	if err != nil {
		t.Fatalf("ChoiceValues() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ChoiceValues() = %#v, want nothing for a non-choice attribute", got)
	}
}

func TestDeleteAttributeRefusesWhileBound(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	uniqueAttr := seedAttr(t, db, "hostname")
	// A SECOND attribute, bound but not the unique one: otherwise the
	// unique-identifier guard fires first and this would not be testing the
	// binding guard at all.
	attr := seedAttr(t, db, "cpu_count")
	ct := mustType(t, repo, "host", uniqueAttr.ID)
	if err := db.Create(&citypemodel.CITypeAttribute{TypeID: ct.ID, AttrID: attr.ID, DefaultShow: true}).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.DeleteAttribute(ctx, citypebiz.AttributeDeleteSpec{
		ID: attr.ID, Previous: attr, Actor: 7, ActorIsSuperuser: true,
	})
	if !errors.Is(err, citypebiz.ErrAttributeInUse) {
		t.Fatalf("DeleteAttribute() error = %v, want ErrAttributeInUse", err)
	}

	var live int64
	if err := db.Model(&citypemodel.Attribute{}).Where("id = ?", attr.ID).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatal("the attribute was deleted despite the refusal")
	}
}

func TestDeleteAttributeRefusesWhileItIdentifiesAModel(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "hostname")
	// A type whose unique attribute is bound but whose BINDING is bypassed:
	// the guard order matters, so create the type without the auto-binding.
	ct := citypemodel.CIType{Name: "host", Alias: "host", UniqueID: attr.ID, Enabled: true}
	if err := db.Create(&ct).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.DeleteAttribute(context.Background(), citypebiz.AttributeDeleteSpec{
		ID: attr.ID, Previous: attr, Actor: 7, ActorIsSuperuser: true,
	})
	if !errors.Is(err, citypebiz.ErrAttributeIsUniqueKey) {
		t.Fatalf("DeleteAttribute() error = %v, want ErrAttributeIsUniqueKey", err)
	}
}

func TestDeleteAttributeRefusesWhileItLabelsInstances(t *testing.T) {
	repo, db := newAttrRepo(t)
	attr := seedAttr(t, db, "display_name")
	other := seedAttr(t, db, "hostname")
	show := attr.ID
	ct := citypemodel.CIType{
		Name: "host", Alias: "host", UniqueID: other.ID, Enabled: true, ShowID: &show,
	}
	if err := db.Create(&ct).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.DeleteAttribute(context.Background(), citypebiz.AttributeDeleteSpec{
		ID: attr.ID, Previous: attr, Actor: 7, ActorIsSuperuser: true,
	})
	// cantor had no guard for this at all; the port adds one because a model
	// whose display attribute vanished renders nothing.
	if !errors.Is(err, citypebiz.ErrAttributeIsShowAttr) {
		t.Fatalf("DeleteAttribute() error = %v, want ErrAttributeIsShowAttr", err)
	}
}

func TestDeleteAttributeCleansDependants(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedTypedAttr(t, db, citypemodel.Attribute{
		Name: "env", ValueType: "2", IsChoice: true,
	})
	// An unbound choice attribute: group items and choice rows may legitimately
	// exist for it, and the delete has to clear both.
	group := citypemodel.CITypeAttributeGroup{Name: "网络", TypeID: 999, Order: 0}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&citypemodel.CITypeAttributeGroupItem{GroupID: group.ID, AttrID: attr.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&valuemodel.ChoiceText{AttrID: attr.ID, Value: "prod"}).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteAttribute(ctx, citypebiz.AttributeDeleteSpec{
		ID: attr.ID, Previous: attr, Actor: 7, ActorIsSuperuser: true,
	}); err != nil {
		t.Fatalf("DeleteAttribute() error = %v", err)
	}

	for _, probe := range []struct {
		name  string
		model any
		where string
	}{
		{"group items", &citypemodel.CITypeAttributeGroupItem{}, "attr_id = ?"},
		{"choice values", &valuemodel.ChoiceText{}, "attr_id = ?"},
	} {
		var n int64
		if err := db.Model(probe.model).Where(probe.where, attr.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s = %d live rows, want 0", probe.name, n)
		}
	}
}

// --- the is_index migration ---

// THE HEADLINE FIX. cantor's is_index change compared the two value tables only
// to decide where to copy from and to, never whether they were the same table.
// For every value type except TEXT the forced derivation makes them the same,
// so the copy re-inserted each row into the very table the following DELETE
// cleared — destroying every value of the attribute from one checkbox.
func TestIndexToggleOnANonTextAttributeDoesNotEraseValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  string
		isPass bool
		isLink bool
		isFile bool
	}{
		{name: "int", value: "0"},
		{name: "float", value: "1"},
		{name: "datetime", value: "3"},
		{name: "date", value: "4"},
		{name: "time", value: "5"},
		{name: "bool", value: "7"},
		{name: "json", value: "6"},
		{name: "password", value: "2", isPass: true},
		{name: "link", value: "2", isLink: true},
		{name: "file", value: "2", isFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, db := newAttrRepo(t)
			ctx := context.Background()
			attr := seedTypedAttr(t, db, citypemodel.Attribute{
				Name: "a", ValueType: tc.value,
				IsPassword: tc.isPass, IsLink: tc.isLink, IsFile: tc.isFile,
			})
			table, err := tableFor(attr)
			if err != nil {
				t.Fatal(err)
			}
			insertRawValue(t, db, table, 1, attr.ID)

			before := countRawValues(t, db, table, attr.ID)
			if before == 0 {
				t.Fatalf("fixture did not write a value into %s", table)
			}

			// Flip the flag. For these types the storage placement ignores it,
			// so the only correct outcome is that nothing moves.
			updated := attr
			updated.IsIndex = !attr.IsIndex
			if err := repo.UpdateAttribute(ctx, citypebiz.AttributeUpdateSpec{
				Attribute: updated, Previous: attr, Actor: 7,
			}); err != nil {
				t.Fatalf("UpdateAttribute() error = %v", err)
			}

			if got := countRawValues(t, db, table, attr.ID); got != before {
				t.Fatalf("values in %s = %d, want %d — the flag does not move this attribute's data, "+
					"so the toggle must be a no-op", table, got, before)
			}
		})
	}
}

func TestPromotingTextValuesMovesThem(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedTypedAttr(t, db, citypemodel.Attribute{Name: "note", ValueType: "2", IsIndex: false})
	if err := db.Create(&valuemodel.ValueText{CIID: 1, AttrID: attr.ID, Value: "hello"}).Error; err != nil {
		t.Fatal(err)
	}

	updated := attr
	updated.IsIndex = true
	if err := repo.UpdateAttribute(ctx, citypebiz.AttributeUpdateSpec{
		Attribute: updated, Previous: attr, Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateAttribute() error = %v", err)
	}

	if got := countRawValues(t, db, valuemodel.TableTexts, attr.ID); got != 0 {
		t.Fatalf("unindexed rows = %d, want 0 after promotion", got)
	}
	var moved []valuemodel.ValueIndexText
	if err := db.Where("attr_id = ?", attr.ID).Find(&moved).Error; err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].Value != "hello" {
		t.Fatalf("indexed rows = %+v, want the value carried across", moved)
	}
}

func TestPromotingTextValuesRefusesOverlongValues(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedTypedAttr(t, db, citypemodel.Attribute{Name: "note", ValueType: "2", IsIndex: false})
	long := strings.Repeat("x", valuemodel.MaxIndexedTextLen+1)
	if err := db.Create(&valuemodel.ValueText{CIID: 1, AttrID: attr.ID, Value: long}).Error; err != nil {
		t.Fatal(err)
	}

	updated := attr
	updated.IsIndex = true
	err := repo.UpdateAttribute(ctx, citypebiz.AttributeUpdateSpec{
		Attribute: updated, Previous: attr, Actor: 7,
	})
	if !errors.Is(err, citypebiz.ErrAttributeIndexMigration) {
		t.Fatalf("UpdateAttribute() error = %v, want ErrAttributeIndexMigration", err)
	}

	// The refusal must leave BOTH the flag and the data where they were.
	var stored citypemodel.Attribute
	if err := db.First(&stored, attr.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.IsIndex {
		t.Fatal("the flag was written despite the refused migration")
	}
	if got := countRawValues(t, db, valuemodel.TableTexts, attr.ID); got != 1 {
		t.Fatalf("unindexed rows = %d, want the value left in place", got)
	}
}

func TestDemotingTextValuesMovesThemBack(t *testing.T) {
	repo, db := newAttrRepo(t)
	ctx := context.Background()
	attr := seedTypedAttr(t, db, citypemodel.Attribute{Name: "note", ValueType: "2", IsIndex: true})
	if err := db.Create(&valuemodel.ValueIndexText{CIID: 1, AttrID: attr.ID, Value: "hello"}).Error; err != nil {
		t.Fatal(err)
	}

	updated := attr
	updated.IsIndex = false
	if err := repo.UpdateAttribute(ctx, citypebiz.AttributeUpdateSpec{
		Attribute: updated, Previous: attr, Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateAttribute() error = %v", err)
	}

	if got := countRawValues(t, db, valuemodel.TableIndexTexts, attr.ID); got != 0 {
		t.Fatalf("indexed rows = %d, want 0 after demotion", got)
	}
	if got := countRawValues(t, db, valuemodel.TableTexts, attr.ID); got != 1 {
		t.Fatalf("unindexed rows = %d, want the value carried across", got)
	}
}

// The placement rule is the thing the migration depends on, so the claim "only
// TEXT can move" is asserted rather than assumed.
func TestOnlyTextValuesCanChangeTable(t *testing.T) {
	mods := []valuemodel.StorageModifiers{
		{}, {IsPassword: true}, {IsLink: true}, {IsFile: true},
	}
	moved := map[string]bool{}
	for _, vt := range []valuemodel.ValueType{"0", "1", "2", "3", "4", "5", "6", "7"} {
		for _, m := range mods {
			a, err := vt.StorageTable(vt.EffectiveIndex(false, m))
			if err != nil {
				continue
			}
			b, err := vt.StorageTable(vt.EffectiveIndex(true, m))
			if err != nil {
				continue
			}
			if a != b {
				moved[string(vt)] = true
			}
		}
	}
	if len(moved) != 1 || !moved["2"] {
		t.Fatalf("value types whose table can change = %v, want exactly TEXT (\"2\"); "+
			"moveAttributeValues handles only that pair", moved)
	}
}

// --- helpers ---

func insertRawValue(t *testing.T, db *gorm.DB, table string, ciID, attrID model.ID) {
	t.Helper()
	var row any
	switch table {
	case valuemodel.TableIndexIntegers:
		row = &valuemodel.ValueIndexInt{CIID: ciID, AttrID: attrID, Value: 1}
	case valuemodel.TableIndexFloats:
		row = &valuemodel.ValueIndexFloat{CIID: ciID, AttrID: attrID, Value: 1}
	case valuemodel.TableIndexTexts:
		row = &valuemodel.ValueIndexText{CIID: ciID, AttrID: attrID, Value: "v"}
	case valuemodel.TableIndexDateTime:
		row = &valuemodel.ValueIndexDateTime{CIID: ciID, AttrID: attrID}
	case valuemodel.TableTexts:
		row = &valuemodel.ValueText{CIID: ciID, AttrID: attrID, Value: "v"}
	case valuemodel.TableJSON:
		row = &valuemodel.ValueJSON{CIID: ciID, AttrID: attrID, Value: map[string]any{"k": "v"}}
	default:
		t.Fatalf("insertRawValue: unknown table %q", table)
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("insert raw value into %s: %v", table, err)
	}
}

func countRawValues(t *testing.T, db *gorm.DB, table string, attrID model.ID) int64 {
	t.Helper()
	tbl := valueModel(table)
	if tbl == nil {
		t.Fatalf("countRawValues: unknown table %q", table)
	}
	var n int64
	if err := db.Model(tbl).Where("attr_id = ?", attrID).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestGetAttributeResolvesMissingAsNil(t *testing.T) {
	repo, _ := newAttrRepo(t)
	got, err := repo.GetAttribute(context.Background(), 404)
	if err != nil {
		t.Fatalf("GetAttribute() error = %v", err)
	}
	if got != nil {
		t.Fatalf("GetAttribute(missing) = %+v, want nil", got)
	}
}
