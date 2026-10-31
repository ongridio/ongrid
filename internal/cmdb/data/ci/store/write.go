package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// WriteCI resolves an instance by its unique value and creates or updates it,
// in ONE transaction that holds a per-model lock.
//
// WHY THE LOCK IS NOT OPTIONAL. The value that identifies an instance lives in
// a typed EAV table chosen by the attribute's own definition, so no index over
// c_cis can express "unique". Nothing in the schema can therefore refuse a
// duplicate; the only thing standing between two simultaneous creates of the
// same unique value is the window between the lookup and the insert. cantor
// closed that window with a Redis lock keyed on the model's name. With no
// Redis, the port takes the equivalent database lock:
//
//   - MySQL: `SELECT ... FOR UPDATE` on the model row, which serialises writers
//     for that model and nothing else.
//   - SQLite: a write transaction already excludes other writers, so taking the
//     row is a no-op that costs one query.
//
// The lock is per MODEL rather than global, which is the granularity cantor
// chose: two models can be written concurrently, and the same model cannot.
func (r *Repo) WriteCI(ctx context.Context, in cibiz.WriteSpec) (*cibiz.WriteResult, error) {
	var result cibiz.WriteResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = writeCITx(tx, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// writeCITx is the write, taking the caller's transaction — a subnet's own
// write validates its range in the same one. See ApplySubnetWrite.
func writeCITx(tx *gorm.DB, in cibiz.WriteSpec) (cibiz.WriteResult, error) {
	var result cibiz.WriteResult

	if err := lockTypeRow(tx, in.TypeID); err != nil {
		return result, err
	}

	existing, err := findCIByUnique(tx, in.TypeID, in.UniqueAttrID, in.Specs, in.UniqueValue)
	if err != nil {
		return result, err
	}

	switch {
	case existing != nil && in.Policy == cibiz.ExistReject:
		return result, fmt.Errorf("%w: %v", cibiz.ErrAlreadyExists, in.UniqueValue)
	case existing == nil && in.Policy == cibiz.ExistNeed:
		return result, fmt.Errorf("%w: %v", cibiz.ErrNotFound, in.UniqueValue)
	case existing != nil:
		if existing.TypeID != in.TypeID {
			// cantor silently re-homed the instance. Moving an instance
			// between models reinterprets every one of its values against
			// different definitions, which is a repair an operator
			// performs on purpose and not a side effect of a create.
			return result, fmt.Errorf("%w: instance %d belongs to model %d",
				cibiz.ErrAlreadyExists, existing.ID, existing.TypeID)
		}
		changes, err := applyValues(tx, existing.ID, in.TypeID, in.Specs, in.Values, in.Audit)
		if err != nil {
			return result, err
		}
		if in.MarkAutoDiscovery {
			if err := tx.Model(&cimodel.CI{}).Where("id = ?", existing.ID).Update("a", true).Error; err != nil {
				return result, fmt.Errorf("mark discovered instance: %w", err)
			}
		}
		if err := writeRelationSpecsTx(tx, existing.ID, in); err != nil {
			return result, err
		}
		return cibiz.WriteResult{CIID: existing.ID, Created: false, Changes: changes}, nil
	}

	ci := &cimodel.CI{TypeID: in.TypeID, IsAutoDiscovery: in.MarkAutoDiscovery}
	if err := insertCIWithValues(tx, ci, in.Specs, in.Values, in.Audit); err != nil {
		return result, err
	}
	// The containment edge is written HERE rather than by the caller
	// afterwards, so a container that refuses the child takes the child
	// with it. See WriteSpec.Parent.
	if in.Parent != nil {
		if _, err := addRelationTx(tx, cibiz.RelationAddSpec{
			FirstCIID:         in.Parent.ParentID,
			SecondCIID:        ci.ID,
			RelationTypeID:    in.Parent.RelationTypeID,
			FirstTypeID:       in.Parent.ParentTypeID,
			SecondTypeID:      in.Parent.ChildTypeID,
			Constraint:        in.Parent.Constraint,
			EnforceConstraint: true,
			Actor:             in.Parent.Actor,
		}); err != nil {
			return result, err
		}
	}
	if err := writeRelationSpecsTx(tx, ci.ID, in); err != nil {
		return result, err
	}
	return cibiz.WriteResult{CIID: ci.ID, Created: true}, nil
}

// writeRelationSpecsTx lays down the edges a discovered instance established.
//
// IT RUNS INSIDE THE INSTANCE'S TRANSACTION, so an edge that the model graph
// refuses rolls the instance back with it. cantor wrote the instance and then
// dispatched a Celery task for the edges, which is how an accepted machine
// could end up with no place in the graph and no record that anything failed.
//
// A SELF-LOOP IS SKIPPED. A discovery rule that maps a field onto the same
// model can match the instance itself when the provider reports its own
// identifier; an instance related to itself is always wrong, and refusing the
// whole accept would throw away a good instance over one bad edge.
func writeRelationSpecsTx(tx *gorm.DB, ciID model.ID, in cibiz.WriteSpec) error {
	for _, rel := range in.Relations {
		if rel.PeerCIID == 0 || rel.PeerCIID == ciID || rel.RelationTypeID == 0 {
			continue
		}
		firstID, secondID := ciID, rel.PeerCIID
		firstType, secondType := rel.DiscoveredTypeID, rel.PeerTypeID
		if !rel.DiscoveredIsFirst {
			firstID, secondID = rel.PeerCIID, ciID
			firstType, secondType = rel.PeerTypeID, rel.DiscoveredTypeID
		}
		source := string(model.RelationFromAutoDiscovery)
		if _, err := addRelationTx(tx, cibiz.RelationAddSpec{
			FirstCIID:         firstID,
			SecondCIID:        secondID,
			RelationTypeID:    rel.RelationTypeID,
			FirstTypeID:       firstType,
			SecondTypeID:      secondType,
			Constraint:        rel.Constraint,
			EnforceConstraint: true,
			Source:            &source,
			Actor:             in.Audit.Actor,
		}); err != nil {
			return fmt.Errorf("cmdb ci: write discovered relation: %w", err)
		}
	}
	return nil
}

// lockTypeRow takes the per-model write lock.
//
// The row is read and discarded; only the lock it takes matters. On MySQL
// `FOR UPDATE` blocks a second writer until the first commits, which is what
// makes the lookup above meaningful. On SQLite the clause is meaningless
// because the write transaction already holds the database, and GORM's SQLite
// driver would reject the syntax — so it is emitted only where it works.
func lockTypeRow(tx *gorm.DB, typeID model.ID) error {
	var ct citypemodel.CIType
	q := tx.Model(&citypemodel.CIType{}).Where("id = ?", typeID)
	if tx.Dialector.Name() != "sqlite" {
		// GORM's own locking clause, so the qualifier is written the way the
		// active dialect expects rather than as a hand-built string.
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&ct).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: model %d", cibiz.ErrTypeNotFound, typeID)
		}
		return fmt.Errorf("cmdb ci: lock model %d: %w", typeID, err)
	}
	return nil
}

