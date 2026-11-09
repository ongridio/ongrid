package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM implementation of the cmdb/biz/ci persistence port.
//
// It is the only place a *gorm.DB is visible in the CMDB write path, and the
// only place that knows the six value tables by name. The write SEMANTICS
// (which rows change) live in cmdb/biz/ci.PlanValueUpsert; this package
// executes the resulting plan inside one transaction. data may import biz for
// exactly this reason — see .go-arch-lint.yml (cmdb_data mayDependOn cmdb_biz).
type Repo struct {
	db *gorm.DB
}

var _ cibiz.Repo = (*Repo)(nil)

// NewRepo builds the repo around an open *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// BuildPropsDocument/ParsePropsDocument live in biz; these aliases keep call
// sites in this package readable.
var (
	buildProps = cibiz.BuildPropsDocument
	planUpsert = cibiz.PlanValueUpsert
)

// CreateCIWithValues inserts the CI, its value rows and its audit rows in one
// transaction.
func (r *Repo) CreateCIWithValues(ctx context.Context, in cibiz.CreateInput) (*cimodel.CI, error) {
	if in.CI == nil {
		return nil, fmt.Errorf("cmdb ci store: create: nil CI")
	}
	if err := cibiz.ValidateRequired(in.Specs, in.Values); err != nil {
		return nil, err
	}

	ci := *in.CI
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return insertCIWithValues(tx, &ci, in.Specs, in.Values, in.Audit)
	})
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// insertCIWithValues is the create body, taking the caller's transaction.
//
// It is split out so the transactional write path can run it inside ITS
// transaction, together with the unique-value lookup and the per-model lock.
// Two transactions would leave the gap that the lock exists to close.
func insertCIWithValues(tx *gorm.DB, ci *cimodel.CI, specs map[cmdbmodel.ID]cibiz.AttributeSpec, values map[cmdbmodel.ID]any, audit cibiz.AuditInfo) error {
	// The CI must exist before values can reference it, so the plan is built
	// with the assigned id rather than a placeholder.
	if err := tx.Create(ci).Error; err != nil {
		return fmt.Errorf("insert CI: %w", err)
	}

	plan, changes, err := planUpsert(ci.ID, ci.TypeID, specs, nil, values)
	if err != nil {
		return err
	}
	if err := applyPlan(tx, ci.ID, plan); err != nil {
		return err
	}

	// The document is rendered from the FINAL rows, so it cannot disagree with
	// the value tables.
	final := applyPlanToRows(nil, plan)
	doc, err := buildProps(specs, final, nil)
	if err != nil {
		return err
	}
	if err := tx.Model(&cimodel.CI{}).Where("id = ?", ci.ID).
		Update("props_json", doc).Error; err != nil {
		return fmt.Errorf("write CI document: %w", err)
	}
	ci.PropsJSON = doc

	return writeAudit(tx, ci.TypeID, changes, audit)
}

// UpsertCIValues applies value changes to an existing CI in one transaction.
func (r *Repo) UpsertCIValues(ctx context.Context, in cibiz.UpdateInput) ([]cibiz.Change, error) {
	if in.CIID == 0 {
		return nil, fmt.Errorf("cmdb ci store: update: zero ci id")
	}
	// An update checks only that supplied required values are non-empty; it
	// deliberately does NOT require every required attribute to be resent, or
	// patching one attribute would mean resending the whole CI. See
	// cibiz.ValidateRequiredPresence.
	if err := cibiz.ValidateSuppliedValues(in.Specs, in.Values); err != nil {
		return nil, err
	}

	var changes []cibiz.Change
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ci cimodel.CI
		if err := tx.First(&ci, in.CIID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.ErrNotFound
			}
			return fmt.Errorf("load CI: %w", err)
		}

		var err error
		changes, err = applyValues(tx, in.CIID, in.TypeID, in.Specs, in.Values, in.Audit)
		return err
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}

// applyValues is the update body, taking the caller's transaction.
func applyValues(tx *gorm.DB, ciID, typeID cmdbmodel.ID, specs map[cmdbmodel.ID]cibiz.AttributeSpec, values map[cmdbmodel.ID]any, audit cibiz.AuditInfo) ([]cibiz.Change, error) {
	var changes []cibiz.Change
	{
		existing, err := loadValueRows(tx, ciID)
		if err != nil {
			return nil, err
		}

		plan, chg, err := planUpsert(ciID, typeID, specs, existing, values)
		if err != nil {
			return nil, err
		}
		if plan.Empty() {
			// Nothing to write and, deliberately, no audit row: a no-op edit
			// must not appear in the change history.
			return nil, nil
		}
		if err := applyPlan(tx, ciID, plan); err != nil {
			return nil, err
		}

		final := applyPlanToRows(existing, plan)
		doc, err := buildProps(specs, final, nil)
		if err != nil {
			return nil, err
		}
		if err := tx.Model(&cimodel.CI{}).Where("id = ?", ciID).
			Update("props_json", doc).Error; err != nil {
			return nil, fmt.Errorf("write CI document: %w", err)
		}

		if err := writeAudit(tx, typeID, chg, audit); err != nil {
			return nil, err
		}
		changes = chg
	}
	return changes, nil
}

