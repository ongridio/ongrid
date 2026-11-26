package store

import (
	"context"
	"errors"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fullSpecs is a type whose attributes exercise every storage destination the
// planner can choose: indexed and unindexed text, int, float, datetime, bool,
// json, multi-valued, and a password that must land unindexed.
func fullSpecs() map[cmdbmodel.ID]cibiz.AttributeSpec {
	return map[cmdbmodel.ID]cibiz.AttributeSpec{
		1: {ID: 1, Name: "hostname", ValueType: value.TypeText, IsIndex: true, IsUnique: true, IsRequired: true},
		2: {ID: 2, Name: "cpu_count", ValueType: value.TypeInt, IsIndex: true},
		3: {ID: 3, Name: "load_avg", ValueType: value.TypeFloat, IsIndex: true},
		4: {ID: 4, Name: "booted_at", ValueType: value.TypeDateTime, IsIndex: true},
		5: {ID: 5, Name: "enabled", ValueType: value.TypeBool, IsIndex: true},
		6: {ID: 6, Name: "labels", ValueType: value.TypeJSON, IsIndex: true},
		7: {ID: 7, Name: "private_ips", ValueType: value.TypeText, IsIndex: true, IsList: true},
		8: {ID: 8, Name: "description", ValueType: value.TypeText, IsIndex: false},
		9: {ID: 9, Name: "root_password", ValueType: value.TypeText, IsIndex: true, IsPassword: true},
	}
}

func seedCI(t *testing.T, repo *Repo, values map[cmdbmodel.ID]any) *cimodel.CI {
	t.Helper()
	ci := &cimodel.CI{TypeID: 42}
	created, err := repo.CreateCIWithValues(context.Background(), cibiz.CreateInput{
		CI:     ci,
		Specs:  fullSpecs(),
		Values: values,
		Audit:  cibiz.AuditInfo{Actor: 7, Origin: "ui"},
	})
	if err != nil {
		t.Fatalf("CreateCIWithValues() error = %v", err)
	}
	return created
}

// The end-to-end proof: one CI with a value in every table round-trips, and
// the document column reflects the value tables.
func TestCreateCIWithValuesRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{
		1: "web-1",
		2: 8,
		3: 1.5,
		4: "2026-10-16 08:30:00",
		5: "yes",
		6: map[string]any{"env": "prod"},
		7: []any{"10.0.0.1", "10.0.0.2"},
		8: "a longer free-text description",
		9: "hunter2",
	})
	if ci.ID == 0 {
		t.Fatal("CI got no id")
	}

	rows, err := repo.LoadValueRows(ctx, ci.ID)
	if err != nil {
		t.Fatalf("LoadValueRows() error = %v", err)
	}

	byAttr := map[cmdbmodel.ID][]cibiz.ValueRow{}
	for _, row := range rows {
		byAttr[row.AttrID] = append(byAttr[row.AttrID], row)
	}
	assertTable := func(attrID cmdbmodel.ID, wantTable string) {
		t.Helper()
		if len(byAttr[attrID]) == 0 {
			t.Fatalf("attr %d has no rows", attrID)
		}
		if byAttr[attrID][0].Table != wantTable {
			t.Fatalf("attr %d table = %q, want %q", attrID, byAttr[attrID][0].Table, wantTable)
		}
	}
	assertTable(1, value.TableIndexTexts)
	assertTable(2, value.TableIndexIntegers)
	assertTable(3, value.TableIndexFloats)
	assertTable(4, value.TableIndexDateTime)
	assertTable(5, value.TableIndexIntegers) // BOOL shares the integer table
	assertTable(6, value.TableJSON)
	assertTable(8, value.TableTexts) // declared unindexed
	// A password forces the unindexed table even though is_index is set.
	assertTable(9, value.TableTexts)

	if len(byAttr[7]) != 2 {
		t.Fatalf("multi-valued attr rows = %d, want 2", len(byAttr[7]))
	}
	if byAttr[5][0].Int != 1 {
		t.Fatalf("bool carrier = %d, want 1 for \"yes\"", byAttr[5][0].Int)
	}

	// The document must reflect the final rows.
	doc, err := cibiz.ParsePropsDocument(ci.PropsJSON)
	if err != nil {
		t.Fatalf("ParsePropsDocument() error = %v", err)
	}
	if doc["hostname"] != "web-1" {
		t.Fatalf("doc hostname = %v, want web-1", doc["hostname"])
	}
	if doc["cpu_count"] != float64(8) {
		t.Fatalf("doc cpu_count = %v, want 8", doc["cpu_count"])
	}
	ips, ok := doc["private_ips"].([]any)
	if !ok || len(ips) != 2 {
		t.Fatalf("doc private_ips = %#v, want a 2-element array", doc["private_ips"])
	}
	// The secret must never appear in the document.
	if _, leaked := doc["root_password"]; leaked {
		t.Fatal("password attribute leaked into the CI document")
	}

	// An operation record and one history row per attribute change must exist.
	var recCount int64
	if err := db.Model(&cimodel.OperationRecord{}).Count(&recCount).Error; err != nil {
		t.Fatal(err)
	}
	if recCount != 1 {
		t.Fatalf("operation records = %d, want 1", recCount)
	}
	var histCount int64
	if err := db.Model(&cimodel.AttributeHistory{}).Count(&histCount).Error; err != nil {
		t.Fatal(err)
	}
	if histCount == 0 {
		t.Fatal("no attribute history was written")
	}
}