// findCIByUnique locates the instance a model identifies by one attribute's
// value, or returns nil.
//
// The comparison is DIALECT-AWARE, and this is the first place the rule from
// the query compiler applies: on SQLite a bare `value = ?` neither uses the
// NOCASE index nor matches case-insensitively, so the comparison carries
// `COLLATE NOCASE` on the COLUMN side. MySQL needs nothing — its collation is
// already case-insensitive — and would reject the clause in that position on
// some versions, so it is emitted only for SQLite.
//
// The consequence for behaviour is the one that matters: an instance's unique
// value is matched case-insensitively on BOTH dialects, which is what cantor
// did by running only on MySQL.
func findCIByUnique(tx *gorm.DB, typeID, attrID model.ID, specs map[model.ID]cibiz.AttributeSpec, raw any) (*cimodel.CI, error) {
	spec, ok := specs[attrID]
	if !ok {
		return nil, fmt.Errorf("%w: unique attribute %d is not bound to the model",
			cibiz.ErrUniqueValueRequired, attrID)
	}
	table, err := spec.StorageTable()
	if err != nil {
		return nil, err
	}
	stored, err := spec.ValueType.Validate(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", cibiz.ErrUniqueValueRequired, err)
	}

	// The value table first, then the CI row: the other order would fetch every
	// instance of the model to compare one attribute.
	sub := tx.Table(table).Select("ci_id").Where("attr_id = ?", attrID)
	sub = compareValue(tx, sub, table, stored)

	var ci cimodel.CI
	err = tx.Where("type_id = ?", typeID).Where("id IN (?)", sub).First(&ci).Error
	switch {
	case err == nil:
		return &ci, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb ci: look up unique value: %w", err)
	}
}