// LoadValueRows returns every value row for a CI across all six value tables.
func (r *Repo) LoadValueRows(ctx context.Context, ciID cmdbmodel.ID) ([]cibiz.ValueRow, error) {
	return loadValueRows(r.db.WithContext(ctx), ciID)
}

// DeleteCI soft-deletes a CI and removes the rows that have no identity of
// their own.
//
// Value rows are deleted outright — they are the CI's attributes, not separate
// entities, and leaving them would make a re-created CI inherit stale values.
// Relations are soft-deleted because they do have identity and appear in
// history. Audit rows are never touched: an audit trail that disappears with
// its subject is not an audit trail.
func (r *Repo) DeleteCI(ctx context.Context, ciID cmdbmodel.ID) error {
	if ciID == 0 {
		return fmt.Errorf("cmdb ci store: delete: zero ci id")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return deleteCITx(tx, ciID)
	})
}

// deleteCITx is the delete body, taking the caller's transaction — a rack's
// deletion clears its devices' positions in the same one.
func deleteCITx(tx *gorm.DB, ciID cmdbmodel.ID) error {
	var ci cimodel.CI
	if err := tx.First(&ci, ciID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.ErrNotFound
		}
		return fmt.Errorf("load CI: %w", err)
	}

	for _, model := range valueModels {
		if err := tx.Where("ci_id = ?", ciID).Delete(model).Error; err != nil {
			return fmt.Errorf("delete value rows from %T: %w", model, err)
		}
	}

	if err := tx.Where("first_ci_id = ? OR second_ci_id = ?", ciID, ciID).
		Delete(&cimodel.Relation{}).Error; err != nil {
		return fmt.Errorf("delete relations: %w", err)
	}

	if err := tx.Delete(&ci).Error; err != nil {
		return fmt.Errorf("delete CI: %w", err)
	}
	return nil
}

// loadValueRows reads all six value tables for one CI.
//
// The rows are returned grouped by table in a fixed order, and within a table
// by ascending id, so a multi-valued attribute comes back in the order it was
// written and the rendered document is stable across reads.
func loadValueRows(db *gorm.DB, ciID cmdbmodel.ID) ([]cibiz.ValueRow, error) {
	var out []cibiz.ValueRow

	var ints []value.ValueIndexInt
	if err := db.Where("ci_id = ?", ciID).Order("id").Find(&ints).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexIntegers, err)
	}
	for _, row := range ints {
		out = append(out, cibiz.ValueRow{RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexIntegers, Int: row.Value})
	}

	var floats []value.ValueIndexFloat
	if err := db.Where("ci_id = ?", ciID).Order("id").Find(&floats).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexFloats, err)
	}
	for _, row := range floats {
		out = append(out, cibiz.ValueRow{RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexFloats, Float: row.Value})
	}

	var idxTexts []value.ValueIndexText
	if err := db.Where("ci_id = ?", ciID).Order("id").Find(&idxTexts).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexTexts, err)
	}
	for _, row := range idxTexts {
		out = append(out, cibiz.ValueRow{RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexTexts, Text: row.Value})
	}

	var times []value.ValueIndexDateTime
	if err := db.Where("ci_id = ?", ciID).Order("id").Find(&times).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableIndexDateTime, err)
	}
	for _, row := range times {
		out = append(out, cibiz.ValueRow{RowID: row.ID, AttrID: row.AttrID, Table: value.TableIndexDateTime, Time: row.Value})
	}

	var texts []value.ValueText
	if err := db.Where("ci_id = ?", ciID).Order("id").Find(&texts).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableTexts, err)
	}
	for _, row := range texts {
		out = append(out, cibiz.ValueRow{RowID: row.ID, AttrID: row.AttrID, Table: value.TableTexts, Text: row.Value})
	}

	var jsons []value.ValueJSON
	if err := db.Where("ci_id = ?", ciID).Order("id").Find(&jsons).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", value.TableJSON, err)
	}
	for _, row := range jsons {
		out = append(out, cibiz.ValueRow{RowID: row.ID, AttrID: row.AttrID, Table: value.TableJSON, JSON: row.Value})
	}

	return out, nil
}

// valueModels lists the six live value tables for bulk operations. The three
// deprecated cantor tables are excluded because nothing is ever written to
// them; see internal/cmdb/model/value/valuetables.go.
var valueModels = []any{
	&value.ValueIndexInt{},
	&value.ValueIndexFloat{},
	&value.ValueIndexText{},
	&value.ValueIndexDateTime{},
	&value.ValueText{},
	&value.ValueJSON{},
}

// applyPlan executes the plan. Deletes run before inserts, which is what keeps
// a rewritten multi-valued attribute's row ids ascending.
func applyPlan(tx *gorm.DB, ciID cmdbmodel.ID, plan cibiz.ValuePlan) error {
	for _, op := range plan.Deletes {
		model, err := modelForTable(op.Table)
		if err != nil {
			return err
		}
		if err := tx.Where("id = ? AND ci_id = ?", op.RowID, ciID).Delete(model).Error; err != nil {
			return fmt.Errorf("delete %s row %d: %w", op.Table, op.RowID, err)
		}
	}
	for _, op := range plan.Inserts {
		if err := insertOp(tx, ciID, op); err != nil {
			return err
		}
	}
	return nil
}

