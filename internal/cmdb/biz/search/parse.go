package search

import (
	"fmt"
	"strconv"
	"strings"
)

// Op is how a term combines with everything to its left in the expression.
//
// The four values are cantor's four prefixes, one for one. cantor's names for
// them are its SQL spellings — "&", "|", "~", "|~" — and they are kept here
// because they appear in the console's saved expressions and in cantor's own
// documentation; what they MEAN is stated per value.
type Op uint8

const (
	// OpAnd is "&" (or a bare term, or a leading "+" that cantor accepts and
	// ignores): the term must hold.
	OpAnd Op = iota
	// OpOr is "-": the term may hold instead of what precedes it.
	OpOr
	// OpAndNot is "~": what precedes the term must hold AND the term must not.
	OpAndNot
	// OpOrNot is "-~": what precedes the term must hold OR the term must not.
	OpOrNot
)

// TermKind is what a term's key addresses.
type TermKind uint8

const (
	// KindAttr is an attribute term: `hostname:web-01`.
	KindAttr TermKind = iota
	// KindType is a model term: `_type:host`. Its key is one of cantor's three
	// spellings — `_type`, `type`, `ci_type` — because the console emits all
	// three: the filter builder writes `ci_type` for a built-in column and
	// buildSearchQ writes `_type`.
	KindType
	// KindID is an instance-id term: `_id:42`, also spelled `id` and `ci_id`.
	KindID
	// KindAny is a bare word term with no key at all: `*web-01*`. It is what
	// the console's search box emits next to a `_type` term.
	KindAny
)

// RawTerm is one parsed term whose references have not been looked up yet.
//
// The split between RawTerm and the resolved Node exists because the value's
// SHAPE is syntax and the value's TYPE is metadata: `cpu:>5` is a comparison
// whatever `cpu` turns out to be, while the operand `5` can only be coerced
// once `cpu`'s value type is known. Parse reads the shape; Resolve reads the
// dictionary.
type RawTerm struct {
	// Op is how this term combines with everything to its left.
	Op Op

	Kind TermKind

	// Key is the term's left-hand side with its prefix operator removed, still
	// as written — a name, an alias, or a decimal id. Empty for KindAny.
	Key string

	// Text is the whole term as written, kept for error messages so a report
	// can quote what the caller sent rather than a reconstruction of it.
	Text string

	Match RawMatch
}

// RawMatch is a term's right-hand side before the attribute's type is known.
type RawMatch struct {
	Kind MatchKind

	// Operands are the raw operand strings, one per comparison, two for a
	// range, and any number for a set.
	Operands []string

	// Patterns are the wildcard alternatives, already translated from the
	// DSL's `*` into SQL's `%` and with the caller's literal `%`, `_` and `\`
	// escaped.
	Patterns []string

	// Prefix is the literal prefix of a MatchPrefix pattern, as written.
	Prefix string

	// Cmp is the comparison for MatchCompare.
	Cmp Compare
}

// Query is a parsed expression. An empty Query has no terms, which means the
// expression constrained nothing.
type Query struct {
	Terms []RawTerm
}

// Empty reports whether the expression had no constraining terms.
func (q *Query) Empty() bool { return q == nil || len(q.Terms) == 0 }

// The keys cantor reserves for the built-in instance columns. A key in this
// set addresses the instance, never the attribute dictionary, so an attribute
// that happens to be named `type` or `id` can only be reached by its alias or
// by its numeric id. Reproduced deliberately: a query that meant the built-in
// column and quietly matched an attribute instead would be worse than the
// inconvenience.
var (
	keysForType = map[string]bool{"ci_type": true, "type": true, "_type": true}
	keysForID   = map[string]bool{"id": true, "ci_id": true, "_id": true}
)

// Parse reads an expression into terms. It does no lookups and touches no
// dictionary, so it can be tested against cantor's own output directly.
func Parse(expr string) (*Query, error) {
	q := &Query{}
	for _, tok := range Tokenize(expr) {
		t, keep, err := parseTerm(tok)
		if err != nil {
			return nil, err
		}
		if !keep {
			// A lone `*` is cantor's "match everything" term: it contributes no
			// predicate at all rather than one that is always true, which
			// matters because a leading always-true term would consume the
			// prefix of the term after it.
			continue
		}
		q.Terms = append(q.Terms, t)
	}
	return q, nil
}

