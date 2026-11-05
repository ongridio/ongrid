package ci

import (
	"errors"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

func spec(id model.ID, name string, vt value.ValueType, opts ...func(*AttributeSpec)) AttributeSpec {
	s := AttributeSpec{ID: id, Name: name, Alias: name, ValueType: vt, IsIndex: true}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func list(s *AttributeSpec)      { s.IsList = true }
func unindexed(s *AttributeSpec) { s.IsIndex = false }
func required(s *AttributeSpec)  { s.IsRequired = true }
func dynamic(s *AttributeSpec)   { s.IsDynamic = true }
func password(s *AttributeSpec)  { s.IsPassword = true; s.IsIndex = true }
func fileAttr(s *AttributeSpec)  { s.IsFile = true }
func scripted(s *AttributeSpec)  { s.IsComputed = true; s.HasComputeScript = true }

func specsOf(s ...AttributeSpec) map[model.ID]AttributeSpec {
	m := make(map[model.ID]AttributeSpec, len(s))
	for _, x := range s {
		m[x.ID] = x
	}
	return m
}

// --- single-valued ---

func TestPlanInsertsWhenNoRowExists(t *testing.T) {
	specs := specsOf(spec(1, "cpu_count", value.TypeInt))
	plan, changes, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: 8})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Inserts) != 1 || len(plan.Deletes) != 0 {
		t.Fatalf("plan = %+v, want one insert", plan)
	}
	if plan.Inserts[0].Int != 8 || plan.Inserts[0].Table != value.TableIndexIntegers {
		t.Fatalf("insert = %+v, want int 8 into the integer table", plan.Inserts[0])
	}
	if len(changes) != 1 || changes[0].Operate != model.OperateAdd || changes[0].New != int32(8) {
		t.Fatalf("changes = %+v, want one ADD", changes)
	}
}

// An identical value must produce no writes at all — otherwise every no-op
// edit would rewrite rows and emit spurious history.
func TestPlanIsNoOpWhenValueIsUnchanged(t *testing.T) {
	specs := specsOf(spec(1, "cpu_count", value.TypeInt))
	existing := []ValueRow{{RowID: 99, AttrID: 1, Table: value.TableIndexIntegers, Int: 8}}

	plan, changes, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: 8})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if !plan.Empty() {
		t.Fatalf("plan = %+v, want empty", plan)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %+v, want none", changes)
	}
}

func TestPlanUpdatesWhenValueDiffers(t *testing.T) {
	specs := specsOf(spec(1, "cpu_count", value.TypeInt))
	existing := []ValueRow{{RowID: 99, AttrID: 1, Table: value.TableIndexIntegers, Int: 8}}

	plan, changes, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: 16})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0].RowID != 99 {
		t.Fatalf("deletes = %+v, want the existing row 99", plan.Deletes)
	}
	if len(plan.Inserts) != 1 || plan.Inserts[0].Int != 16 {
		t.Fatalf("inserts = %+v, want the new value 16", plan.Inserts)
	}
	if len(changes) != 1 || changes[0].Operate != model.OperateUpdate {
		t.Fatalf("changes = %+v, want one UPDATE", changes)
	}
	if changes[0].Old != "8" || changes[0].New != int32(16) {
		t.Fatalf("change old/new = %v/%v, want 8/16", changes[0].Old, changes[0].New)
	}
}

func TestPlanDeletesOnNilValue(t *testing.T) {
	specs := specsOf(spec(1, "cpu_count", value.TypeInt))
	existing := []ValueRow{{RowID: 99, AttrID: 1, Table: value.TableIndexIntegers, Int: 8}}

	plan, changes, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: nil})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0].RowID != 99 || len(plan.Inserts) != 0 {
		t.Fatalf("plan = %+v, want only a delete of row 99", plan)
	}
	if len(changes) != 1 || changes[0].Operate != model.OperateDelete {
		t.Fatalf("changes = %+v, want one DELETE", changes)
	}
}

