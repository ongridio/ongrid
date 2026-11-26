package value

import (
	"time"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The six value tables below are the complete set cantor ever writes. The
// three legacy tables (c_value_integers, c_value_floats, c_value_datetime)
// are deliberately absent: cantor's own model marks them "Deprecated in a
// future version", and TableMap can never produce a key that selects them —
// see TestStorageTableRefusesDeadTableCombinations. GORM never drops tables,
// so on an adopted database they survive as inert orphans.
//
// INDEX NAMING. The four composite `*_attr_value_index` names are copied
// verbatim from cantor's __table_args__. They are distinctive enough to be
// unique across the whole database, so reusing them makes AutoMigrate over an
// existing cantor database a no-op instead of a duplicate-index generator —
// and these are the indexes that actually serve attribute filtering.
//
// The single-column lookup indexes are NOT cantor's names. cantor relied on
// MySQL, where index names are scoped per table, so `ci_id` and `type_id`
// appear on many tables. SQLite scopes index names per DATABASE, so a
// dual-dialect schema cannot reuse them: creating a second index named
// `ci_id` fails with "index ci_id already exists". They are therefore
// table-qualified as idx_<table>_<column> throughout.
//
// The cost is that adopting an existing cantor MySQL database gains a set of
// second indexes over columns cantor's foreign keys already indexed. That is
// a bounded, one-time duplication rather than a correctness problem, and the
// alternative — a schema that cannot be created on SQLite at all — is worse.

// ValueIndexInt stores INT and BOOL values that are filtered or sorted on.
// BOOL shares this table because cantor's table_name map sends index_0 and
// index_7 both here.
type ValueIndexInt struct {
	cmdbmodel.Base
	CIID   cmdbmodel.ID    `gorm:"column:ci_id;not null;index:idx_c_value_index_integers_ci_id"`
	AttrID cmdbmodel.ID    `gorm:"column:attr_id;not null;index:integer_attr_value_index,priority:1"`
	Value  cmdbmodel.Int32 `gorm:"column:value;not null;index:integer_attr_value_index,priority:2"`
}

func (ValueIndexInt) TableName() string { return TableIndexIntegers }

// ValueIndexFloat stores FLOAT values.
type ValueIndexFloat struct {
	cmdbmodel.Base
	CIID   cmdbmodel.ID `gorm:"column:ci_id;not null;index:idx_c_value_index_floats_ci_id"`
	AttrID cmdbmodel.ID `gorm:"column:attr_id;not null;index:float_attr_value_index,priority:1"`
	Value  float64      `gorm:"column:value;not null;index:float_attr_value_index,priority:2"`
}

func (ValueIndexFloat) TableName() string { return TableIndexFloats }

// ValueIndexText stores indexed TEXT and TIME values.
//
// cantor declares value as String(128) — NOT the 255 a naive port would pick.
// The width is load-bearing in two ways: MySQL cannot index an unbounded TEXT
// column, and cantor routes over-length text to the unindexed c_value_texts
// table. A port that widened this to 255 would silently accept 129-255
// character values into the indexed table on MySQL, and then fail to insert
// them on a cantor database whose column is still VARCHAR(128).
type ValueIndexText struct {
	cmdbmodel.Base
	CIID   cmdbmodel.ID `gorm:"column:ci_id;not null;index:idx_c_value_index_texts_ci_id"`
	AttrID cmdbmodel.ID `gorm:"column:attr_id;not null;index:text_attr_value_index,priority:1"`
	Value  string       `gorm:"column:value;size:128;not null;index:text_attr_value_index,priority:2"`
}

func (ValueIndexText) TableName() string { return TableIndexTexts }

// MaxIndexedTextLen is the storage width of the indexed text column.
//
// cantor does NOT route over-length values to c_value_texts — there is no such
// logic anywhere in its write path. On MySQL with the default strict mode an
// indexed TEXT attribute longer than this therefore fails with "Data too long
// for column" rather than silently truncating. Java-style silent truncation is
// worse than an error, and AGENTS.md forbids silently dropping data, so the
// port rejects the value with an explicit error instead. Callers that want a
// long searchable string must declare the attribute unindexed, which is a
// decision the model-config UI already exposes.
const MaxIndexedTextLen = 128

// ValueIndexDateTime stores DATETIME and DATE values. cantor's table_name map
// sends both index_3 (DATETIME) and index_4 (DATE) here.
type ValueIndexDateTime struct {
	cmdbmodel.Base
	CIID   cmdbmodel.ID `gorm:"column:ci_id;not null;index:idx_c_value_index_datetime_ci_id"`
	AttrID cmdbmodel.ID `gorm:"column:attr_id;not null;index:datetime_attr_value_index,priority:1"`
	Value  time.Time    `gorm:"column:value;not null;index:datetime_attr_value_index,priority:2"`
}

func (ValueIndexDateTime) TableName() string { return TableIndexDateTime }

// ValueText stores TEXT values that are neither indexed nor filtered on, and
// every password / link / file value (cantor forces those unindexed).
//
// Multiple rows per (ci_id, attr_id) represent a multi-valued (is_list)
// attribute. Cantor does NOT store multi-values as a JSON array, and that is
// what makes the query DSL's IN / RANGE / LIKE operators work uniformly: each
// element is an ordinary row in the same typed table.
type ValueText struct {
	cmdbmodel.Base
	CIID   cmdbmodel.ID `gorm:"column:ci_id;not null;index:idx_c_value_texts_ci_id"`
	AttrID cmdbmodel.ID `gorm:"column:attr_id;not null;index:idx_c_value_texts_attr_id"`
	Value  string       `gorm:"column:value;type:text;not null"`
}

func (ValueText) TableName() string { return TableTexts }

// ValueJSON stores JSON values. JSON always lands here whether or not the
// attribute declares is_index, because cantor's table_name map points both
// "6" and "index_6" at this table.
type ValueJSON struct {
	cmdbmodel.Base
	CIID   cmdbmodel.ID `gorm:"column:ci_id;not null;index:idx_c_value_json_ci_id"`
	AttrID cmdbmodel.ID `gorm:"column:attr_id;not null;index:idx_c_value_json_attr_id"`
	// serializer:json keeps the column typed as JSON on MySQL and TEXT on
	// SQLite without pulling in gorm.io/datatypes.
	Value map[string]any `gorm:"column:value;serializer:json;not null"`
}

func (ValueJSON) TableName() string { return TableJSON }

// --- choice tables ---
//
// A choice attribute's allowed values live in a table picked by the
// attribute's value type, mirroring ValueTypeMap.choice. Only three exist:
// INT, FLOAT, and a shared text table for TEXT/TIME/DATE/DATETIME.

// ChoiceInt backs INT-valued choice attributes.
type ChoiceInt struct {
	cmdbmodel.Base
	AttrID cmdbmodel.ID    `gorm:"column:attr_id;not null;index:idx_c_choice_integers_attr_id"`
	Value  cmdbmodel.Int32 `gorm:"column:value;not null"`
	Option map[string]any  `gorm:"column:option;serializer:json"`
}

func (ChoiceInt) TableName() string { return "c_choice_integers" }

// ChoiceFloat backs FLOAT-valued choice attributes.
type ChoiceFloat struct {
	cmdbmodel.Base
	AttrID cmdbmodel.ID   `gorm:"column:attr_id;not null;index:idx_c_choice_floats_attr_id"`
	Value  float64        `gorm:"column:value;not null"`
	Option map[string]any `gorm:"column:option;serializer:json"`
}

func (ChoiceFloat) TableName() string { return "c_choice_floats" }

// ChoiceText backs TEXT-, TIME-, DATE- and DATETIME-valued choice attributes,
// which cantor all route to this table.
type ChoiceText struct {
	cmdbmodel.Base
	AttrID cmdbmodel.ID   `gorm:"column:attr_id;not null;index:idx_c_choice_texts_attr_id"`
	Value  string         `gorm:"column:value;type:text;not null"`
	Option map[string]any `gorm:"column:option;serializer:json"`
}

func (ChoiceText) TableName() string { return "c_choice_texts" }

// ChoiceTable returns the choice table for a value type, mirroring
// ValueTypeMap.choice. It reports false for types that cannot be choices —
// JSON and BOOL are not in cantor's choice map, so a choice attribute using
// them is a data error rather than something to guess at.
func (v ValueType) ChoiceTable() (string, bool) {
	switch v {
	case TypeInt:
		return "c_choice_integers", true
	case TypeFloat:
		return "c_choice_floats", true
	case TypeText, TypeTime, TypeDate, TypeDateTime:
		return "c_choice_texts", true
	default:
		return "", false
	}
}