// parseTerm reads one term. keep is false for the bare `*`.
func parseTerm(tok string) (RawTerm, bool, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return RawTerm{}, false, nil
	}

	op, rest := splitOp(tok)

	// A parenthesis is decided FIRST, before the token's contents are read.
	// cantor does the same, and the order is why a parenthesised list of
	// conditions is not read as one — see parseFreeText.
	if strings.HasPrefix(rest, "(") {
		if op != OpAnd {
			return RawTerm{}, false, fmt.Errorf(
				"%w: %q puts an operator before a parenthesised list. An operator belongs on a "+
					"condition, not on a list of values",
				ErrUnsupported, tok)
		}
		m, err := parseFreeText(tok)
		if err != nil {
			return RawTerm{}, false, err
		}
		return RawTerm{Kind: KindAny, Text: tok, Match: m}, true, nil
	}

	// A term carrying no key is free text. cantor guards its attribute branch
	// with `not q.startswith("*")`, so `*hostname:x` searches for the literal
	// text `hostname:x` rather than filtering on hostname. The prefix
	// characters are not operators here: a keyless term has no key for one to
	// attach to, so `-web-01` searches for the text `-web-01`.
	if !strings.Contains(tok, ":") || strings.HasPrefix(tok, "*") {
		if tok == "*" {
			return RawTerm{}, false, nil
		}
		m, err := parseFreeText(tok)
		if err != nil {
			return RawTerm{}, false, err
		}
		return RawTerm{Kind: KindAny, Text: tok, Match: m}, true, nil
	}

	key, value := splitKeyValue(rest)

	t := RawTerm{Op: op, Key: key, Text: tok}
	switch {
	case keysForType[key]:
		t.Kind = KindType
		m, err := parseTypeValue(value)
		if err != nil {
			return RawTerm{}, false, err
		}
		t.Match = m
	case keysForID[key]:
		t.Kind = KindID
		m, err := parseIDValue(value)
		if err != nil {
			return RawTerm{}, false, err
		}
		t.Match = m
	default:
		if key == "" {
			// `:x` — cantor looks up the empty name, finds nothing, and raises.
			return RawTerm{}, false, fmt.Errorf("%w: %q has no attribute name before the colon", ErrUnknownAttribute, tok)
		}
		t.Kind = KindAttr
		m, err := parseAttrValue(key, value)
		if err != nil {
			return RawTerm{}, false, err
		}
		t.Match = m
	}
	return t, true, nil
}

// unwrap removes the parentheses around a list, when they are there.
func unwrap(v string) string {
	if len(v) >= 2 && strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		return v[1 : len(v)-1]
	}
	return v
}

// splitOp removes a term's prefix operator and reports how it combines.
//
// The order of the tests is cantor's and it matters: `-~` is checked before
// `-`, so `-~x` is OpOrNot and not OpOr of the key `~x`. A `+` prefix means
// AND — cantor accepts it as an explicit spelling of the default.
func splitOp(tok string) (Op, string) {
	switch {
	case strings.HasPrefix(tok, "+"):
		return OpAnd, strings.TrimSpace(tok[1:])
	case strings.HasPrefix(tok, "-~"):
		return OpOrNot, strings.TrimSpace(tok[2:])
	case strings.HasPrefix(tok, "-"):
		return OpOr, strings.TrimSpace(tok[1:])
	case strings.HasPrefix(tok, "~"):
		return OpAndNot, strings.TrimSpace(tok[1:])
	default:
		return OpAnd, tok
	}
}

// splitKeyValue splits a term at its FIRST colon. A value may itself contain
// colons — `url:https://x` is one term whose value is `https://x` — which is
// why the split is not a general one.
func splitKeyValue(tok string) (string, string) {
	if i := strings.Index(tok, ":"); i >= 0 {
		return strings.TrimSpace(tok[:i]), strings.TrimSpace(tok[i+1:])
	}
	return strings.TrimSpace(tok), ""
}

