package ci

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// ValueRow is one existing row already stored in a value table. RowID is the
// value table's own primary key, needed to address a specific row when a
// multi-valued attribute is re-ordered.
type ValueRow struct {
	RowID  model.ID
	AttrID model.ID
	Table  string

	Int   model.Int32
	Float float64
	Text  string
	Time  time.Time
	JSON  map[string]any
}

// IntValue reads a stored value row as a whole number.
//
// A TEXT row is parsed, because cantor's rack code guarded with
// `str(u_start).isdigit()`, which is what a deployment whose unit attributes are
// stored as text produces. A value in a table that is neither numeric nor text
// is not a number at all, which is reported rather than coerced.
func IntValue(row ValueRow) (int32, bool) {
	switch row.Table {
	case value.TableIndexIntegers:
		return int32(row.Int), true
	case value.TableIndexFloats:
		return int32(row.Float), true
	case value.TableIndexTexts:
		n, err := strconv.Atoi(strings.TrimSpace(row.Text))
		if err != nil {
			return 0, false
		}
		return int32(n), true
	}
	return 0, false
}

// OpKind distinguishes the two mutations the planner can emit.
type OpKind uint8

const (
	// OpInsert creates a value row.
	OpInsert OpKind = iota
	// OpDelete removes a value row addressed by RowID.
	OpDelete
)

// ValueOp is one row-level mutation to apply inside the write transaction.
type ValueOp struct {
	Kind   OpKind
	Table  string
	AttrID model.ID

	// RowID is set for OpDelete.
	RowID model.ID

	// Exactly one of the following is set for OpInsert, according to Table.
	Int   model.Int32
	Float float64
	Text  string
	Time  time.Time
	JSON  map[string]any
}

// Change is one attribute-level change, in cantor's shape, ready to be
// persisted as an attribute-history row.
type Change struct {
	CIID      model.ID
	AttrID    model.ID
	TypeID    model.ID
	Operate   model.OperateType
	Old       any
	New       any
	IsDynamic bool
}

// ValuePlan is the ordered set of row mutations for one write.
//
// Deletes are ordered before inserts so that re-writing a multi-valued
// attribute keeps ascending row ids, which is what makes a subsequent read
// return the values in the order the caller supplied. cantor does the same
// thing explicitly for the same reason (value.py: "Delete first and then add
// to ensure id sorting").
type ValuePlan struct {
	Deletes []ValueOp
	Inserts []ValueOp
}

// Ops returns every mutation, deletes first.
func (p ValuePlan) Ops() []ValueOp {
	out := make([]ValueOp, 0, len(p.Deletes)+len(p.Inserts))
	out = append(out, p.Deletes...)
	out = append(out, p.Inserts...)
	return out
}

// Empty reports whether the plan would change nothing.
func (p ValuePlan) Empty() bool { return len(p.Deletes) == 0 && len(p.Inserts) == 0 }

// ListOpAdd and ListOpDelete are the incremental multi-value operations
// cantor accepts as {"op": "add"|"delete", "v": [...]}.
const (
	ListOpAdd    = "add"
	ListOpDelete = "delete"
)

// ListDelta is the incremental form cantor accepts for a multi-valued
// attribute, as an alternative to sending the whole list.
type ListDelta struct {
	Op     string
	Values []any
}

