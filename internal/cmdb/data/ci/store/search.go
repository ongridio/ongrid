package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// The store is the instance module's query port. Asserted here rather than
// left to the composition root, so a method drifting out of the interface is a
// build failure at the implementation.
var (
	_ cibiz.QueryRepo    = (*Repo)(nil)
	_ cibiz.RelationRepo = (*Repo)(nil)
)

// This file compiles a resolved query plan into SQL, and is the only place in
// CMDB that knows a dialect.
//
// THE SHAPE IS A JOIN OF ci_id SETS. Each node of the plan compiles to a SELECT
// producing one column, `ci_id`, with no duplicates; an AND joins two such
// sets, an OR unions them, and a NOT complements the set of all instances. The
// outer query then joins that result to c_cis to read the rows.
//
// WHY NOT A WHERE CLAUSE. The obvious spelling — one WHERE over c_cis with an
// EXISTS per attribute term — is correct and catastrophically slow, and the
// plan for it says so out loud. `EXPLAIN QUERY PLAN` shows
// `SCAN c_cis | SEARCH pv EXISTS USING INDEX text_attr_value_index`: the
// predicate must be evaluated for EVERY instance, so the value index is probed
// once per row of c_cis rather than once per query. Measured on 200k instances
// and 600k value rows, one selective hostname lookup:
//
//	WHERE EXISTS (correlated)      131.500 ms     SCAN c_cis
//	JOIN (SELECT ci_id FROM ...)     0.030 ms     SEARCH ... USING INDEX text_attr_value_index
//
// That is a factor of ~4000, and it is invisible to a correctness test: the
// answers are identical and only the plan differs. The set-join shape is also
// what cantor built — it derived the same way and folded the same operators
// onto INNER JOIN / UNION / LEFT JOIN — so this is a return to the design that
// was measured good, not a new invention. What is NOT borrowed is cantor's
// MySQL-only spelling: SQL_CALC_FOUND_ROWS and FOUND_ROWS() are replaced by a
// counting query, and USING(ci_id) by ON, which is the same plan on both
// dialects.
//
// THREE FURTHER RULES HERE ARE NOT STYLE, and each was measured first:
//
//  1. A VALUE IS COMPARED AS ITS COLUMN'S OWN TYPE. cantor interpolated the
//     caller's text as a quoted literal, so `cpu:8` compared an INT column with
//     the string '8'. MySQL coerces that and uses the index; SQLite coerces it
//     differently. The plan carries operands already converted, and this file
//     binds them, so both dialects run the same comparison over the same index
//     and use no string coercion at all.
//
//  2. ON SQLITE A TEXT COMPARISON PUTS `COLLATE NOCASE` ON THE COLUMN SIDE.
//     Not on the parameter, and not on the whole expression. Measured over 200k
//     rows: `value = ?` is a scan that returns NOTHING when the case differs,
//     while `value COLLATE NOCASE = ?` is an index seek that returns the row.
//     `value IN (?,?) COLLATE NOCASE` is likewise a scan returning nothing, and
//     `value COLLATE NOCASE IN (?,?)` a seek returning both. MySQL needs no
//     equivalent — its tables are utf8mb4_0900_ai_ci.
//
//  3. A TRAILING-WILDCARD PATTERN IS A RANGE, NOT A LIKE. SQLite rewrites
//     `LIKE 'web%'` into a range only for a LITERAL pattern; a bound parameter
//     leaves the index unused and the query falls back to scanning c_cis.
//     Measured: 0.283 ms as a range against 11.580 ms as a bound LIKE, on the
//     operator the console's search box emits for every keystroke. MySQL serves
//     both, so the range is used on both dialects rather than branching.
//
// An equality is emitted as `=`, never as `LIKE`; MySQL's collation makes it
// case-insensitive and rule 2 does the same on SQLite.

// valueAlias is the correlation name for a value table inside a compiled set.
const valueAlias = "pv"

