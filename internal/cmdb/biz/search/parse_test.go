package search

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// This file is the golden table for the query language, one row per operator.
//
// EVERY ROW WAS CHECKED AGAINST CANTOR'S OWN CODE, not against its
// documentation. The reference implementation was loaded and run —
// /tmp/cmdb-dsl/oracle.py stub-loads the real
// api/lib/cmdb/search/ci/db/search.py and prints the SQL each expression
// produces — and each row below carries a comment saying either that the port
// agrees with that output or how and why it departs from it. The plan records
// the raw oracle output.
//
// A row marked DEVIATION is a place where cantor's ANSWER is not what the
// operator is named, and the port answers the name. Those are the rows worth
// reading: they are the only places a query migrating from cantor can produce
// a different result.

// --- the fixture dictionary ---
//
// The same one the oracle was driven with, so the two can be compared row for
// row: one attribute per storage-table branch, plus the two reserved-name
// traps (a single-character name, which cantor's parenthesis guard breaks on,
// and a JSON attribute, which is not matchable at all).

const (
	atHostname model.ID = 1
	atNote     model.ID = 2
	atCPU      model.ID = 3
	atEnabled  model.ID = 4
	atCreated  model.ID = 5
	atPwd      model.ID = 6
	atTags     model.ID = 7
	atA        model.ID = 8
	atPayload  model.ID = 9
)

const (
	tyHost   model.ID = 1
	tySwitch model.ID = 2
)

func testCatalog() *Catalog {
	return NewCatalog([]Attr{
		{ID: atHostname, Name: "hostname", Alias: "主机名", ValueType: value.TypeText, IsIndex: true},
		{ID: atNote, Name: "note", Alias: "备注", ValueType: value.TypeText},
		{ID: atCPU, Name: "cpu", Alias: "核数", ValueType: value.TypeInt},
		{ID: atEnabled, Name: "enabled", Alias: "启用", ValueType: value.TypeBool},
		{ID: atCreated, Name: "created", Alias: "创建时间", ValueType: value.TypeDate},
		{ID: atPwd, Name: "pwd", Alias: "密码", ValueType: value.TypeText, IsIndex: true, IsPassword: true},
		{ID: atTags, Name: "tags", Alias: "标签", ValueType: value.TypeText, IsIndex: true, IsList: true},
		{ID: atA, Name: "a", Alias: "甲", ValueType: value.TypeText, IsIndex: true},
		{ID: atPayload, Name: "payload", Alias: "载荷", ValueType: value.TypeJSON, IsIndex: true},
	}, []Type{
		{ID: tyHost, Name: "host", Alias: "主机"},
		{ID: tySwitch, Name: "switch", Alias: "交换机"},
	})
}

// dump renders a plan as a compact canonical form, so a golden row reads as
// the predicate it is. It lives in the test file because nothing in production
// needs to print a plan.
func dump(t *testing.T, expr string) string {
	t.Helper()
	q, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", expr, err)
	}
	plan, err := Resolve(q, testCatalog(), "", nil)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", expr, err)
	}
	if plan.Root == nil {
		return "<no condition>"
	}
	return dumpNode(plan.Root)
}

func dumpNode(n Node) string {
	switch x := n.(type) {
	case *And:
		return "(and " + dumpNode(x.Left) + " " + dumpNode(x.Right) + ")"
	case *Or:
		return "(or " + dumpNode(x.Left) + " " + dumpNode(x.Right) + ")"
	case *Not:
		return "(not " + dumpNode(x.Inner) + ")"
	case *AttrTerm:
		return "(attr " + x.Attr.Name + " " + dumpMatch(x.Match) + ")"
	case *TypeTerm:
		ids := make([]string, 0, len(x.Types))
		for _, id := range x.Types {
			ids = append(ids, strconv.FormatInt(int64(id), 10))
		}
		return "(type " + strings.Join(ids, ";") + ")"
	case *IDTerm:
		ids := make([]string, 0, len(x.IDs))
		for _, id := range x.IDs {
			ids = append(ids, strconv.FormatInt(int64(id), 10))
		}
		return "(id " + strings.Join(ids, ";") + ")"
	case *AnyTerm:
		return "(any " + dumpMatch(x.Match) + ")"
	default:
		return fmt.Sprintf("?%T", n)
	}
}

