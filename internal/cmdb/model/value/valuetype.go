// Package value models nexus-cantor's attribute-value store.
//
// cantor keeps every CI attribute value in one of a small set of typed
// "value tables", one row per (ci_id, attr_id) pair — optionally several rows
// for a multi-valued (is_list) attribute. This file is the Go port of
// cantor's api/lib/cmdb/utils.py::ValueTypeMap plus the TableMap property
// that derives which physical table a value belongs in.
//
// The derivation is NOT obvious and getting it wrong writes values into
// tables nothing reads, so it is reproduced here faithfully and covered by
// table-driven tests mirroring cantor's own mapping table.
package value

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ValueType is cantor's ValueTypeEnum. The values are the single-character
// codes cantor stores in c_attributes.value_type, so they must match exactly.
//
// Note the aliases in cantor's enum: PASSWORD, LINK and FILE are all TEXT
// ("2"), and REFERENCE is INT ("0"). Those are not distinct storage types —
// they are carried by the boolean flags on c_attributes and are represented
// in Go by StorageModifiers, not by additional ValueType constants.
type ValueType string

const (
	TypeInt      ValueType = "0"
	TypeFloat    ValueType = "1"
	TypeText     ValueType = "2"
	TypeDateTime ValueType = "3"
	TypeDate     ValueType = "4"
	TypeTime     ValueType = "5"
	TypeJSON     ValueType = "6"
	TypeBool     ValueType = "7"
)

// Physical value-table names. Cantor's prefix is preserved so an existing
// cantor database can be adopted in place, and because Ongrid already owns
// unprefixed "relations"/"relation_types".
const (
	TableIndexIntegers = "c_value_index_integers"
	TableIndexFloats   = "c_value_index_floats"
	TableIndexTexts    = "c_value_index_texts"
	TableIndexDateTime = "c_value_index_datetime"
	TableTexts         = "c_value_texts"
	TableJSON          = "c_value_json"
)

// maxInt is cantor's guard in string2int: values above the signed 32-bit
// ceiling are rejected rather than silently truncated, because cantor's
// value column is MySQL INT.
const (
	maxInt = 2147483647
	minInt = -2147483648
)

// timeRegexp mirrors cantor's TIME_RE, which extracts the first
// HH:MM:SS-shaped token from an incoming value.
var timeRegexp = regexp.MustCompile(`(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d`)

// Errors returned by Validate. Callers map these onto errs.ErrInvalid at the
// HTTP boundary.
var (
	ErrValueTypeUnknown  = errors.New("cmdb: unknown attribute value type")
	ErrValueOutOfRange   = errors.New("cmdb: attribute value out of range")
	ErrValueUnparsable   = errors.New("cmdb: attribute value cannot be parsed")
	ErrValueNotStorable  = errors.New("cmdb: value type has no unindexed table")
	ErrValueTypeMismatch = errors.New("cmdb: attribute value has the wrong type")
)

// Valid reports whether v is one of the eight storage types.
func (v ValueType) Valid() bool {
	switch v {
	case TypeInt, TypeFloat, TypeText, TypeDateTime, TypeDate, TypeTime, TypeJSON, TypeBool:
		return true
	default:
		return false
	}
}

// StorageModifiers are the c_attributes flags that override the index
// decision. It mirrors the first branch of cantor's TableMap.table: a
// password, link or file attribute is ALWAYS stored unindexed, regardless of
// its declared is_index.
type StorageModifiers struct {
	IsPassword bool
	IsLink     bool
	IsFile     bool
}

// forcesUnindexed reports whether any modifier pins the value to the
// unindexed table.
func (m StorageModifiers) forcesUnindexed() bool {
	return m.IsPassword || m.IsLink || m.IsFile
}

// EffectiveIndex reproduces TableMap.table's forced derivation of is_index.
//
// declared is the attribute's own is_index flag (c_attributes.is_index).
// The rule, in cantor's order:
//
//  1. password / link / file  -> false, unconditionally;
//  2. any type other than TEXT or JSON -> true, unconditionally. This is why
//     INT, FLOAT, DATE, TIME, DATETIME and BOOL can never land in an
//     unindexed table, and therefore why c_value_integers / c_value_floats /
//     c_value_datetime are dead tables;
//  3. TEXT and JSON -> whatever the attribute declares.
func (v ValueType) EffectiveIndex(declared bool, m StorageModifiers) bool {
	if m.forcesUnindexed() {
		return false
	}
	if v != TypeText && v != TypeJSON {
		return true
	}
	return declared
}