// parseAttrValue reads the right-hand side of an attribute term.
//
// The dispatch is cantor's, in cantor's order, with one change: cantor coerces
// a BOOLEAN operand BEFORE it decides the shape, which turns `enabled:(true;false)`
// into the equality test `value = "0"`. Here the shape is read first and every
// operand is coerced afterwards, so a set of booleans stays a set.
func parseAttrValue(key, value string) (RawMatch, error) {
	switch {
	case strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")"):
		return RawMatch{Kind: MatchIn, Operands: splitList(value[1 : len(value)-1])}, nil

	case strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]"):
		inner := value[1 : len(value)-1]
		if !strings.Contains(inner, rangeSep) {
			// cantor requires `_TO_` to take the range branch; without it the
			// bracketed text falls through to an equality test against the
			// literal `[a]`. That is almost certainly a typo, and answering
			// "no instances" for it hides the typo, so it is refused.
			return RawMatch{}, fmt.Errorf("%w: %q is bracketed but has no %s separator",
				ErrSyntax, value, rangeSep)
		}
		parts := strings.Split(inner, rangeSep)
		if len(parts) != 2 {
			// cantor unpacks into exactly two variables and raises ValueError
			// on anything else, i.e. a 500.
			return RawMatch{}, fmt.Errorf("%w: %q has %d %s separators, want 1",
				ErrSyntax, value, len(parts)-1, rangeSep)
		}
		return RawMatch{
			Kind:     MatchRange,
			Operands: []string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])},
		}, nil

	case value == "":
		// `hostname:` — cantor emits `LIKE ""`, i.e. "the value is the empty
		// string". Kept as an equality against "" rather than treated as
		// absent: an attribute set to "" and one never set are different
		// records, and `~hostname:*` is how the second is asked for.
		return RawMatch{Kind: MatchValue, Operands: []string{""}}, nil

	case strings.HasPrefix(value, ">="):
		return RawMatch{Kind: MatchCompare, Cmp: CmpGE, Operands: []string{strings.TrimSpace(value[2:])}}, nil
	case strings.HasPrefix(value, "<="):
		return RawMatch{Kind: MatchCompare, Cmp: CmpLE, Operands: []string{strings.TrimSpace(value[2:])}}, nil
	case strings.HasPrefix(value, ">"):
		return RawMatch{Kind: MatchCompare, Cmp: CmpGT, Operands: []string{strings.TrimSpace(value[1:])}}, nil
	case strings.HasPrefix(value, "<"):
		return RawMatch{Kind: MatchCompare, Cmp: CmpLT, Operands: []string{strings.TrimSpace(value[1:])}}, nil

	case value == "*":
		// The console's "has a value" operator. Note what this is NOT: cantor
		// branches here on whether the QUERY contains a `_type` term, so the
		// same operator selects "has a value" under one query and "has no
		// value" under another. The predicate is fixed here, and `~` supplies
		// the other one.
		return RawMatch{Kind: MatchPresent}, nil

	case strings.Contains(value, "*"):
		if p, ok := purePrefix(value); ok {
			return RawMatch{Kind: MatchPrefix, Prefix: p}, nil
		}
		return RawMatch{Kind: MatchLike, Patterns: []string{likePattern(value)}}, nil

	default:
		return RawMatch{Kind: MatchValue, Operands: []string{value}}, nil
	}
}

// parseTypeValue reads a `_type` term: a model name, an alias, a decimal id,
// or any of those separated by `;`, optionally inside parentheses.
func parseTypeValue(value string) (RawMatch, error) {
	refs := splitList(unwrap(value))
	for _, r := range refs {
		if r == "" {
			return RawMatch{}, fmt.Errorf("%w: a _type term names no model", ErrSyntax)
		}
	}
	return RawMatch{Kind: MatchIn, Operands: refs}, nil
}

// parseIDValue reads an `_id` term.
//
// cantor emitted `WHERE c_cis.id in 42,43` for the unbracketed multi-id form,
// which is a syntax error on both dialects, so the only working spelling was
// `_id:(42;43)`. Both are accepted here and compile to a real IN list.
func parseIDValue(value string) (RawMatch, error) {
	refs := splitList(unwrap(value))
	for _, r := range refs {
		if _, err := strconv.ParseInt(r, 10, 32); err != nil {
			return RawMatch{}, fmt.Errorf("%w: _id operands must be decimal instance ids, got %q", ErrBadOperand, r)
		}
	}
	return RawMatch{Kind: MatchIn, Operands: refs}, nil
}

// parseFreeText reads a term with no key.
//
// Two shapes: a bare word or wildcard, and a bracketed list. A bracketed list
// that CONTAINS a colon is refused rather than read as a list of values,
// because cantor does not read it as one — it splits at the first colon and
// looks up the text before it as an attribute name, so
// `(hostname:x;cpu:y)` either raises "attribute hostname does not exist" (for
// `(http://x;http://y)`) or silently matches the literal value `x;cpu:y`
// (when the first key happens to be a real attribute). Neither is what the
// caller wrote, and a boolean group is not in the language.
func parseFreeText(tok string) (RawMatch, error) {
	if strings.HasPrefix(tok, "(") && strings.HasSuffix(tok, ")") {
		inner := tok[1 : len(tok)-1]
		if strings.Contains(inner, ":") {
			return RawMatch{}, fmt.Errorf(
				"%w: %q looks like a group of conditions. Grouping conditions is not part of the "+
					"query language; write the conditions separated by commas instead of semicolons",
				ErrUnsupported, tok)
		}
		return RawMatch{Kind: MatchIn, Operands: splitList(inner)}, nil
	}
	if strings.Contains(tok, "*") {
		if p, ok := purePrefix(tok); ok {
			return RawMatch{Kind: MatchPrefix, Prefix: p}, nil
		}
		return RawMatch{Kind: MatchLike, Patterns: []string{likePattern(tok)}}, nil
	}
	return RawMatch{Kind: MatchValue, Operands: []string{tok}}, nil
}

