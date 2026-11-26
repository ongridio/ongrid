package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// The query compiler's behavioural tests.
//
// THE DATASET AND THE EXPECTED ANSWERS COME FROM OUTSIDE THIS REPOSITORY. The
// rows below are the same ones the language's own reference implementation was
// run against (nexus-cantor's api/lib/cmdb/search/ci/db/search.py, driven
// through /tmp/cmdb-dsl/oracle.py over the 200k-row-shape fixture in
// /tmp/cmdb-dsl/dataset.tsv), so `cantor` on each golden row is a recorded
// answer rather than a restatement of what this code does. Where the port
// deliberately answers differently, the row says which rule it follows instead
// and the plan records the same divergence.
//
// The dataset is chosen to be adversarial rather than pretty: three spellings
// of one hostname differing only in case, values that share a prefix, a
// multi-valued attribute, instances with NO value for an attribute, an
// instance of a second model whose values would match a first-model query, a
// literal "-" and a literal "web,01".

const (
	saHostname model.ID = 1
	saNote     model.ID = 2
	saCPU      model.ID = 3
	saEnabled  model.ID = 4
	saCreated  model.ID = 5
	saPwd      model.ID = 6
	saTags     model.ID = 7
)

// searchAttrs is the fixture's attribute dictionary, written into c_attributes
// so the catalog queries read real rows.
var searchAttrs = []struct {
	ID    model.ID
	Name  string
	Alias string
	Type  value.ValueType
	Index bool
	List  bool
	Pwd   bool
}{
	{saHostname, "hostname", "主机名", value.TypeText, true, false, false},
	{saNote, "note", "备注", value.TypeText, false, false, false},
	{saCPU, "cpu", "核数", value.TypeInt, false, false, false},
	{saEnabled, "enabled", "启用", value.TypeBool, false, false, false},
	{saCreated, "created", "创建时间", value.TypeDate, false, false, false},
	{saPwd, "pwd", "密码", value.TypeText, true, false, true},
	{saTags, "tags", "标签", value.TypeText, true, true, false},
}

// The 34 instances. A value is the RAW text; the seed converts it through the
// production rule (value.ValueType.Validate), so the fixture cannot drift from
// the write path, and an absent key means the instance has NO value row for
// that attribute — which the language distinguishes from an empty value.
var searchDataset = []struct {
	ID     model.ID
	TypeID model.ID
	Values map[model.ID]any
}{
	{1, 1, map[model.ID]any{1: "web-01", 2: "hello", 3: "8", 4: "1", 5: "2024-01-02 00:00:00", 6: "hunter2", 7: []string{"a", "b", "prod"}}},
	{2, 1, map[model.ID]any{1: "Web-01", 2: "hello world", 3: "4", 4: "0", 5: "2024-01-02 12:30:00", 6: "secret", 7: []string{"a"}}},
	{3, 1, map[model.ID]any{1: "WEB-01", 2: "world", 3: "2", 4: "1", 5: "2023-12-31 00:00:00", 6: "hunter2x", 7: []string{"b"}}},
	{4, 1, map[model.ID]any{1: "web", 2: "x", 7: []string{"prod"}}},
	{5, 1, map[model.ID]any{1: "Web"}},
	{6, 1, map[model.ID]any{1: "WEB"}},
	{7, 1, map[model.ID]any{1: "web-02", 2: "hello there", 3: "16", 5: "2024-06-15 00:00:00", 7: []string{"prod", "prod-2"}}},
	{8, 1, map[model.ID]any{1: "web-10", 3: "8"}},
	{9, 1, map[model.ID]any{1: "web-010", 3: "80"}},
	{10, 1, map[model.ID]any{1: "web-01-prod"}},
	{11, 1, map[model.ID]any{1: "myweb"}},
	{12, 1, map[model.ID]any{1: "theweb"}},
	{13, 1, map[model.ID]any{1: "2server", 3: "2"}},
	{14, 1, map[model.ID]any{1: "9server"}},
	{15, 1, map[model.ID]any{1: "-", 7: []string{"-"}}},
	{16, 1, map[model.ID]any{1: "-abc"}},
	{17, 1, map[model.ID]any{1: "web,01"}},
	{18, 2, map[model.ID]any{1: "web-99", 2: "hello", 3: "8", 4: "1", 5: "2024-01-02 00:00:00", 7: []string{"a"}}},
	{19, 2, map[model.ID]any{1: "x", 2: "switch-note", 3: "8", 4: "0", 7: []string{"b"}}},
	{20, 1, map[model.ID]any{1: "alpha"}},
	{21, 1, map[model.ID]any{1: "beta", 2: "bravo"}},
	{22, 1, map[model.ID]any{1: "abc"}},
	{23, 1, map[model.ID]any{1: "db-01", 2: "database node", 3: "16", 4: "1", 5: "2024-03-01 00:00:00", 6: "pgpass", 7: []string{"db", "prod"}}},
	{24, 1, map[model.ID]any{1: "db-02", 2: "database node", 3: "16", 4: "0", 5: "2024-03-01 00:00:00", 7: []string{"db"}}},
	{25, 2, map[model.ID]any{1: "sw-core-01", 2: "core switch", 3: "4", 4: "1", 5: "2023-11-11 00:00:00", 7: []string{"core"}}},
	{26, 2, map[model.ID]any{1: "sw-core-02", 2: "core switch", 3: "4", 4: "1", 5: "2023-11-11 00:00:00", 7: []string{"core"}}},
	{27, 1, map[model.ID]any{1: "cache-01", 3: "2", 4: "1", 7: []string{"cache", "prod"}}},
	{28, 1, map[model.ID]any{1: "cache-02", 3: "2", 4: "0", 7: []string{"cache"}}},
	{29, 1, map[model.ID]any{1: "web-01-old"}},
	{30, 1, map[model.ID]any{1: "prod-web-01"}},
	{42, 1, map[model.ID]any{1: "host-42", 5: "2024-01-02 00:00:00"}},
	{43, 2, map[model.ID]any{1: "sw-43"}},
	{44, 1, map[model.ID]any{1: "host-44"}},
	{45, 1, map[model.ID]any{1: "host-45"}},
}