// Deletes must be emitted before inserts so a rewrite keeps ascending row ids
// and a subsequent read returns values in the caller's order.
func TestPlanOrdersDeletesBeforeInserts(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	existing := []ValueRow{
		{RowID: 5, AttrID: 1, Table: value.TableIndexTexts, Text: "a"},
		{RowID: 6, AttrID: 1, Table: value.TableIndexTexts, Text: "b"},
	}

	plan, _, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: []any{"a", "c"}})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	ops := plan.Ops()
	if len(ops) != 2 {
		t.Fatalf("ops = %+v, want delete then insert", ops)
	}
	if ops[0].Kind != OpDelete || ops[1].Kind != OpInsert {
		t.Fatalf("op order = %v then %v, want delete then insert", ops[0].Kind, ops[1].Kind)
	}
}

// --- multi-valued: the positional diff ---

// cantor compares positionally, not as a set. Reordering must produce writes,
// because row ids are what preserve the caller's ordering.
func TestPlanListReordersProduceWrites(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	existing := []ValueRow{
		{RowID: 5, AttrID: 1, Table: value.TableIndexTexts, Text: "a"},
		{RowID: 6, AttrID: 1, Table: value.TableIndexTexts, Text: "b"},
	}

	plan, _, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: []any{"b", "a"}})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Deletes) != 2 || len(plan.Inserts) != 2 {
		t.Fatalf("plan = %+v, want both rows rewritten (positional, not set)", plan)
	}
}

// A pure append touches only the new tail — the prefix is left alone.
func TestPlanListAppendsOnlyTheTail(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	existing := []ValueRow{
		{RowID: 5, AttrID: 1, Table: value.TableIndexTexts, Text: "a"},
		{RowID: 6, AttrID: 1, Table: value.TableIndexTexts, Text: "b"},
	}

	plan, changes, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: []any{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Deletes) != 0 {
		t.Fatalf("deletes = %+v, want none (prefix unchanged)", plan.Deletes)
	}
	if len(plan.Inserts) != 1 || plan.Inserts[0].Text != "c" {
		t.Fatalf("inserts = %+v, want only \"c\"", plan.Inserts)
	}
	if len(changes) != 1 || changes[0].Operate != model.OperateAdd {
		t.Fatalf("changes = %+v, want one ADD", changes)
	}
}

// Shrinking the list deletes only the removed suffix.
func TestPlanListShrinkDeletesSuffix(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	existing := []ValueRow{
		{RowID: 5, AttrID: 1, Table: value.TableIndexTexts, Text: "a"},
		{RowID: 6, AttrID: 1, Table: value.TableIndexTexts, Text: "b"},
		{RowID: 7, AttrID: 1, Table: value.TableIndexTexts, Text: "c"},
	}

	plan, _, err := PlanValueUpsert(10, 3, specs, existing, map[model.ID]any{1: []any{"a"}})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Inserts) != 0 {
		t.Fatalf("inserts = %+v, want none", plan.Inserts)
	}
	if len(plan.Deletes) != 2 || plan.Deletes[0].RowID != 6 || plan.Deletes[1].RowID != 7 {
		t.Fatalf("deletes = %+v, want rows 6 and 7", plan.Deletes)
	}
}

func TestPlanListDeltaAddSkipsExisting(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	existing := []ValueRow{{RowID: 5, AttrID: 1, Table: value.TableIndexTexts, Text: "a"}}

	plan, changes, err := PlanValueUpsert(10, 3, specs, existing,
		map[model.ID]any{1: ListDelta{Op: ListOpAdd, Values: []any{"a", "b"}}})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Inserts) != 1 || plan.Inserts[0].Text != "b" {
		t.Fatalf("inserts = %+v, want only \"b\"", plan.Inserts)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one", changes)
	}
}

func TestPlanListDeltaDeleteRemovesOnlyPresent(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	existing := []ValueRow{
		{RowID: 5, AttrID: 1, Table: value.TableIndexTexts, Text: "a"},
		{RowID: 6, AttrID: 1, Table: value.TableIndexTexts, Text: "b"},
	}

	plan, _, err := PlanValueUpsert(10, 3, specs, existing,
		map[model.ID]any{1: ListDelta{Op: ListOpDelete, Values: []any{"b", "zzz"}}})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0].RowID != 6 {
		t.Fatalf("deletes = %+v, want only row 6", plan.Deletes)
	}
	if len(plan.Inserts) != 0 {
		t.Fatalf("inserts = %+v, want none", plan.Inserts)
	}
}

