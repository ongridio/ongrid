package store

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(testDSN(t)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func TestMigrateCreatesTheModelDefinitionTables(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	want := []string{
		"c_relation_types",
		"c_ci_type_groups", "c_ci_type_group_items",
		"c_ci_types", "c_ci_type_inheritance", "c_ci_type_relations",
		"c_attributes", "c_ci_type_attributes",
		"c_ci_type_attribute_groups", "c_ci_type_attribute_group_items",
		"c_c_t_u_c", "c_c_t_t",
	}
	for _, table := range want {
		if !db.Migrator().HasTable(table) {
			t.Errorf("table %s was not created", table)
		}
	}
}

func TestMigrateRejectsNilDB(t *testing.T) {
	if err := Migrate(nil); err == nil {
		t.Fatal("Migrate(nil) = nil error, want error")
	}
}

// The relation-type dictionary is the smallest entity and the natural first
// vertical slice, so its defaults must match cantor's exactly: a 7-character
// colour and the #1890ff fallback.
func TestRelationTypeDefaults(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	rt := citypemodel.RelationType{Name: "runs_on"}
	if err := db.Create(&rt).Error; err != nil {
		t.Fatalf("create relation type: %v", err)
	}
	var got citypemodel.RelationType
	if err := db.First(&got, rt.ID).Error; err != nil {
		t.Fatalf("read relation type: %v", err)
	}
	if got.Color != "#1890ff" {
		t.Fatalf("default colour = %q, want #1890ff", got.Color)
	}
}

// Attribute defaults matter for storage-table selection: an attribute created
// without explicit flags must be an unindexed TEXT ("2"), which is what cantor
// defaults to.
func TestAttributeDefaults(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	attr := citypemodel.Attribute{Name: "hostname", Alias: "hostname"}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribute: %v", err)
	}
	var got citypemodel.Attribute
	if err := db.First(&got, attr.ID).Error; err != nil {
		t.Fatalf("read attribute: %v", err)
	}
	if got.ValueType != "2" {
		t.Fatalf("default value_type = %q, want \"2\" (TEXT)", got.ValueType)
	}
	if got.IsIndex || got.IsList || got.IsUnique || got.IsPassword {
		t.Fatalf("attribute flags did not default to false: %+v", got)
	}
}

// JSON-serialised columns must round-trip on the pure-Go SQLite, including an
// empty/NULL value — several of these hold encrypted ciphertext bags in
// cantor, so a silent truncation would destroy credentials.
func TestAttributeAndRelationJSONRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	attr := citypemodel.Attribute{
		Name: "credential", Alias: "credential",
		ChoiceWebHook: map[string]any{
			"headers":       map[string]any{"Cookie": "CIPHERTEXT"},
			"authorization": map[string]any{"token": "CIPHERTEXT2"},
		},
		Option:  map[string]any{"file_storage": "local"},
		Default: map[string]any{"default": "$created_at"},
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribute: %v", err)
	}

	var got citypemodel.Attribute
	if err := db.First(&got, attr.ID).Error; err != nil {
		t.Fatalf("read attribute: %v", err)
	}
	hdrs, ok := got.ChoiceWebHook["headers"].(map[string]any)
	if !ok || hdrs["Cookie"] != "CIPHERTEXT" {
		t.Fatalf("ChoiceWebHook did not round-trip: %#v", got.ChoiceWebHook)
	}
	if got.Default["default"] != "$created_at" {
		t.Fatalf("Default did not round-trip: %#v", got.Default)
	}

	// A NULL JSON column must read back as nil, not panic.
	plain := citypemodel.Attribute{Name: "cpu", Alias: "cpu"}
	if err := db.Create(&plain).Error; err != nil {
		t.Fatalf("create plain attribute: %v", err)
	}
	var gotPlain citypemodel.Attribute
	if err := db.First(&gotPlain, plain.ID).Error; err != nil {
		t.Fatalf("read plain attribute: %v", err)
	}
	if gotPlain.ChoiceWebHook != nil || gotPlain.Option != nil {
		t.Fatalf("NULL JSON columns became non-nil: %#v", gotPlain.ChoiceWebHook)
	}

	ctr := citypemodel.CITypeRelation{
		ParentID: 1, ChildID: 2, RelationTypeID: 3,
		ParentAttrIDs: citypemodel.IDList{1, 2},
	}
	if err := db.Create(&ctr).Error; err != nil {
		t.Fatalf("create type relation: %v", err)
	}
	var gotCTR citypemodel.CITypeRelation
	if err := db.First(&gotCTR, ctr.ID).Error; err != nil {
		t.Fatalf("read type relation: %v", err)
	}
	if len(gotCTR.ParentAttrIDs) != 2 || gotCTR.ParentAttrIDs[0] != 1 || gotCTR.ParentAttrIDs[1] != 2 {
		t.Fatalf("ParentAttrIDs did not round-trip: %#v", gotCTR.ParentAttrIDs)
	}
}

// Index names must be unique across the whole database, not just per table:
// SQLite enforces that and MySQL does not. Asserted non-vacuously.
func TestIndexNamesAreGloballyUniqueAcrossBothMigrators(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("citype Migrate() error = %v", err)
	}

	models := []any{
		&citypemodel.RelationType{}, &citypemodel.CITypeGroup{}, &citypemodel.CITypeGroupItem{},
		&citypemodel.CIType{}, &citypemodel.CITypeInheritance{}, &citypemodel.CITypeRelation{},
		&citypemodel.Attribute{}, &citypemodel.CITypeAttribute{},
		&citypemodel.CITypeAttributeGroup{}, &citypemodel.CITypeAttributeGroupItem{},
		&citypemodel.CITypeUniqueConstraint{}, &citypemodel.CITypeTrigger{},
	}

	seen := map[string]string{}
	for _, m := range models {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			t.Fatalf("parse %T: %v", m, err)
		}
		for _, idx := range stmt.Schema.ParseIndexes() {
			if idx.Class == "CHECK" {
				continue
			}
			if prev, dup := seen[idx.Name]; dup {
				t.Errorf("index %s declared on both %s and %T", idx.Name, prev, m)
				continue
			}
			seen[idx.Name] = stmt.Schema.Table
		}
	}
	if len(seen) < 15 {
		t.Fatalf("only %d distinct index names discovered; the uniqueness check would be vacuous", len(seen))
	}
}
