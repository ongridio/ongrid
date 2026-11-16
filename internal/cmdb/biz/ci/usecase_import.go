package ci

import (
	"context"
	"fmt"
	"sort"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Importing a sheet.
//
// WHAT THE PORT ADDS TO CANTOR HERE IS A BACKEND. cantor's import was the
// console's own: a browser library parsed the file and then posted one instance
// at a time, so a five-thousand-row sheet was five thousand requests, each its
// own transaction, with no record of where it stopped. There was nothing to
// validate against — the console knew the model only as a form — and nothing to
// report: a failure was one request's error among five thousand.
//
// TWO ENDPOINTS, ONE ROW BUILDER. `Preview` answers "what would this do" and
// writes nothing; `Commit` does it in one transaction. They share the row
// builder so a preview cannot promise something the commit then refuses.
//
// THE CONSOLE STILL PARSES THE FILE, and that is a deliberate division: reading
// xlsx is a browser library's job and the alternative is a Go dependency the
// project has not reviewed. What the console sends is the SHEET — headers and
// cells — so the mapping from a column to an attribute, which needs the model,
// happens here.

// ImportRequest is one sheet.
type ImportRequest struct {
	// TypeRef names the model the rows are instances of.
	TypeRef string

	// Headers are the sheet's first row, in order.
	Headers []string

	// Rows are the data rows, each a list of cells positioned like the headers.
	// A short row is padded and a long one is ignored, because a spreadsheet is
	// a rectangle only by convention.
	Rows [][]any

	// ExistPolicy decides what a row does when its identifying value is already
	// taken. Empty means reject, which is a create.
	ExistPolicy ExistPolicy

	// NoAttributePolicy decides what happens to a column the model does not
	// carry. Empty means the column is dropped — and it is REPORTED either way,
	// which is what stops a filled-in column from vanishing.
	NoAttributePolicy ExistPolicy

	Audit AuditInfo
}

// ImportPreview is what a sheet would do.
type ImportPreview struct {
	// Total, Valid and Invalid count the data rows.
	Total   int `json:"total"`
	Valid   int `json:"valid"`
	Invalid int `json:"invalid"`

	// Columns is the sheet's columns, matched against the model. It includes
	// the ones that did NOT match, because a column the operator filled in and
	// the port ignored is the failure an import must not have.
	Columns []ImportColumn `json:"columns"`

	// Rows is one entry per data row, in the sheet's order.
	Rows []ImportRow `json:"rows"`

	// Creates and Updates are the actions the valid rows would take.
	Creates int `json:"creates"`
	Updates int `json:"updates"`
}

// ImportResult is what a commit did.
type ImportResult struct {
	// Written is how many rows were written.
	Written int `json:"written"`

	// Creates and Updates say which kind each was.
	Creates int `json:"creates"`
	Updates int `json:"updates"`

	// Rows is the outcome per row, in the sheet's order, carrying each row's
	// instance id.
	Rows []ImportRow `json:"rows"`
}

// PreviewImport validates a sheet and writes nothing.
func (u *Usecase) PreviewImport(ctx context.Context, req ImportRequest) (*ImportPreview, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	set, columns, err := u.prepareImport(ctx, req)
	if err != nil {
		return nil, err
	}
	rows, err := u.buildImportRows(ctx, set, columns, req)
	if err != nil {
		return nil, err
	}
	return summariseImport(columns, rows), nil
}

// CommitImport writes a sheet, in one transaction.
//
// EVERY ROW OR NONE. The alternative — skipping the bad ones and reporting the
// good — leaves an operator with a half-imported sheet to reconcile against the
// file, and no way to tell "the import finished" from "the import stopped". The
// preview is what tells them what to fix.
func (u *Usecase) CommitImport(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	set, columns, err := u.prepareImport(ctx, req)
	if err != nil {
		return nil, err
	}
	rows, err := u.buildImportRows(ctx, set, columns, req)
	if err != nil {
		return nil, err
	}
	preview := summariseImport(columns, rows)
	if preview.Invalid > 0 {
		return nil, fmt.Errorf("%w: %d of %d rows cannot be written (%s)",
			errs.ErrInvalid, preview.Invalid, preview.Total, firstIssues(rows, 3))
	}

	policy := importPolicy(req.ExistPolicy)
	specs := make([]WriteSpec, 0, len(rows))
	for _, row := range rows {
		values, err := NormalizeValues(set, row.Values, req.NoAttributePolicy)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", row.Number, err)
		}
		if err := u.applyDefaults(set, values); err != nil {
			return nil, fmt.Errorf("row %d: %w", row.Number, err)
		}
		unique, err := u.uniqueValue(set, values)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", row.Number, err)
		}
		specs = append(specs, WriteSpec{
			TypeRef: set.Type.Name, TypeID: set.Type.ID,
			UniqueAttrID: set.UniqueAttrID, UniqueValue: unique,
			Policy: policy,
			Specs:  u.plannerSpecs(set),
			Values: values, Audit: req.Audit,
		})
	}

	// THE WRITE IS ONE CALL, and therefore one transaction. A loop here would
	// put the transaction boundary back in the use case — which is exactly the
	// shape that made cantor's import non-atomic.
	written, err := u.repo.WriteCIBatch(ctx, specs)
	if err != nil {
		return nil, err
	}

	out := &ImportResult{Written: len(written), Rows: rows}
	for i := range out.Rows {
		out.Rows[i].InstanceID = written[i].CIID
		if written[i].Created {
			out.Rows[i].Action = importActionCreate
			out.Creates++
		} else {
			out.Rows[i].Action = importActionUpdate
			out.Updates++
		}
	}
	return out, nil
}

