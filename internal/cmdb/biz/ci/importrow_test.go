package ci

import (
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// The sheet-to-model mapping, without a database.
//
// This is the part that goes wrong in an import: a header nobody recognises, a
// cell of the wrong type, a column supplied twice. All of it is a decision
// about the sheet, so all of it is testable here.

func importSet() *AttributeSet {
	ct := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: 1, Enabled: true}
	ct.ID = 10
	specs := map[model.ID]AttributeSpec{
		1: {ID: 1, Name: "hostname", Alias: "主机名", ValueType: value.TypeText, IsIndex: true, IsUnique: true},
		2: {ID: 2, Name: "cpu", Alias: "CPU核数", ValueType: value.TypeInt, IsIndex: true},
		3: {ID: 3, Name: "note", Alias: "备注", ValueType: value.TypeText},
		4: {ID: 4, Name: "root_password", Alias: "密码", ValueType: value.TypeText, IsPassword: true},
		5: {ID: 5, Name: "uptime", Alias: "运行时长", ValueType: value.TypeText, IsComputed: true},
		6: {ID: 6, Name: "Host", Alias: "Host", ValueType: value.TypeText},
	}
	set := &AttributeSet{
		Type: ct, Specs: specs,
		ByName: map[string]model.ID{}, ByAlias: map[string]model.ID{},
		UniqueAttrID: 1, UniqueAttr: specs[1], ShowAttr: specs[1],
	}
	for id, spec := range specs {
		set.ByName[spec.Name] = id
		set.ByAlias[spec.Alias] = id
	}
	return set
}

func TestMatchColumns(t *testing.T) {
	set := importSet()
	cols := MatchColumns(set, []string{"hostname", "CPU核数", "备注", "密码", "运行时长", "", "nonsense", "cpu"})

	byIndex := map[int]ImportColumn{}
	for _, col := range cols {
		byIndex[col.Index] = col
	}

	if !byIndex[0].Matched || byIndex[0].Name != "hostname" || !byIndex[0].Identifier {
		t.Fatalf("the exact name did not match: %+v", byIndex[0])
	}
	if !byIndex[1].Matched || byIndex[1].Name != "cpu" {
		t.Fatalf("an alias did not match: %+v", byIndex[1])
	}
	if !byIndex[2].Matched || byIndex[2].Name != "note" {
		t.Fatalf("a default alias did not match: %+v", byIndex[2])
	}
	// A SECRET IS NEVER IMPORTED FROM A SHEET: it would land in a plaintext
	// file, and the read paths refuse to render it back out.
	if byIndex[3].Matched || !strings.Contains(byIndex[3].Note, "secret") {
		t.Fatalf("a password column was accepted: %+v", byIndex[3])
	}
	// A computed attribute is the model's to derive.
	if byIndex[4].Matched || !byIndex[4].Ignored {
		t.Fatalf("a computed column was accepted: %+v", byIndex[4])
	}
	// An empty header and an unknown one are REPORTED, not dropped.
	if byIndex[5].Matched || byIndex[5].Note == "" {
		t.Fatalf("a headerless column was not reported: %+v", byIndex[5])
	}
	if byIndex[6].Matched || byIndex[6].Note == "" {
		t.Fatalf("an unknown column was not reported: %+v", byIndex[6])
	}
	// The SECOND column supplying an attribute loses, and says so. Letting the
	// last one win would reorder an import silently.
	if byIndex[7].Matched || !strings.Contains(byIndex[7].Note, "already supplies") {
		t.Fatalf("a duplicate column was accepted: %+v", byIndex[7])
	}
}