func dumpMatch(m Match) string {
	switch m.Kind {
	case MatchPresent:
		return "present"
	case MatchLike:
		return "like " + strconv.Quote(strings.Join(m.Patterns, "|"))
	case MatchPrefix:
		return "prefix " + strconv.Quote(m.Prefix)
	case MatchRange:
		return "range " + fmtVal(m.Values[0]) + ".." + fmtVal(m.Values[1])
	case MatchCompare:
		return m.Cmp.String() + " " + fmtVal(m.Values[0])
	case MatchIn:
		parts := make([]string, 0, len(m.Values))
		for _, v := range m.Values {
			parts = append(parts, fmtVal(v))
		}
		return "in (" + strings.Join(parts, ",") + ")"
	default:
		return "= " + fmtVal(m.Values[0])
	}
}

func fmtVal(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case time.Time:
		return t.Format("2006-01-02T15:04:05")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func TestParseGolden(t *testing.T) {
	cases := []struct {
		expr string
		want string
		why  string
	}{
		// --- equality ---
		{"hostname:web-01", `(attr hostname = "web-01")`, ""},
		{"主机名:web-01", `(attr hostname = "web-01")`, "an alias addresses the same attribute"},
		{"1:web-01", `(attr hostname = "web-01")`, "so does the decimal id"},

		// --- the four prefixes, on the LEFT of an existing term ---
		{"hostname:a,note:b", `(and (attr hostname = "a") (attr note = "b"))`, "comma is AND"},
		{"hostname:a,-note:b", `(or (attr hostname = "a") (attr note = "b"))`, ""},
		{"hostname:a,~note:b", `(and (attr hostname = "a") (not (attr note = "b")))`, "~ is AND NOT"},
		{"hostname:a,-~note:b", `(or (attr hostname = "a") (not (attr note = "b")))`, "-~ is OR NOT"},
		{"hostname:a,+note:b", `(and (attr hostname = "a") (attr note = "b"))`, "+ is an explicit AND"},

		// --- the same prefixes on the FIRST term. cantor discards a leading
		// prefix because its left fold has nothing to fold it against, so
		// `~hostname:x` on its own selects the instances that DO match. A
		// caller who writes `~` is asking for the complement, so the prefix is
		// applied. This is the port's one deviation from cantor's
		// set-operation shape that changes a standalone query's answer.
		{"hostname:web-01", `(attr hostname = "web-01")`, ""},
		{"-hostname:web-01", `(attr hostname = "web-01")`, "a leading OR has nothing to combine with"},
		{"+hostname:web-01", `(attr hostname = "web-01")`, "a leading AND likewise"},
		{"~hostname:web-01", `(not (attr hostname = "web-01"))`, "DEVIATION: cantor answers the positive here"},
		{"-~hostname:web-01", `(not (attr hostname = "web-01"))`, "cantor agrees: it negates the comparison"},

		// --- IN ---
		{"hostname:(a;b)", `(attr hostname in ("a","b"))`, ""},
		{"hostname:(a; b)", `(attr hostname in ("a","b"))`, "operands are trimmed; cantor keeps the space and matches nothing"},
		{"hostname:()", `(attr hostname in (""))`, "cantor emits LIKE \"\", i.e. the empty string"},
		{"~hostname:(a;b)", `(not (attr hostname in ("a","b")))`, ""},

		// A single-character attribute name with an IN list. cantor's
		// parenthesis guard inspects the third byte of the token and raises
		// "not supported before parentheses" for ANY one-character name, so
		// `a:(1;2)` was unanswerable. The guard does not exist here.
		{"a:(1;2)", `(attr a in ("1","2"))`, "DEVIATION: cantor rejects every one-character IN"},

		// --- RANGE ---
		{"hostname:[a _TO_ z]", `(attr hostname range "a".."z")`, ""},
		{"hostname:[a _TO_ z]", `(attr hostname range "a".."z")`, ""},
		{"~hostname:[a _TO_ z]", `(not (attr hostname range "a".."z"))`, ""},

		// --- comparison ---
		{"cpu:>5", `(attr cpu > 5)`, "the operand is coerced to the column's own type"},
		{"cpu:>=5", `(attr cpu >= 5)`, ""},
		{"cpu:<5", `(attr cpu < 5)`, ""},
		{"cpu:<=5", `(attr cpu <= 5)`, ""},

		// cantor's comparison handler ignores the negation flag entirely, so
		// `-~hostname:>5` compiles to `> '5'` and returns what the POSITIVE
		// query returns. The port negates, as the operator says.
		{"-~cpu:>5", `(not (attr cpu > 5))`, "DEVIATION: cantor ignores the negation on a comparison"},

		// --- wildcards ---
		{"hostname:web*", `(attr hostname prefix "web")`, "a trailing-* pattern compiles to a range, because a bound LIKE cannot use the index"},
		{"hostname:*web", `(attr hostname like "%web")`, ""},
		{"hostname:*web*", `(attr hostname like "%web%")`, ""},
		{"hostname:a*b", `(attr hostname like "a%b")`, ""},
		{"-~hostname:*web*", `(not (attr hostname like "%web%"))`, ""},

		// A wildcard the caller did not type stays literal. cantor escapes
		// neither % nor _, so `note:50%` matches every value beginning "50" —
		// a wildcard amplification nobody asked for.
		{"note:50%", `(attr note = "50%")`, "DEVIATION: cantor's LIKE made the typed % a wildcard"},
		{"note:50%*", `(attr note prefix "50%")`, "as a range the typed % needs no escaping at all"},
		{"note:a_b", `(attr note = "a_b")`, "DEVIATION: cantor's LIKE made the typed _ a single-character wildcard"},
		{"note:a_b*", `(attr note prefix "a_b")`, ""},

		// --- "has a value" ---
		//
		// `attr:*` is the console's "has a value" operator and `~attr:*` is
		// its negation. cantor compiles the two depending on whether the query
		// happens to contain a `_type` term: with one, `~attr:*` is the
		// anti-join over `LIKE "%"` and means absent; without one it takes a
		// different branch and means PRESENT. A predicate that depends on what
		// else is in the query is not a predicate.
		{"hostname:*", `(attr hostname present)`, ""},
		{"~hostname:*", `(not (attr hostname present))`, "DEVIATION: cantor answers 'present' when no _type term is present"},
		{"_type:host,~hostname:*", `(and (type 1) (not (attr hostname present)))`, "cantor agrees once a _type term is there"},
		{"-~hostname:*", `(not (attr hostname present))`, "DEVIATION: without a _type cantor matches nothing at all"},

		// --- reserved keys ---
		{"_type:host", `(type 1)`, ""},
		{"ci_type:host", `(type 1)`, "cantor's other spelling for the same built-in"},
		{"_type:(host;switch)", `(type 1;2)`, ""},
		{"_type:主机", `(type 1)`, ""},
		{"_type:host,hostname:x", `(and (type 1) (attr hostname = "x"))`, ""},

		// The type term is an ordinary predicate here. cantor treats it as a
		// QUERY-LEVEL scope: it applies `type_id IN (...)` to the whole result
		// and silently drops the prefix operator, so `-_type:` narrows instead
		// of widening. A caller who writes `-` is asking to widen.
		{"_type:host,-hostname:x", `(or (type 1) (attr hostname = "x"))`, "DEVIATION: cantor ANDs the type and ignores the OR"},

		{"_id:42", `(id 42)`, ""},
		// cantor emitted `WHERE c_cis.id in 42,43`, a syntax error on both
		// dialects; only the bracketed `_id:(42;43)` spelling worked.
		{"_id:42;43", `(id 42;43)`, "DEVIATION: cantor produces invalid SQL for this spelling"},
		{"_id:(42;43)", `(id 42;43)`, ""},

		// --- values by type ---
		{"cpu:8", `(attr cpu = 8)`, "an int operand reaches the column as an int"},
		{"enabled:true", `(attr enabled = 1)`, "cantor's BOOL_TRUE set decides"},
		{"enabled:yes", `(attr enabled = 1)`, ""},
		{"enabled:nonsense", `(attr enabled = 0)`, "cantor's membership test yields false, never an error"},
		// cantor coerces a BOOL operand BEFORE reading its shape, so the whole
		// set collapses to the single value "0" and the query means
		// `enabled = false`.
		{"enabled:(true;false)", `(attr enabled in (1,0))`, "DEVIATION: cantor collapses a set of booleans to one value"},
		{"created:2024-01-02", `(attr created = 2024-01-02T00:00:00)`, "a bare date is midnight"},
		{"created:[2024-01-01 _TO_ 2024-12-31]", `(attr created range 2024-01-01T00:00:00..2024-12-31T00:00:00)`, ""},
		{"tags:(a;b)", `(attr tags in ("a","b"))`, "a multi-valued attribute's rows are matched individually"},

		// --- keyless terms ---
		{"web-01", `(any = "web-01")`, "cantor's free text is an exact match, not a substring"},
		{"*web-01*", `(any like "%web-01%")`, "the console's search box emits this"},
		{"(a;b)", `(any in ("a","b"))`, ""},
		{"-", `(any = "-")`, "a keyless term has no prefix: the - is part of the value"},
		{"*hostname:x", `(any like "%hostname:x")`, "cantor guards its attribute branch with `not startswith('*')`"},
		{"web-01,cpu:8", `(and (any = "web-01") (attr cpu = 8))`, ""},

		// --- escaping ---
		{`hostname:web\,01`, `(attr hostname = "web,01")`, "a trailing backslash hides a comma"},

		// --- nothing at all ---
		{"", `<no condition>`, ""},
		{"*", `<no condition>`, "a lone wildcard contributes no predicate"},

		// --- longer expressions, to pin the fold's shape ---
		{"hostname:web-01,cpu:>2,~note:x",
			`(and (and (attr hostname = "web-01") (attr cpu > 2)) (not (attr note = "x")))`, "left fold"},
		{"note:x,-hostname:a,-~cpu:>2",
			`(or (or (attr note = "x") (attr hostname = "a")) (not (attr cpu > 2)))`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got := dump(t, tc.expr)
			if got != tc.want {
				t.Fatalf("%q\n got %s\nwant %s\n%s", tc.expr, got, tc.want, tc.why)
			}
		})
	}
}

