package value

import (
	"errors"
	"testing"
	"time"
)

// TestEffectiveIndexAndStorageTable mirrors cantor's TableMap.table switch and
// its ValueTypeMap.table_name map (api/lib/cmdb/utils.py:110-134 plus the
// TableMap property at :147). If this table ever drifts from the source, the
// port would write values into tables that no reader queries.
func TestEffectiveIndexAndStorageTable(t *testing.T) {
	tests := []struct {
		name     string
		vt       ValueType
		declared bool
		mods     StorageModifiers
		wantIdx  bool
		wantTbl  string
	}{
		// Branch 2 of TableMap: non-TEXT/JSON types are forced indexed.
		{"INT unindexed-declared is forced indexed", TypeInt, false, StorageModifiers{}, true, TableIndexIntegers},
		{"INT indexed", TypeInt, true, StorageModifiers{}, true, TableIndexIntegers},
		{"BOOL shares the integer table", TypeBool, false, StorageModifiers{}, true, TableIndexIntegers},
		{"FLOAT", TypeFloat, false, StorageModifiers{}, true, TableIndexFloats},
		{"DATETIME", TypeDateTime, false, StorageModifiers{}, true, TableIndexDateTime},
		{"DATE shares the datetime table", TypeDate, false, StorageModifiers{}, true, TableIndexDateTime},
		{"TIME shares the text index table", TypeTime, false, StorageModifiers{}, true, TableIndexTexts},

		// Branch 3: TEXT honours the declared flag.
		{"TEXT indexed", TypeText, true, StorageModifiers{}, true, TableIndexTexts},
		{"TEXT unindexed", TypeText, false, StorageModifiers{}, false, TableTexts},
		{"TEXT with no declared index goes unindexed", TypeText, false, StorageModifiers{}, false, TableTexts},

		// Branch 1: password/link/file are pinned unindexed even when TEXT is
		// declared indexed. This is the rule that keeps secrets out of the
		// indexed table (and out of any denormalized document).
		{"password forces unindexed", TypeText, true, StorageModifiers{IsPassword: true}, false, TableTexts},
		{"link forces unindexed", TypeText, true, StorageModifiers{IsLink: true}, false, TableTexts},
		{"file forces unindexed", TypeText, true, StorageModifiers{IsFile: true}, false, TableTexts},

		// JSON always resolves to c_value_json, indexed or not.
		{"JSON indexed", TypeJSON, true, StorageModifiers{}, true, TableJSON},
		{"JSON unindexed", TypeJSON, false, StorageModifiers{}, false, TableJSON},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotIdx := tc.vt.EffectiveIndex(tc.declared, tc.mods)
			if gotIdx != tc.wantIdx {
				t.Fatalf("EffectiveIndex(%s, declared=%v, %+v) = %v, want %v",
					tc.vt, tc.declared, tc.mods, gotIdx, tc.wantIdx)
			}
			gotTbl, err := tc.vt.StorageTable(gotIdx)
			if err != nil {
				t.Fatalf("StorageTable(%v) unexpected error: %v", gotIdx, err)
			}
			if gotTbl != tc.wantTbl {
				t.Fatalf("StorageTable(%v) = %q, want %q", gotIdx, gotTbl, tc.wantTbl)
			}
		})
	}
}

// The unindexed variants of non-TEXT/JSON types are unreachable through
// TableMap, which is exactly why c_value_integers / c_value_floats /
// c_value_datetime are dead. StorageTable must refuse them rather than pick a
// table that nothing reads.
func TestStorageTableRefusesDeadTableCombinations(t *testing.T) {
	for _, vt := range []ValueType{TypeInt, TypeFloat, TypeDateTime, TypeDate, TypeTime, TypeBool} {
		if _, err := vt.StorageTable(false); !errors.Is(err, ErrValueNotStorable) {
			t.Fatalf("StorageTable(%s, unindexed) error = %v, want ErrValueNotStorable", vt, err)
		}
	}
}

func TestStorageTableRejectsUnknownType(t *testing.T) {
	if _, err := ValueType("9").StorageTable(true); !errors.Is(err, ErrValueTypeUnknown) {
		t.Fatalf("unknown type error = %v, want ErrValueTypeUnknown", err)
	}
}

func TestValidateInt(t *testing.T) {
	tests := []struct {
		in      any
		want    int32
		wantErr error
	}{
		{in: "42", want: 42},
		{in: 42, want: 42},
		{in: int64(42), want: 42},
		{in: 42.0, want: 42},
		{in: " 7 ", want: 7, wantErr: nil}, // cantor does float(x); Go trims
		// cantor's int(float(x)) truncates toward zero, so "3.9" is 3.
		{in: "3.9", want: 3},
		{in: -3.9, want: -3},
		// Above the signed 32-bit ceiling cantor raises rather than truncates.
		{in: "2147483647", want: 2147483647},
		{in: "2147483648", wantErr: ErrValueOutOfRange},
		{in: "abc", wantErr: ErrValueUnparsable},
	}
	for _, tc := range tests {
		got, err := TypeInt.Validate(tc.in)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(%#v) error = %v, want %v", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Validate(%#v) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("Validate(%#v) = %v (%T), want %v", tc.in, got, got, tc.want)
		}
	}
}

