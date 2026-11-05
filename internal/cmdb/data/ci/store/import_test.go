package store

import (
	"context"
	"strings"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// The sheet import against a database.
//
// WHAT IS TESTED HERE IS WHAT A PREVIEW AND A COMMIT DO that makes an import
// differ from a loop of instance writes: one transaction, rows that see each
// other, and a preview that agrees with the commit.

func newImportFixture(t *testing.T) (*cibiz.Usecase, *Repo) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.AutoMigrate(&citypemodel.CIType{}, &citypemodel.Attribute{}, &citypemodel.CITypeAttribute{}); err != nil {
		t.Fatalf("migrate dictionary: %v", err)
	}

	// One model with a text identifier, a number and a text field.
	ct := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: 1, Enabled: true}
	ct.ID = 1
	if err := db.Create(&ct).Error; err != nil {
		t.Fatal(err)
	}
	for id, spec := range map[model.ID]struct {
		name, vt string
	}{
		1: {"hostname", "2"},
		2: {"cpu", "0"},
		3: {"note", "2"},
	} {
		attr := citypemodel.Attribute{Name: spec.name, Alias: spec.name, ValueType: spec.vt, IsIndex: true}
		attr.ID = id
		if err := db.Create(&attr).Error; err != nil {
			t.Fatal(err)
		}
		binding := citypemodel.CITypeAttribute{TypeID: 1, AttrID: id}
		if err := db.Create(&binding).Error; err != nil {
			t.Fatal(err)
		}
	}

	repo := NewRepo(db)
	return cibiz.NewUsecase(repo, &hostResolver{}, nil), repo
}

// hostResolver answers the model side for the import fixture's single model.
type hostResolver struct{}

func (hostResolver) ResolveForCI(_ context.Context, ref string) (*cibiz.AttributeSet, error) {
	if ref != "host" && ref != "1" {
		return nil, nil
	}
	ct := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: 1, Enabled: true}
	ct.ID = 1
	specs := map[model.ID]cibiz.AttributeSpec{
		1: {ID: 1, Name: "hostname", Alias: "hostname", ValueType: "2", IsIndex: true, IsUnique: true},
		2: {ID: 2, Name: "cpu", Alias: "cpu", ValueType: "0", IsIndex: true},
		3: {ID: 3, Name: "note", Alias: "note", ValueType: "2"},
	}
	set := &cibiz.AttributeSet{
		Type: ct, Specs: specs,
		ByName: map[string]model.ID{}, ByAlias: map[string]model.ID{},
		UniqueAttrID: 1, UniqueAttr: specs[1], ShowAttr: specs[1],
	}
	for id, spec := range specs {
		set.ByName[spec.Name] = id
		set.ByAlias[spec.Alias] = id
	}
	return set, nil
}

func (hostResolver) RelationBetween(context.Context, model.ID, model.ID) (cibiz.TypeRelationRule, bool, error) {
	return cibiz.TypeRelationRule{}, false, nil
}

func (hostResolver) HierarchyLevels(context.Context, model.ID, int, bool) (cibiz.HierarchyLevels, error) {
	return cibiz.HierarchyLevels{}, nil
}

func importSheet(policy cibiz.ExistPolicy, rows ...[]any) cibiz.ImportRequest {
	return cibiz.ImportRequest{
		TypeRef:     "host",
		Headers:     []string{"hostname", "cpu", "note"},
		Rows:        rows,
		ExistPolicy: policy,
	}
}

func TestImportPreviewWritesNothing(t *testing.T) {
	uc, _ := newImportFixture(t)
	ctx := context.Background()

	got, err := uc.PreviewImport(ctx, importSheet(cibiz.ExistReject,
		[]any{"web-01", "8", "first"},
		[]any{"web-02", "not-a-number", ""},
	))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got.Total != 2 || got.Valid != 1 || got.Invalid != 1 || got.Creates != 1 {
		t.Fatalf("preview = %+v", got)
	}
	if got.Rows[1].OK || len(got.Rows[1].Issues) == 0 {
		t.Fatalf("the bad row was not reported: %+v", got.Rows[1])
	}
	// NOTHING WAS WRITTEN, which is what makes a preview a preview.
	list, err := uc.Search(ctx, cibiz.SearchRequest{Query: "_type:host"})
	if err != nil {
		t.Fatal(err)
	}
	if list.NumFound != 0 {
		t.Fatalf("the preview wrote %d instances", list.NumFound)
	}
}

