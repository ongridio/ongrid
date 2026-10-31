package search

import (
	"fmt"
	"strconv"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// Catalog is the metadata a query is resolved against: the attribute
// dictionary and the models, both keyed the three ways a caller can name them.
//
// It is built by the caller — the instance module, which owns the ports that
// reach the model tables — rather than by this package, so that this package
// stays free of storage and so that the dictionary is loaded ONCE for a
// request rather than once per term.
type Catalog struct {
	attrs map[string]Attr
	types map[string]Type
}

// NewCatalog indexes the dictionary. A name, an alias and a decimal id all
// address the same entry, in cantor's precedence: the NAME wins, so an
// attribute whose alias collides with another attribute's name is reachable by
// both but resolves unambiguously.
func NewCatalog(attrs []Attr, types []Type) *Catalog {
	c := &Catalog{
		attrs: make(map[string]Attr, len(attrs)*3),
		types: make(map[string]Type, len(types)*3),
	}
	for _, a := range attrs {
		if a.Name != "" {
			if _, seen := c.attrs[a.Name]; !seen {
				c.attrs[a.Name] = a
			}
		}
	}
	for _, a := range attrs {
		if a.Alias != "" {
			if _, seen := c.attrs[a.Alias]; !seen {
				c.attrs[a.Alias] = a
			}
		}
		if a.ID > 0 {
			id := strconv.FormatInt(int64(a.ID), 10)
			if _, seen := c.attrs[id]; !seen {
				c.attrs[id] = a
			}
		}
	}
	for _, t := range types {
		if t.Name != "" {
			c.types[t.Name] = t
		}
	}
	for _, t := range types {
		if t.Alias != "" {
			if _, seen := c.types[t.Alias]; !seen {
				c.types[t.Alias] = t
			}
		}
		if t.ID > 0 {
			if _, seen := c.types[strconv.FormatInt(int64(t.ID), 10)]; !seen {
				c.types[strconv.FormatInt(int64(t.ID), 10)] = t
			}
		}
	}
	return c
}

// Attr resolves an attribute reference.
func (c *Catalog) Attr(ref string) (Attr, bool) {
	if c == nil {
		return Attr{}, false
	}
	a, ok := c.attrs[ref]
	return a, ok
}

// Type resolves a model reference.
func (c *Catalog) Type(ref string) (Type, bool) {
	if c == nil {
		return Type{}, false
	}
	t, ok := c.types[ref]
	return t, ok
}

// Attrs returns the whole attribute dictionary, in the order it was built.
func (c *Catalog) Attrs() []Attr {
	if c == nil {
		return nil
	}
	out := make([]Attr, 0, len(c.attrs))
	seen := make(map[model.ID]bool, len(c.attrs))
	for _, a := range c.attrs {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a)
	}
	return out
}

// Refs is every name a request mentions, so the caller can load the dictionary
// in one round trip instead of resolving term by term.
type Refs struct {
	// Attributes are attribute references, deduplicated.
	Attributes []string
	// Types are model references, deduplicated.
	Types []string
}

// CollectRefs gathers the names a parsed query mentions, plus the sort field
// and facet fields, since those are dictionary lookups too.
//
// A built-in sort field is not collected: it names a column of the instance
// table, not an attribute, and asking the dictionary for it would be a
// pointless lookup that could also resolve to something if an attribute ever
// shared the name.
func CollectRefs(q *Query, sortField string, facets []string) Refs {
	seenA := map[string]bool{}
	seenT := map[string]bool{}
	var r Refs

	addAttr := func(ref string) {
		if ref != "" && !seenA[ref] {
			seenA[ref] = true
			r.Attributes = append(r.Attributes, ref)
		}
	}
	addType := func(ref string) {
		if ref != "" && !seenT[ref] {
			seenT[ref] = true
			r.Types = append(r.Types, ref)
		}
	}

	if q != nil {
		for _, t := range q.Terms {
			switch t.Kind {
			case KindAttr:
				addAttr(t.Key)
			case KindType:
				for _, ref := range t.Match.Operands {
					addType(ref)
				}
			}
		}
	}
	if field, _ := ParseSort(sortField); field != "" {
		if _, builtin := BuiltinSortColumn(field); !builtin {
			addAttr(field)
		}
	}
	for _, f := range facets {
		_, rest := splitOp(f)
		key, _ := splitKeyValue(rest)
		if key == "" {
			continue
		}
		if _, builtin := BuiltinSortColumn(key); !builtin {
			addAttr(key)
		}
	}
	return r
}