// PlanValueUpsert is the pure port of cantor's
// CIValueManager.create_or_update_attr_value (api/lib/cmdb/value.py:294).
//
// It compares incoming values against the rows already stored and produces the
// row-level mutations plus the attribute-level change list. Doing the
// comparison here — rather than inside the data layer — keeps the write
// semantics testable without a database, which matters because the
// multi-valued branch is positional and easy to get subtly wrong.
//
// Semantics reproduced faithfully:
//
//   - A single-valued attribute with no existing row is inserted; an existing
//     equal value is a no-op; a differing value updates in place (which the
//     caller expresses as delete+insert, since a value table row's value is
//     the whole payload); a nil value deletes the row.
//   - A multi-valued attribute accepts either an ordered list or a
//     {"op","v"} delta. For the ordered form cantor finds the first differing
//     position, deletes every row from there on, and appends the remaining
//     tail — it is NOT a set union, and the port must not turn it into one.
//   - is_file attributes are treated as single-valued even when is_list is
//     set (cantor: `attr.is_list and not attr.is_file`).
//   - Attributes flagged is_dynamic still have their values written but do not
//     produce history entries.
//   - Values for attributes not bound to the type are rejected rather than
//     ignored. cantor's loop silently `continue`s on those, which hides caller
//     bugs; the port reports them.
func PlanValueUpsert(
	ciID, typeID model.ID,
	specs map[model.ID]AttributeSpec,
	existing []ValueRow,
	incoming map[model.ID]any,
) (ValuePlan, []Change, error) {
	byAttr := groupRowsByAttr(existing)

	var plan ValuePlan
	var changes []Change

	// Deterministic ordering: map iteration order must not decide row ids.
	attrIDs := make([]model.ID, 0, len(incoming))
	for attrID := range incoming {
		attrIDs = append(attrIDs, attrID)
	}
	sort.Slice(attrIDs, func(i, j int) bool { return attrIDs[i] < attrIDs[j] })

	for _, attrID := range attrIDs {
		spec, ok := specs[attrID]
		if !ok {
			return ValuePlan{}, nil, fmt.Errorf("%w: attr_id=%d", ErrUnknownAttribute, attrID)
		}
		if spec.HasComputeScript {
			return ValuePlan{}, nil, fmt.Errorf("%w: attr_id=%d (%s)", ErrComputedScriptUnsupported, attrID, spec.Name)
		}
		table, err := spec.StorageTable()
		if err != nil {
			return ValuePlan{}, nil, fmt.Errorf("attr_id=%d (%s): %w", attrID, spec.Name, err)
		}

		raw := incoming[attrID]
		if raw == nil {
			// A nil value clears the attribute. Required attributes cannot be
			// cleared; that check runs before planning (see ValidateRequired).
			for _, row := range byAttr[attrID] {
				plan.Deletes = append(plan.Deletes, ValueOp{Kind: OpDelete, Table: table, AttrID: attrID, RowID: row.RowID})
				changes = appendChange(changes, spec, ciID, typeID, model.OperateDelete, rowText(row), nil)
			}
			continue
		}

		if spec.Multivalued() {
			ops, chg, err := planMultiValued(ciID, typeID, spec, table, byAttr[attrID], raw)
			if err != nil {
				return ValuePlan{}, nil, err
			}
			plan.Deletes = append(plan.Deletes, ops.Deletes...)
			plan.Inserts = append(plan.Inserts, ops.Inserts...)
			changes = append(changes, chg...)
			continue
		}

		ops, chg, err := planSingleValued(ciID, typeID, spec, table, byAttr[attrID], raw)
		if err != nil {
			return ValuePlan{}, nil, err
		}
		plan.Deletes = append(plan.Deletes, ops.Deletes...)
		plan.Inserts = append(plan.Inserts, ops.Inserts...)
		changes = append(changes, chg...)
	}

	return plan, changes, nil
}