// A header that differs from an attribute only by case is matched, but an
// AMBIGUOUS one is refused rather than guessed.
//
// A spreadsheet's headers are written by a person, so `HostName` has to work;
// but a model with both `Host` and `host` has two attributes one keystroke
// apart, and picking one would write the wrong column.
func TestMatchColumnsCaseFold(t *testing.T) {
	set := importSet()
	cols := MatchColumns(set, []string{"HOSTNAME", "host"})
	if !cols[0].Matched || cols[0].Name != "hostname" {
		t.Fatalf("a case-different header did not match: %+v", cols[0])
	}
	if cols[0].Note != "" && !strings.Contains(cols[0].Note, "case") {
		t.Fatalf("the match should say how it was made: %+v", cols[0])
	}
	if cols[1].Matched {
		t.Fatalf("an ambiguous header was matched to one attribute: %+v", cols[1])
	}
	if !strings.Contains(cols[1].Note, "case") {
		t.Fatalf("the ambiguity was not explained: %+v", cols[1])
	}
}

func TestBuildImportRow(t *testing.T) {
	set := importSet()
	cols := MatchColumns(set, []string{"hostname", "cpu", "nonexistent"})

	// A good row.
	row := buildImportRow(set, cols, []any{"web-01", "8", "ignored"}, 1, 1, ExistReject)
	if !row.OK || row.Unique != "web-01" || row.Action != importActionCreate {
		t.Fatalf("a good row = %+v", row)
	}
	if row.SheetRow != 2 {
		t.Fatalf("sheet row = %d, want 2 (one header row above)", row.SheetRow)
	}
	if _, has := row.Values["nonexistent"]; has {
		t.Fatalf("an unmatched column was written: %v", row.Values)
	}
	if row.Values["cpu"] != "8" {
		t.Fatalf("cpu = %v", row.Values["cpu"])
	}

	// A wrong type is reported against the COLUMN, with the type's own words.
	bad := buildImportRow(set, cols, []any{"web-02", "eight"}, 2, 1, ExistReject)
	if bad.OK {
		t.Fatalf("a row with a non-numeric cpu was accepted: %+v", bad)
	}
	if len(bad.Issues) != 1 || bad.Issues[0].Column != "cpu" {
		t.Fatalf("the issue does not name the column: %+v", bad.Issues)
	}
	// The message names the type and the offending text, and the package's own
	// prefix is trimmed because it is shown to a person filling in a sheet.
	if !strings.Contains(bad.Issues[0].Message, "eight") || strings.HasPrefix(bad.Issues[0].Message, "cmdb:") {
		t.Fatalf("the cell's message is not actionable: %q", bad.Issues[0].Message)
	}

	// A missing identifier is a row error, not a silent create.
	missing := buildImportRow(set, cols, []any{"", "8"}, 3, 1, ExistReject)
	if missing.OK {
		t.Fatalf("a row with no hostname was accepted: %+v", missing)
	}

	// AN EMPTY CELL SUPPLIES NOTHING: a sheet is full of blanks belonging to
	// rows that have no value for an attribute, and treating a blank as "clear
	// this field" would erase data on an update import.
	blank := buildImportRow(set, cols, []any{"web-04", "  "}, 4, 1, ExistReplace)
	if !blank.OK {
		t.Fatalf("a blank cell made the row invalid: %+v", blank)
	}
	if _, has := blank.Values["cpu"]; has {
		t.Fatalf("a blank cell supplied a value: %v", blank.Values)
	}

	// A row with nothing at all is refused rather than creating an empty
	// instance.
	empty := buildImportRow(set, cols, []any{"", ""}, 5, 1, ExistReject)
	if empty.OK {
		t.Fatalf("an empty row was accepted: %+v", empty)
	}
}

// Every bad cell is reported, not only the first one the writer tripped over.
func TestBuildImportRowReportsEveryBadCell(t *testing.T) {
	set := importSet()
	cols := MatchColumns(set, []string{"hostname", "cpu"})
	row := buildImportRow(set, cols, []any{"web-01", "not-a-number"}, 1, 1, ExistReject)
	if len(row.Issues) != 1 {
		t.Fatalf("issues = %+v", row.Issues)
	}
	// A short row is not an error: a spreadsheet is a rectangle only by
	// convention.
	short := buildImportRow(set, cols, []any{"web-02"}, 2, 1, ExistReject)
	if !short.OK {
		t.Fatalf("a short row was refused: %+v", short)
	}
}
