package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// This file answers the two dictionary questions a query asks: what does this
// attribute reference name, and what does this model reference name.
//
// IT READS THE MODEL TABLES DIRECTLY rather than going through the model
// module, and that is not a shortcut — the instance module cannot depend on the
// model module, because the model module already implements the instance
// module's TypeResolver port and the dependency would be a cycle. The precedent
// is already here: the write path locks the CI type row by reading c_ci_types,
// for the same reason.
//
// Both lookups are ONE query for the whole reference set. A query naming five
// attributes reads one row per attribute in a single statement, not five
// statements, which matters because the BATCH is what makes loading the
// dictionary per request affordable at all.

// QueryAttributes loads the dictionary entries a query mentions.
//
// A reference may be a NAME, an ALIAS, or a decimal ID, as cantor's
// AttributeCache accepted all three. References nothing matches are simply
// absent from the result — the caller reports them as unknown, because only the
// caller knows whether the reference came from a term, a sort field or a facet
// and can say which.
func (r *Repo) QueryAttributes(ctx context.Context, refs []string) ([]search.Attr, error) {
	names, ids := splitRefs(refs)
	if len(names) == 0 && len(ids) == 0 {
		return nil, nil
	}

	q := r.db.WithContext(ctx).Model(&citypemodel.Attribute{})
	switch {
	case len(names) > 0 && len(ids) > 0:
		q = q.Where("name IN ? OR alias IN ? OR id IN ?", names, names, ids)
	case len(names) > 0:
		q = q.Where("name IN ? OR alias IN ?", names, names)
	default:
		q = q.Where("id IN ?", ids)
	}

	var rows []citypemodel.Attribute
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: load attributes: %w", err)
	}

	out := make([]search.Attr, 0, len(rows))
	for _, row := range rows {
		out = append(out, search.Attr{
			ID:         row.ID,
			Name:       row.Name,
			Alias:      row.Alias,
			ValueType:  value.ValueType(row.ValueType),
			IsIndex:    row.IsIndex,
			IsList:     row.IsList,
			IsPassword: row.IsPassword,
			IsLink:     row.IsLink,
			IsFile:     row.IsFile,
		})
	}
	return out, nil
}

// QueryTypes loads the models a query mentions, by any of the three names.
func (r *Repo) QueryTypes(ctx context.Context, refs []string) ([]search.Type, error) {
	names, ids := splitRefs(refs)
	if len(names) == 0 && len(ids) == 0 {
		return nil, nil
	}

	q := r.db.WithContext(ctx).Model(&citypemodel.CIType{})
	switch {
	case len(names) > 0 && len(ids) > 0:
		q = q.Where("name IN ? OR alias IN ? OR id IN ?", names, names, ids)
	case len(names) > 0:
		q = q.Where("name IN ? OR alias IN ?", names, names)
	default:
		q = q.Where("id IN ?", ids)
	}

	var rows []citypemodel.CIType
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: load types: %w", err)
	}

	out := make([]search.Type, 0, len(rows))
	for _, row := range rows {
		out = append(out, search.Type{ID: row.ID, Name: row.Name, Alias: row.Alias})
	}
	return out, nil
}

// splitRefs separates decimal references from textual ones.
//
// The separation exists so the id predicate only ever compares integers with
// integers. Handing a name to `id IN (...)` relies on the server coercing the
// string to a number, which MySQL does with a warning and SQLite does by never
// matching — the same answer for a good reason in one dialect and a bad one in
// the other.
func splitRefs(refs []string) (names []string, ids []model.ID) {
	for _, ref := range refs {
		if n, err := strconv.ParseInt(ref, 10, 32); err == nil && n > 0 {
			ids = append(ids, model.ID(n))
			continue
		}
		names = append(names, ref)
	}
	return names, ids
}