// planSingleValued handles a scalar attribute: at most one row exists.
func planSingleValued(
	ciID, typeID model.ID,
	spec AttributeSpec,
	table string,
	rows []ValueRow,
	raw any,
) (ValuePlan, []Change, error) {
	stored, err := spec.ValueType.Validate(raw)
	if err != nil {
		return ValuePlan{}, nil, fmt.Errorf("attr_id=%d (%s): %w", spec.ID, spec.Name, err)
	}
	if stored == nil {
		// A validated value that deserialises to nil (e.g. an empty JSON
		// string) is treated as absent, matching cantor's falsy check.
		return ValuePlan{}, nil, nil
	}

	var plan ValuePlan
	// cantor reads a single existing row; extra rows would be a data defect,
	// so all of them are replaced rather than leaving orphans behind.
	for _, row := range rows {
		if rowText(row) == fmt.Sprint(stored) {
			continue
		}
		plan.Deletes = append(plan.Deletes, ValueOp{Kind: OpDelete, Table: table, AttrID: spec.ID, RowID: row.RowID})
	}

	insert, err := buildInsert(spec.ID, table, spec.ValueType, stored)
	if err != nil {
		return ValuePlan{}, nil, err
	}

	var changes []Change
	if len(rows) == 0 {
		plan.Inserts = append(plan.Inserts, insert)
		changes = appendChange(changes, spec, ciID, typeID, model.OperateAdd, nil, stored)
		return plan, changes, nil
	}
	// Only a real difference is a change; an identical value is a no-op.
	if len(plan.Deletes) == 0 {
		return ValuePlan{}, nil, nil
	}
	plan.Inserts = append(plan.Inserts, insert)
	changes = appendChange(changes, spec, ciID, typeID, model.OperateUpdate, rowText(rows[0]), stored)
	return plan, changes, nil
}

// planMultiValued handles an is_list attribute: zero or more rows.
func planMultiValued(
	ciID, typeID model.ID,
	spec AttributeSpec,
	table string,
	rows []ValueRow,
	raw any,
) (ValuePlan, []Change, error) {
	// Validate and coerce every element up front so a bad element cannot leave
	// a half-applied plan.
	coerce := func(list []any) ([]any, error) {
		out := make([]any, 0, len(list))
		for _, item := range list {
			stored, err := spec.ValueType.Validate(item)
			if err != nil {
				return nil, fmt.Errorf("attr_id=%d (%s): %w", spec.ID, spec.Name, err)
			}
			if stored != nil {
				out = append(out, stored)
			}
		}
		return out, nil
	}

	var plan ValuePlan
	var changes []Change

	switch v := raw.(type) {
	case ListDelta:
		want, err := coerce(v.Values)
		if err != nil {
			return ValuePlan{}, nil, err
		}
		existing := make([]string, len(rows))
		for i, row := range rows {
			existing[i] = rowText(row)
		}
		switch v.Op {
		case ListOpAdd:
			for _, item := range want {
				if containsString(existing, fmt.Sprint(item)) {
					continue
				}
				insert, err := buildInsert(spec.ID, table, spec.ValueType, item)
				if err != nil {
					return ValuePlan{}, nil, err
				}
				plan.Inserts = append(plan.Inserts, insert)
				changes = appendChange(changes, spec, ciID, typeID, model.OperateAdd, nil, item)
			}
		case ListOpDelete:
			for _, item := range want {
				idx := indexOfString(existing, fmt.Sprint(item))
				if idx < 0 {
					continue
				}
				plan.Deletes = append(plan.Deletes, ValueOp{Kind: OpDelete, Table: table, AttrID: spec.ID, RowID: rows[idx].RowID})
				changes = appendChange(changes, spec, ciID, typeID, model.OperateDelete, item, nil)
			}
		default:
			return ValuePlan{}, nil, fmt.Errorf("%w: attr_id=%d unknown list op %q", ErrUnknownAttribute, spec.ID, v.Op)
		}
		return plan, changes, nil

	case []any:
		want, err := coerce(v)
		if err != nil {
			return ValuePlan{}, nil, err
		}
		return planOrderedList(ciID, typeID, spec, table, rows, want)

	default:
		return ValuePlan{}, nil, fmt.Errorf("%w: attr_id=%d is multi-valued but received %T", ErrUnknownAttribute, spec.ID, raw)
	}
}

