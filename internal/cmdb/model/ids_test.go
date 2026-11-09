// Package model_test is an external test package so it can import the CMDB
// model sub-packages, which the model package itself cannot (they import it).
package model_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	metamodel "github.com/ongridio/ongrid/internal/cmdb/model/meta"
	valuemodel "github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// everyModel lists each CMDB entity so the checks below cover the whole
// schema rather than a sample.
func everyModel() []any {
	return []any{
		&metamodel.SchemaMeta{},
		&cimodel.CI{}, &cimodel.Relation{}, &cimodel.OperationRecord{},
		&cimodel.AttributeHistory{}, &cimodel.RelationHistory{},
		&cimodel.TypeHistory{}, &cimodel.TriggerHistory{},
		&citypemodel.RelationType{}, &citypemodel.CITypeGroup{}, &citypemodel.CITypeGroupItem{},
		&citypemodel.CIType{}, &citypemodel.CITypeInheritance{}, &citypemodel.CITypeRelation{},
		&citypemodel.Attribute{}, &citypemodel.CITypeAttribute{},
		&citypemodel.CITypeAttributeGroup{}, &citypemodel.CITypeAttributeGroupItem{},
		&citypemodel.CITypeUniqueConstraint{}, &citypemodel.CITypeTrigger{},
		&valuemodel.ValueIndexInt{}, &valuemodel.ValueIndexFloat{}, &valuemodel.ValueIndexText{},
		&valuemodel.ValueIndexDateTime{}, &valuemodel.ValueText{}, &valuemodel.ValueJSON{},
		&valuemodel.ChoiceInt{}, &valuemodel.ChoiceFloat{}, &valuemodel.ChoiceText{},
	}
}

func mysqlDDL(t *testing.T, m any) (*gorm.DB, *gorm.Statement) {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{SkipInitializeWithVersion: true}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open mysql dialector: %v", err)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil {
		t.Fatalf("parse %T: %v", m, err)
	}
	return db, stmt
}

// deleteMarkerColumn is the one column CMDB declares wider than cantor, and it
// is safe because cantor does not have it: AutoMigrate ADDs it rather than
// MODIFYing an existing column, so no foreign-key type mismatch can arise.
const deleteMarkerColumn = "delete_marker"

// TestNoCMDBColumnWidensACantorColumn pins the single most consequential schema
// decision in the port.
//
// Every CMDB id and foreign key must be MySQL `int`, exactly matching
// nexus-cantor's int(11), so that AutoMigrate against an existing cantor
// database is a no-op. Declaring them as Ongrid's habitual uint64 emits
// `bigint unsigned`, which GORM would then try to MODIFY into place — failing
// with MySQL error 3780 on foreign-key child columns and rewriting every table
// elsewhere.
//
// The assertion is "no CMDB column emits bigint (except delete_marker)" rather
// than "columns named *_id are int", because name-based inference is
// unreliable: c_records.ticket_id is a varchar(32) reference string, and
// banning it would be wrong while allowing a genuine integer reference to
// widen would be the actual bug.
//
// This is checked against GORM's own MySQL type mapper rather than by
// inspecting Go types, because the Go type is one step removed from the DDL and
// only the DDL decides whether an ALTER is issued.
func TestNoCMDBColumnWidensACantorColumn(t *testing.T) {
	for _, m := range everyModel() {
		db, stmt := mysqlDDL(t, m)
		for _, f := range stmt.Schema.Fields {
			if f.DBName == "" || f.DBName == deleteMarkerColumn {
				continue
			}
			full := db.Migrator().FullDataTypeOf(f).SQL
			if strings.Contains(full, "bigint") {
				t.Errorf("%s.%s declared as %q; cantor declares its integer columns as int(11), "+
					"so a bigint here forces a type-changing ALTER (MySQL error 3780 on FK children). "+
					"Use model.ID; see internal/cmdb/model/ids.go",
					stmt.Schema.Table, f.DBName, full)
			}
		}
	}
}

// Every new CMDB identifier column must actually be declared as model.ID. This
// catches the opposite mistake — a fresh column added with uint64 that would
// widen a cantor column on adoption.
func TestEveryIntegerIdentifierUsesModelID(t *testing.T) {
	var idType = reflect.TypeOf(cmdbmodel.ID(0))

	checked := 0
	for _, m := range everyModel() {
		_, stmt := mysqlDDL(t, m)
		for _, f := range stmt.Schema.Fields {
			if f.DBName == "" || f.DBName == deleteMarkerColumn {
				continue
			}
			// Fields whose Go type is the ID alias are exactly the ones the
			// schema intends as identifiers.
			if f.FieldType == idType {
				checked++
				continue
			}
			// A raw int64/uint64 integer column would also widen. Report only
			// those, so strings and timestamps stay out of the way.
			switch f.FieldType.Kind() {
			case reflect.Int64, reflect.Uint64, reflect.Uint32, reflect.Int:
				t.Errorf("%s.%s is a raw %s; integer CMDB columns must be declared as model.ID",
					stmt.Schema.Table, f.DBName, f.FieldType.Kind())
			}
		}
	}

	// Guard against a vacuous pass: if the alias stopped being recognised, the
	// loop above would assert nothing.
	if checked < 30 {
		t.Fatalf("only %d identifier fields recognised as model.ID; the check would be vacuous", checked)
	}
}

// The alias target itself must stay int32. A future widening would be caught by
// the DDL check above, but pinning it here makes the intent explicit and fails
// fast with a clearer message.
func TestModelIDIsInt32(t *testing.T) {
	// These assignments type-check only while ID is exactly int32.
	var id cmdbmodel.ID = 1
	var asInt32 int32 = id
	var back cmdbmodel.ID = asInt32
	if back != 1 {
		t.Fatalf("round-trip = %d, want 1", back)
	}
	if math.MaxInt32 != 2147483647 {
		t.Fatalf("unexpected int32 ceiling: %d", math.MaxInt32)
	}
}