// Resolve turns a parsed query and a loaded dictionary into a plan.
//
// The fold is where the four prefix operators become boolean structure. cantor
// folds the same four onto set operations, and the difference is what happens
// to the FIRST term's prefix: a left fold has nothing to fold it against, so
// cantor drops it and `q=~hostname:x` alone selects the instances that DO
// match. Here the prefix is applied, because `~` means "not" and a caller who
// writes it is asking for the complement. See ast.go for every other place
// this port departs from cantor's set-operation shape and why.
func Resolve(q *Query, cat *Catalog, sortField string, facets []string) (*Plan, error) {
	root, err := fold(q, cat)
	if err != nil {
		return nil, err
	}
	sort, err := resolveSort(sortField, cat)
	if err != nil {
		return nil, err
	}
	facetAttrs, err := resolveFacets(facets, cat)
	if err != nil {
		return nil, err
	}
	return &Plan{Root: root, Sort: sort, Facets: facetAttrs}, nil
}

// fold builds the predicate tree, left to right.
func fold(q *Query, cat *Catalog) (Node, error) {
	if q == nil {
		return nil, nil
	}
	var acc Node
	for _, t := range q.Terms {
		n, keep, err := resolveTerm(t, cat)
		if err != nil {
			return nil, err
		}
		if !keep || n == nil {
			continue
		}
		if acc == nil {
			// The first term has nothing on its left, so only a negating
			// prefix has anything to say.
			switch t.Op {
			case OpAndNot, OpOrNot:
				acc = &Not{Inner: n}
			default:
				acc = n
			}
			continue
		}
		switch t.Op {
		case OpAnd:
			acc = &And{Left: acc, Right: n}
		case OpOr:
			acc = &Or{Left: acc, Right: n}
		case OpAndNot:
			acc = &And{Left: acc, Right: &Not{Inner: n}}
		case OpOrNot:
			acc = &Or{Left: acc, Right: &Not{Inner: n}}
		}
	}
	return acc, nil
}

// resolveTerm resolves one term's references and coerces its operands. keep is
// false for a term that contributes no predicate.
func resolveTerm(t RawTerm, cat *Catalog) (Node, bool, error) {
	switch t.Kind {
	case KindType:
		types := make([]model.ID, 0, len(t.Match.Operands))
		for _, ref := range t.Match.Operands {
			ty, ok := cat.Type(ref)
			if !ok {
				return nil, false, fmt.Errorf("%w: %q in %q", ErrUnknownType, ref, t.Text)
			}
			types = append(types, ty.ID)
		}
		if len(types) == 0 {
			return nil, false, nil
		}
		return &TypeTerm{Types: types}, true, nil

	case KindID:
		ids := make([]model.ID, 0, len(t.Match.Operands))
		for _, ref := range t.Match.Operands {
			n, err := strconv.ParseInt(ref, 10, 32)
			if err != nil || n <= 0 {
				return nil, false, fmt.Errorf("%w: %q in %q", ErrBadOperand, ref, t.Text)
			}
			ids = append(ids, model.ID(n))
		}
		if len(ids) == 0 {
			return nil, false, nil
		}
		return &IDTerm{IDs: ids}, true, nil

	case KindAny:
		return &AnyTerm{Match: resolveAnyMatch(t.Match)}, true, nil
	}

	attr, ok := cat.Attr(t.Key)
	if !ok {
		return nil, false, fmt.Errorf("%w: %q in %q", ErrUnknownAttribute, t.Key, t.Text)
	}
	if !attr.Matchable() {
		return nil, false, fmt.Errorf("%w: %q in %q", ErrUnsearchable, t.Key, t.Text)
	}
	m, err := resolveMatch(attr, t.Match)
	if err != nil {
		return nil, false, err
	}
	return &AttrTerm{Attr: attr, Match: m}, true, nil
}

// resolveMatch coerces every operand to the attribute's storage carrier.
//
// Coercion is not cosmetic. cantor interpolated the caller's text straight
// into SQL as a quoted literal against whatever column the attribute happened
// to use, so `cpu:8` compared an INT column with the string '8' and `created:2024-01-02`
// compared a DATETIME column with the string '2024-01-02 00:00:00'. MySQL
// coerces and SQLite coerces differently, and neither can use the index. Every
// operand becomes the column's own type here, so both dialects run the same
// comparison over the same index.
func resolveMatch(attr Attr, m RawMatch) (Match, error) {
	out := Match{Kind: m.Kind, Cmp: m.Cmp, Patterns: m.Patterns, Prefix: m.Prefix}
	if m.Kind == MatchPresent || m.Kind == MatchLike || m.Kind == MatchPrefix {
		return out, nil
	}
	out.Values = make([]any, 0, len(m.Operands))
	for _, raw := range m.Operands {
		v, err := coerceOperand(attr, raw)
		if err != nil {
			return Match{}, err
		}
		out.Values = append(out.Values, v)
	}
	return out, nil
}