// A no-op edit must write nothing: no value rows, no history, no audit row.
func TestUpsertWithNoChangeWritesNothing(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{1: "web-1", 2: 8})

	var before int64
	if err := db.Model(&cimodel.AttributeHistory{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}

	changes, err := repo.UpsertCIValues(ctx, cibiz.UpdateInput{
		CIID: ci.ID, TypeID: 42, Specs: fullSpecs(),
		Values: map[cmdbmodel.ID]any{1: "web-1", 2: 8},
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if err != nil {
		t.Fatalf("UpsertCIValues() error = %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %+v, want none for an unchanged value", changes)
	}

	var after int64
	if err := db.Model(&cimodel.AttributeHistory{}).Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("history rows went %d -> %d for a no-op edit", before, after)
	}
}

// An update rewrites the row, records the change, and refreshes the document.
func TestUpsertUpdatesRowHistoryAndDocument(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{1: "web-1", 2: 8, 7: []any{"10.0.0.1"}})

	changes, err := repo.UpsertCIValues(ctx, cibiz.UpdateInput{
		CIID: ci.ID, TypeID: 42, Specs: fullSpecs(),
		Values: map[cmdbmodel.ID]any{2: 16, 7: []any{"10.0.0.1", "10.0.0.9"}},
		Audit:  cibiz.AuditInfo{Actor: 9, Origin: "ui", TicketID: "CHG-1"},
	})
	if err != nil {
		t.Fatalf("UpsertCIValues() error = %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want 2 (cpu_count update + one list append)", changes)
	}

	rows, err := repo.LoadValueRows(ctx, ci.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cpu *cibiz.ValueRow
	var ips int
	for i := range rows {
		switch rows[i].AttrID {
		case 2:
			cpu = &rows[i]
		case 7:
			ips++
		}
	}
	if cpu == nil || cpu.Int != 16 {
		t.Fatalf("cpu row = %+v, want value 16", cpu)
	}
	if ips != 2 {
		t.Fatalf("multi-valued rows = %d, want 2 after the append", ips)
	}

	updated, err := repo.LoadCI(ctx, ci.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := cibiz.ParsePropsDocument(updated.PropsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if doc["cpu_count"] != float64(16) {
		t.Fatalf("doc cpu_count = %v, want 16 after the update", doc["cpu_count"])
	}
}

// Deleting a CI removes its value rows and relations, soft-deletes the CI, and
// must NOT touch the audit trail.
func TestDeleteCIRemovesValuesAndRelationsButKeepsAudit(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{1: "web-1"})
	other := seedCI(t, repo, map[cmdbmodel.ID]any{1: "db-1"})

	rel := &cimodel.Relation{FirstCIID: ci.ID, SecondCIID: other.ID, RelationTypeID: 1}
	if err := db.Create(rel).Error; err != nil {
		t.Fatal(err)
	}

	var histBefore int64
	if err := db.Model(&cimodel.AttributeHistory{}).Count(&histBefore).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteCI(ctx, ci.ID); err != nil {
		t.Fatalf("DeleteCI() error = %v", err)
	}

	// Soft-deleted, so invisible to a normal read but still present unscoped.
	if got, err := repo.LoadCI(ctx, ci.ID); err != nil || got != nil {
		t.Fatalf("LoadCI after delete = %+v, %v; want nil", got, err)
	}
	var unscoped int64
	if err := db.Unscoped().Model(&cimodel.CI{}).Where("id = ?", ci.ID).Count(&unscoped).Error; err != nil {
		t.Fatal(err)
	}
	if unscoped != 1 {
		t.Fatalf("unscoped CI count = %d, want 1 (soft delete, not a hard delete)", unscoped)
	}

	rows, err := repo.LoadValueRows(ctx, ci.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("value rows after delete = %+v, want none", rows)
	}

	var rels int64
	if err := db.Model(&cimodel.Relation{}).Where("first_ci_id = ? OR second_ci_id = ?", ci.ID, ci.ID).Count(&rels).Error; err != nil {
		t.Fatal(err)
	}
	if rels != 0 {
		t.Fatalf("live relations = %d, want 0 after delete", rels)
	}

	// The audit trail must survive its subject.
	var histAfter int64
	if err := db.Model(&cimodel.AttributeHistory{}).Count(&histAfter).Error; err != nil {
		t.Fatal(err)
	}
	if histAfter != histBefore {
		t.Fatalf("history rows went %d -> %d; deleting a CI must not erase its audit trail", histBefore, histAfter)
	}
}

// A failed write must leave NOTHING behind: the whole create is one
// transaction, so a rejected value cannot leave a half-populated CI.
func TestCreateRollsBackEverythingOnInvalidValue(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)

	// cpu_count is an INT; a non-numeric value must fail the plan.
	_, err := repo.CreateCIWithValues(context.Background(), cibiz.CreateInput{
		CI:     &cimodel.CI{TypeID: 42},
		Specs:  fullSpecs(),
		Values: map[cmdbmodel.ID]any{1: "web-1", 2: "not-a-number"},
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if err == nil {
		t.Fatal("CreateCIWithValues() with a bad value = nil error, want error")
	}

	// No CI row may survive the rollback.
	var ciCount int64
	if err := db.Model(&cimodel.CI{}).Count(&ciCount).Error; err != nil {
		t.Fatal(err)
	}
	if ciCount != 0 {
		t.Fatalf("CI rows = %d after a rolled-back create, want 0", ciCount)
	}
	var valCount int64
	if err := db.Model(&value.ValueIndexText{}).Count(&valCount).Error; err != nil {
		t.Fatal(err)
	}
	if valCount != 0 {
		t.Fatalf("value rows = %d after a rolled-back create, want 0", valCount)
	}
	var auditCount int64
	if err := db.Model(&cimodel.OperationRecord{}).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 0 {
		t.Fatalf("audit rows = %d after a rolled-back create, want 0", auditCount)
	}
}

// Unknown attributes are rejected rather than silently ignored.
//
// This is a deliberate divergence: cantor's write loop does
// `attr = key2attr.get(key); if not attr: continue`, so a typo'd attribute
// name is dropped without a word. Silently discarding caller input is exactly
// what AGENTS.md forbids, so the port reports it.
func TestCreateRejectsUnknownAttributeWithoutWriting(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)

	_, err := repo.CreateCIWithValues(context.Background(), cibiz.CreateInput{
		CI:     &cimodel.CI{TypeID: 42},
		Specs:  fullSpecs(),
		Values: map[cmdbmodel.ID]any{1: "web-1", 999: "nope"}, // required provided, so only the unknown attr is at fault
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if !errors.Is(err, cibiz.ErrUnknownAttribute) {
		t.Fatalf("error = %v, want ErrUnknownAttribute", err)
	}
}

// A required attribute missing is rejected by the biz rule, before any write.
func TestCreateRejectsMissingRequiredAttribute(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)

	_, err := repo.CreateCIWithValues(context.Background(), cibiz.CreateInput{
		CI:     &cimodel.CI{TypeID: 42},
		Specs:  fullSpecs(),
		Values: map[cmdbmodel.ID]any{2: 8}, // hostname (required) omitted
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if !errors.Is(err, cibiz.ErrRequiredAttribute) {
		t.Fatalf("error = %v, want ErrRequiredAttribute", err)
	}
}

// When both rules would fire, the completeness check wins.
//
// That matches cantor's ordering: the required-presence check lives in the
// create path (ci.py:459) and runs before the value write loop where an
// unknown attribute would be seen. Pinning it here makes the precedence a
// decision rather than an accident of call order.
func TestCreateReportsMissingRequiredBeforeUnknownAttribute(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)

	_, err := repo.CreateCIWithValues(context.Background(), cibiz.CreateInput{
		CI:     &cimodel.CI{TypeID: 42},
		Specs:  fullSpecs(),
		Values: map[cmdbmodel.ID]any{999: "nope"}, // hostname omitted AND an unknown attr supplied
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if !errors.Is(err, cibiz.ErrRequiredAttribute) {
		t.Fatalf("error = %v, want ErrRequiredAttribute to take precedence", err)
	}
}

// An update must NOT require the whole CI to be resent: patching one attribute
// is the normal case, and cantor has no presence check on update at all.
func TestUpdateDoesNotRequireResendingRequiredAttributes(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{1: "web-1", 2: 8})

	changes, err := repo.UpsertCIValues(ctx, cibiz.UpdateInput{
		CIID: ci.ID, TypeID: 42, Specs: fullSpecs(),
		Values: map[cmdbmodel.ID]any{2: 32}, // hostname (required) deliberately not resent
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if err != nil {
		t.Fatalf("UpsertCIValues() with a partial patch error = %v, want nil", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want 1", changes)
	}
}

// But an update that supplies an EMPTY value for a required attribute is still
// refused, because that is the per-value rule, not the presence rule.
func TestUpdateRejectsEmptyRequiredValue(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{1: "web-1"})

	_, err := repo.UpsertCIValues(ctx, cibiz.UpdateInput{
		CIID: ci.ID, TypeID: 42, Specs: fullSpecs(),
		Values: map[cmdbmodel.ID]any{1: ""},
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if !errors.Is(err, cibiz.ErrRequiredAttribute) {
		t.Fatalf("error = %v, want ErrRequiredAttribute", err)
	}
}

func TestUpsertOnMissingCIReportsNotFound(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)

	_, err := repo.UpsertCIValues(context.Background(), cibiz.UpdateInput{
		CIID: 12345, TypeID: 42, Specs: fullSpecs(),
		Values: map[cmdbmodel.ID]any{1: "x"},
		Audit:  cibiz.AuditInfo{Actor: 7},
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want errs.ErrNotFound", err)
	}
}

func TestDeleteMissingCIReportsNotFound(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := NewRepo(db).DeleteCI(context.Background(), 12345); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want errs.ErrNotFound", err)
	}
}

// Clearing an attribute deletes its row and drops it from the document.
func TestUpsertNilClearsValueAndDocument(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	ci := seedCI(t, repo, map[cmdbmodel.ID]any{1: "web-1", 8: "free text"})

	if _, err := repo.UpsertCIValues(ctx, cibiz.UpdateInput{
		CIID: ci.ID, TypeID: 42, Specs: fullSpecs(),
		Values: map[cmdbmodel.ID]any{8: nil},
		Audit:  cibiz.AuditInfo{Actor: 7},
	}); err != nil {
		t.Fatalf("UpsertCIValues() error = %v", err)
	}

	rows, err := repo.LoadValueRows(ctx, ci.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.AttrID == 8 {
			t.Fatalf("attr 8 still has a row after being cleared: %+v", row)
		}
	}

	updated, err := repo.LoadCI(ctx, ci.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := cibiz.ParsePropsDocument(updated.PropsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := doc["description"]; present {
		t.Fatalf("cleared attribute still in document: %#v", doc)
	}
}

// Dynamic attributes are stored but generate no history, so discovery churn
// does not flood the audit log.
func TestDynamicAttributeWritesNoHistory(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repo := NewRepo(db)
	ctx := context.Background()

	specs := map[cmdbmodel.ID]cibiz.AttributeSpec{
		1: {ID: 1, Name: "hostname", ValueType: value.TypeText, IsIndex: true, IsRequired: true},
		2: {ID: 2, Name: "last_seen", ValueType: value.TypeText, IsIndex: true, IsDynamic: true},
	}
	ci := &cimodel.CI{TypeID: 42}
	created, err := repo.CreateCIWithValues(ctx, cibiz.CreateInput{
		CI: ci, Specs: specs, Values: map[cmdbmodel.ID]any{1: "web-1", 2: "t0"},
		Audit: cibiz.AuditInfo{Actor: 7},
	})
	if err != nil {
		t.Fatalf("CreateCIWithValues() error = %v", err)
	}

	changes, err := repo.UpsertCIValues(ctx, cibiz.UpdateInput{
		CIID: created.ID, TypeID: 42, Specs: specs, Values: map[cmdbmodel.ID]any{2: "t1"},
		Audit: cibiz.AuditInfo{Actor: 7},
	})
	if err != nil {
		t.Fatalf("UpsertCIValues() error = %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %+v, want none for a dynamic attribute", changes)
	}

	// The value itself must still have been written.
	rows, err := repo.LoadValueRows(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	for _, row := range rows {
		if row.AttrID == 2 {
			seen = row.Text
		}
	}
	if seen != "t1" {
		t.Fatalf("dynamic attribute value = %q, want t1 (stored despite no history)", seen)
	}
}

// Guards the arch-lint contract at compile time.
var _ cibiz.Repo = (*Repo)(nil)

// Sanity: the store must not silently drop the deprecated tables from its
// awareness — they are simply never read or written.
func TestRepoNeverTouchesDeprecatedValueTables(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	for _, dead := range []string{"c_value_integers", "c_value_floats", "c_value_datetime"} {
		if db.Migrator().HasTable(dead) {
			t.Fatalf("deprecated table %s exists on a fresh database", dead)
		}
	}
	_ = cmdbmodel.OperateAdd // keep the domain-constants import honest
}
