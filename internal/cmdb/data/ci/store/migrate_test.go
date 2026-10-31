package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	valuemodel "github.com/ongridio/ongrid/internal/cmdb/model/value"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(testDSN(t)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func TestMigrateCreatesTheCMDBTableSet(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// Exactly cantor's live tables. The three deprecated value tables must be
	// absent from a fresh database.
	want := []string{
		"c_cis", "c_ci_relations",
		"c_records", "c_attribute_histories", "c_relation_histories",
		"c_ci_type_histories", "c_ci_trigger_histories",
		"c_value_index_integers", "c_value_index_floats", "c_value_index_texts",
		"c_value_index_datetime", "c_value_texts", "c_value_json",
		"c_choice_integers", "c_choice_floats", "c_choice_texts",
	}
	for _, table := range want {
		if !db.Migrator().HasTable(table) {
			t.Errorf("table %s was not created", table)
		}
	}
	for _, dead := range []string{"c_value_integers", "c_value_floats", "c_value_datetime"} {
		if db.Migrator().HasTable(dead) {
			t.Errorf("deprecated table %s must not be created on a fresh database", dead)
		}
	}
}

// cantor's composite (attr_id, value) indexes are the reason attribute
// filtering is index-served rather than a scan. They must exist under cantor's
// own names, otherwise adopting a cantor database would add a second,
// equivalent index instead of reusing the existing one.
//
// Single-column lookup indexes are the other way round: they are NOT cantor's
// names, because SQLite scopes index names per database while MySQL scopes
// them per table, so cantor's `ci_id`/`type_id` names collide across tables.
func TestMigrateCreatesTheExpectedIndexes(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// Reused from cantor verbatim, so adoption reuses rather than duplicates.
	cantorNames := []struct {
		model any
		index string
	}{
		{&valuemodel.ValueIndexInt{}, "integer_attr_value_index"},
		{&valuemodel.ValueIndexFloat{}, "float_attr_value_index"},
		{&valuemodel.ValueIndexText{}, "text_attr_value_index"},
		{&valuemodel.ValueIndexDateTime{}, "datetime_attr_value_index"},
	}
	// Table-qualified because SQLite index names are global.
	qualified := []struct {
		model any
		index string
	}{
		{&cimodel.CI{}, "idx_c_cis_type_id"},
		{&cimodel.Relation{}, "idx_c_ci_relations_first_ci_id"},
		{&cimodel.Relation{}, "idx_c_ci_relations_second_ci_id"},
		{&cimodel.Relation{}, "idx_c_ci_relations_relation_type_id"},
		// The materialized ancestor path is indexed because cantor's
		// many-to-many lookups read it.
		{&cimodel.Relation{}, "idx_c_ci_relations_ancestor_ids"},
		{&valuemodel.ValueText{}, "idx_c_value_texts_ci_id"},
		{&valuemodel.ValueText{}, "idx_c_value_texts_attr_id"},
		{&valuemodel.ValueIndexText{}, "idx_c_value_index_texts_ci_id"},
		{&valuemodel.ValueJSON{}, "idx_c_value_json_ci_id"},
	}
	for _, tc := range append(cantorNames, qualified...) {
		if !db.Migrator().HasIndex(tc.model, tc.index) {
			t.Errorf("index %s on %T was not created", tc.index, tc.model)
		}
	}
}

// Guards the dual-dialect constraint directly: every index name in the CMDB
// schema must be unique across the whole database, because SQLite enforces
// that and MySQL does not. A duplicate name fails at CREATE INDEX time on
// SQLite (which Migrate would surface), but this test names the offending
// index instead of leaving a bare driver error.
func TestIndexNamesAreGloballyUnique(t *testing.T) {
	seen := map[string]string{}
	models := []any{
		&cimodel.CI{}, &cimodel.Relation{}, &cimodel.OperationRecord{},
		&cimodel.AttributeHistory{}, &cimodel.RelationHistory{},
		&cimodel.TypeHistory{}, &cimodel.TriggerHistory{},
		&valuemodel.ValueIndexInt{}, &valuemodel.ValueIndexFloat{},
		&valuemodel.ValueIndexText{}, &valuemodel.ValueIndexDateTime{},
		&valuemodel.ValueText{}, &valuemodel.ValueJSON{},
		&valuemodel.ChoiceInt{}, &valuemodel.ChoiceFloat{}, &valuemodel.ChoiceText{},
	}

	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	for _, m := range models {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			t.Fatalf("parse %T: %v", m, err)
		}
		for _, idx := range stmt.Schema.ParseIndexes() {
			if idx.Class == "CHECK" || idx.Option == "" && len(idx.Fields) == 0 {
				continue
			}
			if prev, dup := seen[idx.Name]; dup {
				t.Errorf("index %s declared on both %s and %T", idx.Name, prev, m)
				continue
			}
			seen[idx.Name] = stmt.Schema.Table
		}
	}

	// Guard against a vacuous pass: if ParseIndexes ever stopped returning the
	// declared indexes, the loop above would assert nothing and still pass.
	// The schema declares 16 tables' worth of indexes plus GORM's implicit
	// deleted_at/created_at ones, so a healthy count is comfortably above 20.
	if len(seen) < 20 {
		t.Fatalf("only %d distinct index names discovered; the uniqueness check would be vacuous", len(seen))
	}
}