// Everything the language refuses, and why. Each of these is a caller error
// and must reach the HTTP layer as a 400 — cantor raised a Python exception
// for several of them (a 500), or worse, answered with a wrong set.
func TestParseRefusals(t *testing.T) {
	cases := []struct {
		expr string
		want error
		why  string
	}{
		{"nope:x", ErrUnknownAttribute, ""},
		{":x", ErrUnknownAttribute, "a term with no name"},
		{"_type:nope", ErrUnknownType, ""},
		{"_id:abc", ErrBadOperand, "cantor emitted `= abc`, a syntax error"},
		{"_id:", ErrBadOperand, "cantor emitted `= ` , a syntax error"},
		{"cpu:", ErrBadOperand, "the empty string is not an int"},
		{"cpu:>", ErrBadOperand, "a comparison with no operand"},
		{"cpu:abc", ErrBadOperand, ""},
		{"created:not-a-date", ErrBadOperand, ""},
		{"created:[a]", ErrSyntax, "bracketed with no _TO_; cantor silently matched the literal \"[a]\""},
		{"created:[a _TO_ b _TO_ c]", ErrSyntax, "cantor raised ValueError, i.e. a 500"},

		// A password attribute is not matchable. See Attr.Matchable.
		{"pwd:hunter2", ErrUnsearchable, "DEVIATION: cantor matches passwords, making search a guessing oracle"},
		{"payload:x", ErrUnsearchable, "a JSON attribute has no matchable representation"},

		// A parenthesised list of CONDITIONS is not in the language. cantor
		// splits at the first colon and looks the prefix up as an attribute
		// name, so `(http://x;http://y)` raises "attribute http does not
		// exist" and `(hostname:x;cpu:y)` silently matches the literal value
		// `x;cpu:y`. Refusing says so.
		{"(hostname:x;cpu:y)", ErrUnsupported, ""},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			// Either phase may be the one that refuses — a bad key is a
			// dispatch decision, a bad operand needs the type — and a caller
			// sees the same 400 either way, so the test accepts both.
			q, err := Parse(tc.expr)
			if err == nil {
				_, err = Resolve(q, testCatalog(), "", nil)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v %s", err, tc.want, tc.why)
			}
			// Every refusal is the caller's mistake, so the HTTP layer must
			// answer 400 and never 500.
			if got := errs.HTTPStatus(err); got != 400 {
				t.Fatalf("HTTPStatus(%v) = %d, want 400", err, got)
			}
		})
	}
}