// newSearchRepo builds a repository over the fixture, migrated and seeded.
func newSearchRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate ci: %v", err)
	}
	if err := db.AutoMigrate(&citypemodel.Attribute{}, &citypemodel.CIType{}); err != nil {
		t.Fatalf("migrate dictionary: %v", err)
	}

	for _, a := range searchAttrs {
		row := citypemodel.Attribute{
			Name: a.Name, Alias: a.Alias, ValueType: string(a.Type),
			IsIndex: a.Index, IsList: a.List, IsPassword: a.Pwd,
		}
		row.ID = a.ID
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed attribute %s: %v", a.Name, err)
		}
	}
	for _, ct := range []citypemodel.CIType{
		{Name: "host", Alias: "主机", UniqueID: saHostname, Enabled: true},
		{Name: "switch", Alias: "交换机", UniqueID: saHostname, Enabled: true},
	} {
		if err := db.Create(&ct).Error; err != nil {
			t.Fatalf("seed type %s: %v", ct.Name, err)
		}
	}

	for _, ci := range searchDataset {
		row := cimodel.CI{TypeID: ci.TypeID}
		row.ID = ci.ID
		row.PropsJSON = "{}"
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed ci %d: %v", ci.ID, err)
		}
		for aid, raw := range ci.Values {
			writeSeedValue(t, db, ci.ID, aid, raw)
		}
	}
	return NewRepo(db), db
}