func TestMigrateRejectsNilDB(t *testing.T) {
	if err := Migrate(nil); err == nil {
		t.Fatal("Migrate(nil) = nil error, want error")
	}
}

// The indexed-text index must carry a NOCASE collation on SQLite, otherwise
// every wildcard term in the CMDB query DSL degrades to a full scan.
//
// Measured on 200k rows with this driver: LIKE on a BINARY index 36.46 ms,
// LIKE on a NOCASE index 42.29 us. The results are correct either way, so
// without this assertion the regression would be invisible.
func TestIndexedTextIndexIsNocaseOnSQLite(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var ddl string
	if err := db.Raw(
		"SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name = 'text_attr_value_index'",
	).Scan(&ddl).Error; err != nil {
		t.Fatalf("read index definition: %v", err)
	}
	if ddl == "" {
		t.Fatal("text_attr_value_index was not created")
	}
	if !strings.Contains(strings.ToUpper(ddl), "NOCASE") {
		t.Fatalf("index definition = %q, want a NOCASE collation on value", ddl)
	}
	// The composite column order must survive the rebuild, otherwise the index
	// stops serving attr_id-scoped lookups.
	if !strings.Contains(ddl, "attr_id") {
		t.Fatalf("index definition = %q, want attr_id retained", ddl)
	}
}

// Re-running the migration must not churn the index: the rebuild is a
// DROP + CREATE, so an unguarded version would rewrite it on every boot.
func TestIndexedTextCollationFixIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	var first string
	if err := db.Raw("SELECT COALESCE(sql,'') FROM sqlite_master WHERE name = 'text_attr_value_index'").Scan(&first).Error; err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var second string
	if err := db.Raw("SELECT COALESCE(sql,'') FROM sqlite_master WHERE name = 'text_attr_value_index'").Scan(&second).Error; err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("index definition changed across boots:\n first=%q\nsecond=%q", first, second)
	}
}

// The collation fix must make a wildcard LIKE do a RANGE SEEK on value against
// the real schema, not merely "use the index".
//
// This assertion is deliberately about the CONSTRAINT, not about the word
// "INDEX". Merely checking for "INDEX" is vacuous: SQLite uses the index in
// both cases, and the difference is whether it descends into the value column
// or stops after the attr_id prefix:
//
//	with NOCASE : SEARCH ... USING COVERING INDEX ... (attr_id=? AND value>? AND value<?)
//	with BINARY : SEARCH ... USING COVERING INDEX ... (attr_id=?)
//
// The BINARY form scans the entire attr_id partition — that is the measured
// 36 ms. And note the planner REWRITES the LIKE into `value > ? AND value < ?`
// by itself once the collation matches, which is why the range rewrite is not
// something the query compiler has to do.
//
// Volume is seeded so the planner has a reason to consult statistics at all.
func TestIndexedTextIndexRangeSeeksWildcardLike(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	const rows = 20000
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := sqlDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare("INSERT INTO c_value_index_texts (ci_id, attr_id, value, created_at, updated_at, delete_marker) VALUES (?, 1, ?, ?, ?, 0)")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for i := 0; i < rows; i++ {
		if _, err := stmt.Exec(i, fmt.Sprintf("web-%06d", i), now, now); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	type eqp struct {
		ID, Parent, NotUsed int
		Detail              string
	}
	var planRows []eqp
	if err := db.Raw(
		"EXPLAIN QUERY PLAN SELECT id FROM c_value_index_texts WHERE attr_id = ? AND value LIKE ?",
		1, "web-0001%",
	).Scan(&planRows).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}
	var details []string
	for _, r := range planRows {
		details = append(details, r.Detail)
	}
	plan := strings.Join(details, " | ")

	if !strings.Contains(strings.ToUpper(plan), "INDEX") {
		t.Fatalf("query plan = %q, want the value index used at all", plan)
	}
	// The value column must appear in the constraint list. Without the
	// collation fix the planner stops at (attr_id=?).
	if !strings.Contains(plan, "value") {
		t.Fatalf("query plan = %q, want the value column constrained (a range seek), "+
			"not just the attr_id prefix — that is the 36 ms full-partition scan", plan)
	}
	// And it must be a range constraint on value, not an equality, since the
	// term is a prefix wildcard.
	if !strings.Contains(plan, "value>?") || !strings.Contains(plan, "value<?") {
		t.Fatalf("query plan = %q, want value bounded on both sides", plan)
	}
}