func mustParse(t *testing.T, expr string) *Query {
	t.Helper()
	q, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", expr, err)
	}
	return q
}

// --- sort ---

func TestResolveSort(t *testing.T) {
	cat := testCatalog()
	cases := []struct {
		raw      string
		field    string
		desc     bool
		column   string
		attrName string
	}{
		{"", "", false, "", ""},
		{"-_updated_at", "_updated_at", true, "updated_at", ""},
		{"_id", "_id", false, "id", ""},
		{"ci_type", "ci_type", false, "type_id", ""},
		{"-cpu", "cpu", true, "", "cpu"},
		{"核数", "核数", false, "", "cpu"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			spec, err := resolveSort(tc.raw, cat)
			if err != nil {
				t.Fatalf("resolveSort(%q) error = %v", tc.raw, err)
			}
			if spec.Desc != tc.desc || spec.Column != tc.column {
				t.Fatalf("sort = %+v, want desc=%v column=%q", spec, tc.desc, tc.column)
			}
			switch {
			case tc.attrName == "" && spec.Attr != nil:
				t.Fatalf("attr = %+v, want none", spec.Attr)
			case tc.attrName != "" && (spec.Attr == nil || spec.Attr.Name != tc.attrName):
				t.Fatalf("attr = %+v, want %q", spec.Attr, tc.attrName)
			}
		})
	}

	if _, err := resolveSort("nope", cat); !errors.Is(err, ErrUnknownAttribute) {
		t.Fatalf("unknown sort field error = %v, want ErrUnknownAttribute", err)
	}
}