// writeSeedValue stores one fixture value through the SAME rule the write path
// uses: the value type validates the carrier, and the effective index plus the
// storage modifiers choose the table.
func writeSeedValue(t *testing.T, db *gorm.DB, ciID, attrID model.ID, raw any) {
	t.Helper()
	var spec struct {
		Type  value.ValueType
		Index bool
		Pwd   bool
	}
	for _, a := range searchAttrs {
		if a.ID == attrID {
			spec.Type, spec.Index, spec.Pwd = a.Type, a.Index, a.Pwd
		}
	}

	items := []any{raw}
	if list, ok := raw.([]string); ok {
		items = items[:0]
		for _, item := range list {
			items = append(items, item)
		}
	}

	for _, item := range items {
		stored, err := spec.Type.Validate(item)
		if err != nil {
			t.Fatalf("seed ci %d attr %d: %v", ciID, attrID, err)
		}
		indexed := spec.Type.EffectiveIndex(spec.Index, value.StorageModifiers{IsPassword: spec.Pwd})
		table, err := spec.Type.StorageTable(indexed)
		if err != nil {
			t.Fatalf("seed ci %d attr %d: %v", ciID, attrID, err)
		}
		if err := insertSeedRow(db, table, ciID, attrID, stored); err != nil {
			t.Fatalf("seed ci %d attr %d into %s: %v", ciID, attrID, table, err)
		}
	}
}

func insertSeedRow(db *gorm.DB, table string, ciID, attrID model.ID, stored any) error {
	switch table {
	case value.TableIndexIntegers:
		return db.Create(&value.ValueIndexInt{CIID: ciID, AttrID: attrID, Value: stored.(int32)}).Error
	case value.TableIndexFloats:
		return db.Create(&value.ValueIndexFloat{CIID: ciID, AttrID: attrID, Value: stored.(float64)}).Error
	case value.TableIndexTexts:
		return db.Create(&value.ValueIndexText{CIID: ciID, AttrID: attrID, Value: stored.(string)}).Error
	case value.TableIndexDateTime:
		return db.Create(&value.ValueIndexDateTime{CIID: ciID, AttrID: attrID, Value: stored.(time.Time)}).Error
	case value.TableTexts:
		return db.Create(&value.ValueText{CIID: ciID, AttrID: attrID, Value: stored.(string)}).Error
	default:
		return fmt.Errorf("unexpected table %q", table)
	}
}