// likeEscape is the LIKE escape character, bound rather than written into the
// SQL. A backslash is the sensible choice and also a quoting hazard: MySQL
// reads `'\'` as an escaped quote and needs `'\\'`, while SQLite accepts `'\'`
// and reads the doubled form as two characters. Binding it sidesteps the
// dialect difference entirely. parse.go escapes this same character in every
// pattern it builds.
const likeEscape = `\`

// ciSelect is a compiled set of instance ids: one column, `ci_id`, no
// duplicates.
type ciSelect struct {
	sql  string
	args []any
}

// compiler emits SQL for one dialect.
type compiler struct {
	sqlite bool
}

func newCompiler(db *gorm.DB) compiler {
	return compiler{sqlite: db.Dialector.Name() == "sqlite"}
}

// searchQueries is the SQL one search runs: the count and the page.
//
// It is built separately from running it so a test can hand the exact
// statement to EXPLAIN QUERY PLAN. The plan, not the answer, is where this
// compiler's mistakes live — a predicate that scans returns the right rows and
// is three orders of magnitude slower — so the statement has to be inspectable
// without being re-derived by the test.
type searchQueries struct {
	countSQL string
	pageSQL  string
	args     []any
	pageArgs []any
}

func (r *Repo) searchQueries(plan search.Plan, limit, offset int) (searchQueries, error) {
	c := newCompiler(r.db)
	set, err := c.source(plan.Root)
	if err != nil {
		return searchQueries{}, err
	}

	from := "c_cis"
	var args []any
	if set.sql != "" {
		from = "c_cis INNER JOIN (" + set.sql + ") AS m ON m.ci_id = c_cis.id"
		args = set.args
	}
	where := from + " WHERE c_cis.delete_marker = 0"

	order, err := c.order(plan.Sort)
	if err != nil {
		return searchQueries{}, err
	}

	q := searchQueries{
		countSQL: "SELECT COUNT(*) FROM " + where,
		pageSQL:  "SELECT c_cis.* FROM " + where + " ORDER BY " + order,
		args:     args,
		pageArgs: args,
	}
	if limit > 0 {
		q.pageSQL += " LIMIT ? OFFSET ?"
		q.pageArgs = append(append([]any{}, args...), limit, offset)
	}
	return q, nil
}

// QueryInstances returns a page of the instances matching a plan, in the plan's
// order, with the total match count before paging.
func (r *Repo) QueryInstances(ctx context.Context, plan search.Plan, limit, offset int) ([]cimodel.CI, int64, error) {
	q, err := r.searchQueries(plan, limit, offset)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.db.WithContext(ctx).Raw(q.countSQL, q.args...).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: search count: %w", err)
	}

	var out []cimodel.CI
	if err := r.db.WithContext(ctx).Raw(q.pageSQL, q.pageArgs...).Scan(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: search: %w", err)
	}
	return out, total, nil
}

// source compiles one node of the plan into a set of instance ids.
func (c compiler) source(n search.Node) (ciSelect, error) {
	switch x := n.(type) {
	case nil:
		return ciSelect{}, nil

	case *search.AttrTerm:
		return c.attrSource(x.Attr, x.Match)

	case *search.AnyTerm:
		return c.anySource(x.Match)

	case *search.TypeTerm:
		if len(x.Types) == 0 {
			return ciSelect{}, nil
		}
		return ciSelect{
			sql: "SELECT id AS ci_id FROM c_cis WHERE c_cis.delete_marker = 0 AND c_cis.type_id IN (" +
				placeholders(len(x.Types)) + ")",
			args: modelIDsToAny(x.Types),
		}, nil

	case *search.IDTerm:
		if len(x.IDs) == 0 {
			return ciSelect{}, nil
		}
		return ciSelect{
			sql: "SELECT id AS ci_id FROM c_cis WHERE c_cis.delete_marker = 0 AND c_cis.id IN (" +
				placeholders(len(x.IDs)) + ")",
			args: modelIDsToAny(x.IDs),
		}, nil

	case *search.And:
		return c.andSource(x)

	case *search.Or:
		return c.orSource(x)

	case *search.Not:
		inner, err := c.source(x.Inner)
		if err != nil || inner.sql == "" {
			return ciSelect{}, err
		}
		// The complement is taken over every instance, which is what "not" means
		// and what cantor's left-join anti-join produced. It is the only shape
		// here that must read all of c_cis, and it is reachable only from a bare
		// `~` or an OR-with-NOT — never from the AND-with-NOT that the console's
		// filter builder produces, which the anti-join in andSource handles.
		return ciSelect{
			sql: "SELECT id AS ci_id FROM c_cis WHERE c_cis.delete_marker = 0 AND c_cis.id NOT IN (" +
				inner.sql + ")",
			args: inner.args,
		}, nil

	default:
		return ciSelect{}, fmt.Errorf("%w: cannot compile %T", search.ErrUnsupported, n)
	}
}

// andSource joins an AND chain's operand sets.
//
// The chain is FLATTENED first, so `a AND b AND c` becomes one SELECT with two
// joins rather than three nested derived tables — the nesting is what stops an
// engine from flattening the whole thing into one plan.
//
// A negated operand becomes an anti-join rather than a complement: `a AND NOT b`
// is "a, minus b", so the query drives from a and probes b, and never reads the
// instances that neither side mentions.
func (c compiler) andSource(x *search.And) (ciSelect, error) {
	ops := flattenAnd(x)

	// The driver is the first operand that is not a negation. Driving from a
	// complement would mean scanning all of c_cis to build a set that is then
	// immediately intersected with something smaller.
	driver := -1
	for i, op := range ops {
		if _, negated := op.(*search.Not); !negated {
			driver = i
			break
		}
	}
	if driver < 0 {
		driver = 0
	}

	head, err := c.source(ops[driver])
	if err != nil {
		return ciSelect{}, err
	}
	if head.sql == "" {
		// One operand constrains nothing, so the chain is unconstrained.
		return ciSelect{}, nil
	}

	var b strings.Builder
	b.WriteString("SELECT d.ci_id FROM (")
	b.WriteString(head.sql)
	b.WriteString(") d")
	args := append([]any{}, head.args...)
	var negatives []string

	for i, op := range ops {
		if i == driver {
			continue
		}
		alias := "j" + strconv.Itoa(i)
		if not, ok := op.(*search.Not); ok {
			inner, err := c.source(not.Inner)
			if err != nil {
				return ciSelect{}, err
			}
			if inner.sql == "" {
				continue
			}
			b.WriteString(" LEFT JOIN (")
			b.WriteString(inner.sql)
			b.WriteString(") ")
			b.WriteString(alias)
			b.WriteString(" ON ")
			b.WriteString(alias)
			b.WriteString(".ci_id = d.ci_id")
			args = append(args, inner.args...)
			negatives = append(negatives, alias+".ci_id IS NULL")
			continue
		}
		set, err := c.source(op)
		if err != nil {
			return ciSelect{}, err
		}
		if set.sql == "" {
			continue
		}
		b.WriteString(" INNER JOIN (")
		b.WriteString(set.sql)
		b.WriteString(") ")
		b.WriteString(alias)
		b.WriteString(" ON ")
		b.WriteString(alias)
		b.WriteString(".ci_id = d.ci_id")
		args = append(args, set.args...)
	}
	if len(negatives) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(negatives, " AND "))
	}
	return ciSelect{sql: b.String(), args: args}, nil
}

// orSource unions an OR chain's operand sets.
//
// UNION rather than UNION ALL: several terms can match the same instance, and
// the set of instances an OR denotes contains each of them once. cantor used
// UNION ALL and relied on a DISTINCT in the paging wrapper to remove the
// duplicates, which meant paying for them twice.
func (c compiler) orSource(x *search.Or) (ciSelect, error) {
	ops := flattenOr(x)
	parts := make([]string, 0, len(ops))
	var args []any
	for _, op := range ops {
		set, err := c.source(op)
		if err != nil {
			return ciSelect{}, err
		}
		if set.sql == "" {
			// An operand that constrains nothing makes the disjunction true for
			// every instance, so it cannot simply be dropped.
			return ciSelect{sql: "SELECT id AS ci_id FROM c_cis WHERE c_cis.delete_marker = 0"}, nil
		}
		parts = append(parts, "SELECT ci_id FROM ("+set.sql+")")
		args = append(args, set.args...)
	}
	if len(parts) == 0 {
		return ciSelect{}, nil
	}
	return ciSelect{sql: strings.Join(parts, " UNION "), args: args}, nil
}

// attrSource compiles one attribute term into a set of instance ids.
func (c compiler) attrSource(a search.Attr, m search.Match) (ciSelect, error) {
	table, err := a.Table()
	if err != nil {
		return ciSelect{}, fmt.Errorf("%w: attribute %q: %v", search.ErrUnsearchable, a.Name, err)
	}
	cond, err := c.valueCondition(table, m)
	if err != nil {
		return ciSelect{}, err
	}

	var b strings.Builder
	// DISTINCT is what stops a multi-valued attribute from widening the result:
	// an instance whose tags are (a, b) has two rows matching `tags:(a;b)`, and
	// without it the join to c_cis would emit that instance twice. It is applied
	// to every attribute term rather than only to the ones the model marks
	// multi-valued, because a single-valued attribute with two rows is a data
	// defect this layer cannot rule out — cantor hid the consequences behind an
	// outer DISTINCT, and paying ~30% on a query that matches a hundred thousand
	// instances is cheaper than emitting a row twice.
	b.WriteString("SELECT DISTINCT ci_id FROM ")
	b.WriteString(table)
	b.WriteString(" ")
	b.WriteString(valueAlias)
	b.WriteString(" WHERE ")
	b.WriteString(valueAlias)
	b.WriteString(".delete_marker = 0 AND ")
	b.WriteString(valueAlias)
	b.WriteString(".attr_id = ?")
	args := []any{a.ID}
	if cond.sql != "" {
		b.WriteString(" AND ")
		b.WriteString(cond.sql)
		args = append(args, cond.args...)
	}
	return ciSelect{sql: b.String(), args: args}, nil
}

// anySource compiles a keyless term: the free-text search, which asks whether
// ANY attribute holds a value.
//
// It searches the four INDEXED value tables only. cantor's QUERY_CI_BY_NO_ATTR
// unions exactly those, so a password, a link, a file, and any attribute the
// model marked unindexed are invisible to it. That is preserved rather than
// widened: c_value_texts is where cantor puts every secret, and a free-text
// search that read it would be a credential oracle for the same reason
// Attr.Matchable refuses a password term.
func (c compiler) anySource(m search.Match) (ciSelect, error) {
	tables := []string{
		value.TableIndexTexts,
		value.TableIndexIntegers,
		value.TableIndexFloats,
		value.TableIndexDateTime,
	}
	parts := make([]string, 0, len(tables))
	var args []any
	for _, table := range tables {
		cond, err := c.valueCondition(table, m)
		if err != nil {
			return ciSelect{}, err
		}
		if cond.sql == "" {
			continue
		}
		parts = append(parts, "SELECT ci_id FROM "+table+" "+valueAlias+
			" WHERE "+valueAlias+".delete_marker = 0 AND "+cond.sql)
		args = append(args, cond.args...)
	}
	if len(parts) == 0 {
		return ciSelect{}, nil
	}
	return ciSelect{sql: strings.Join(parts, " UNION "), args: args}, nil
}

// valueCondition compiles a match against the `value` column of one table.
//
// An empty sql means "any row will do", which is what MatchPresent is.
func (c compiler) valueCondition(table string, m search.Match) (fragment, error) {
	col := valueAlias + ".value"
	text := isTextTable(table)
	if c.sqlite && text {
		col += " COLLATE NOCASE"
	}

	switch m.Kind {
	case search.MatchPresent:
		return fragment{}, nil

	case search.MatchValue:
		if len(m.Values) == 0 {
			return fragment{sql: "1 = 0"}, nil
		}
		return fragment{sql: col + " = ?", args: []any{m.Values[0]}}, nil

	case search.MatchIn:
		return inList(col, m.Values), nil

	case search.MatchRange:
		if len(m.Values) != 2 {
			return fragment{}, fmt.Errorf("%w: a range needs two bounds", search.ErrSyntax)
		}
		return fragment{sql: col + " BETWEEN ? AND ?", args: m.Values}, nil

	case search.MatchCompare:
		if len(m.Values) == 0 {
			return fragment{sql: "1 = 0"}, nil
		}
		return fragment{sql: col + " " + m.Cmp.String() + " ?", args: []any{m.Values[0]}}, nil

	case search.MatchPrefix:
		// A prefix is a range, but only for a TEXT column. On a number or a date
		// "starts with 5" has to mean the text form — cantor emitted
		// `LIKE "5%"` there and matched 50 — so the LIKE path is kept for those
		// columns and only the text ones get the range that the index can serve.
		if !text {
			return likeCondition(search.LikePrefixPattern(m.Prefix)), nil
		}
		upper, ok := prefixUpperBound(m.Prefix)
		if !ok {
			return fragment{sql: col + " >= ?", args: []any{m.Prefix}}, nil
		}
		return fragment{sql: col + " >= ? AND " + col + " < ?", args: []any{m.Prefix, upper}}, nil

	case search.MatchLike:
		if len(m.Patterns) == 0 {
			return fragment{sql: "1 = 0"}, nil
		}
		cond := fragment{}
		for i, p := range m.Patterns {
			if i > 0 {
				cond.sql += " OR "
			}
			cond.sql += valueAlias + ".value LIKE ? ESCAPE ?"
			cond.args = append(cond.args, p, likeEscape)
		}
		return cond, nil
	}
	return fragment{}, fmt.Errorf("%w: unknown match kind %d", search.ErrUnsupported, m.Kind)
}

// likeCondition builds a single-pattern LIKE clause.
func likeCondition(pattern string) fragment {
	return fragment{
		sql:  valueAlias + ".value LIKE ? ESCAPE ?",
		args: []any{pattern, likeEscape},
	}
}

// fragment is one compiled piece of SQL and the arguments it binds, in
// placeholder order.
type fragment struct {
	sql  string
	args []any
}

// prefixUpperBound returns the exclusive upper bound of a prefix range: the
// smallest string greater than every string that begins with the prefix. It is
// the prefix with its last byte incremented, carrying leftwards past any 0xFF.
//
// It reports false when there is no such string, which happens only when every
// byte is 0xFF — then `>= prefix` alone is the correct condition.
func prefixUpperBound(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}

// isTextTable reports whether a value table's `value` column holds text.
func isTextTable(table string) bool {
	return table == value.TableIndexTexts || table == value.TableTexts
}

// inList builds `col IN (?,?,...)`, expanding the placeholders here rather
// than relying on the driver to expand a bound slice: the fragment is
// assembled by hand, so its placeholders should be too.
func inList(col string, values []any) fragment {
	if len(values) == 0 {
		// An empty set matches nothing. Writing it out rather than returning an
		// empty fragment keeps `attr:( )` from collapsing into "no condition",
		// which would match everything — the opposite of what was asked.
		return fragment{sql: "1 = 0"}
	}
	return fragment{sql: col + " IN (" + placeholders(len(values)) + ")", args: values}
}

// placeholders renders n `?`s separated by commas.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// modelIDsToAny widens an id list for binding.
func modelIDsToAny(ids []model.ID) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

// flattenAnd collects an AND chain's operands left to right.
func flattenAnd(n search.Node) []search.Node {
	if a, ok := n.(*search.And); ok {
		return append(flattenAnd(a.Left), flattenAnd(a.Right)...)
	}
	return []search.Node{n}
}

// flattenOr collects an OR chain's operands left to right.
func flattenOr(n search.Node) []search.Node {
	if o, ok := n.(*search.Or); ok {
		return append(flattenOr(o.Left), flattenOr(o.Right)...)
	}
	return []search.Node{n}
}

// order compiles the ORDER BY clause.
//
// Sorting by an attribute is a correlated subquery, because the value lives in
// a table chosen by the attribute's definition and the outer query is over
// c_cis. Where an attribute is multi-valued the subquery picks one row
// arbitrarily; cantor had the same arbitrariness, since its DISTINCT
// projection collapsed the duplicates its join produced without saying which
// survived.
func (c compiler) order(s search.SortSpec) (string, error) {
	dir := "ASC"
	if s.Desc {
		dir = "DESC"
	}
	if s.Attr == nil {
		col := "c_cis.id"
		if s.Column != "" {
			// The column name comes from BuiltinSortColumn's fixed table, never
			// from the request, so there is nothing to inject.
			col = "c_cis." + s.Column
		}
		return col + " " + dir, nil
	}

	table, err := s.Attr.Table()
	if err != nil {
		return "", fmt.Errorf("%w: sort by %q: %v", search.ErrUnsearchable, s.Attr.Name, err)
	}
	col := valueAlias + ".value"
	if c.sqlite && isTextTable(table) {
		col += " COLLATE NOCASE"
	}
	// The attribute id is INLINED rather than bound. It is a model.ID — a
	// validated integer that came from the dictionary, not from the request —
	// and the ORDER BY clause is assembled separately from the arguments, so a
	// placeholder here would have no argument to consume and would reach the
	// server as a literal `?`.
	return "(SELECT " + col + " FROM " + table + " " + valueAlias +
		" WHERE " + valueAlias + ".ci_id = c_cis.id AND " + valueAlias +
		".delete_marker = 0 AND " + valueAlias + ".attr_id = " +
		strconv.FormatInt(int64(s.Attr.ID), 10) + " LIMIT 1) " + dir, nil
}

// QueryFacets aggregates the matching instances by an attribute's values.
//
// The matched set is joined in rather than fed back as a list of ids, so the
// whole facet is one query per attribute and the database does the filtering.
// cantor ran the id query, then formatted its SQL text into a second query's IN
// list, which re-planned both halves and built a string proportional to the
// match set.
func (r *Repo) QueryFacets(ctx context.Context, plan search.Plan, attrs []search.Attr, limit int) (map[model.ID][]cibiz.FacetBucket, error) {
	out := make(map[model.ID][]cibiz.FacetBucket, len(attrs))
	if len(attrs) == 0 {
		return out, nil
	}
	c := newCompiler(r.db)
	set, err := c.source(plan.Root)
	if err != nil {
		return nil, err
	}

	for _, a := range attrs {
		buckets, err := r.facetFor(ctx, c, a, set, limit)
		if err != nil {
			return nil, err
		}
		out[a.ID] = buckets
	}
	return out, nil
}

func (r *Repo) facetFor(ctx context.Context, c compiler, a search.Attr, set ciSelect, limit int) ([]cibiz.FacetBucket, error) {
	table, err := a.Table()
	if err != nil {
		return nil, fmt.Errorf("%w: facet %q: %v", search.ErrUnsearchable, a.Name, err)
	}
	col := valueAlias + ".value"
	if c.sqlite && isTextTable(table) {
		col += " COLLATE NOCASE"
	}

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(col)
	b.WriteString(" AS value, COUNT(DISTINCT ")
	b.WriteString(valueAlias)
	b.WriteString(".ci_id) AS n FROM ")
	b.WriteString(table)
	b.WriteString(" ")
	b.WriteString(valueAlias)
	// c_cis is joined for its delete marker: a compiled set built from a value
	// table says nothing about whether the instance it points at is live.
	b.WriteString(" INNER JOIN c_cis ON c_cis.id = ")
	b.WriteString(valueAlias)
	b.WriteString(".ci_id AND c_cis.delete_marker = 0")
	args := []any{}
	if set.sql != "" {
		b.WriteString(" INNER JOIN (")
		b.WriteString(set.sql)
		b.WriteString(") AS m ON m.ci_id = ")
		b.WriteString(valueAlias)
		b.WriteString(".ci_id")
		args = append(args, set.args...)
	}
	b.WriteString(" WHERE ")
	b.WriteString(valueAlias)
	b.WriteString(".delete_marker = 0 AND ")
	b.WriteString(valueAlias)
	b.WriteString(".attr_id = ?")
	args = append(args, a.ID)

	b.WriteString(" GROUP BY ")
	b.WriteString(col)
	// Descending count, then value, so a facet renders the same way twice.
	// cantor left the order to the GROUP BY.
	b.WriteString(" ORDER BY n DESC, value ASC")
	if limit > 0 {
		b.WriteString(" LIMIT ?")
		args = append(args, limit)
	}

	// The value is scanned into the type its table actually stores, so the
	// bucket arrives in the shape a caller should see rather than as whatever
	// the driver's default conversion produces — an INT facet is a number, not
	// the string "8", and a DATE facet is the same layout a CI's value reads
	// back as.
	sql := b.String()
	switch table {
	case value.TableIndexIntegers:
		var rows []facetRow[int32]
		if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("cmdb ci: facet %s: %w", a.Name, err)
		}
		return facetBuckets(rows, func(v int32) any {
			if a.ValueType == value.TypeBool {
				return value.BoolFromCarrier(v)
			}
			return v
		}), nil

	case value.TableIndexFloats:
		var rows []facetRow[float64]
		if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("cmdb ci: facet %s: %w", a.Name, err)
		}
		return facetBuckets(rows, func(v float64) any { return v }), nil

	case value.TableIndexDateTime:
		var rows []facetRow[string]
		if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("cmdb ci: facet %s: %w", a.Name, err)
		}
		return facetBuckets(rows, func(v string) any { return v }), nil

	default:
		var rows []facetRow[string]
		if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("cmdb ci: facet %s: %w", a.Name, err)
		}
		return facetBuckets(rows, func(v string) any { return v }), nil
	}
}

// facetRow is the shape every facet scan shares: the grouped value and how
// many distinct instances held it.
type facetRow[T any] struct {
	Value T     `gorm:"column:value"`
	N     int64 `gorm:"column:n"`
}

func facetBuckets[T any](rows []facetRow[T], conv func(T) any) []cibiz.FacetBucket {
	out := make([]cibiz.FacetBucket, 0, len(rows))
	for _, row := range rows {
		out = append(out, cibiz.FacetBucket{Value: conv(row.Value), Count: row.N})
	}
	return out
}