// InValues builds an `attr IN (raw…)` match with the operands coerced to the
// column's own carrier type.
//
// IT IS EXPORTED FOR CALLERS THAT MUST NOT SPLICE VALUES INTO AN EXPRESSION.
// The language is the right way to say almost everything, but a caller holding
// a VALUE rather than a query — a discovery target's agent id, read off a
// request — cannot put it in a query string without letting the language's own
// punctuation change what the value means. This builds the predicate directly,
// through the same coercion the parser uses, so the two cannot disagree about
// what "the value 8" is for an INT attribute.
func InValues(attr Attr, raw []string) (Match, error) {
	values := make([]any, 0, len(raw))
	for _, item := range raw {
		coerced, err := coerceOperand(attr, item)
		if err != nil {
			return Match{}, err
		}
		values = append(values, coerced)
	}
	return Match{Kind: MatchIn, Values: values}, nil
}

// coerceOperand reads one operand as the attribute's value type.
func coerceOperand(attr Attr, raw string) (any, error) {
	s := raw
	// cantor's DATE rule, applied where it applies: a bare calendar date
	// becomes midnight. It did this for DATE but not for DATETIME, where it
	// instead left a ten-character string to be coerced by the server — which
	// MySQL resolves to midnight and SQLite resolves to nothing. Both types
	// get the extension here so the two dialects agree.
	if attr.ValueType == value.TypeDate || attr.ValueType == value.TypeDateTime {
		if len(s) == len("2006-01-02") {
			s += " 00:00:00"
		}
	}
	v, err := attr.ValueType.Validate(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not a valid %s value (%v)",
			ErrBadOperand, raw, attr.ValueType, err)
	}
	if v == nil {
		return nil, fmt.Errorf("%w: %q in %q", ErrBadOperand, raw, attr.Name)
	}
	// Text is stored as a string in the value table, and the JSON carrier is
	// never reachable here (Attr.Matchable refuses it), so the only non-string
	// carriers reaching the compiler are the typed columns' own.
	return v, nil
}

// resolveAnyMatch prepares a keyless term. There is no attribute to type the
// operands against, so they stay text: the free-text search compares them
// against every indexed value table, and a textual comparison is what that
// means on both dialects.
func resolveAnyMatch(m RawMatch) Match {
	out := Match{Kind: m.Kind, Patterns: m.Patterns, Prefix: m.Prefix}
	for _, raw := range m.Operands {
		out.Values = append(out.Values, raw)
	}
	return out
}

// resolveSort reads the `sort` parameter into a plan.
func resolveSort(field string, cat *Catalog) (SortSpec, error) {
	f, desc := ParseSort(field)
	spec := SortSpec{Field: f, Desc: desc}
	if f == "" {
		return spec, nil
	}
	if col, ok := BuiltinSortColumn(f); ok {
		spec.Column = col
		return spec, nil
	}
	attr, ok := cat.Attr(f)
	if !ok {
		return SortSpec{}, fmt.Errorf("%w: sort field %q", ErrUnknownAttribute, f)
	}
	if !attr.Matchable() {
		return SortSpec{}, fmt.Errorf("%w: sort field %q", ErrUnsearchable, f)
	}
	spec.Attr = &attr
	return spec, nil
}

// resolveFacets reads the `facet` parameter.
//
// A facet is a READ, not a filter: it returns the distinct values it grouped
// on. It therefore needs the narrower permission — a facet over a password
// attribute would print the credential that every other read path is careful
// to withhold. See Attr.Facetable.
func resolveFacets(refs []string, cat *Catalog) ([]Attr, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]Attr, 0, len(refs))
	for _, ref := range refs {
		// A facet may be written with the same operator prefixes a term takes
		// — cantor runs it through the same name resolver — so the prefix is
		// removed rather than looked up as part of the name.
		_, rest := splitOp(ref)
		key, _ := splitKeyValue(rest)
		if key == "" {
			continue
		}
		attr, ok := cat.Attr(key)
		if !ok {
			return nil, fmt.Errorf("%w: facet %q", ErrUnknownAttribute, key)
		}
		if !attr.Facetable() {
			return nil, fmt.Errorf("%w: facet %q", ErrUnsearchable, key)
		}
		out = append(out, attr)
	}
	return out, nil
}