// prepareImport resolves the model and the column mapping, and checks the
// sheet's shape.
func (u *Usecase) prepareImport(ctx context.Context, req ImportRequest) (*AttributeSet, []ImportColumn, error) {
	if len(req.Headers) == 0 {
		return nil, nil, fmt.Errorf("%w: the sheet has no header row", errs.ErrInvalid)
	}
	if len(req.Headers) > MaxImportColumns {
		return nil, nil, fmt.Errorf("%w: the sheet has %d columns, more than the %d an import may carry",
			errs.ErrInvalid, len(req.Headers), MaxImportColumns)
	}
	if len(req.Rows) > MaxImportRows {
		return nil, nil, fmt.Errorf("%w: the sheet has %d rows, more than the %d an import may carry; split it",
			errs.ErrInvalid, len(req.Rows), MaxImportRows)
	}
	if !importPolicy(req.ExistPolicy).Valid() {
		return nil, nil, fmt.Errorf("%w: %q", ErrExistPolicyInvalid, req.ExistPolicy)
	}

	set, err := u.resolveSet(ctx, req.TypeRef)
	if err != nil {
		return nil, nil, err
	}
	columns := MatchColumns(set, req.Headers)

	matched, _ := ImportColumnSummary(columns)
	if matched == 0 {
		return nil, nil, fmt.Errorf("%w: none of the sheet's columns names an attribute of %s",
			errs.ErrInvalid, set.Type.Name)
	}
	// Nothing identifies the rows, so nothing says which instance each one is
	// about — and the write's own lookup would fail row by row.
	identified := false
	for _, col := range columns {
		if col.Matched && col.Identifier {
			identified = true
			break
		}
	}
	if !identified {
		return nil, nil, fmt.Errorf("%w: no column supplies %s, which is what identifies an instance of %s",
			errs.ErrInvalid, set.UniqueAttr.Name, set.Type.Name)
	}
	return set, columns, nil
}

// buildImportRows resolves every row, and then asks ONE query which rows already
// exist.
func (u *Usecase) buildImportRows(ctx context.Context, set *AttributeSet, columns []ImportColumn, req ImportRequest) ([]ImportRow, error) {
	policy := importPolicy(req.ExistPolicy)
	rows := make([]ImportRow, 0, len(req.Rows))
	for i, cells := range req.Rows {
		rows = append(rows, buildImportRow(set, columns, cells, i+1, 1, policy))
	}

	// Which rows would update rather than create. It is asked of the valid rows
	// only, in one query — a preview that issued one query per row would make
	// looking at a sheet as expensive as importing it.
	values := make([]any, 0, len(rows))
	positions := make([]int, 0, len(rows))
	for i, row := range rows {
		if !row.OK {
			continue
		}
		normalized, err := NormalizeValues(set, row.Values, req.NoAttributePolicy)
		if err != nil {
			continue
		}
		unique, ok := normalized[set.UniqueAttrID]
		if !ok {
			continue
		}
		values = append(values, unique)
		positions = append(positions, i)
	}
	if len(values) == 0 {
		return rows, nil
	}
	ids, err := u.repo.FindCIsByUniqueValues(ctx, set.Type.ID, set.UniqueAttrID, set.Specs, values)
	if err != nil {
		return nil, err
	}
	for i, pos := range positions {
		if ids[i] == 0 {
			continue
		}
		rows[pos].InstanceID = ids[i]
		// THE ACTION IS WHAT WOULD HAPPEN, not what the policy would allow: a
		// row whose identifier is taken is an update under `replace` and a
		// refusal under `reject`, and the console shows the two differently.
		if policy == ExistReplace {
			rows[pos].Action = importActionUpdate
			continue
		}
		rows[pos].OK = false
		rows[pos].Action = ""
		rows[pos].Issues = append(rows[pos].Issues, ImportIssue{
			Message: fmt.Sprintf("an instance with this %s already exists (id %d), and this import replaces nothing",
				set.UniqueAttr.Name, ids[i]),
		})
	}
	return rows, nil
}

// importPolicy defaults a sheet's policy to a create.
func importPolicy(p ExistPolicy) ExistPolicy {
	if p == "" {
		return ExistReject
	}
	return p
}

// summariseImport counts what the rows would do.
func summariseImport(columns []ImportColumn, rows []ImportRow) *ImportPreview {
	out := &ImportPreview{Total: len(rows), Columns: columns, Rows: rows}
	for _, row := range rows {
		if !row.OK {
			out.Invalid++
			continue
		}
		out.Valid++
		if row.InstanceID != 0 {
			out.Updates++
		} else {
			out.Creates++
		}
	}
	return out
}

// firstIssues renders the first few problems, so a refusal names something
// actionable rather than only counting.
func firstIssues(rows []ImportRow, limit int) string {
	parts := make([]string, 0, limit)
	for _, row := range rows {
		if row.OK {
			continue
		}
		msgs := make([]string, 0, len(row.Issues))
		for _, issue := range row.Issues {
			msgs = append(msgs, issue.Message)
		}
		parts = append(parts, fmt.Sprintf("row %d: %s", row.SheetRow, strings.Join(msgs, "; ")))
		if len(parts) == limit {
			break
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " | ")
}

// ImportTypeRefOf is a convenience for a caller that has a model id.
func ImportTypeRefOf(id model.ID) string { return fmt.Sprint(id) }