// --- facets ---

// A facet returns the distinct values it grouped on, so it is a read of the
// attribute. A password attribute may be neither matched nor faceted, and a
// link or file attribute may not be faceted either — cantor excluded those
// from every CI response, and a bucket list would print them.
func TestFacetsRefuseWhatAReadWouldWithhold(t *testing.T) {
	cat := testCatalog()

	ok, err := resolveFacets([]string{"hostname"}, cat)
	if err != nil || len(ok) != 1 || ok[0].ID != atHostname {
		t.Fatalf("resolveFacets(hostname) = %+v, %v", ok, err)
	}

	for _, ref := range []string{"pwd", "payload"} {
		if _, err := resolveFacets([]string{ref}, cat); !errors.Is(err, ErrUnsearchable) {
			t.Fatalf("resolveFacets(%q) error = %v, want ErrUnsearchable", ref, err)
		}
	}
	if _, err := resolveFacets([]string{"nope"}, cat); !errors.Is(err, ErrUnknownAttribute) {
		t.Fatalf("error = %v, want ErrUnknownAttribute", err)
	}
}

// --- refs ---

// CollectRefs decides what the dictionary load has to fetch. A built-in column
// must NOT be collected: it is not an attribute, and asking for it would be a
// wasted lookup that could also resolve if an attribute ever shared the name.
func TestCollectRefs(t *testing.T) {
	q := mustParse(t, "_type:host,hostname:x,-note:y,cpu:>2")
	r := CollectRefs(q, "-cpu", []string{"hostname"})
	want := []string{"hostname", "note", "cpu"}
	if strings.Join(r.Attributes, ",") != strings.Join(want, ",") {
		t.Fatalf("attributes = %v, want %v (deduplicated, in first-seen order)", r.Attributes, want)
	}
	if strings.Join(r.Types, ",") != "host" {
		t.Fatalf("types = %v, want [host]", r.Types)
	}

	// A built-in sort field is not an attribute reference.
	// A facet list may be written `facet=hostname,cpu`.
	r2 := CollectRefs(mustParse(t, ""), "_updated_at", []string{"hostname", "cpu"})
	if strings.Join(r2.Attributes, ",") != "hostname,cpu" {
		t.Fatalf("attributes = %v, want [hostname cpu]", r2.Attributes)
	}
}