func TestValidateFloat(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want float64
	}{{"1.5", 1.5}, {1.5, 1.5}, {2, 2.0}, {int64(2), 2.0}} {
		got, err := TypeFloat.Validate(tc.in)
		if err != nil {
			t.Fatalf("Validate(%#v) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("Validate(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := TypeFloat.Validate("nope"); !errors.Is(err, ErrValueUnparsable) {
		t.Fatalf("float from garbage error = %v, want ErrValueUnparsable", err)
	}
}

// BOOL is a membership test against cantor's BOOL_TRUE set; an unrecognised
// value is false, not an error. That is cantor's behaviour and it is
// deliberately preserved.
func TestValidateBool(t *testing.T) {
	trueish := []any{true, "true", "TRUE", "1", 1, int64(1), "Yes", "y", "Y", 1.0}
	for _, in := range trueish {
		got, err := TypeBool.Validate(in)
		if err != nil {
			t.Fatalf("Validate(%#v) error = %v", in, err)
		}
		if got != int32(1) {
			t.Fatalf("Validate(%#v) = %v, want 1", in, got)
		}
	}
	falseish := []any{false, "false", "0", 0, int64(0), "no", "n", "", "definitely-not-a-bool"}
	for _, in := range falseish {
		got, err := TypeBool.Validate(in)
		if err != nil {
			t.Fatalf("Validate(%#v) error = %v", in, err)
		}
		if got != int32(0) {
			t.Fatalf("Validate(%#v) = %v, want 0", in, got)
		}
	}
	if !BoolFromCarrier(1) || BoolFromCarrier(0) {
		t.Fatal("BoolFromCarrier round-trip failed")
	}
}

func TestValidateDate(t *testing.T) {
	// cantor accepts both forms and keeps only the date part.
	for _, in := range []string{"2026-10-16", "2026-10-16 13:45:01"} {
		got, err := TypeDate.Validate(in)
		if err != nil {
			t.Fatalf("Validate(%q) error = %v", in, err)
		}
		want := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
		if !got.(time.Time).Equal(want) {
			t.Fatalf("Validate(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := TypeDate.Validate("16/10/2026"); !errors.Is(err, ErrValueUnparsable) {
		t.Fatalf("bad date error = %v, want ErrValueUnparsable", err)
	}
}

// DATETIME rewrites 'T' to a space and drops 'Z' before parsing, which is how
// cantor tolerates RFC3339 input arriving from the SPA.
func TestValidateDateTimeNormalisesRFC3339(t *testing.T) {
	got, err := TypeDateTime.Validate("2026-10-16T13:45:01Z")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tm := got.(time.Time)
	if tm.Year() != 2026 || tm.Month() != 10 || tm.Day() != 16 || tm.Hour() != 13 || tm.Minute() != 45 || tm.Second() != 1 {
		t.Fatalf("Validate() = %v, want 2026-10-16 13:45:01", tm)
	}
	if _, err := TypeDateTime.Validate("not a datetime"); !errors.Is(err, ErrValueUnparsable) {
		t.Fatalf("bad datetime error = %v, want ErrValueUnparsable", err)
	}
}

// TIME extracts the first HH:MM:SS-shaped token (cantor's TIME_RE.findall(x)[0])
// and errors when there is none, matching cantor's IndexError on an empty list.
func TestValidateTime(t *testing.T) {
	got, err := TypeTime.Validate("2026-10-16 13:45:01")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got != "13:45:01" {
		t.Fatalf("Validate() = %v, want 13:45:01", got)
	}
	if got, err := TypeTime.Validate("23:59:59"); err != nil || got != "23:59:59" {
		t.Fatalf("Validate(23:59:59) = %v, %v", got, err)
	}
	// 24:00:00 must NOT match the regex (hours are 00-23).
	if _, err := TypeTime.Validate("24:00:00"); !errors.Is(err, ErrValueUnparsable) {
		t.Fatalf("Validate(24:00:00) error = %v, want ErrValueUnparsable", err)
	}
}

func TestValidateJSON(t *testing.T) {
	got, err := TypeJSON.Validate(`{"a":1}`)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["a"] != float64(1) {
		t.Fatalf("Validate() = %#v, want map[a:1]", got)
	}
	// Empty string is treated as absent, matching cantor's
	// `if isinstance(x, str) and x else x`.
	if got, err := TypeJSON.Validate(""); err != nil || got != nil {
		t.Fatalf("Validate(\"\") = %v, %v; want nil, nil", got, err)
	}
	if _, err := TypeJSON.Validate("{not json"); !errors.Is(err, ErrValueUnparsable) {
		t.Fatalf("bad json error = %v, want ErrValueUnparsable", err)
	}
}

func TestValidateNilIsAbsent(t *testing.T) {
	for _, vt := range []ValueType{TypeInt, TypeFloat, TypeText, TypeTime, TypeDate, TypeDateTime, TypeJSON, TypeBool} {
		got, err := vt.Validate(nil)
		if err != nil || got != nil {
			t.Fatalf("Validate(nil) for %s = %v, %v; want nil, nil", vt, got, err)
		}
	}
}

func TestValidateRejectsUnknownType(t *testing.T) {
	if _, err := ValueType("9").Validate("x"); !errors.Is(err, ErrValueTypeUnknown) {
		t.Fatalf("error = %v, want ErrValueTypeUnknown", err)
	}
}

// A password/link/file attribute must never be denormalised into a document
// column, because those values are secrets or opaque references and cantor
// only ever returns them from a dedicated endpoint.
func TestPasswordSafeOnlyForPlainAttributes(t *testing.T) {
	if !(StorageModifiers{}).IsPasswordSafe() {
		t.Fatal("plain attribute should be safe to denormalise")
	}
	for _, m := range []StorageModifiers{{IsPassword: true}, {IsLink: true}, {IsFile: true}} {
		if m.IsPasswordSafe() {
			t.Fatalf("%+v should not be safe to denormalise", m)
		}
	}
}