// runExpr runs an expression through the whole language at the SQL boundary:
// parse, resolve against the fixture's own dictionary, compile, execute.
//
// It deliberately goes through search.Parse rather than assembling a plan by
// hand, so a golden row exercises the tokenizer, the fold and the compiler
// together — the three places a query can be read differently from how it was
// written.
func runExpr(t *testing.T, repo *Repo, expr string) []model.ID {
	t.Helper()
	ctx := context.Background()

	q, err := search.Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", expr, err)
	}
	refs := search.CollectRefs(q, "", nil)
	attrs, err := repo.QueryAttributes(ctx, refs.Attributes)
	if err != nil {
		t.Fatalf("QueryAttributes(%q) error = %v", expr, err)
	}
	types, err := repo.QueryTypes(ctx, refs.Types)
	if err != nil {
		t.Fatalf("QueryTypes(%q) error = %v", expr, err)
	}
	plan, err := search.Resolve(q, search.NewCatalog(attrs, types), "", nil)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", expr, err)
	}
	rows, _, err := repo.QueryInstances(ctx, *plan, 1000, 0)
	if err != nil {
		t.Fatalf("QueryInstances(%q) error = %v", expr, err)
	}
	ids := make([]model.ID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// allIDs is every seeded instance. An expression with no conditions matches all
// of them, which several golden rows rely on.
func allIDs() []model.ID {
	ids := make([]model.ID, 0, len(searchDataset))
	for _, ci := range searchDataset {
		ids = append(ids, ci.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// idSet parses a compact id list: "1,2,7" or "1-4,9". Long expectation lists
// are written as ranges so a reader can see the shape of the answer instead of
// counting commas.
func idSet(t *testing.T, spec string) []model.ID {
	t.Helper()
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	var out []model.ID
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		lo, hi, ranged := strings.Cut(part, "-")
		first, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			t.Fatalf("bad id spec %q: %v", spec, err)
		}
		if !ranged {
			out = append(out, model.ID(first))
			continue
		}
		last, err := strconv.Atoi(strings.TrimSpace(hi))
		if err != nil {
			t.Fatalf("bad id spec %q: %v", spec, err)
		}
		for i := first; i <= last; i++ {
			out = append(out, model.ID(i))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func formatIDs(ids []model.ID) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(int64(id), 10))
	}
	return strings.Join(parts, ",")
}

// The golden answers. Every row's `want` comes from an independent evaluator of
// the language's specification (see the file comment); `cantor` records what the
// reference implementation actually returned on the same data, and is present
// exactly where the two disagree.
//
// THE DEVIATIONS ARE THE POINT OF THE TABLE. Each one is a place where cantor's
// answer is not what the operator is named, and this port answers the name.
// They fall into four groups, all of them properties of cantor's set-operation
// fold rather than of the language:
//
//  1. A PREFIX ON THE FIRST TERM IS IGNORED, because a left fold has nothing to
//     fold it against. `~hostname:web-01` alone therefore selects the instances
//     that DO have that hostname. When the prefix is `-~` cantor negates the
//     comparison instead, so the two spellings of "not" disagree with each
//     other.
//  2. NEGATION IS SOMETIMES ROW-LEVEL. `-~attr:x` compiles to a union with
//     "rows whose value is not x", which is TRUE of any instance that has a
//     row at all — the negation of "x OR y", not of "x AND y". For a
//     multi-valued attribute it also means "has some other value", so
//     `-~tags:(a)` matches an instance tagged (a, b).
//  3. TEXT COMPARISONS FOLLOW THE DIALECT. cantor ran on MySQL, whose tables
//     are case-insensitive, so a range matched uppercase values. The port
//     produces MySQL's answer on both dialects by comparing with a
//     case-insensitive collation; on SQLite cantor's own answer differed.
//  4. TWO THINGS ARE REFUSED RATHER THAN ANSWERED: a term against a password
//     attribute, because matching it makes the search endpoint a credential
//     oracle, and a list of ids without parentheses, whose SQL cantor could not
//     parse at all.
func TestSearchGolden(t *testing.T) {
	repo, _ := newSearchRepo(t)

	cases := []struct {
		expr   string
		want   string
		cantor string
	}{
		{expr: "_type:host", want: "1-17,20-24,27-30,42,44-45"},
		{expr: "_type:(host;switch)", want: "1-30,42-45"},
		{expr: "_type:host,hostname:x", want: ""},

		{expr: "hostname:web-01", want: "1-3"},
		{expr: "主机名:web-01", want: "1-3"},
		{expr: "hostname:web-01,note:hello", want: "1"},
		{expr: "hostname:web-01,cpu:>2,~note:x", want: "1-2"},
		{expr: "hostname:", want: ""},

		{expr: "-hostname:web-01", want: "1-3"},
		{expr: "+hostname:web-01", want: "1-3"},
		{expr: "~hostname:web-01", want: "4-30,42-45", cantor: "1-3"},
		{expr: "-~hostname:web-01", want: "4-30,42-45"},

		{expr: "hostname:(a;b)", want: ""},
		{expr: "-hostname:(a;b)", want: ""},
		{expr: "~hostname:(a;b)", want: "1-30,42-45", cantor: ""},
		{expr: "-~hostname:(a;b)", want: "1-30,42-45"},

		{expr: "cpu:>5", want: "1,7-9,18-19,23-24"},
		{expr: "enabled:true", want: "1,3,18,23,25-27"},
		// The fixture's instance 2 holds a DATETIME for a DATE attribute, which
		// cantor's own write path cannot produce: str2date TRUNCATES to the date
		// (utils.py's deserialize table). cantor-on-the-fixture therefore
		// answered 1,18,42, while a database written by the write path holds
		// midnight for both instances 1 and 2 and both match. The port answers
		// what its own write path would have stored, which is also what cantor
		// answers on a database cantor wrote.
		{expr: "created:2024-01-02", want: "1-2,18,42", cantor: "1,18,42 (fixture artefact)"},
		{expr: "tags:(a;b)", want: "1-3,18-19"},

		{expr: "hostname:[a _TO_ z]", want: "1-12,17-30,42-45", cantor: "1,4,7-12,17-30,42-45"},
		{expr: "~hostname:[a _TO_ z]", want: "13-16", cantor: "1,4,7-12,17-30,42-45"},
		{expr: "-~hostname:[a _TO_ z]", want: "13-16", cantor: "2-3,5-6,13-16"},

		{expr: "hostname:>5", want: "1-12,14,17-30,42-45"},
		{expr: "~hostname:>5", want: "13,15-16", cantor: "1-12,14,17-30,42-45"},
		{expr: "-~hostname:>5", want: "13,15-16", cantor: "1-12,14,17-30,42-45"},
		{expr: "-~cpu:>2", want: "3-6,10-17,20-22,27-30,42-45", cantor: "1-2,7-9,18-19,23-26"},

		{expr: "hostname:web*", want: "1-10,17-18,29"},
		{expr: "hostname:*web", want: "4-6,11-12"},
		{expr: "hostname:*web*", want: "1-12,17-18,29-30"},
		{expr: "note:*", want: "1-4,7,18-19,21,23-26"},
		{expr: "~note:*", want: "5-6,8-17,20,22,27-30,42-45"},
		{expr: "~hostname:*", want: "", cantor: "1-30,42-45"},
		{expr: "-~hostname:*", want: ""},

		{expr: "_id:42", want: "42"},
		{expr: "_id:42;43", want: "42-43", cantor: "SQL syntax error"},
		{expr: "_id:(42;43)", want: "42-43"},

		{expr: "web-01", want: "1-3"},
		{expr: "*web-01*", want: "1-3,9-10,29-30"},
		{expr: "(a;b)", want: "1-3,18-19"},
		{expr: "-", want: "15"},
		{expr: `hostname:web\,01`, want: "17"},

		{expr: "hostname:a", want: ""},
		{expr: "hostname:a,-note:b", want: ""},
		{expr: "hostname:a,-~note:b", want: "1-30,42-45", cantor: "1-4,7,18-19,21,23-26"},

		{expr: "", want: "1-30,42-45"},
		{expr: "*", want: "1-30,42-45"},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got := runExpr(t, repo, tc.expr)
			want := idSet(t, tc.want)
			if formatIDs(got) != formatIDs(want) {
				t.Fatalf("%q\n got %s\nwant %s\ncantor on the same data: %s",
					tc.expr, formatIDs(got), formatIDs(want), tc.cantor)
			}
		})
	}
}