func TestPlanListRejectsUnknownDeltaOp(t *testing.T) {
	specs := specsOf(spec(1, "ips", value.TypeText, list))
	_, _, err := PlanValueUpsert(10, 3, specs, nil,
		map[model.ID]any{1: ListDelta{Op: "replace", Values: []any{"a"}}})
	if err == nil {
		t.Fatal("unknown list op = nil error, want error")
	}
}

// is_file attributes are single-valued even when is_list is set, matching
// cantor's `attr.is_list and not attr.is_file`.
func TestPlanFileAttributeIsSingleValued(t *testing.T) {
	specs := specsOf(spec(1, "attachment", value.TypeText, list, fileAttr))
	plan, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: "files/x.pdf"})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Inserts) != 1 || plan.Inserts[0].Text != "files/x.pdf" {
		t.Fatalf("inserts = %+v, want a single row", plan.Inserts)
	}
}

// --- storage-table selection through the planner ---

// A password attribute is stored unindexed even though is_index is set,
// which is cantor's forced rule.
func TestPlanPasswordAttributeLandsInUnindexedTable(t *testing.T) {
	specs := specsOf(spec(1, "root_pw", value.TypeText, password))
	plan, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: "hunter2"})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if plan.Inserts[0].Table != value.TableTexts {
		t.Fatalf("table = %q, want %q (password forces unindexed)", plan.Inserts[0].Table, value.TableTexts)
	}
}

// A long string on an indexed TEXT attribute is rejected, not truncated and
// not silently moved to the unindexed table: cantor has no such routing, and a
// value that changed tables would quietly stop being searchable.
func TestPlanRejectsOverLongIndexedText(t *testing.T) {
	specs := specsOf(spec(1, "description", value.TypeText))
	long := make([]byte, value.MaxIndexedTextLen+1)
	for i := range long {
		long[i] = 'x'
	}

	_, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: string(long)})
	if !errors.Is(err, value.ErrValueOutOfRange) {
		t.Fatalf("error = %v, want ErrValueOutOfRange", err)
	}

	// The same value is fine once the attribute is declared unindexed.
	unindexedSpecs := specsOf(spec(1, "description", value.TypeText, unindexed))
	if _, _, err := PlanValueUpsert(10, 3, unindexedSpecs, nil, map[model.ID]any{1: string(long)}); err != nil {
		t.Fatalf("unindexed long text error = %v, want nil", err)
	}
}

func TestPlanBoolSharesTheIntegerTable(t *testing.T) {
	specs := specsOf(spec(1, "enabled", value.TypeBool))
	plan, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: "yes"})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if plan.Inserts[0].Table != value.TableIndexIntegers || plan.Inserts[0].Int != 1 {
		t.Fatalf("insert = %+v, want int 1 in the integer table", plan.Inserts[0])
	}
}

func TestPlanDateLandsInTheDatetimeTable(t *testing.T) {
	specs := specsOf(spec(1, "purchased", value.TypeDate))
	plan, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: "2026-10-16"})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if plan.Inserts[0].Table != value.TableIndexDateTime {
		t.Fatalf("table = %q, want %q", plan.Inserts[0].Table, value.TableIndexDateTime)
	}
}

// --- rejections ---

func TestPlanRejectsUnknownAttribute(t *testing.T) {
	specs := specsOf(spec(1, "cpu_count", value.TypeInt))
	_, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{42: 1})
	if !errors.Is(err, ErrUnknownAttribute) {
		t.Fatalf("error = %v, want ErrUnknownAttribute", err)
	}
}