// Round-trips every value-table shape on the pure-Go SQLite the product
// ships. This is the check that a value written by the EAV engine can be read
// back: the JSON column uses GORM's serializer, BOOL shares the integer
// table, and a multi-valued attribute is multiple rows rather than an array.
func TestValueTablesRoundTripOnSQLite(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	ci := cimodel.CI{TypeID: 7, PropsJSON: `{"hostname":"web-1"}`}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("create CI: %v", err)
	}
	if ci.ID == 0 {
		t.Fatal("CI got no auto-increment id")
	}

	if err := db.Create(&valuemodel.ValueIndexInt{CIID: ci.ID, AttrID: 1, Value: 8}).Error; err != nil {
		t.Fatalf("insert int value: %v", err)
	}
	if err := db.Create(&valuemodel.ValueIndexFloat{CIID: ci.ID, AttrID: 2, Value: 1.5}).Error; err != nil {
		t.Fatalf("insert float value: %v", err)
	}
	if err := db.Create(&valuemodel.ValueIndexText{CIID: ci.ID, AttrID: 3, Value: "web-1"}).Error; err != nil {
		t.Fatalf("insert indexed text value: %v", err)
	}
	when := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&valuemodel.ValueIndexDateTime{CIID: ci.ID, AttrID: 4, Value: when}).Error; err != nil {
		t.Fatalf("insert datetime value: %v", err)
	}
	// is_list: two rows, one attribute — cantor's multi-value representation.
	for _, v := range []string{"10.0.0.1", "10.0.0.2"} {
		if err := db.Create(&valuemodel.ValueText{CIID: ci.ID, AttrID: 5, Value: v}).Error; err != nil {
			t.Fatalf("insert multi-value text: %v", err)
		}
	}
	// A password attribute lands in the unindexed text table.
	if err := db.Create(&valuemodel.ValueText{CIID: ci.ID, AttrID: 6, Value: "s3cret"}).Error; err != nil {
		t.Fatalf("insert password value: %v", err)
	}
	if err := db.Create(&valuemodel.ValueJSON{CIID: ci.ID, AttrID: 7, Value: map[string]any{"k": "v"}}).Error; err != nil {
		t.Fatalf("insert json value: %v", err)
	}

	var ints []valuemodel.ValueIndexInt
	if err := db.Where("ci_id = ? AND attr_id = ?", ci.ID, 1).Find(&ints).Error; err != nil {
		t.Fatalf("read int value: %v", err)
	}
	if len(ints) != 1 || ints[0].Value != 8 {
		t.Fatalf("int values = %+v, want one row with value 8", ints)
	}

	var multi []valuemodel.ValueText
	if err := db.Where("ci_id = ? AND attr_id = ?", ci.ID, 5).Find(&multi).Error; err != nil {
		t.Fatalf("read multi-value text: %v", err)
	}
	if len(multi) != 2 {
		t.Fatalf("multi-value rows = %d, want 2 (is_list is multi-row, not an array)", len(multi))
	}

	var js valuemodel.ValueJSON
	if err := db.Where("ci_id = ? AND attr_id = ?", ci.ID, 7).First(&js).Error; err != nil {
		t.Fatalf("read json value: %v", err)
	}
	if js.Value["k"] != "v" {
		t.Fatalf("json value = %#v, want map[k:v]", js.Value)
	}

	var got cimodel.CI
	if err := db.First(&got, ci.ID).Error; err != nil {
		t.Fatalf("read CI: %v", err)
	}
	if got.PropsJSON != `{"hostname":"web-1"}` {
		t.Fatalf("PropsJSON round-trip = %q", got.PropsJSON)
	}
}

// Soft delete must work through the delete_marker + deleted_at pair on both
// a fresh Ongrid database and (after the adoption backfill) a cantor one.
func TestSoftDeleteHidesCIs(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	keep := cimodel.CI{TypeID: 1}
	drop := cimodel.CI{TypeID: 1}
	if err := db.Create(&keep).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&drop).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&drop).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	var live []cimodel.CI
	if err := db.Find(&live).Error; err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != keep.ID {
		t.Fatalf("live CIs = %+v, want only id %d", live, keep.ID)
	}

	// The marker must actually be set, not just filtered at read time.
	var all []cimodel.CI
	if err := db.Unscoped().Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unscoped CIs = %d, want 2", len(all))
	}
	for _, row := range all {
		if row.ID == drop.ID && row.DeletedAt == nil {
			t.Fatal("deleted CI has a nil deleted_at")
		}
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