// rangeSep is cantor's range separator, spaces and all.
const rangeSep = "_TO_"

// splitList splits a `;`-separated operand list, mirroring Python's
// `"".split(";")`: an EMPTY input is one empty operand, not none. That is what
// makes `hostname:()` the query "the value is the empty string", which is what
// cantor answers there, and it is not the same question as `~hostname:*`.
//
// cantor splits with no trimming, so `a; b` yields the operand " b" and matches
// nothing. Values are trimmed here: a space after a separator is a typing
// habit, not a value, and silently matching nothing on it is the failure mode
// this whole package exists to avoid.
func splitList(s string) []string {
	parts := strings.Split(s, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// purePrefix reports a wildcard value whose only wildcard is a trailing `*`,
// and returns the literal text before it.
//
// `web*` is the one wildcard shape an index can serve, and it is the shape the
// console's search box sends on every keystroke. A value with any other
// wildcard — a leading one, or an interior one — can only be scanned, so it
// stays on the LIKE path.
func purePrefix(v string) (string, bool) {
	if !strings.HasSuffix(v, "*") {
		return "", false
	}
	prefix := v[:len(v)-1]
	if strings.Contains(prefix, "*") {
		return "", false
	}
	if prefix == "" {
		// Just "*", which is "has a value" and is handled before this.
		return "", false
	}
	return prefix, true
}

// LikePrefixPattern escapes a literal prefix and appends the trailing
// wildcard, for the one case where a prefix must be matched as a LIKE: a
// numeric or date column, where "starts with" can only mean the text form and
// no range expresses it. It is exported because the compiler decides that,
// once it knows which table the attribute lives in.
func LikePrefixPattern(prefix string) string { return likePattern(prefix + "*") }

// likePattern translates a DSL wildcard value into a SQL LIKE pattern.
//
// THE ORDER MATTERS, and cantor gets it only half right. The caller's literal
// `%`, `_` and `\` must be escaped BEFORE the `*` wildcards become `%`,
// otherwise a `%` the caller typed becomes indistinguishable from a wildcard
// they did not. cantor escapes neither `%` nor `_`, so `note:50%` matches any
// value starting with "50" — a wildcard amplification the caller never asked
// for. `\` is escaped too, because SQLite has no default LIKE escape character
// and MySQL's default is `\`, so leaving it alone would let a trailing
// backslash escape the closing quote in one dialect and not the other.
//
// cantor's other quirk here is skipped deliberately: its free-text branch also
// rewrites a literal backslash-n to `%`, which is a Python `'\n'`-versus-`'\\n'`
// slip rather than a feature.
func likePattern(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 8)
	for _, r := range v {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '*':
			b.WriteByte('%')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseSort reads the `sort` parameter.
//
// A leading `-` means descending and a leading `+` ascending, in cantor's
// spelling. The field itself is not resolved here — it may be an attribute or
// one of the built-in columns — so this returns the raw field and direction.
func ParseSort(raw string) (field string, desc bool) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "+"):
		return strings.TrimSpace(raw[1:]), false
	case strings.HasPrefix(raw, "-"):
		return strings.TrimSpace(raw[1:]), true
	default:
		return raw, false
	}
}

// builtinSortColumns maps cantor's spellings for the instance's own columns to
// the physical column names. Sorting by an attribute the model does not have
// is impossible, but sorting by when a record changed is always available, and
// cantor's `_updated_at` is what the console's default ordering asks for.
//
// The three spellings per column are cantor's: the filter builder writes
// `ci_type` / `ci_id` for the built-in columns it shows, while saved
// expressions and buildSearchQ write the underscore forms.
//
// Every name here is in cantor's BUILTIN_KEYWORDS, so no attribute can be
// created under one of them and there is nothing for these to shadow. That is
// why `_updated_at` is present and a plausible `_created_at` is not: the first
// is reserved by cantor and the second is not, so treating the second as
// built-in would hide an attribute someone could legitimately create.
var builtinSortColumns = map[string]string{
	"id": "id", "ci_id": "id", "_id": "id",
	"type": "type_id", "ci_type": "type_id", "_type": "type_id",
	"_updated_at": "updated_at", "_updated_by": "updated_by",
}

// BuiltinSortColumn reports the physical c_cis column a built-in sort field
// names, and whether the field is built-in at all. A field that is not
// built-in is looked up in the attribute dictionary instead.
func BuiltinSortColumn(field string) (string, bool) {
	col, ok := builtinSortColumns[field]
	return col, ok
}