// --- the catalog ---

func TestCatalogResolvesByAllThreeNames(t *testing.T) {
	cat := testCatalog()
	for _, ref := range []string{"hostname", "主机名", "1"} {
		a, ok := cat.Attr(ref)
		if !ok || a.ID != atHostname {
			t.Fatalf("Attr(%q) = %+v, %v", ref, a, ok)
		}
	}
	for _, ref := range []string{"host", "主机", "1"} {
		ty, ok := cat.Type(ref)
		if !ok || ty.ID != tyHost {
			t.Fatalf("Type(%q) = %+v, %v", ref, ty, ok)
		}
	}
	if _, ok := cat.Attr("nope"); ok {
		t.Fatal("an unknown reference resolved")
	}
}

// A name beats an alias: every attribute has a unique name, while an alias is
// free text, so an alias that collides with another attribute's name must not
// be able to steal the reference.
func TestCatalogPrefersTheName(t *testing.T) {
	cat := NewCatalog([]Attr{
		{ID: 1, Name: "cpu", ValueType: value.TypeInt},
		{ID: 2, Name: "cores", Alias: "cpu", ValueType: value.TypeInt},
	}, nil)
	a, ok := cat.Attr("cpu")
	if !ok || a.ID != 1 {
		t.Fatalf("Attr(cpu) = %+v, want the attribute NAMED cpu", a)
	}
}

// --- storage tables ---

// A term must look in the same table the write path chose, and for the two
// branches that are easy to get wrong the derivation is the value package's,
// not a second copy of the rule.
func TestAttrTableFollowsTheWritePath(t *testing.T) {
	cat := testCatalog()
	cases := map[string]string{
		"hostname": value.TableIndexTexts,    // indexed TEXT
		"note":     value.TableTexts,         // unindexed TEXT
		"cpu":      value.TableIndexIntegers, // INT is always indexed
		"enabled":  value.TableIndexIntegers, // BOOL shares the INT table
		"created":  value.TableIndexDateTime, // DATE and DATETIME share
		"pwd":      value.TableTexts,         // a password is FORCED unindexed
		"tags":     value.TableIndexTexts,
	}
	for ref, want := range cases {
		a, ok := cat.Attr(ref)
		if !ok {
			t.Fatalf("fixture is missing %q", ref)
		}
		got, err := a.Table()
		if err != nil {
			t.Fatalf("%s.Table() error = %v", ref, err)
		}
		if got != want {
			t.Fatalf("%s.Table() = %s, want %s", ref, got, want)
		}
	}
}