// compareValue narrows a value-table subquery to rows matching one stored
// value, using the comparison form the dialect can serve.
func compareValue(tx *gorm.DB, q *gorm.DB, table string, stored value.StoredValue) *gorm.DB {
	isText := table == value.TableTexts || table == value.TableIndexTexts || table == value.TableJSON
	if isText {
		if tx.Dialector.Name() == "sqlite" {
			return q.Where("value COLLATE NOCASE = ?", stored)
		}
		return q.Where("value = ?", stored)
	}
	return q.Where("value = ?", stored)
}

// --- reads ---

// LoadCI returns a CI by id, or nil.
func (r *Repo) LoadCI(ctx context.Context, ciID model.ID) (*cimodel.CI, error) {
	var ci cimodel.CI
	err := r.db.WithContext(ctx).First(&ci, ciID).Error
	switch {
	case err == nil:
		return &ci, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb ci: load %d: %w", ciID, err)
	}
}

// LoadCIs returns the CIs with the given ids, keyed by id.
func (r *Repo) LoadCIs(ctx context.Context, ciIDs []model.ID) (map[model.ID]*cimodel.CI, error) {
	out := make(map[model.ID]*cimodel.CI, len(ciIDs))
	if len(ciIDs) == 0 {
		return out, nil
	}
	var rows []cimodel.CI
	if err := r.db.WithContext(ctx).Where("id IN ?", ciIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: load instances: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// ListCIsByType returns a page of the model's instances plus the total.
//
// Ordered by id DESCENDING, which is cantor's order and the one a console
// wants: the instance someone just created is the one they are looking for.
func (r *Repo) ListCIsByType(ctx context.Context, typeID model.ID, limit, offset int) ([]cimodel.CI, int64, error) {
	q := r.db.WithContext(ctx).Model(&cimodel.CI{}).Where("type_id = ?", typeID)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: count: %w", err)
	}

	var out []cimodel.CI
	if err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: list: %w", err)
	}
	return out, total, nil
}

// CountCIsByType returns how many live instances a model has.
func (r *Repo) CountCIsByType(ctx context.Context, typeID model.ID) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&cimodel.CI{}).Where("type_id = ?", typeID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("cmdb ci: count by type: %w", err)
	}
	return n, nil
}

