package store

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// The batch write and the batched identity lookup a sheet needs.

// WriteCIBatch applies many instance writes in one transaction.
//
// THE ROWS SEE EACH OTHER. Every write runs through writeCITx in the SAME
// transaction, so a sheet holding two rows with one identifying value resolves
// the second against the row the first just inserted — which is what makes
// "add a column to every row" behave the way a person reading the sheet expects.
// cantor's per-row requests could not: each was its own transaction, so the
// second row's lookup did not see the first's insert.
func (r *Repo) WriteCIBatch(ctx context.Context, specs []cibiz.WriteSpec) ([]cibiz.WriteResult, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]cibiz.WriteResult, len(specs))
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i, spec := range specs {
			got, err := writeCITx(tx, spec)
			if err != nil {
				// The row's position is added by the caller, which knows the
				// sheet's numbering; the store reports which write failed.
				return fmt.Errorf("row %d: %w", i+1, err)
			}
			out[i] = got
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: batch write: %w", err)
	}
	return out, nil
}

// FindCIsByUniqueValues returns the instance each value identifies.
//
// ONE QUERY FOR EVERY ROW, keyed by the coerced value's rendered form. The
// coercion is the value layer's, so a sheet's ` 10 ` and its `10` are the same
// value here — which they are to the writer as well.
func (r *Repo) FindCIsByUniqueValues(ctx context.Context, typeID, attrID model.ID, specs map[model.ID]cibiz.AttributeSpec, values []any) ([]model.ID, error) {
	out := make([]model.ID, len(values))
	spec, ok := specs[attrID]
	if !ok || len(values) == 0 {
		return out, nil
	}
	table, err := spec.StorageTable()
	if err != nil {
		return nil, err
	}

	// The stored forms, deduplicated, with the positions each one stands for.
	key := func(stored value.StoredValue) string { return storedKey(table, stored) }
	want := map[string][]int{}
	storedList := make([]any, 0, len(values))
	seen := map[string]bool{}
	for i, raw := range values {
		if raw == nil {
			continue
		}
		stored, err := spec.ValueType.Validate(raw)
		if err != nil {
			// A value the model cannot hold identifies nothing. The preview
			// reports the cell; here it simply has no instance behind it.
			continue
		}
		k := key(stored)
		want[k] = append(want[k], i)
		if !seen[k] {
			seen[k] = true
			storedList = append(storedList, stored)
		}
	}
	if len(storedList) == 0 {
		return out, nil
	}

	var rows []struct {
		CIID  model.ID `gorm:"column:ci_id"`
		Value string   `gorm:"column:value"`
	}
	q := r.db.WithContext(ctx).Table(table).
		Select("ci_id, value").
		Where("attr_id = ?", attrID)
	if isTextTable(table) && r.db.Dialector.Name() == "sqlite" {
		// The comparison form the dialect can serve — see compareValue.
		q = q.Where("value COLLATE NOCASE IN ?", storedList)
	} else {
		q = q.Where("value IN ?", storedList)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: look up unique values: %w", err)
	}

	// The row's model, so a value that belongs to another model's instance is
	// not mistaken for this one's.
	ids := make([]model.ID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.CIID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	var live []cimodel.CI
	if err := r.db.WithContext(ctx).Model(&cimodel.CI{}).
		Where("type_id = ? AND delete_marker = 0 AND id IN ?", typeID, ids).
		Find(&live).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: filter unique values by model: %w", err)
	}
	allowed := make(map[model.ID]bool, len(live))
	for _, ci := range live {
		allowed[ci.ID] = true
	}

	for _, row := range rows {
		if !allowed[row.CIID] {
			continue
		}
		for _, idx := range want[foldKey(table, row.Value)] {
			out[idx] = row.CIID
		}
	}
	return out, nil
}

// storedKey renders a coerced value the way the lookup keys it.
func storedKey(table string, stored value.StoredValue) string {
	return foldKey(table, fmt.Sprint(stored))
}

// foldKey folds a TEXT value's case, because the database's own comparison for
// those columns is case-insensitive — SQLite through the index's NOCASE
// collation and MySQL through the table's collation. A Go-side key that did not
// fold would look up an instance the query had just found.
func foldKey(table, s string) string {
	if isTextTable(table) {
		return strings.ToLower(s)
	}
	return s
}
