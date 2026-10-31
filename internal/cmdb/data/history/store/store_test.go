package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	historybiz "github.com/ongridio/ongrid/internal/cmdb/biz/history"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// The history reads against a database: the join that turns a change row into
// a person-readable record, the filters, and the order.

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "?", "_", "#", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:history-"+name+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(
		&cimodel.OperationRecord{}, &cimodel.AttributeHistory{},
		&cimodel.RelationHistory{}, &cimodel.TypeHistory{}, &cimodel.TriggerHistory{},
		&citypemodel.Attribute{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func ptr[T any](v T) *T { return &v }

func at(hour int) time.Time {
	return time.Date(2026, 10, 26, hour, 0, 0, 0, time.UTC)
}

// seed writes two records and one row of each history kind.
func seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	records := []cimodel.OperationRecord{
		{UID: 7, Origin: ptr("ui"), TypeID: ptr(model.ID(1))},
		{UID: 8, Origin: ptr("import"), TicketID: ptr("CHG-1"), TypeID: ptr(model.ID(2))},
	}
	for i := range records {
		records[i].ID = model.ID(i + 1)
		if err := db.Create(&records[i]).Error; err != nil {
			t.Fatalf("seed record: %v", err)
		}
		if err := db.Model(&cimodel.OperationRecord{}).Where("id = ?", records[i].ID).
			Update("created_at", at(10+i)).Error; err != nil {
			t.Fatalf("stamp record: %v", err)
		}
	}

	histories := []cimodel.AttributeHistory{
		{RecordID: 1, CIID: 100, AttrID: ptr(model.ID(10)), OperateType: ptr("u"), Old: ptr("a"), New: ptr("b")},
		{RecordID: 1, CIID: 100, AttrID: ptr(model.ID(11)), OperateType: ptr("u"), Old: ptr("4"), New: ptr("8")},
		{RecordID: 2, CIID: 101, AttrID: ptr(model.ID(10)), OperateType: ptr("a"), New: ptr("c")},
	}
	for i := range histories {
		if err := db.Create(&histories[i]).Error; err != nil {
			t.Fatalf("seed attribute history: %v", err)
		}
	}

	relation := cimodel.RelationHistory{
		RecordID: 1, FirstCIID: ptr(model.ID(100)), SecondCIID: ptr(model.ID(101)),
		RelationTypeID: ptr(model.ID(3)), RelationID: 9, OperateType: ptr("a"),
	}
	if err := db.Create(&relation).Error; err != nil {
		t.Fatalf("seed relation history: %v", err)
	}

	typ := cimodel.TypeHistory{
		OperateType: ptr("10"), TypeID: 1, AttrID: ptr(model.ID(10)),
		UID: ptr(model.ID(7)), Change: map[string]any{"alias": "主机名"},
	}
	typ.ID = 40
	if err := db.Create(&typ).Error; err != nil {
		t.Fatalf("seed type history: %v", err)
	}
	if err := db.Model(&cimodel.TypeHistory{}).Where("id = ?", 40).Update("created_at", at(9)).Error; err != nil {
		t.Fatalf("stamp type history: %v", err)
	}

	trigger := cimodel.TriggerHistory{
		RecordID: ptr(model.ID(1)), CIID: 100, TriggerID: ptr(model.ID(4)),
		TriggerName: ptr("notify"), IsOK: true, OperateType: ptr("a"),
	}
	trigger.ID = 50
	if err := db.Create(&trigger).Error; err != nil {
		t.Fatalf("seed trigger history: %v", err)
	}

	// Attribute 10 is a password that was later soft-deleted; 11 is ordinary.
	// The history join must still see 10's flags after deletion.
	attributes := []citypemodel.Attribute{
		{Name: "password", Alias: "密码", ValueType: "2", IsPassword: true},
		{Name: "cpu", Alias: "CPU", ValueType: "3"},
	}
	attributes[0].ID = 10
	attributes[1].ID = 11
	for i := range attributes {
		if err := db.Create(&attributes[i]).Error; err != nil {
			t.Fatalf("seed attribute: %v", err)
		}
	}
	if err := db.Unscoped().Model(&citypemodel.Attribute{}).
		Where("id = ?", 10).UpdateColumn("delete_marker", 1).Error; err != nil {
		t.Fatalf("soft-delete password attribute: %v", err)
	}
}