// planOrderedList reproduces cantor's positional diff: find the first index
// that differs, delete every stored row from there on, then append the new
// tail. It is NOT a set operation — a caller that reorders two values gets
// two writes, which is intentional: the stored row ids are what preserve the
// caller's ordering.
func planOrderedList(
	ciID, typeID model.ID,
	spec AttributeSpec,
	table string,
	rows []ValueRow,
	want []any,
) (ValuePlan, []Change, error) {
	existing := make([]string, len(rows))
	for i, row := range rows {
		existing[i] = rowText(row)
	}

	common := len(want)
	if len(existing) < common {
		common = len(existing)
	}
	firstDiff := 0
	for firstDiff < common && existing[firstDiff] == fmt.Sprint(want[firstDiff]) {
		firstDiff++
	}

	var plan ValuePlan
	var changes []Change
	for idx := firstDiff; idx < len(rows); idx++ {
		plan.Deletes = append(plan.Deletes, ValueOp{Kind: OpDelete, Table: table, AttrID: spec.ID, RowID: rows[idx].RowID})
		changes = appendChange(changes, spec, ciID, typeID, model.OperateDelete, existing[idx], nil)
	}
	for idx := firstDiff; idx < len(want); idx++ {
		insert, err := buildInsert(spec.ID, table, spec.ValueType, want[idx])
		if err != nil {
			return ValuePlan{}, nil, err
		}
		plan.Inserts = append(plan.Inserts, insert)
		changes = appendChange(changes, spec, ciID, typeID, model.OperateAdd, nil, want[idx])
	}
	return plan, changes, nil
}

// buildInsert converts a validated value into the row mutation for its table,
// enforcing the indexed-text width limit that MySQL imposes.
func buildInsert(attrID model.ID, table string, vt value.ValueType, stored value.StoredValue) (ValueOp, error) {
	op := ValueOp{Kind: OpInsert, Table: table, AttrID: attrID}
	switch {
	case table == value.TableIndexIntegers:
		// The integer carrier is int32 because the column is MySQL int(11);
		// value.Validate already rejected anything wider, so this assertion
		// can only fail if the carrier type itself changed.
		i, ok := stored.(int32)
		if !ok {
			return ValueOp{}, fmt.Errorf("cmdb: attr_id=%d expected int carrier, got %T", attrID, stored)
		}
		op.Int = i
	case table == value.TableIndexFloats:
		f, ok := stored.(float64)
		if !ok {
			return ValueOp{}, fmt.Errorf("cmdb: attr_id=%d expected float carrier, got %T", attrID, stored)
		}
		op.Float = f
	case table == value.TableIndexDateTime:
		t, ok := stored.(time.Time)
		if !ok {
			return ValueOp{}, fmt.Errorf("cmdb: attr_id=%d expected time carrier, got %T", attrID, stored)
		}
		op.Time = t
	case table == value.TableIndexTexts, table == value.TableTexts:
		s, ok := stored.(string)
		if !ok {
			s = fmt.Sprint(stored)
		}
		if table == value.TableIndexTexts && len(s) > value.MaxIndexedTextLen {
			// Reject rather than truncate, and rather than silently moving the
			// value to the unindexed table — cantor has no such routing, and a
			// value that moves tables would stop being searchable without the
			// model changing.
			return ValueOp{}, fmt.Errorf("%w: attr_id=%d text length %d exceeds %d; declare the attribute unindexed",
				value.ErrValueOutOfRange, attrID, len(s), value.MaxIndexedTextLen)
		}
		op.Text = s
	case table == value.TableJSON:
		m, _ := stored.(map[string]any)
		if m == nil {
			// cantor stores JSON in a JSON column; a non-object carrier (array,
			// scalar) has no representation in this table's Go type, so it is
			// wrapped rather than dropped.
			m = map[string]any{"value": stored}
		}
		op.JSON = m
	default:
		return ValueOp{}, fmt.Errorf("cmdb: attr_id=%d unknown value table %q", attrID, table)
	}
	return op, nil
}

// appendChange records a history entry, honouring cantor's rule that dynamic
// attributes still store values but do not generate history.
func appendChange(changes []Change, spec AttributeSpec, ciID, typeID model.ID, op model.OperateType, old, new any) []Change {
	if spec.IsDynamic {
		return changes
	}
	return append(changes, Change{
		CIID: ciID, AttrID: spec.ID, TypeID: typeID,
		Operate: op, Old: old, New: new,
	})
}

