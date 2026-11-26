package ci

import (
	"fmt"
	"sort"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// Turning a spreadsheet into instances.
//
// A SHEET IS A GRID OF TEXT AND A MODEL IS A SET OF TYPED ATTRIBUTES, and the
// whole job here is the mapping between them. It is PURE — no storage, no
// time, no context — because it is the part that goes wrong: a header nobody
// recognises, a cell that is not the type the attribute declares, a column
// supplied twice. All of those are decisions about the sheet, and a caller
// wants to be told about them BEFORE anything is written.
//
// THE COLUMN MAPPING LIVES HERE RATHER THAN IN THE CONSOLE because it needs the
// model's attribute set. cantor's import was the console's own: it parsed the
// sheet with a browser library and posted one instance at a time, so the only
// thing that knew what a column meant was a form on a page.

// Bounds on one import.
//
// THEY ARE THE PORT'S. cantor's import was a browser loop with no bound at all
// — a sheet of fifty thousand rows was fifty thousand requests, and a failure
// in the middle left the ones before it written. The bounds here are the point
// at which the request is refused rather than truncated, because a partial
// import that looks complete is the state this module exists to prevent.
const (
	// MaxImportRows is how many data rows one request may carry.
	MaxImportRows = 5000

	// MaxImportColumns is how many columns one sheet may carry. A model with
	// more attributes than this is a model whose sheet is unreadable anyway.
	MaxImportColumns = 200
)

// ImportIssue is one problem, either with a row as a whole or with one of its
// cells.
type ImportIssue struct {
	// Column is the sheet's own header, so a person can find the cell.
	Column string `json:"column,omitempty"`

	Message string `json:"message"`
}

// ImportColumn is one column of the sheet, matched against the model.
type ImportColumn struct {
	// Index is the column's position in the sheet, 0-based.
	Index int `json:"index"`

	// Header is what the sheet called it, trimmed.
	Header string `json:"header"`

	// AttrID, Name and Alias describe the attribute it was matched to.
	AttrID model.ID `json:"attr_id,omitempty"`
	Name   string   `json:"attr,omitempty"`
	Alias  string   `json:"alias,omitempty"`

	// Identifier marks the attribute that identifies an instance. A column
	// matched to anything else supplies a value; this one decides WHICH
	// instance the row is about.
	Identifier bool `json:"identifier,omitempty"`

	// Ignored marks an attribute the model carries but does not accept a value
	// for on a write — a computed one.
	Ignored bool `json:"ignored,omitempty"`

	// Matched says whether a value in this column will be written.
	Matched bool `json:"matched"`

	// Note explains an unmatched column. It is reported rather than dropped:
	// a column the operator filled in and the port silently discarded is the
	// failure mode an import must not have.
	Note string `json:"note,omitempty"`
}

// MatchColumns maps a sheet's headers onto a model's attributes.
//
// EXACT NAMES WIN, THEN A CASE-INSENSITIVE FALLBACK, and an AMBIGUOUS fallback
// is refused rather than guessed. A spreadsheet's headers are written by a
// person, so `主机名` and `HostName` both have to work; but a model with both
// `Host` and `host` has two attributes one keystroke apart, and picking one of
// them would write the wrong column.
func MatchColumns(set *AttributeSet, headers []string) []ImportColumn {
	out := make([]ImportColumn, 0, len(headers))
	claimed := map[model.ID]string{}

	for i, raw := range headers {
		header := strings.TrimSpace(raw)
		col := ImportColumn{Index: i, Header: header}

		switch {
		case header == "":
			col.Note = "the column has no header, so nothing says what it carries"
		default:
			id, note := resolveHeader(set, header)
			if id == 0 {
				col.Note = note
				break
			}
			spec, ok := set.Specs[id]
			if !ok {
				col.Note = "the attribute is not part of this model"
				break
			}
			if !spec.Modifiers().IsPasswordSafe() {
				col.AttrID, col.Name, col.Alias = spec.ID, spec.Name, spec.Alias
				col.Note = "a secret is never imported from a sheet, and never rendered into one"
				break
			}
			if spec.IsComputed {
				col.AttrID, col.Name, col.Alias, col.Ignored = spec.ID, spec.Name, spec.Alias, true
				col.Note = "the model derives this attribute; a supplied value would be overwritten"
				break
			}
			if prev, dup := claimed[id]; dup {
				col.AttrID, col.Name, col.Alias = spec.ID, spec.Name, spec.Alias
				col.Note = fmt.Sprintf("the column %q already supplies this attribute", prev)
				break
			}
			claimed[id] = header
			col.AttrID, col.Name, col.Alias = spec.ID, spec.Name, spec.Alias
			col.Identifier = id == set.UniqueAttrID
			col.Matched = true
			col.Note = note
		}
		out = append(out, col)
	}
	return out
}

// resolveHeader finds the attribute a header names, and says how.
func resolveHeader(set *AttributeSet, header string) (model.ID, string) {
	if id, ok := set.ResolveAttrID(header); ok {
		return id, ""
	}

	// The fallback: fold case and try again. It is deliberately second, so an
	// exact name or alias can never lose to a casefolded one.
	lower := strings.ToLower(header)
	var found []model.ID
	for name, id := range set.ByName {
		if strings.ToLower(name) == lower {
			found = append(found, id)
		}
	}
	for alias, id := range set.ByAlias {
		if strings.ToLower(alias) == lower {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		return 0, "no attribute of this model is named that"
	case 1:
		return found[0], "matched by ignoring case"
	default:
		return 0, fmt.Sprintf("%d attributes of this model differ from it only by case", len(found))
	}
}

// ImportRow is one sheet row, and what would happen to it.
type ImportRow struct {
	// Number is the row's position among the DATA rows, 1-based.
	Number int `json:"row"`

	// SheetRow is the spreadsheet's own row number, so a person can find it.
	// It is Number plus the header's position, which is 1 unless the sheet has
	// no header.
	SheetRow int `json:"sheet_row"`

	// OK says the row can be written.
	OK bool `json:"ok"`

	// Action is what the write would do: "create", "update", or empty when the
	// row cannot be written.
	Action string `json:"action,omitempty"`

	// Unique is the value that identifies the instance, as the row gave it.
	Unique string `json:"unique,omitempty"`

	// InstanceID is the instance the row would update. Zero means it would be
	// created.
	InstanceID model.ID `json:"ci_id,omitempty"`

	Issues []ImportIssue `json:"issues,omitempty"`

	// Values is the resolved write, kept so the commit does not resolve twice.
	Values map[string]any `json:"-"`
}

// The actions a row can take.
const (
	importActionCreate = "create"
	importActionUpdate = "update"
)

// buildImportRow resolves one sheet row against the model.
//
// AN EMPTY CELL SUPPLIES NOTHING. That is the difference between a sheet and a
// form: a spreadsheet is full of blank cells belonging to rows that have no
// value for a particular attribute, and treating a blank as "clear this field"
// would erase data on an update import — the one outcome a person filling in a
// sheet cannot foresee.
func buildImportRow(set *AttributeSet, columns []ImportColumn, cells []any, number, headerRows int, policy ExistPolicy) ImportRow {
	row := ImportRow{
		Number:   number,
		SheetRow: number + headerRows,
		Values:   map[string]any{},
	}

	supplied := map[string]any{}
	for _, col := range columns {
		if !col.Matched {
			continue
		}
		if col.Index >= len(cells) {
			continue
		}
		raw := cells[col.Index]
		if isBlankCell(raw) {
			continue
		}
		supplied[col.Name] = raw
	}

	if len(supplied) == 0 {
		row.Issues = append(row.Issues, ImportIssue{
			Message: "the row has no values in any column this model accepts",
		})
		return row
	}

	// The per-cell type check comes FIRST, so a row of bad cells reports all of
	// them rather than only the first thing the writer happened to trip over.
	for _, col := range columns {
		if !col.Matched {
			continue
		}
		raw, ok := supplied[col.Name]
		if !ok {
			continue
		}
		spec := set.Specs[col.AttrID]
		row.Issues = appendCellIssue(row.Issues, col, spec, raw)
	}
	if len(row.Issues) > 0 {
		return row
	}

	normalized, err := NormalizeValues(set, supplied, "")
	if err != nil {
		row.Issues = append(row.Issues, ImportIssue{Message: err.Error()})
		return row
	}
	if err := applyDefaultsStatic(set, normalized, ""); err != nil {
		row.Issues = append(row.Issues, ImportIssue{Message: err.Error()})
		return row
	}
	if err := ValidateRequired(set.Specs, normalized); err != nil {
		row.Issues = append(row.Issues, ImportIssue{Message: err.Error()})
		return row
	}

	unique, err := uniqueValueOf(set, normalized)
	if err != nil {
		row.Issues = append(row.Issues, ImportIssue{Message: err.Error()})
		return row
	}
	row.Unique = fmt.Sprint(unique)
	row.OK = true
	row.Action = importActionCreate
	if policy == ExistReplace {
		row.Action = importActionUpdate
	}
	for name, raw := range supplied {
		row.Values[name] = raw
	}
	return row
}

// appendCellIssue checks one cell against its attribute's type.
func appendCellIssue(issues []ImportIssue, col ImportColumn, spec AttributeSpec, raw any) []ImportIssue {
	if _, err := spec.ValueType.Validate(raw); err != nil {
		// The value layer's message names the type and the offending text,
		// which is what a person needs to fix the cell.
		return append(issues, ImportIssue{Column: col.Header, Message: trimValuePrefix(err.Error())})
	}
	return issues
}

// trimValuePrefix drops the package prefix a wrapped error carries, because the
// message is shown to a person filling in a spreadsheet.
func trimValuePrefix(msg string) string {
	return strings.TrimPrefix(msg, "cmdb: ")
}

// isBlankCell reports whether a spreadsheet cell carries nothing.
//
// A CELL OF SPACES IS BLANK, and so is a nil: a sheet written by hand has both
// wherever somebody left a column alone.
func isBlankCell(raw any) bool {
	switch v := raw.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	}
	return false
}

// applyDefaultsStatic applies the model's defaults without a clock reading.
//
// THE TIME-DEPENDENT DEFAULTS ARE SKIPPED, and that is what keeps a preview
// honest: `$created_at` cannot be evaluated before the write commits, so a
// preview that evaluated it would show a value the commit would not use. The
// commit applies the real defaults.
func applyDefaultsStatic(set *AttributeSet, values map[model.ID]any, _ string) error {
	ids := make([]model.ID, 0, len(set.Specs))
	for id := range set.Specs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		spec := set.Specs[id]
		if _, done := values[id]; done {
			continue
		}
		token, literal, ok := ClassifyDefault(spec.Default)
		if !ok {
			continue
		}
		if token != DefaultNone {
			// A computed token: the commit fills it in.
			continue
		}
		values[id] = literal
	}
	return nil
}

// uniqueValueOf resolves a row's identifying value without the use case's
// clock.
func uniqueValueOf(set *AttributeSet, values map[model.ID]any) (any, error) {
	if raw, ok := values[set.UniqueAttrID]; ok && !isEmptyValue(raw) {
		return raw, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUniqueValueRequired, set.UniqueAttr.Name)
}

// ImportColumnSummary counts what the mapping found, which is what a preview's
// header line shows.
func ImportColumnSummary(columns []ImportColumn) (matched, unmatched int) {
	for _, col := range columns {
		if col.Matched {
			matched++
		} else {
			unmatched++
		}
	}
	return matched, unmatched
}