// A password attribute cannot be matched, and belonging to a model that does
// not exist is reported rather than silently matching nothing.
func TestSearchRefuses(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	for _, expr := range []string{"pwd:hunter2", "密码:hunter2", "a:(1;2)", "nope:x", "_type:nope"} {
		q := mustParseQuery(t, expr)
		refs := search.CollectRefs(q, "", nil)
		attrs, err := repo.QueryAttributes(ctx, refs.Attributes)
		if err != nil {
			t.Fatal(err)
		}
		types, err := repo.QueryTypes(ctx, refs.Types)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := search.Resolve(q, search.NewCatalog(attrs, types), "", nil); err == nil {
			t.Fatalf("%q resolved, want a refusal", expr)
		}
	}
}

func mustParseQuery(t *testing.T, expr string) *search.Query {
	t.Helper()
	q, err := search.Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", expr, err)
	}
	return q
}

// --- facets ---

// resolvePlan parses an expression and resolves it against the fixture,
// including the sort and facet fields.
func resolvePlan(t *testing.T, repo *Repo, expr, sort string, facets []string) search.Plan {
	t.Helper()
	ctx := context.Background()
	q := mustParseQuery(t, expr)
	refs := search.CollectRefs(q, sort, facets)
	attrs, err := repo.QueryAttributes(ctx, refs.Attributes)
	if err != nil {
		t.Fatal(err)
	}
	types, err := repo.QueryTypes(ctx, refs.Types)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := search.Resolve(q, search.NewCatalog(attrs, types), sort, facets)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", expr, err)
	}
	return *plan
}