// ValueRowsForCIs returns every value row for the given instances, keyed by
// instance id, in ONE query per value table.
//
// Six queries for a page of any size, rather than one per instance: a page of a
// hundred instances over two hundred attributes is the difference between six
// queries and six hundred.
func (r *Repo) ValueRowsForCIs(ctx context.Context, ciIDs []model.ID) (map[model.ID][]cibiz.ValueRow, error) {
	out := make(map[model.ID][]cibiz.ValueRow, len(ciIDs))
	if len(ciIDs) == 0 {
		return out, nil
	}

	db := r.db.WithContext(ctx)

	var ints []value.ValueIndexInt
	if err := db.Where("ci_id IN ?", ciIDs).Order("id").Find(&ints).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexIntegers, err)
	}
	for _, row := range ints {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexIntegers, Int: row.Value,
		})
	}

	var floats []value.ValueIndexFloat
	if err := db.Where("ci_id IN ?", ciIDs).Order("id").Find(&floats).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexFloats, err)
	}
	for _, row := range floats {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexFloats, Float: row.Value,
		})
	}

	var idxTexts []value.ValueIndexText
	if err := db.Where("ci_id IN ?", ciIDs).Order("id").Find(&idxTexts).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexTexts, err)
	}
	for _, row := range idxTexts {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexTexts, Text: row.Value,
		})
	}

	var times []value.ValueIndexDateTime
	if err := db.Where("ci_id IN ?", ciIDs).Order("id").Find(&times).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexDateTime, err)
	}
	for _, row := range times {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexDateTime, Time: row.Value,
		})
	}

	var texts []value.ValueText
	if err := db.Where("ci_id IN ?", ciIDs).Order("id").Find(&texts).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableTexts, err)
	}
	for _, row := range texts {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableTexts, Text: row.Value,
		})
	}

	var jsons []value.ValueJSON
	if err := db.Where("ci_id IN ?", ciIDs).Order("id").Find(&jsons).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableJSON, err)
	}
	for _, row := range jsons {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableJSON, JSON: row.Value,
		})
	}
	return out, nil
}

// ValuesForAttribute returns one attribute's stored values for a set of
// instances.
//
// SIX STATEMENTS, NONE OF THEM A SCAN. Every value table indexes `ci_id`, so
// each is an index probe over the requested instances rather than a read of the
// table. The attribute lives in exactly ONE of the six — which one is a
// function of its value type — but the repository does not know the value type
// and must not be told a table NAME: a SQL identifier chosen by the caller is
// the one thing AGENTS.md forbids outright, and getting it from an enum here
// would still mean the port's contract accepted a string.
//
// Rows are ordered by id, which is the order the value tables were written in.
// For a multi-valued attribute that is the order a reader sees the values in,
// and it is stable — cantor's dict comprehension over an unordered SELECT was
// not.
func (r *Repo) ValuesForAttribute(ctx context.Context, attrID model.ID, ciIDs []model.ID) (map[model.ID][]cibiz.ValueRow, error) {
	return valuesForAttribute(ctx, r.db, attrID, ciIDs)
}

// valuesForAttribute is the read itself, taking the caller's handle so a
// transaction can use it — the rack occupancy read is one.
func valuesForAttribute(ctx context.Context, handle *gorm.DB, attrID model.ID, ciIDs []model.ID) (map[model.ID][]cibiz.ValueRow, error) {
	out := make(map[model.ID][]cibiz.ValueRow, len(ciIDs))
	if attrID <= 0 || len(ciIDs) == 0 {
		return out, nil
	}
	db := handle.WithContext(ctx)
	forAttr := func() *gorm.DB {
		return db.Where("attr_id = ? AND ci_id IN ?", attrID, ciIDs).Order("id")
	}

	var ints []value.ValueIndexInt
	if err := forAttr().Find(&ints).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexIntegers, err)
	}
	for _, row := range ints {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexIntegers, Int: row.Value,
		})
	}

	var floats []value.ValueIndexFloat
	if err := forAttr().Find(&floats).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexFloats, err)
	}
	for _, row := range floats {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexFloats, Float: row.Value,
		})
	}

	var idxTexts []value.ValueIndexText
	if err := forAttr().Find(&idxTexts).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexTexts, err)
	}
	for _, row := range idxTexts {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexTexts, Text: row.Value,
		})
	}

	var times []value.ValueIndexDateTime
	if err := forAttr().Find(&times).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexDateTime, err)
	}
	for _, row := range times {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexDateTime, Time: row.Value,
		})
	}

	var texts []value.ValueText
	if err := forAttr().Find(&texts).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableTexts, err)
	}
	for _, row := range texts {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableTexts, Text: row.Value,
		})
	}

	var jsons []value.ValueJSON
	if err := forAttr().Find(&jsons).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableJSON, err)
	}
	for _, row := range jsons {
		out[row.CIID] = append(out[row.CIID], cibiz.ValueRow{
			RowID: row.ID, AttrID: row.AttrID, Table: value.TableJSON, JSON: row.Value,
		})
	}
	return out, nil
}