// A computed-script attribute is refused rather than silently stored as empty,
// because an absent value is indistinguishable from an empty one downstream.
func TestPlanRefusesComputedScriptAttribute(t *testing.T) {
	specs := specsOf(spec(1, "derived", value.TypeText, scripted))
	_, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: "x"})
	if !errors.Is(err, ErrComputedScriptUnsupported) {
		t.Fatalf("error = %v, want ErrComputedScriptUnsupported", err)
	}
}

// Dynamic attributes are stored but write no history — cantor's rule, so that
// auto-discovery churn does not flood the audit log.
func TestPlanDynamicAttributeWritesNoHistory(t *testing.T) {
	specs := specsOf(spec(1, "last_seen", value.TypeText, dynamic))
	plan, changes, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: "now"})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.Inserts) != 1 {
		t.Fatalf("inserts = %+v, want the value stored", plan.Inserts)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %+v, want none for a dynamic attribute", changes)
	}
}

// --- determinism ---

// Map iteration order must not decide the emitted row order, otherwise ids and
// history rows would be non-deterministic between runs.
func TestPlanIsDeterministicAcrossAttributeOrder(t *testing.T) {
	specs := specsOf(
		spec(3, "c", value.TypeText),
		spec(1, "a", value.TypeText),
		spec(2, "b", value.TypeText),
	)
	incoming := map[model.ID]any{3: "cc", 1: "aa", 2: "bb"}

	var first []model.ID
	for i := 0; i < 20; i++ {
		plan, _, err := PlanValueUpsert(10, 3, specs, nil, incoming)
		if err != nil {
			t.Fatalf("plan error = %v", err)
		}
		got := make([]model.ID, 0, len(plan.Inserts))
		for _, op := range plan.Inserts {
			got = append(got, op.AttrID)
		}
		if i == 0 {
			first = got
			continue
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d order = %v, want %v", i, got, first)
			}
		}
	}
	if len(first) != 3 || first[0] != 1 || first[1] != 2 || first[2] != 3 {
		t.Fatalf("order = %v, want ascending attr ids", first)
	}
}

// --- required validation ---

func TestValidateRequired(t *testing.T) {
	specs := specsOf(
		spec(1, "hostname", value.TypeText, required),
		spec(2, "cpu_count", value.TypeInt, required),
	)

	if err := ValidateRequired(specs, map[model.ID]any{1: "web-1", 2: 4}); err != nil {
		t.Fatalf("satisfied required error = %v, want nil", err)
	}
	if err := ValidateRequired(specs, map[model.ID]any{1: "web-1"}); !errors.Is(err, ErrRequiredAttribute) {
		t.Fatalf("missing required error = %v, want ErrRequiredAttribute", err)
	}
	if err := ValidateRequired(specs, map[model.ID]any{1: "", 2: 4}); !errors.Is(err, ErrRequiredAttribute) {
		t.Fatalf("empty required error = %v, want ErrRequiredAttribute", err)
	}
	// 0 is a legitimate value for a required integer — cantor's truthiness
	// check would wrongly reject it, and the port must not.
	if err := ValidateRequired(specs, map[model.ID]any{1: "web-1", 2: 0}); err != nil {
		t.Fatalf("required int 0 error = %v, want nil (0 is not empty)", err)
	}
}

func TestValidateRequiredReportsFirstByID(t *testing.T) {
	specs := specsOf(
		spec(7, "seven", value.TypeText, required),
		spec(2, "two", value.TypeText, required),
	)
	err := ValidateRequired(specs, map[model.ID]any{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !contains(err.Error(), "two") {
		t.Fatalf("error = %q, want it to name the lowest-id attribute first", err)
	}
}

func TestListDeltaAndTimeCarrierRoundTrip(t *testing.T) {
	when := time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC)
	specs := specsOf(spec(1, "seen_at", value.TypeDateTime))

	plan, _, err := PlanValueUpsert(10, 3, specs, nil, map[model.ID]any{1: when})
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if !plan.Inserts[0].Time.Equal(when) {
		t.Fatalf("time = %v, want %v", plan.Inserts[0].Time, when)
	}
	if got := rowText(ValueRow{Table: value.TableIndexDateTime, Time: when}); got != "2026-10-16 08:30:00" {
		t.Fatalf("rowText = %q", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