// A facet is a READ of the attribute, so it returns the values the matching
// instances hold and how many hold each. Buckets come back ordered by
// descending count and then by value, so the same facet renders the same way
// twice — cantor left the order to the GROUP BY.
func TestQueryFacets(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	plan := resolvePlan(t, repo, "_type:host", "", []string{"tags"})
	got, err := repo.QueryFacets(ctx, plan, plan.Facets, cibiz.MaxFacetBuckets)
	if err != nil {
		t.Fatalf("QueryFacets() error = %v", err)
	}

	buckets := got[saTags]
	var rendered []string
	for _, b := range buckets {
		rendered = append(rendered, fmt.Sprintf("%v:%d", b.Value, b.Count))
	}
	// tags over type 1 only: prod is on 1,4,7,23,27; a on 1,2; b on 1,3;
	// cache on 27,28; db on 23,24; "-" on 15; prod-2 on 7. Instance 18 and 19
	// are type 2 and must not contribute.
	want := "prod:5,a:2,b:2,cache:2,db:2,-:1,prod-2:1"
	if strings.Join(rendered, ",") != want {
		t.Fatalf("tags facet = %s\nwant %s", strings.Join(rendered, ","), want)
	}
}

// A numeric facet returns numbers, not the strings the column holds, and a
// boolean facet returns booleans — the bucket is a value a caller can compare,
// not a transport detail.
func TestQueryFacetsTypedValues(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	plan := resolvePlan(t, repo, "_type:host", "", []string{"cpu", "enabled"})
	got, err := repo.QueryFacets(ctx, plan, plan.Facets, cibiz.MaxFacetBuckets)
	if err != nil {
		t.Fatal(err)
	}

	var cpus []string
	for _, b := range got[saCPU] {
		if _, ok := b.Value.(int32); !ok {
			t.Fatalf("cpu bucket value = %T, want int32", b.Value)
		}
		cpus = append(cpus, fmt.Sprintf("%v:%d", b.Value, b.Count))
	}
	// Over type 1: cpu 2 is on 3,13,27,28; 16 on 7,23,24; 8 on 1,8; 4 on 2;
	// 80 on 9. Instance 18, 19, 25 and 26 are type 2.
	if strings.Join(cpus, ",") != "2:4,16:3,8:2,4:1,80:1" {
		t.Fatalf("cpu facet = %s", strings.Join(cpus, ","))
	}

	var enabled []string
	for _, b := range got[saEnabled] {
		if _, ok := b.Value.(bool); !ok {
			t.Fatalf("enabled bucket value = %T, want bool", b.Value)
		}
		enabled = append(enabled, fmt.Sprintf("%v:%d", b.Value, b.Count))
	}
	// true on 1,3,23,27 and false on 2,24,28 — the type-1 instances that carry
	// the attribute at all. 18, 19, 25 and 26 are type 2.
	if strings.Join(enabled, ",") != "true:4,false:3" {
		t.Fatalf("enabled facet = %s", strings.Join(enabled, ","))
	}
}

// The facet limit is applied in SQL, so a high-cardinality attribute cannot
// make the request assemble an unbounded result.
func TestQueryFacetsRespectsTheLimit(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()
	plan := resolvePlan(t, repo, "", "", []string{"hostname"})

	got, err := repo.QueryFacets(ctx, plan, plan.Facets, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[saHostname]) != 3 {
		t.Fatalf("buckets = %d, want the limit of 3", len(got[saHostname]))
	}
}

// --- ordering ---