func insertOp(tx *gorm.DB, ciID cmdbmodel.ID, op cibiz.ValueOp) error {
	switch op.Table {
	case value.TableIndexIntegers:
		if err := tx.Create(&value.ValueIndexInt{CIID: ciID, AttrID: op.AttrID, Value: op.Int}).Error; err != nil {
			return fmt.Errorf("insert %s: %w", op.Table, err)
		}
	case value.TableIndexFloats:
		if err := tx.Create(&value.ValueIndexFloat{CIID: ciID, AttrID: op.AttrID, Value: op.Float}).Error; err != nil {
			return fmt.Errorf("insert %s: %w", op.Table, err)
		}
	case value.TableIndexTexts:
		if err := tx.Create(&value.ValueIndexText{CIID: ciID, AttrID: op.AttrID, Value: op.Text}).Error; err != nil {
			return fmt.Errorf("insert %s: %w", op.Table, err)
		}
	case value.TableIndexDateTime:
		if err := tx.Create(&value.ValueIndexDateTime{CIID: ciID, AttrID: op.AttrID, Value: op.Time}).Error; err != nil {
			return fmt.Errorf("insert %s: %w", op.Table, err)
		}
	case value.TableTexts:
		if err := tx.Create(&value.ValueText{CIID: ciID, AttrID: op.AttrID, Value: op.Text}).Error; err != nil {
			return fmt.Errorf("insert %s: %w", op.Table, err)
		}
	case value.TableJSON:
		if err := tx.Create(&value.ValueJSON{CIID: ciID, AttrID: op.AttrID, Value: op.JSON}).Error; err != nil {
			return fmt.Errorf("insert %s: %w", op.Table, err)
		}
	default:
		return fmt.Errorf("cmdb ci store: unknown value table %q", op.Table)
	}
	return nil
}

func modelForTable(table string) (any, error) {
	switch table {
	case value.TableIndexIntegers:
		return &value.ValueIndexInt{}, nil
	case value.TableIndexFloats:
		return &value.ValueIndexFloat{}, nil
	case value.TableIndexTexts:
		return &value.ValueIndexText{}, nil
	case value.TableIndexDateTime:
		return &value.ValueIndexDateTime{}, nil
	case value.TableTexts:
		return &value.ValueText{}, nil
	case value.TableJSON:
		return &value.ValueJSON{}, nil
	default:
		return nil, fmt.Errorf("cmdb ci store: unknown value table %q", table)
	}
}

// applyPlanToRows computes the final row set in memory, so the document can be
// rendered without re-reading the tables inside the transaction.
func applyPlanToRows(existing []cibiz.ValueRow, plan cibiz.ValuePlan) []cibiz.ValueRow {
	if plan.Empty() {
		return existing
	}
	deleted := make(map[cmdbmodel.ID]bool, len(plan.Deletes))
	for _, op := range plan.Deletes {
		deleted[op.RowID] = true
	}
	out := make([]cibiz.ValueRow, 0, len(existing)+len(plan.Inserts))
	for _, row := range existing {
		if !deleted[row.RowID] {
			out = append(out, row)
		}
	}
	for _, op := range plan.Inserts {
		out = append(out, cibiz.ValueRow{
			AttrID: op.AttrID,
			Table:  op.Table,
			Int:    op.Int,
			Float:  op.Float,
			Text:   op.Text,
			Time:   op.Time,
			JSON:   op.JSON,
		})
	}
	return out
}

// writeAudit persists the operation record and the attribute-level history for
// a write. It is a no-op when there were no changes, so an empty edit leaves
// no audit noise.
func writeAudit(tx *gorm.DB, typeID cmdbmodel.ID, changes []cibiz.Change, in cibiz.AuditInfo) error {
	if len(changes) == 0 {
		return nil
	}
	rec := cibiz.BuildOperationRecord(typeID, in)
	if err := tx.Create(rec).Error; err != nil {
		return fmt.Errorf("insert operation record: %w", err)
	}
	operate := string(cmdbmodel.OperateAdd)
	for _, chg := range changes {
		history := &cimodel.AttributeHistory{
			RecordID:    rec.ID,
			CIID:        chg.CIID,
			AttrID:      &chg.AttrID,
			OperateType: operateFor(chg, &operate),
		}
		if chg.Old != nil {
			old := fmt.Sprint(chg.Old)
			history.Old = &old
		}
		if chg.New != nil {
			now := fmt.Sprint(chg.New)
			history.New = &now
		}
		if err := tx.Create(history).Error; err != nil {
			return fmt.Errorf("insert attribute history: %w", err)
		}
	}
	return nil
}

func operateFor(chg cibiz.Change, fallback *string) *string {
	op := string(chg.Operate)
	if op == "" {
		return fallback
	}
	return &op
}
