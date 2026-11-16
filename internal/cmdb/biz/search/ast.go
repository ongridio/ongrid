package search

import (
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// This file is the shape of a parsed query: the metadata a term is resolved
// against, and the boolean formula a query reduces to.
//
// THE FORMULA IS PLAIN BOOLEAN LOGIC, and that is a deliberate departure from
// cantor's internal representation. cantor compiles a query into a chain of
// SQL set operations — INNER JOIN ... USING(ci_id) for AND, UNION ALL for OR,
// LEFT JOIN ... IS NULL for NOT — and folds the terms left to right. The set
// algebra it builds is correct, but the SHAPE leaks into the language in three
// ways that are not semantics at all:
//
//  1. The first term's prefix is discarded, because a left fold has nothing to
//     fold it with. `q=~hostname:x` alone therefore selects the instances that
//     DO have that hostname.
//  2. `~attr:*` (the console's "is empty" operator) means "has no value" only
//     when some other term precedes it. On its own, with no `_type` term in
//     the query, cantor takes a different branch and selects the instances
//     that DO have a value — the opposite.
//  3. `-~attr:(a;b)` builds `NOT LIKE a OR NOT LIKE b`, which is the negation
//     of `LIKE a AND LIKE b`, not of `LIKE a OR LIKE b`. For a single-valued
//     attribute that is true of almost every row, so the operator returns very
//     nearly everything.
//
// Each of those is a property of a fold over set operations, not of what the
// operator is named, and each is reachable from a raw `q=` parameter. The port
// therefore parses to a formula and compiles the formula, so that `~X` means
// "not X" wherever X appears. The places this changes an observable answer are
// recorded against the operators in parse.go and pinned by the golden tests in
// parse_test.go.
//
// A formula also compiles to ONE WHERE clause over c_cis rather than a nest of
// derived tables, which is what makes it portable to SQLite — cantor's SQL is
// MySQL-only (SQL_CALC_FOUND_ROWS, USING(ci_id), UNION ALL).

// Attr is one entry of the attribute dictionary as the query language sees it.
//
// It is a local type rather than the instance module's AttributeSpec because
// the instance module imports THIS one (it executes the plan this package
// produces); sharing the type would be a cycle. The duplication is confined to
// the fields a query needs, and the storage-table derivation is not duplicated
// — it calls the value package's own rule.
type Attr struct {
	ID    model.ID
	Name  string
	Alias string

	ValueType value.ValueType

	// IsIndex is the attribute's DECLARED flag; the three modifiers below
	// override it, exactly as they do on the write path.
	IsIndex    bool
	IsList     bool
	IsPassword bool
	IsLink     bool
	IsFile     bool
}

// Multivalued mirrors the write path's rule: a file attribute is single-valued
// even when is_list is set.
func (a Attr) Multivalued() bool { return a.IsList && !a.IsFile }

// Table resolves the physical value table this attribute's values live in,
// through the value package's own derivation so a query can never look in a
// different table from the one the write path chose.
func (a Attr) Table() (string, error) {
	indexed := a.ValueType.EffectiveIndex(a.IsIndex, value.StorageModifiers{
		IsPassword: a.IsPassword,
		IsLink:     a.IsLink,
		IsFile:     a.IsFile,
	})
	return a.ValueType.StorageTable(indexed)
}

// Matchable reports whether this attribute's values can be matched by a
// predicate at all.
//
// A PASSWORD is deliberately not matchable. cantor stores one in the unindexed
// text table and matches it happily, which turns the search endpoint into an
// oracle for guessing credentials: a caller who cannot read a value can still
// test a guess against it, one request at a time. Refusing the term is the
// only answer that does not leak, and it costs nothing — the console's filter
// builder never offers a password attribute as a filter, and no correct query
// needs one.
//
// A JSON attribute is not matchable either, for a duller reason: cantor routed
// it to c_value_json and then emitted a LIKE against a JSON column, which
// either fails or coerces to a string comparison nothing useful can satisfy.
func (a Attr) Matchable() bool {
	return !a.IsPassword && a.ValueType != value.TypeJSON
}

// Facetable reports whether this attribute's values may be returned as facet
// buckets.
//
// A facet returns the DISTINCT VALUES it grouped on, so it is a read of the
// attribute, not a filter on it — which is why it is a narrower permission
// than Matchable. It excludes everything cantor excluded from a CI response
// (Attr.Modifiers().IsPasswordSafe — password, link and file), because a facet
// over a password attribute would print the credential and a facet over a link
// or file attribute would print a handle the read path deliberately withholds.
func (a Attr) Facetable() bool {
	if _, err := a.Table(); err != nil {
		return false
	}
	if a.ValueType == value.TypeJSON {
		return false
	}
	return !a.IsPassword && !a.IsLink && !a.IsFile
}

// Type is one CI model, as a `_type` term names it.
type Type struct {
	ID    model.ID
	Name  string
	Alias string
}

// Node is a compiled query: a boolean predicate over CI instances.
type Node interface{ node() }

// And is the conjunction of two predicates.
type And struct{ Left, Right Node }

// Or is the disjunction of two predicates.
type Or struct{ Left, Right Node }

// Not is the negation of a predicate.
type Not struct{ Inner Node }

func (*And) node() {}
func (*Or) node()  {}
func (*Not) node() {}

// AttrTerm matches an attribute's values.
type AttrTerm struct {
	Attr  Attr
	Match Match
}

// TypeTerm matches the instance's model.
type TypeTerm struct{ Types []model.ID }

// IDTerm matches the instance id.
type IDTerm struct{ IDs []model.ID }

// AnyTerm is a bare word: cantor's "search every attribute" form, emitted by
// the console as `,*fuzzy*` alongside a `_type` term. It matches a value in
// any of the four INDEXED value tables — not in c_value_texts or
// c_value_json — which is why a password, a link, a file or an unindexed text
// value is invisible to the free-text search. That is cantor's behaviour and
// it is preserved: widening it would make the free-text search a credential
// oracle, for the same reason Attr.Searchable exists.
type AnyTerm struct{ Match Match }

func (*AttrTerm) node() {}
func (*TypeTerm) node() {}
func (*IDTerm) node()   {}
func (*AnyTerm) node()  {}

// MatchKind is how an attribute's values are compared.
type MatchKind uint8

const (
	// MatchValue is equality (and, when the typed value is text, a case-
	// insensitive comparison — see the compiler).
	MatchValue MatchKind = iota
	// MatchIn is membership in a set.
	MatchIn
	// MatchRange is a closed interval.
	MatchRange
	// MatchCompare is a half-open comparison against one operand.
	MatchCompare
	// MatchLike is a wildcard match. Patterns is one entry per alternative.
	MatchLike
	// MatchPrefix is a wildcard match whose ONLY wildcard is a trailing `*` —
	// `web*`. It is separate from MatchLike because it compiles to a range
	// rather than to LIKE, and the difference is not cosmetic: SQLite rewrites
	// `LIKE 'web%'` into a range only when the pattern is a LITERAL, so a bound
	// parameter leaves the index unused. Measured on 200k rows, the same prefix
	// search cost 0.283 ms as a range and 11.58 ms as a bound LIKE — a factor
	// of 40, on the operator the console's search box emits for every
	// keystroke. Operands holds the literal prefix, unescaped.
	MatchPrefix
	// MatchPresent is "the attribute has at least one value". There is no
	// corresponding "absent" case: absence is Not(MatchPresent), which is also
	// how `~attr:*` reaches the compiler. Two spellings of one predicate would
	// be two places to get the empty-string-versus-no-row distinction wrong —
	// they are different records, and `~attr:*` means the one with no row.
	MatchPresent
)

// Match is one attribute comparison, with its operands ALREADY COERCED to the
// column's own type.
type Match struct {
	Kind MatchKind

	// Values are the operands, in the value type's storage carrier: int32 for
	// INT and BOOL, float64 for FLOAT, time.Time for DATE and DATETIME, string
	// for TEXT and TIME. A JSON attribute is not matchable and never reaches
	// here.
	Values []any

	// Patterns are the LIKE alternatives, with the user's wildcards translated
	// and their literal %, _ and \ escaped. Only MatchLike uses it.
	Patterns []string

	// Prefix is the literal prefix for MatchPrefix, with no escaping: it is
	// compared as a range, where the LIKE metacharacters are ordinary text.
	Prefix string

	// Cmp is the comparison operator. Only MatchCompare uses it.
	Cmp Compare
}

// Compare is a comparison operator.
type Compare uint8

// The four comparisons the DSL accepts.
const (
	CmpGT Compare = iota
	CmpGE
	CmpLT
	CmpLE
)

// String renders the operator as SQL would spell it, for error messages and
// tests.
func (c Compare) String() string {
	switch c {
	case CmpGT:
		return ">"
	case CmpGE:
		return ">="
	case CmpLT:
		return "<"
	default:
		return "<="
	}
}

// Plan is a fully resolved query: what the store is asked to execute.
type Plan struct {
	// Root is the predicate, or nil for an unconstrained query.
	Root Node

	// Sort is the requested ordering, if any.
	Sort SortSpec

	// Facets are the attributes to aggregate the matching instances by.
	Facets []Attr
}

// SortSpec is the requested ordering.
//
// Field is exactly one of: the empty string (order by instance id), an
// attribute, or a built-in instance column. cantor spelled the last two as
// pseudo-attributes `_id`, `_type`, `ci_id`, `ci_type`; the port keeps those
// spellings because the console's sort dropdown is populated from them.
type SortSpec struct {
	// Field is the `sort` parameter's field, without its +/- prefix.
	Field string
	// Desc is true for a `-` prefix.
	Desc bool
	// Attr is set when Field names an attribute.
	Attr *Attr
	// Column is set when Field names a built-in instance column
	// ("id" or "type_id").
	Column string
}