func TestImportCommitIsOneTransaction(t *testing.T) {
	uc, _ := newImportFixture(t)
	ctx := context.Background()

	// A GOOD ROW FOLLOWED BY A BAD ONE. cantor's browser loop wrote the first
	// and reported the second, leaving an operator to reconcile the sheet
	// against a half-imported model.
	_, err := uc.CommitImport(ctx, importSheet(cibiz.ExistReject,
		[]any{"web-01", "8", ""},
		[]any{"web-02", "not-a-number", ""},
	))
	if err == nil {
		t.Fatal("a sheet with an unwritable row was committed")
	}
	// The refusal NAMES the row, because "one of your five thousand rows is
	// wrong" is not a thing anybody can act on.
	if !strings.Contains(err.Error(), "row 3") {
		t.Fatalf("the refusal does not say which row: %v", err)
	}
	list, searchErr := uc.Search(context.Background(), cibiz.SearchRequest{Query: "_type:host"})
	if searchErr != nil {
		t.Fatal(searchErr)
	}
	if list.NumFound != 0 {
		t.Fatalf("the refused import wrote %d instances", list.NumFound)
	}
}

func TestImportCommitWritesTheSheet(t *testing.T) {
	uc, _ := newImportFixture(t)
	ctx := context.Background()

	got, err := uc.CommitImport(ctx, importSheet(cibiz.ExistReject,
		[]any{"web-01", "8", "first"},
		[]any{"web-02", 4, ""},
	))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got.Written != 2 || got.Creates != 2 || got.Updates != 0 {
		t.Fatalf("result = %+v", got)
	}
	for _, row := range got.Rows {
		if row.InstanceID == 0 {
			t.Fatalf("a written row has no instance id: %+v", row)
		}
	}

	list, err := uc.Search(ctx, cibiz.SearchRequest{Query: "_type:host,cpu:8"})
	if err != nil {
		t.Fatal(err)
	}
	if list.NumFound != 1 {
		t.Fatalf("cpu:8 matched %d, want the one row that carries a numeric 8", list.NumFound)
	}

	// A SECOND IMPORT OF THE SAME SHEET is refused under the create policy,
	// and the refusal names the row rather than the sheet.
	_, err = uc.CommitImport(ctx, importSheet(cibiz.ExistReject, []any{"web-01", "9", ""}))
	if err == nil {
		t.Fatal("a duplicate import was accepted")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}

	// Under `replace` the same sheet updates, and the preview says so first.
	preview, err := uc.PreviewImport(ctx, importSheet(cibiz.ExistReplace, []any{"web-01", "9", ""}))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Updates != 1 || preview.Creates != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	done, err := uc.CommitImport(ctx, importSheet(cibiz.ExistReplace, []any{"web-01", "9", ""}))
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if done.Updates != 1 || done.Creates != 0 {
		t.Fatalf("result = %+v", done)
	}
}

// ROWS SEE EACH OTHER, because they are one transaction.
//
// A sheet that adds a column to rows it also creates must resolve the second
// occurrence against the first row's insert. cantor's per-row requests could
// not: each was its own transaction, so the second row's lookup did not see the
// first's.
func TestImportRowsSeeEachOther(t *testing.T) {
	uc, _ := newImportFixture(t)
	ctx := context.Background()

	got, err := uc.CommitImport(ctx, importSheet(cibiz.ExistReplace,
		[]any{"web-01", "4", "first"},
		[]any{"web-01", "8", "second"},
	))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got.Creates != 1 || got.Updates != 1 {
		t.Fatalf("result = %+v; the second row should update the first row's instance", got)
	}
	if got.Rows[0].InstanceID != got.Rows[1].InstanceID {
		t.Fatalf("the two rows are different instances: %d and %d",
			got.Rows[0].InstanceID, got.Rows[1].InstanceID)
	}
	inst, err := uc.Get(ctx, got.Rows[1].InstanceID, []string{"cpu", "note"}, cibiz.RetKeyName)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Values["note"] != "second" || inst.Values["cpu"] == nil {
		t.Fatalf("the last row did not win: %v", inst.Values)
	}
}

// A sheet whose headers match nothing, and one that identifies nothing, are
// refused before any row is examined.
func TestImportRefusesAnUnusableSheet(t *testing.T) {
	uc, _ := newImportFixture(t)
	ctx := context.Background()

	_, err := uc.PreviewImport(ctx, cibiz.ImportRequest{
		TypeRef: "host", Headers: []string{"a", "b"}, Rows: [][]any{{"1", "2"}},
	})
	if err == nil {
		t.Fatal("a sheet whose columns match nothing was accepted")
	}

	_, err = uc.PreviewImport(ctx, cibiz.ImportRequest{
		TypeRef: "host", Headers: []string{"cpu"}, Rows: [][]any{{"8"}},
	})
	if err == nil {
		t.Fatal("a sheet with no identifier column was accepted")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("the refusal does not name the missing identifier: %v", err)
	}

	// The row and column limits are refusals, not truncations.
	wide := make([]string, cibiz.MaxImportColumns+1)
	for i := range wide {
		wide[i] = "cpu"
	}
	if _, err := uc.PreviewImport(ctx, cibiz.ImportRequest{
		TypeRef: "host", Headers: wide, Rows: [][]any{{"1"}},
	}); err == nil {
		t.Fatal("an over-wide sheet was accepted")
	}
}