// StorageTable returns the physical table holding values of this type, given
// an ALREADY-EFFECTIVE index decision (use EffectiveIndex to compute it).
//
// It returns ErrValueNotStorable for the (unindexed, non-TEXT/JSON)
// combinations that cantor's mapping table cannot express. Those rows exist in
// an adopted cantor schema but are never written; reporting an error is
// deliberate, because silently choosing a table would put values where no
// reader looks.
func (v ValueType) StorageTable(indexed bool) (string, error) {
	switch v {
	case TypeInt, TypeBool:
		if !indexed {
			return "", fmt.Errorf("%w: %s unindexed", ErrValueNotStorable, v)
		}
		return TableIndexIntegers, nil
	case TypeFloat:
		if !indexed {
			return "", fmt.Errorf("%w: %s unindexed", ErrValueNotStorable, v)
		}
		return TableIndexFloats, nil
	case TypeDateTime, TypeDate:
		if !indexed {
			return "", fmt.Errorf("%w: %s unindexed", ErrValueNotStorable, v)
		}
		return TableIndexDateTime, nil
	case TypeTime:
		if !indexed {
			return "", fmt.Errorf("%w: %s unindexed", ErrValueNotStorable, v)
		}
		return TableIndexTexts, nil
	case TypeText:
		if indexed {
			return TableIndexTexts, nil
		}
		return TableTexts, nil
	case TypeJSON:
		// JSON always lands in c_value_json: cantor's table_name map has an
		// entry for both "6" and "index_6" pointing at the same table.
		return TableJSON, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrValueTypeUnknown, string(v))
	}
}

// StoredValue is the Go carrier written into a value table's `value` column.
// It is one of: int64 (INT/BOOL), float64 (FLOAT), string (TEXT/TIME),
// time.Time (DATE/DATETIME), or a decoded JSON value (JSON).
type StoredValue = any

// Validate converts an inbound value into the carrier for its storage column.
//
// It follows cantor's ValueTypeMap.deserialize semantics, including the
// quirks, because a CI that round-trips through cantor must produce the same
// stored bytes:
//
//   - INT parses via float first ("3.9" -> 3) and rejects values above the
//     signed 32-bit ceiling instead of truncating;
//   - DATE accepts both "2006-01-02" and "2006-01-02 15:04:05", keeping the
//     date part;
//   - DATETIME rewrites 'T' to a space and drops 'Z' before parsing;
//   - TIME extracts the first HH:MM:SS-shaped token and errors if there is
//     none (cantor indexes [0] and would raise IndexError);
//   - BOOL is a membership test against cantor's BOOL_TRUE set, so an
//     unrecognised value is false rather than an error.
func (v ValueType) Validate(raw any) (StoredValue, error) {
	if !v.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrValueTypeUnknown, string(v))
	}
	if raw == nil {
		return nil, nil
	}

	switch v {
	case TypeInt:
		return toInt(raw)
	case TypeBool:
		return toBoolCarrier(raw), nil
	case TypeFloat:
		return toFloat(raw)
	case TypeText:
		s, ok := raw.(string)
		if !ok {
			// cantor's serialize does str(x) for non-strings; keep that so a
			// number supplied for a text attribute still round-trips.
			return fmt.Sprint(raw), nil
		}
		return s, nil
	case TypeTime:
		s := fmt.Sprint(raw)
		m := timeRegexp.FindString(s)
		if m == "" {
			return nil, fmt.Errorf("%w: time %q", ErrValueUnparsable, s)
		}
		return m, nil
	case TypeDate:
		return toDate(raw)
	case TypeDateTime:
		return toDateTime(raw)
	case TypeJSON:
		return toJSON(raw)
	default:
		return nil, fmt.Errorf("%w: %q", ErrValueTypeUnknown, string(v))
	}
}

