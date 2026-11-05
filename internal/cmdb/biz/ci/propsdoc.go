package ci

import (
	"encoding/json"
	"fmt"
	"sort"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// Reserved keys in the denormalized CI document. They are namespaced with an
// underscore so they cannot collide with an attribute's name or alias.
const (
	PropsKeyID        = "_id"
	PropsKeyTypeID    = "_type_id"
	PropsKeyCreatedAt = "_created_at"
	PropsKeyUpdatedAt = "_updated_at"
)

// BuildPropsDocument renders the denormalized document stored on the CI row.
//
// This column exists because cantor served the same document from Redis or
// Elasticsearch and Ongrid ships neither (see internal/cmdb/model/ci.CI).
// Rendering it here, from the FINAL row set rather than from the incoming
// payload, is what keeps it consistent: it cannot drift from the value tables
// because both are produced by the same transaction from the same plan.
//
// Two rules are load-bearing:
//
//   - PASSWORD, LINK and FILE attributes are omitted entirely. cantor returns
//     those values only from dedicated endpoints and forces them into the
//     unindexed value table; copying them into a document that list endpoints
//     read back would leak them. This is the single most important reason this
//     function is not a generic "dump the map" loop.
//   - A multi-valued attribute renders as a JSON array, preserving row order.
//     The value TABLES keep the multi-row representation (that is what makes
//     the query DSL's IN/RANGE/LIKE operators uniform); the document is a read
//     convenience and may legitimately differ in shape.
//
// fields, when non-nil, restricts the document to those attribute names — the
// per-user column preference cantor applied when it cached a CI.
func BuildPropsDocument(specs map[model.ID]AttributeSpec, rows []ValueRow, fields map[string]bool) (string, error) {
	// Group rows per attribute, preserving the order the repository returned
	// them in so a multi-valued attribute keeps its id ordering.
	byAttr := make(map[model.ID][]ValueRow, len(rows))
	for _, row := range rows {
		byAttr[row.AttrID] = append(byAttr[row.AttrID], row)
	}

	doc := make(map[string]any, len(specs))

	attrIDs := make([]model.ID, 0, len(specs))
	for id := range specs {
		attrIDs = append(attrIDs, id)
	}
	sort.Slice(attrIDs, func(i, j int) bool { return attrIDs[i] < attrIDs[j] })

	for _, id := range attrIDs {
		spec := specs[id]

		// Secrets and opaque references never enter the document.
		if !spec.Modifiers().IsPasswordSafe() {
			continue
		}
		if fields != nil && !fields[spec.Name] && !fields[spec.Alias] {
			continue
		}

		attrRows := byAttr[id]
		if len(attrRows) == 0 {
			continue
		}

		if spec.Multivalued() {
			items := make([]any, 0, len(attrRows))
			for _, row := range attrRows {
				items = append(items, documentValue(row))
			}
			doc[spec.Name] = items
			continue
		}
		doc[spec.Name] = documentValue(attrRows[0])
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("cmdb: encode CI document: %w", err)
	}
	return string(encoded), nil
}

// documentValue converts a stored row into the JSON-friendly form the read
// endpoints expect. Dates and datetimes become cantor's own string forms so a
// document served from this column is byte-identical to one cantor served from
// Redis.
func documentValue(row ValueRow) any {
	switch row.Table {
	case value.TableIndexIntegers:
		return row.Int
	case value.TableIndexFloats:
		return row.Float
	case value.TableIndexDateTime:
		return row.Time.Format("2006-01-02 15:04:05")
	case value.TableJSON:
		if row.JSON == nil {
			return nil
		}
		return row.JSON
	default:
		return row.Text
	}
}

// ParsePropsDocument decodes a document produced by BuildPropsDocument.
//
// It exists so a reader can hydrate a page of CIs from the document column in
// one query, and it deliberately returns a plain map: the document is an
// untyped read convenience, while the typed values live in the value tables.
func ParsePropsDocument(raw string) (map[string]any, error) {
	if raw == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("cmdb: decode CI document: %w", err)
	}
	return out, nil
}

// SelectPropsFields restricts a decoded document to the requested fields,
// always keeping the reserved keys. It is the read-side counterpart to the
// fields argument of BuildPropsDocument, for callers that cache a document
// without the field filter applied.
func SelectPropsFields(doc map[string]any, fields map[string]bool) map[string]any {
	if fields == nil {
		return doc
	}
	out := make(map[string]any, len(fields))
	for k, v := range doc {
		if len(k) > 0 && k[0] == '_' {
			out[k] = v
			continue
		}
		if fields[k] {
			out[k] = v
		}
	}
	return out
}