func TestListAttributeRecordsJoinsTheRecordAndOrdersNewestFirst(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	repo := NewRepo(db)

	rows, total, err := repo.ListAttributeRecords(context.Background(), historybiz.AttributeFilter{
		TypeID: 1, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("rows = %d of %d, want 2", len(rows), total)
	}
	if rows[0].ID != 2 || rows[1].ID != 1 {
		t.Fatalf("order = %d,%d, want newest first", rows[0].ID, rows[1].ID)
	}
	if rows[0].Record.UID != 7 || rows[0].Record.TypeID != 1 || rows[0].Record.Origin != "ui" {
		t.Fatalf("record = %+v", rows[0].Record)
	}
	if rows[0].Record.CreatedAt.IsZero() {
		t.Fatal("the record's created_at was not carried into the row")
	}
}

func TestListAttributeRecordsAppliesEveryFilterAndPages(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	repo := NewRepo(db)
	ctx := context.Background()

	rows, total, err := repo.ListAttributeRecords(ctx, historybiz.AttributeFilter{
		Start: at(10), End: at(10), UID: 7, TypeID: 1, CIID: 100, AttrID: 10,
		OperateType: "u", Page: 1, PageSize: 1,
	})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].AttrID != 10 {
		t.Fatalf("filtered rows = %+v of %d, want the one attr-10 change", rows, total)
	}

	// The second page of a two-row filter is empty, not the first page again.
	rows, total, err = repo.ListAttributeRecords(ctx, historybiz.AttributeFilter{
		TypeID: 1, Page: 2, PageSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 0 {
		t.Fatalf("page 2 = %d of %d, want 0 of 2", len(rows), total)
	}
}

func TestListRelationRecordsFiltersAndCarriesTheEnds(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	repo := NewRepo(db)

	rows, total, err := repo.ListRelationRecords(context.Background(), historybiz.RelationFilter{
		FirstCIID: 100, SecondCIID: 101, OperateType: "a", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("rows = %d of %d", len(rows), total)
	}
	row := rows[0]
	if row.Record.UID != 7 || row.FirstCIID == nil || *row.FirstCIID != 100 ||
		row.SecondCIID == nil || *row.SecondCIID != 101 || row.RelationTypeID == nil || *row.RelationTypeID != 3 {
		t.Fatalf("row = %+v", row)
	}
}

func TestListTypeRecordsFiltersOnItsOwnColumns(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	repo := NewRepo(db)

	rows, total, err := repo.ListTypeRecords(context.Background(), historybiz.TypeFilter{
		Start: at(9), End: at(9), UID: 7, TypeID: 1, OperateType: "10", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].AttrID == nil || *rows[0].AttrID != 10 {
		t.Fatalf("rows = %+v of %d", rows, total)
	}
	if rows[0].Change["alias"] != "主机名" {
		t.Fatalf("change = %+v", rows[0].Change)
	}
}

func TestTriggerRecordsLeftJoinTheRecord(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	// A firing with no operation record: an operator ran the trigger by hand,
	// and the row must still be readable.
	orphan := cimodel.TriggerHistory{
		CIID: 101, TriggerName: ptr("manual"), IsOK: false, OperateType: ptr("a"),
	}
	orphan.ID = 51
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(db)

	rows, total, err := repo.ListTriggerRecords(context.Background(), historybiz.TriggerFilter{
		Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("rows = %d of %d, want 2", len(rows), total)
	}
	if rows[0].ID != 51 || rows[0].UID != 0 {
		t.Fatalf("orphan row = %+v, want the latest row with no uid", rows[0])
	}
	if rows[1].UID != 7 || rows[1].TypeID != 1 || rows[1].TriggerName != "notify" {
		t.Fatalf("joined row = %+v", rows[1])
	}
}

func TestPerCIQueriesFilterAndPage(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	repo := NewRepo(db)
	ctx := context.Background()

	rows, total, err := repo.ListCIAttributeRecords(ctx, 100, 1, 1)
	if err != nil {
		t.Fatalf("ci attributes: %v", err)
	}
	if total != 2 || len(rows) != 1 || rows[0].ID != 2 {
		t.Fatalf("ci attributes = %d rows of %d, first %+v", len(rows), total, rows)
	}

	triggers, total, err := repo.ListCITriggerRecords(ctx, 100, 1, 20)
	if err != nil {
		t.Fatalf("ci triggers: %v", err)
	}
	if total != 1 || len(triggers) != 1 || triggers[0].TriggerName != "notify" {
		t.Fatalf("ci triggers = %+v of %d", triggers, total)
	}
}

// A canceled request must stop its query. AGENTS.md requires new IO paths to
// carry the caller's context; a store that ignores it keeps a pooled
// connection busy for a response nobody will read.
func TestHistoryQueriesHonourContext(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	repo := NewRepo(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := []struct {
		name string
		call func() error
	}{
		{"attributes", func() error {
			_, _, err := repo.ListAttributeRecords(ctx, historybiz.AttributeFilter{Page: 1, PageSize: 20})
			return err
		}},
		{"relations", func() error {
			_, _, err := repo.ListRelationRecords(ctx, historybiz.RelationFilter{Page: 1, PageSize: 20})
			return err
		}},
		{"types", func() error {
			_, _, err := repo.ListTypeRecords(ctx, historybiz.TypeFilter{Page: 1, PageSize: 20})
			return err
		}},
		{"triggers", func() error {
			_, _, err := repo.ListTriggerRecords(ctx, historybiz.TriggerFilter{Page: 1, PageSize: 20})
			return err
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("a canceled context did not stop the query")
			}
		})
	}
}

// The sensitivity flag travels with the history row, so a password attribute
// that has since been soft-deleted is still masked.
func TestListAttributeRecordsCarriesSensitivityFromTheDictionary(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	rows, _, err := NewRepo(db).ListAttributeRecords(context.Background(), historybiz.AttributeFilter{
		Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[model.ID]historybiz.AttributeRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if !byID[1].Sensitive {
		t.Fatalf("row 1 (soft-deleted password) not sensitive: %+v", byID[1])
	}
	if !byID[3].Sensitive {
		t.Fatalf("row 3 (soft-deleted password) not sensitive: %+v", byID[3])
	}
	if byID[2].Sensitive {
		t.Fatalf("row 2 (cpu) marked sensitive: %+v", byID[2])
	}
}