// Sort by an attribute orders by the value, not by the instance id, and a `-`
// prefix reverses it. The assertion is on the ORDER OF VALUES rather than on a
// list of ids, because instances that share a value have no defined order and
// asserting one would be asserting an accident of the query plan.
func TestSortByAttribute(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	values := func(expr, sort string, attr model.ID) []int32 {
		t.Helper()
		plan := resolvePlan(t, repo, expr, sort, nil)
		rows, _, err := repo.QueryInstances(ctx, plan, 1000, 0)
		if err != nil {
			t.Fatalf("QueryInstances(%q, %q) error = %v", expr, sort, err)
		}
		out := make([]int32, 0, len(rows))
		for _, row := range rows {
			got, err := repo.LoadValueRows(ctx, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range got {
				if v.AttrID == attr {
					out = append(out, v.Int)
				}
			}
		}
		return out
	}

	desc := values("_type:host,cpu:>=2", "-cpu", saCPU)
	if len(desc) == 0 {
		t.Fatal("no rows")
	}
	for i := 1; i < len(desc); i++ {
		if desc[i] > desc[i-1] {
			t.Fatalf("descending order violated at %d: %v", i, desc)
		}
	}
	if desc[0] != 80 {
		t.Fatalf("first value = %d, want the largest", desc[0])
	}

	asc := values("_type:host,cpu:>=2", "cpu", saCPU)
	if len(asc) != len(desc) {
		t.Fatalf("page sizes differ: %d and %d", len(asc), len(desc))
	}
	for i := 1; i < len(asc); i++ {
		if asc[i] < asc[i-1] {
			t.Fatalf("ascending order violated at %d: %v", i, asc)
		}
	}
}

// Sorting by an instance column works without the attribute dictionary, and
// the default order is by id — cantor's default, which the console's pager
// relies on for a stable window.
func TestSortByBuiltinColumn(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	// An unknown sort field is an error rather than a silently ignored
	// parameter: cantor dereferenced the missing attribute and crashed, and
	// answering "sorted by nothing" would look like a working query with a
	// useless order.
	q := mustParseQuery(t, "_type:host")
	if _, err := search.Resolve(q, search.NewCatalog(nil, nil), "cid", nil); err == nil {
		t.Fatal("an unknown sort field resolved, want an error")
	}

	plan := resolvePlan(t, repo, "_type:host", "_id", nil)
	rows, _, err := repo.QueryInstances(ctx, plan, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if formatIDs([]model.ID{rows[0].ID, rows[1].ID, rows[2].ID}) != "1,2,3" {
		t.Fatalf("ids = %d,%d,%d, want ascending from 1", rows[0].ID, rows[1].ID, rows[2].ID)
	}
}

// --- paging ---

func TestQueryInstancesPages(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()
	plan := resolvePlan(t, repo, "_type:host", "", nil)

	all, total, err := repo.QueryInstances(ctx, plan, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(all)) != total {
		t.Fatalf("%d rows but total %d", len(all), total)
	}
	if total != 29 {
		t.Fatalf("total = %d, want the 29 host instances", total)
	}

	first, _, err := repo.QueryInstances(ctx, plan, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := repo.QueryInstances(ctx, plan, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if formatIDs([]model.ID{first[0].ID}) != "1" || formatIDs([]model.ID{second[0].ID}) != "6" {
		t.Fatalf("pages start at %d and %d, want 1 and 6", first[0].ID, second[0].ID)
	}
}

// --- the dictionary ---

func TestQueryAttributesByAllThreeNames(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	got, err := repo.QueryAttributes(ctx, []string{"hostname", "主机名", "cpu", "pwd", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	// Four references resolve, two of them to the SAME attribute — the name
	// and the alias — so three rows come back. The unknown reference is absent
	// rather than an error, because only the caller knows whether it came from
	// a term, a sort field or a facet and can say which.
	if len(got) != 3 {
		t.Fatalf("got %d attributes, want 3", len(got))
	}
	byID := map[model.ID]search.Attr{}
	for _, a := range got {
		byID[a.ID] = a
	}
	if byID[saHostname].Name != "hostname" || byID[saCPU].Name != "cpu" {
		t.Fatalf("resolved %+v", got)
	}
	// The flags the compiler turns into a table choice survive the read: a
	// password forces the unindexed table and a list attribute is multi-valued.
	if id, err := byID[saPwd].Table(); err != nil || id != value.TableTexts {
		t.Fatalf("pwd table = %q, %v, want %s", id, err, value.TableTexts)
	}
	// The value type reaches the compiler too, so `cpu:>5` binds a number.
	if byID[saCPU].ValueType != value.TypeInt {
		t.Fatalf("cpu value type = %q", byID[saCPU].ValueType)
	}
}

func TestQueryTypesByAllThreeNames(t *testing.T) {
	repo, _ := newSearchRepo(t)
	ctx := context.Background()

	got, err := repo.QueryTypes(ctx, []string{"host", "交换机", "1", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d types, want 2", len(got))
	}
	names := map[string]model.ID{}
	for _, ty := range got {
		names[ty.Name] = ty.ID
	}
	if names["host"] != 1 || names["switch"] != 2 {
		t.Fatalf("resolved %+v", got)
	}
}