func toInt(raw any) (int32, error) {
	// cantor does int(float(x)), i.e. it accepts "3.9" and truncates toward
	// zero, and accepts a JSON number that arrived as a float.
	var f float64
	switch t := raw.(type) {
	case int:
		f = float64(t)
	case int8:
		f = float64(t)
	case int16:
		f = float64(t)
	case int32:
		f = float64(t)
	case int64:
		f = float64(t)
	case float64:
		f = t
	case json.Number:
		parsed, err := t.Float64()
		if err != nil {
			return 0, fmt.Errorf("%w: int %v", ErrValueUnparsable, raw)
		}
		f = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: int %q", ErrValueUnparsable, t)
		}
		f = parsed
	default:
		return 0, fmt.Errorf("%w: int from %T", ErrValueTypeMismatch, raw)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: int %v", ErrValueUnparsable, raw)
	}
	i := int64(f) // Go truncates toward zero, matching Python's int().
	// cantor guards only the upper bound (string2int checks `v > 2147483647`).
	// The lower bound is checked here too: the column is MySQL int(11), so an
	// underflow would be rejected by the driver with an opaque error instead.
	// Rejecting both ends up front is a strict improvement and cannot accept
	// anything cantor would have stored successfully on a strict-mode server.
	if i > maxInt || i < minInt {
		return 0, fmt.Errorf("%w: %d outside int32 range [%d, %d]", ErrValueOutOfRange, i, minInt, maxInt)
	}
	return int32(i), nil
}

func toFloat(raw any) (float64, error) {
	switch t := raw.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case json.Number:
		parsed, err := t.Float64()
		if err != nil {
			return 0, fmt.Errorf("%w: float %v", ErrValueUnparsable, raw)
		}
		return parsed, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: float %q", ErrValueUnparsable, t)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%w: float from %T", ErrValueTypeMismatch, raw)
	}
}

// boolTrueSet is cantor's BOOL_TRUE config
// (settings.py: ['true','TRUE','True',True,'1',1,'Yes','YES','yes','Y','y']),
// lower-cased into a lookup set. Go's `true` and the string "true" both land
// on "true"; Python's `1` and `"1"` both land on "1".
var boolTrueSet = map[string]bool{
	"true": true,
	"1":    true,
	"yes":  true,
	"y":    true,
}

// toBoolCarrier returns the 0/1 integer cantor writes into
// c_value_index_integers (its BOOL deserialize yields a Python bool, which
// the driver stores as 1/0 in an INT column).
func toBoolCarrier(raw any) int32 {
	switch t := raw.(type) {
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		if boolTrueSet[strings.ToLower(strings.TrimSpace(t))] {
			return 1
		}
		return 0
	case int:
		if t != 0 {
			return 1
		}
		return 0
	case int64:
		if t != 0 {
			return 1
		}
		return 0
	case float64:
		if t != 0 {
			return 1
		}
		return 0
	default:
		// cantor's `x in BOOL_TRUE` returns False for anything unrecognised;
		// it never raises. Preserve that rather than inventing an error.
		return 0
	}
}

// BoolFromCarrier reverses toBoolCarrier for reads.
func BoolFromCarrier(stored int32) bool { return stored != 0 }

func toDate(raw any) (time.Time, error) {
	if t, ok := raw.(time.Time); ok {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	s := strings.TrimSpace(fmt.Sprint(raw))
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, fmt.Errorf("%w: date %q", ErrValueUnparsable, s)
}

func toDateTime(raw any) (time.Time, error) {
	if t, ok := raw.(time.Time); ok {
		return t, nil
	}
	// cantor rewrites RFC3339-ish input into its own format first.
	s := strings.TrimSpace(fmt.Sprint(raw))
	s = strings.ReplaceAll(s, "T", " ")
	s = strings.ReplaceAll(s, "Z", "")
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%w: datetime %q", ErrValueUnparsable, s)
}

func toJSON(raw any) (any, error) {
	if s, ok := raw.(string); ok {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return nil, nil
		}
		var out any
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return nil, fmt.Errorf("%w: json %q", ErrValueUnparsable, s)
		}
		return out, nil
	}
	return raw, nil
}

// IsPasswordSafe reports whether a value of this type may be written into a
// denormalized document column. Password attributes must never be, and
// neither may link/file attributes, matching cantor's rule that those three
// always live in the unindexed text table.
//
// Callers must apply this to EVERY denormalisation, not only to c_cis.
func (m StorageModifiers) IsPasswordSafe() bool { return !m.forcesUnindexed() }