func groupRowsByAttr(rows []ValueRow) map[model.ID][]ValueRow {
	out := make(map[model.ID][]ValueRow, len(rows))
	for _, row := range rows {
		out[row.AttrID] = append(out[row.AttrID], row)
	}
	return out
}

// rowText renders a stored row's payload for comparison and for history —
// the same stringification cantor uses when it serialises a value for
// diagnostics.
func rowText(row ValueRow) string {
	switch {
	case row.Table == value.TableIndexIntegers:
		return fmt.Sprint(row.Int)
	case row.Table == value.TableIndexFloats:
		return fmt.Sprint(row.Float)
	case row.Table == value.TableIndexDateTime:
		return row.Time.Format("2006-01-02 15:04:05")
	case row.Table == value.TableJSON:
		return fmt.Sprint(row.JSON)
	default:
		return row.Text
	}
}

func containsString(haystack []string, needle string) bool {
	return indexOfString(haystack, needle) >= 0
}

func indexOfString(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}

// cantor enforces "required" in TWO different places with two different
// meanings, and conflating them breaks partial updates:
//
//   - Presence, on CREATE only (ci.py:459): every required, non-computed
//     attribute bound to the type must appear in the payload — by name or by
//     alias. Missing means the whole create is refused. An UPDATE has no such
//     check at all (ci.py:463 handles the update branch and only applies the
//     updated_at default), which is what lets a caller patch one attribute
//     without resending the rest.
//   - Non-emptiness, on any SUPPLIED value (value.py:137): if the caller did
//     send a required attribute, an empty value is refused. This applies on
//     both create and update.
//
// Both are reproduced below as separate functions so a caller cannot
// accidentally apply the presence rule to an update.

// ValidateRequiredPresence enforces the create-time completeness rule: every
// required, non-computed attribute must be present in the incoming payload.
//
// Computed attributes are exempt because cantor derives their value rather
// than accepting one. Attributes carrying a default are also satisfied in
// cantor because defaults are materialised into the payload BEFORE this check
// runs — that default-materialisation step is not yet ported, so today a
// required attribute with a default must still be supplied explicitly. That is
// stricter than cantor, never more permissive, so it cannot let bad data in.
func ValidateRequiredPresence(specs map[model.ID]AttributeSpec, incoming map[model.ID]any) error {
	for _, id := range sortedAttrIDs(specs) {
		spec := specs[id]
		if !spec.IsRequired || spec.IsComputed {
			continue
		}
		if _, present := incoming[id]; !present {
			return fmt.Errorf("%w: %s", ErrRequiredAttribute, spec.Name)
		}
	}
	return nil
}

// ValidateSuppliedValues enforces the non-emptiness rule for whatever the
// caller did supply. It runs on both create and update.
func ValidateSuppliedValues(specs map[model.ID]AttributeSpec, incoming map[model.ID]any) error {
	for _, id := range sortedAttrIDs(incoming) {
		spec, known := specs[id]
		if !known || !spec.IsRequired {
			continue
		}
		if isEmptyValue(incoming[id]) {
			return fmt.Errorf("%w: %s", ErrRequiredAttribute, spec.Name)
		}
	}
	return nil
}

// ValidateRequired is the combined rule a CREATE must satisfy.
func ValidateRequired(specs map[model.ID]AttributeSpec, incoming map[model.ID]any) error {
	if err := ValidateRequiredPresence(specs, incoming); err != nil {
		return err
	}
	return ValidateSuppliedValues(specs, incoming)
}

// isEmptyValue reproduces cantor's `not value and value != 0` test: falsy, but
// zero is explicitly allowed.
//
// That exemption is load-bearing. A required integer attribute legitimately
// holds 0, and a required boolean legitimately holds false; a naive
// truthiness test would reject both and make such attributes unusable.
func isEmptyValue(raw any) bool {
	switch v := raw.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	// A multi-valued attribute may also arrive as a delta; an empty delta
	// carries no values and is therefore empty.
	case ListDelta:
		return len(v.Values) == 0
	}
	return false
}

func sortedAttrIDs[V any](m map[model.ID]V) []model.ID {
	ids := make([]model.ID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
