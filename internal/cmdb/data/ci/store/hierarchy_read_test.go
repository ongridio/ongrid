package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// Ground truth for the three hierarchy reads — the descendant count, the nested
// tree, and the path enumeration — on the fixture a separate oracle ran through
// cantor's REAL code. Every `cantor:` field below is a value that came out of
// `Search.statistics`, `Search.search_full` and `Search.search_by_path` with the
// Redis child maps built by cantor's own `cmdb_init_cache`.
//
// The fixture, verbatim from the oracle:
//
//	types   A=1 B=2 C=3 D=4 | M1=5 M2=6 M3=7 | P=8 Q=9 X=10 R=11
//	rules   A→B  B→C  C→D | M1→M2 (many-to-many)  M2→M3 | P→Q  Q→R | P→X  X→R
//	attrs   uid_<T> per type, plus show_B=12 on B and show_P=13 on P
//	cis     1:A  2:A  3:B  4:B  5:B  6:C  7:C  8:D
//	        9:M1 10:M2 11:M3 12:M1 13:M2 14:M3
//	        15:P 16:P 17:Q 18:X 19:R
//	edges   1→3  1→4  3→6  4→7  4→6  6→8  2→5
//	        9→10  10→11(anc "9")  12→13  13→14(anc "12")
//	        15→17  17→19  15→18  18→19
//
// The diamond is 1→{3,4}→{6,6}: instance 6 is reachable from root 1 two ways,
// which is what makes the count's and the list's deduplication observable.

const (
	oracleTypeA  model.ID = 1
	oracleTypeB  model.ID = 2
	oracleTypeC  model.ID = 3
	oracleTypeD  model.ID = 4
	oracleTypeM1 model.ID = 5
	oracleTypeM2 model.ID = 6
	oracleTypeM3 model.ID = 7
	oracleTypeP  model.ID = 8
	oracleTypeQ  model.ID = 9
	oracleTypeX  model.ID = 10
	oracleTypeR  model.ID = 11
)

// oracleAttrIDs is the identifier attribute of each model, in the order the
// oracle numbered them.
var oracleAttrIDs = []model.ID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}

// oracleTypes describes each model: its alias, its unique attribute and its
// display attribute. The display attribute is what `show_id or unique_id`
// resolves to, which is exactly the rule the port implements.
type oracleType struct {
	name       string
	alias      string
	uniqueAttr model.ID
	showAttr   model.ID
}

var oracleTypesByID = map[model.ID]oracleType{
	1:  {"A", "A", 1, 0},
	2:  {"B", "B", 2, 12},
	3:  {"C", "C", 3, 0},
	4:  {"D", "D", 4, 0},
	5:  {"M1", "M1", 5, 0},
	6:  {"M2", "M2", 6, 0},
	7:  {"M3", "M3", 7, 0},
	8:  {"P", "P", 8, 13},
	9:  {"Q", "Q", 9, 0},
	10: {"X", "X", 10, 0},
	11: {"R", "R", 11, 0},
}

// oracleValues is every value row in the fixture.
var oracleValues = []struct {
	ciID   model.ID
	attrID model.ID
	text   string
}{
	{1, 1, "uniq-a1"}, {2, 1, "uniq-a2"},
	{3, 2, "uniq-b1"}, {4, 2, "uniq-b2"}, {5, 2, "uniq-b3"},
	{6, 3, "uniq-c1"}, {7, 3, "uniq-c2"}, {8, 4, "uniq-d1"},
	{9, 5, "uniq-m1a"}, {10, 6, "uniq-m2a"}, {11, 7, "uniq-m3a"},
	{12, 5, "uniq-m1b"}, {13, 6, "uniq-m2b"}, {14, 7, "uniq-m3b"},
	{15, 8, "uniq-p1"}, {16, 8, "uniq-p2"}, {17, 9, "uniq-q1"},
	{18, 10, "uniq-x1"}, {19, 11, "uniq-r1"},
	{3, 12, "SHOW-b1"}, {15, 13, "SHOW-p1"},
}

var oracleCIs = []struct {
	id     model.ID
	typeID model.ID
}{
	{1, 1}, {2, 1}, {3, 2}, {4, 2}, {5, 2}, {6, 3}, {7, 3}, {8, 4},
	{9, 5}, {10, 6}, {11, 7}, {12, 5}, {13, 6}, {14, 7},
	{15, 8}, {16, 8}, {17, 9}, {18, 10}, {19, 11},
}

// oracleResolver is the model side of the fixture.
type oracleResolver struct {
	graphs map[model.ID]cibiz.HierarchyLevels
	rules  map[[2]model.ID]cibiz.TypeRelationRule

	// passwordAttrs marks attributes the resolver reports as secrets, for the
	// test that a display attribute which is a secret is not rendered.
	passwordAttrs map[model.ID]bool
}

func (r *oracleResolver) ResolveForCI(_ context.Context, ref string) (*cibiz.AttributeSet, error) {
	id, err := strconv.ParseInt(ref, 10, 32)
	if err != nil {
		return nil, nil
	}
	spec, ok := oracleTypesByID[model.ID(id)]
	if !ok {
		return nil, nil
	}
	ct := citypemodel.CIType{
		Name: spec.name, Alias: spec.alias, UniqueID: spec.uniqueAttr, Enabled: true,
	}
	ct.ID = model.ID(id)
	if spec.showAttr != 0 {
		show := spec.showAttr
		ct.ShowID = &show
	}
	set := &cibiz.AttributeSet{
		Type:         ct,
		Specs:        map[model.ID]cibiz.AttributeSpec{},
		ByName:       map[string]model.ID{},
		ByAlias:      map[string]model.ID{},
		UniqueAttrID: spec.uniqueAttr,
	}
	for _, attrID := range []model.ID{spec.uniqueAttr, spec.showAttr} {
		if attrID == 0 {
			continue
		}
		s := cibiz.AttributeSpec{ID: attrID, Name: oracleAttrName(attrID), Alias: oracleAttrName(attrID), ValueType: value.TypeText}
		s.IsPassword = r.passwordAttrs[attrID]
		set.Specs[attrID] = s
		set.ByName[s.Name] = attrID
		set.ByAlias[s.Alias] = attrID
		if attrID == spec.uniqueAttr {
			set.UniqueAttr = s
		}
	}
	set.ShowAttr = set.UniqueAttr
	if spec.showAttr != 0 {
		set.ShowAttr = set.Specs[spec.showAttr]
	}
	return set, nil
}

func oracleAttrName(id model.ID) string {
	switch id {
	case 12:
		return "show_B"
	case 13:
		return "show_P"
	}
	return "uid_" + oracleTypeName(model.ID(id))
}

func oracleTypeName(id model.ID) string {
	if spec, ok := oracleTypesByID[id]; ok {
		return spec.name
	}
	return "?"
}

func (r *oracleResolver) RelationBetween(_ context.Context, parentTypeID, childTypeID model.ID) (cibiz.TypeRelationRule, bool, error) {
	rule, ok := r.rules[[2]model.ID{parentTypeID, childTypeID}]
	return rule, ok, nil
}

func (r *oracleResolver) HierarchyLevels(_ context.Context, rootTypeID model.ID, _ int, _ bool) (cibiz.HierarchyLevels, error) {
	return r.graphs[rootTypeID], nil
}

func oracleRule(parent, child model.ID, name string) (model.ID, cibiz.TypeRelationRule) {
	return 0, cibiz.TypeRelationRule{RelationTypeID: 20, Constraint: model.ConstraintOne2Many, RelationTypeName: name}
}

// newOracleHierarchy seeds the fixture and returns a use case over it.
func newOracleHierarchy(t *testing.T) (*cibiz.Usecase, *Repo, *gorm.DB) {
	return newOracleHierarchyWith(t, nil)
}

// newOracleHierarchyWith lets a test adjust the model side before it is used.
func newOracleHierarchyWith(t *testing.T, tweak func(*oracleResolver)) (*cibiz.Usecase, *Repo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The instance tables are this package's; the MODEL dictionary is not, and
	// a `_type:` expression resolves against it. The path search's queries are
	// the only reads here that name a model in the expression itself, so the
	// two dictionary tables are created and seeded locally rather than pulling
	// the model module's migrator into an instance-package test.
	if err := db.AutoMigrate(&citypemodel.CIType{}, &citypemodel.Attribute{}); err != nil {
		t.Fatalf("migrate dictionary: %v", err)
	}
	for id, spec := range oracleTypesByID {
		row := citypemodel.CIType{
			Name: spec.name, Alias: spec.alias, UniqueID: spec.uniqueAttr, Enabled: true,
		}
		row.ID = id
		if spec.showAttr != 0 {
			show := spec.showAttr
			row.ShowID = &show
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed type %d: %v", id, err)
		}
	}
	for _, attrID := range append(oracleAttrIDs, 12, 13) {
		name := oracleAttrName(attrID)
		row := citypemodel.Attribute{Name: name, Alias: name, ValueType: "2"}
		row.ID = attrID
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed attribute %d: %v", attrID, err)
		}
	}

	for _, ci := range oracleCIs {
		row := cimodel.CI{TypeID: ci.typeID, PropsJSON: "{}"}
		row.ID = ci.id
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed ci %d: %v", ci.id, err)
		}
	}
	for _, v := range oracleValues {
		row := value.ValueText{CIID: v.ciID, AttrID: v.attrID, Value: v.text}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed value: %v", err)
		}
	}
	for _, e := range oracleEdges {
		row := cimodel.Relation{FirstCIID: e.first, SecondCIID: e.second, RelationTypeID: 20}
		if e.ancestors != "" {
			anc := e.ancestors
			row.AncestorIDs = &anc
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}

	rules := map[[2]model.ID]cibiz.TypeRelationRule{}
	for _, pair := range [][2]model.ID{{1, 2}, {2, 3}, {3, 4}, {5, 6}, {6, 7}, {8, 9}, {9, 11}, {8, 10}, {10, 11}} {
		_, rule := oracleRule(pair[0], pair[1], "")
		if pair == [2]model.ID{5, 6} {
			rule.Constraint = model.ConstraintMany2Many
		}
		rules[pair] = rule
	}
	rules[[2]model.ID{8, 9}] = cibiz.TypeRelationRule{RelationTypeID: 21, Constraint: model.ConstraintOne2Many, RelationTypeName: "p_q"}
	rules[[2]model.ID{9, 11}] = cibiz.TypeRelationRule{RelationTypeID: 22, Constraint: model.ConstraintOne2Many, RelationTypeName: "q_r"}
	rules[[2]model.ID{8, 10}] = cibiz.TypeRelationRule{RelationTypeID: 23, Constraint: model.ConstraintOne2Many, RelationTypeName: "p_x"}
	rules[[2]model.ID{10, 11}] = cibiz.TypeRelationRule{RelationTypeID: 24, Constraint: model.ConstraintOne2Many, RelationTypeName: "x_r"}

	resolver := &oracleResolver{
		rules: rules,
		graphs: map[model.ID]cibiz.HierarchyLevels{
			oracleTypeA:  {TypesAtLevel: map[int][]model.ID{1: {2}, 2: {3}, 3: {4}}},
			oracleTypeM1: {TypesAtLevel: map[int][]model.ID{1: {6}, 2: {7}}, ManyToManyLevels: []int{1}},
			oracleTypeP:  {TypesAtLevel: map[int][]model.ID{1: {9, 10}, 2: {11}}},
		},
	}
	if tweak != nil {
		tweak(resolver)
	}
	repo := NewRepo(db)
	return cibiz.NewUsecase(repo, resolver, nil), repo, db
}

var oracleEdges = []edge{
	{1, 3, ""}, {1, 4, ""}, {3, 6, ""}, {4, 7, ""}, {4, 6, ""}, {6, 8, ""}, {2, 5, ""},
	{9, 10, ""}, {10, 11, "9"}, {12, 13, ""}, {13, 14, "12"},
	{15, 17, ""}, {17, 19, ""}, {15, 18, ""}, {18, 19, ""},
}

// --- statistics ---

func TestStatisticsMatchesCantor(t *testing.T) {
	cases := []struct {
		name    string
		roots   []model.ID
		level   int
		types   []model.ID
		want    map[string]int64
		detail  map[string]map[string]int64
		cantor  string
		because string
	}{
		{
			name: "level 1", roots: []model.ID{1}, level: 1,
			want: map[string]int64{"1": 2}, detail: map[string]map[string]int64{"1": {"2": 2}},
			cantor: "2",
		},
		{
			name: "level 2 counts the diamond once", roots: []model.ID{1}, level: 2,
			want: map[string]int64{"1": 2}, detail: map[string]map[string]int64{"1": {"3": 2}},
			cantor: "3 for 2 instances: instance 6 is below 1 by two routes and cantor " +
				"counted it per route",
		},
		{
			name: "level 3 counts the diamond once", roots: []model.ID{1}, level: 3,
			want: map[string]int64{"1": 1}, detail: map[string]map[string]int64{"1": {"4": 1}},
			cantor: "2 for the single instance below: same reason",
		},
		{
			name: "the deepest type filter", roots: []model.ID{1}, level: 3, types: []model.ID{4},
			want: map[string]int64{"1": 1}, detail: map[string]map[string]int64{"1": {"4": 1}},
			cantor: "2",
		},
		{
			name: "a middle type at the deepest level matches nothing", roots: []model.ID{1}, level: 3, types: []model.ID{3},
			want: map[string]int64{"1": 0}, detail: map[string]map[string]int64{"1": {}},
			cantor: "0 — the filter is applied at the level being counted, which is the " +
				"only level counted; type C sits at level 2, not 3",
		},
		{
			name: "that same type one level up", roots: []model.ID{1}, level: 2, types: []model.ID{3},
			want: map[string]int64{"1": 2}, detail: map[string]map[string]int64{"1": {"3": 2}},
			cantor: "3",
		},
		{
			name: "two roots are counted separately", roots: []model.ID{1, 2}, level: 3,
			want: map[string]int64{"1": 1, "2": 0}, detail: map[string]map[string]int64{"1": {"4": 1}, "2": {}},
			cantor: `{"1": 2, "2": 0} — per root, never merged`,
		},
		{
			name: "a broken level is zero, not an error", roots: []model.ID{2}, level: 3,
			want: map[string]int64{"2": 0}, detail: map[string]map[string]int64{"2": {}},
			cantor: "0",
		},
		{
			name: "the children of one node", roots: []model.ID{4}, level: 1,
			want: map[string]int64{"4": 2}, detail: map[string]map[string]int64{"4": {"3": 2}},
			cantor: "2",
		},
		{
			name: "many-to-many, one level down", roots: []model.ID{9}, level: 1,
			want: map[string]int64{"9": 1}, detail: map[string]map[string]int64{"9": {"6": 1}},
			cantor: "1",
		},
		{
			name: "many-to-many, two levels down is path-scoped", roots: []model.ID{9}, level: 2,
			want: map[string]int64{"9": 1}, detail: map[string]map[string]int64{"9": {"7": 1}},
			cantor: "1 — the second step is addressed by the path \"9,10\", which is what " +
				"the edge 10→11 was recorded under",
		},
		{
			name: "many-to-many, each root keeps its own branch", roots: []model.ID{9, 12}, level: 2,
			want: map[string]int64{"9": 1, "12": 1}, detail: map[string]map[string]int64{"9": {"7": 1}, "12": {"7": 1}},
			cantor: `{"9": 1, "12": 1}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, _, _ := newOracleHierarchy(t)
			got, err := uc.Statistics(context.Background(), cibiz.StatisticsRequest{
				RootIDs: tc.roots, Level: tc.level, TypeIDs: tc.types,
			})
			if err != nil {
				t.Fatalf("Statistics() error = %v", err)
			}
			if !equalCounts(got.Counts, tc.want) {
				t.Fatalf("counts = %v, want %v\n cantor %s", got.Counts, tc.want, tc.cantor)
			}
			if !equalDetail(got.Detail, tc.detail) {
				t.Fatalf("detail = %v, want %v\n cantor %s", got.Detail, tc.detail, tc.cantor)
			}
		})
	}
}

func equalCounts(got, want map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func equalDetail(got, want map[string]map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if len(got[k]) != len(v) {
			return false
		}
		for typeID, count := range v {
			if got[k][typeID] != count {
				return false
			}
		}
	}
	return true
}

// A hierarchy read carries the WALK's page bound, not the search page's.
//
// The console reads a service tree by asking for one node's children with
// `count=10000`, because a node's children are its contents rather than a page.
// Under the search page's ceiling of 500 a node with six hundred children
// rendered five hundred of them and said nothing about the rest.
func TestHierarchyChildrenAreNotCappedAtTheSearchPageSize(t *testing.T) {
	uc, _, db := newOracleHierarchy(t)

	// Six hundred children of instance 1, all of model B — the shape an IDC
	// full of hosts makes, and more than a search page may carry.
	const children = 600
	for i := 0; i < children; i++ {
		row := cimodel.CI{TypeID: oracleTypeB, PropsJSON: "{}"}
		row.ID = model.ID(1000 + i)
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed child: %v", err)
		}
		edge := cimodel.Relation{FirstCIID: 1, SecondCIID: row.ID, RelationTypeID: 20}
		if err := db.Create(&edge).Error; err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}

	got, err := uc.WalkHierarchy(context.Background(), cibiz.HierarchyRequest{
		RootIDs: []model.ID{1}, Levels: []int{1},
		Search: cibiz.SearchRequest{Count: 10000},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's own two children plus the six hundred.
	if len(got.Hits) != children+2 {
		t.Fatalf("returned %d children, want %d; a tree node's contents are not a page",
			len(got.Hits), children+2)
	}
	if got.NumFound != children+2 {
		t.Fatalf("numfound = %d, want %d", got.NumFound, children+2)
	}
}

// `ancestor_ids` scopes the count to one branch of a many-to-many hierarchy:
// the walk starts at level 1 already path-addressed, so only the edges recorded
// under that path are followed.
func TestStatisticsHonoursTheAncestorPath(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)

	got, err := uc.Statistics(context.Background(), cibiz.StatisticsRequest{
		RootIDs: []model.ID{10}, Level: 1, AncestorIDs: "9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["10"] != 1 || got.Detail["10"]["7"] != 1 {
		t.Fatalf("counts = %v detail = %v; the edge 10→11 is recorded under \"9\"",
			got.Counts, got.Detail)
	}

	// The same node on a path it was not linked under has nothing below it.
	other, err := uc.Statistics(context.Background(), cibiz.StatisticsRequest{
		RootIDs: []model.ID{10}, Level: 1, AncestorIDs: "99",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.Counts["10"] != 0 {
		t.Fatalf("counts = %v; nothing was recorded under the path \"99\"", other.Counts)
	}
}

// A level outside the walk's bound is refused rather than walked. cantor drove
// its loop straight off the query string.
func TestStatisticsBoundsTheLevel(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	for _, level := range []int{0, -1, cibiz.MaxHierarchyLevel + 1} {
		if _, err := uc.Statistics(context.Background(), cibiz.StatisticsRequest{
			RootIDs: []model.ID{1}, Level: level,
		}); err == nil {
			t.Fatalf("level %d was accepted", level)
		}
	}
}

// --- the nested tree ---

// renderTree is the tree in the form the oracle printed: (id, title) in
// pre-order, so a structural difference shows up as a different sequence.
func renderTree(nodes []cibiz.FullTreeNode) string {
	var parts []string
	var walk func(ns []cibiz.FullTreeNode, depth int)
	walk = func(ns []cibiz.FullTreeNode, depth int) {
		for _, n := range ns {
			title := "null"
			if n.Title != nil {
				title = *n.Title
			}
			parts = append(parts, fmt.Sprintf("%s%d:%s:%v", strings.Repeat(".", depth),
				n.ID, title, n.IsLeaf))
			walk(n.Children, depth+1)
		}
	}
	walk(nodes, 0)
	return strings.Join(parts, " ")
}

func TestFullTreeMatchesCantor(t *testing.T) {
	cases := []struct {
		name    string
		roots   []model.ID
		level   int
		types   []model.ID
		want    string
		cantor  string
		because string
	}{
		{
			name: "three levels", roots: []model.ID{1}, level: 3, types: []model.ID{1, 2, 3},
			want: "1:uniq-a1:false" +
				" .3:SHOW-b1:false" + " ..6:uniq-c1:true" +
				" .4:null:false" + " ..7:uniq-c2:true" + " ..6:uniq-c1:true",
			cantor: "identical — including instance 6 under BOTH b1 and b2, which is " +
				"the point of keying the walk by path",
			because: "titles come from the display attribute, so b1 shows SHOW-b1 " +
				"while b2, which has no value for it, is null",
		},
		{
			name: "two levels", roots: []model.ID{1}, level: 2, types: []model.ID{1, 2},
			want:   "1:uniq-a1:false" + " .3:SHOW-b1:true" + " .4:null:true",
			cantor: "identical",
		},
		{
			name: "four levels", roots: []model.ID{1}, level: 4, types: []model.ID{1, 2, 3, 4},
			want: "1:uniq-a1:false" +
				" .3:SHOW-b1:false" + " ..6:uniq-c1:false" + " ...8:uniq-d1:true" +
				" .4:null:false" + " ..7:uniq-c2:false" + " ..6:uniq-c1:false" + " ...8:uniq-d1:true",
			cantor: "identical — and note c2 is NOT a leaf: it sits above the last " +
				"level, so `isLeaf` is positional rather than a statement about its children",
		},
		{
			name: "a level with nothing below it", roots: []model.ID{2}, level: 3, types: []model.ID{1, 2, 3},
			want:   "2:uniq-a2:false" + " .5:null:false",
			cantor: "identical — the tree stops",
		},
		{
			name: "many-to-many", roots: []model.ID{9}, level: 3, types: []model.ID{5, 6, 7},
			want:   "9:uniq-m1a:false" + " .10:uniq-m2a:false" + " ..11:uniq-m3a:true",
			cantor: "identical",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, _, _ := newOracleHierarchy(t)
			got, err := uc.FullTree(context.Background(), cibiz.FullTreeRequest{
				RootIDs: tc.roots, Level: tc.level, TypeIDs: tc.types,
			})
			if err != nil {
				t.Fatalf("FullTree() error = %v", err)
			}
			rendered := renderTree(got)
			if rendered != tc.want {
				t.Fatalf("tree = %s\n   want %s\n cantor %s\nbecause %s",
					rendered, tc.want, tc.cantor, tc.because)
			}
		})
	}
}

// A `type_ids` shorter than `level` is refused. cantor indexed past the end and
// raised IndexError, which the view turned into a 500.
func TestFullTreeRefusesAShortTypeChain(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	_, err := uc.FullTree(context.Background(), cibiz.FullTreeRequest{
		RootIDs: []model.ID{1}, Level: 3, TypeIDs: []model.ID{1},
	})
	if err == nil {
		t.Fatal("a chain shorter than the level was accepted")
	}
}

// A soft-deleted instance is absent from the tree. cantor's child map was built
// from the relation rows joined to a CI whose `deleted` flag the join did not
// check, so a deleted instance stayed in the tree — measured, after
// soft-deleting an instance in the middle of a chain.
func TestFullTreeOmitsDeletedInstances(t *testing.T) {
	uc, repo, db := newOracleHierarchy(t)

	before, err := uc.FullTree(context.Background(), cibiz.FullTreeRequest{
		RootIDs: []model.ID{1}, Level: 3, TypeIDs: []model.ID{1, 2, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renderTree(before), "6:uniq-c1") {
		t.Fatalf("fixture is wrong: %s", renderTree(before))
	}

	if err := repo.DeleteCI(context.Background(), 6); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = db

	after, err := uc.FullTree(context.Background(), cibiz.FullTreeRequest{
		RootIDs: []model.ID{1}, Level: 3, TypeIDs: []model.ID{1, 2, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(renderTree(after), "6:uniq-c1") {
		t.Fatalf("a deleted instance is still in the tree: %s", renderTree(after))
	}
}

// A model whose display attribute is a SECRET is not labelled by it. cantor
// rendered the value straight into the title, which puts a password in every
// tree node of that model — the same exposure the read paths refuse, reached by
// a path that never goes through them.
func TestFullTreeDoesNotLabelWithASecret(t *testing.T) {
	uc, _, _ := newOracleHierarchyWith(t, func(r *oracleResolver) {
		// Attribute 12 is B's display attribute.
		r.passwordAttrs = map[model.ID]bool{12: true}
	})

	got, err := uc.FullTree(context.Background(), cibiz.FullTreeRequest{
		RootIDs: []model.ID{1}, Level: 2, TypeIDs: []model.ID{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := renderTree(got)

	// b1's title would be "SHOW-b1" if the secret were rendered.
	if strings.Contains(rendered, "SHOW-b1") {
		t.Fatalf("a password attribute was rendered as a title: %s", rendered)
	}

	// Its IDENTIFIER is still rendered — only the secret is withheld.
	b1 := findTreeNode(got, 3)
	if b1 == nil {
		t.Fatalf("b1 is missing from the tree: %s", rendered)
	}
	if b1.UniqueValue == nil || *b1.UniqueValue != "uniq-b1" {
		t.Fatalf("b1's identifier = %v, want uniq-b1", b1.UniqueValue)
	}
	if b1.Title != nil {
		t.Fatalf("b1's title = %q; a secret is never a label", *b1.Title)
	}
}

// findTreeNode looks a node up by instance id, depth-first.
func findTreeNode(nodes []cibiz.FullTreeNode, id model.ID) *cibiz.FullTreeNode {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
		if got := findTreeNode(nodes[i].Children, id); got != nil {
			return got
		}
	}
	return nil
}

// --- path enumeration ---

func TestSearchByPathMatchesCantor(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)

	paths := [][]model.ID{
		{oracleTypeP, oracleTypeQ, oracleTypeR},
		{oracleTypeP, oracleTypeX, oracleTypeR},
	}

	got, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP,
		Paths:        paths,
		Page:         1,
		Count:        10,
	})
	if err != nil {
		t.Fatalf("SearchByPath() error = %v", err)
	}

	wantPaths := map[string][][]model.ID{
		"P-Q-R": {{15, 17, 19}},
		"P-X-R": {{15, 18, 19}},
	}
	if !equalPaths(got.Paths, wantPaths) {
		t.Fatalf("paths = %v, want %v", got.Paths, wantPaths)
	}
	if got.NumFound != 2 || got.Total != 2 || got.Page != 1 {
		t.Fatalf("numfound=%d total=%d page=%d, want 2/2/1", got.NumFound, got.Total, got.Page)
	}
	if got.Counter["P-Q-R"] != 1 || got.Counter["P-X-R"] != 1 {
		t.Fatalf("counter = %v", got.Counter)
	}

	// `relation_types` is src alias -> dst alias -> relation name.
	if got.RelationTypes["P"]["Q"] != "p_q" || got.RelationTypes["P"]["X"] != "p_x" ||
		got.RelationTypes["Q"]["R"] != "q_r" || got.RelationTypes["X"]["R"] != "x_r" {
		t.Fatalf("relation_types = %v", got.RelationTypes)
	}
	// `type2show_key` is the model's display attribute, which for P is the
	// declared show_id and for Q its identifier.
	if got.Type2ShowKey["8"] != "show_P" || got.Type2ShowKey["9"] != "uid_Q" ||
		got.Type2ShowKey["10"] != "uid_X" || got.Type2ShowKey["11"] != "uid_R" {
		t.Fatalf("type2show_key = %v", got.Type2ShowKey)
	}

	// A target carries its attributes and its id; an interior node carries only
	// its label, its model alias and its model.
	target := got.ID2CI["19"]
	if target["_id"] != model.ID(19) || target["ci_type_alias"] != "R" || target["uid_R"] != "uniq-r1" {
		t.Fatalf("target row = %v", target)
	}
	interior := got.ID2CI["17"]
	if _, has := interior["_id"]; has {
		t.Fatalf("an interior node carried an id: %v", interior)
	}
	if interior["uid_Q"] != "uniq-q1" || interior["ci_type_alias"] != "Q" || interior["_type"] != model.ID(9) {
		t.Fatalf("interior row = %v", interior)
	}
	// P is a target MODEL but 15 is not the target of these paths; it is an
	// interior node, and it is labelled by its show attribute rather than its
	// identifier.
	if got.ID2CI["15"]["show_P"] != "SHOW-p1" {
		t.Fatalf("P row = %v", got.ID2CI["15"])
	}
}

func equalPaths(got, want map[string][][]model.ID) bool {
	if len(got) != len(want) {
		return false
	}
	for key, wantRows := range want {
		gotRows := got[key]
		if len(gotRows) != len(wantRows) {
			return false
		}
		for i := range wantRows {
			if len(gotRows[i]) != len(wantRows[i]) {
				return false
			}
			for j := range wantRows[i] {
				if gotRows[i][j] != wantRows[i][j] {
					return false
				}
			}
		}
	}
	return true
}

// One declared chain excludes the other's branch.
func TestSearchByPathFollowsOnlyTheDeclaredChain(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	got, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP,
		Paths:        [][]model.ID{{oracleTypeP, oracleTypeQ, oracleTypeR}},
		Page:         1, Count: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != 1 || len(got.Paths["P-Q-R"]) != 1 {
		t.Fatalf("paths = %v", got.Paths)
	}
	if _, has := got.RelationTypes["P"]["X"]; has {
		t.Fatalf("an undeclared edge was reported: %v", got.RelationTypes)
	}
}

// The page's `total` counts the page and `numfound` counts everything, which is
// cantor's pair of names.
func TestSearchByPathPages(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	both := [][]model.ID{{oracleTypeP, oracleTypeQ, oracleTypeR}, {oracleTypeP, oracleTypeX, oracleTypeR}}

	first, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP, Paths: both, Page: 1, Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.NumFound != 2 || first.Total != 1 {
		t.Fatalf("page 1: numfound=%d total=%d, want 2/1", first.NumFound, first.Total)
	}

	second, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP, Paths: both, Page: 2, Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 1 || second.Page != 2 {
		t.Fatalf("page 2: total=%d page=%d, want 1/2", second.Total, second.Page)
	}
	if len(second.Paths["P-X-R"]) != 1 {
		t.Fatalf("page 2 = %v", second.Paths)
	}
}

// A source expression that selects nothing is an empty result, not an error —
// and its `id2ci` is EMPTY. cantor passed the empty id set into its own search,
// whose guard only fired for a non-empty set filtered down to nothing, so the
// expression ran unrestricted and the response carried every instance of every
// model the path named.
func TestSearchByPathReturnsNothingForAnUnmatchedSource(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	got, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP,
		SourceQuery:  "uid_P:uniq-p2",
		Paths: [][]model.ID{
			{oracleTypeP, oracleTypeQ, oracleTypeR},
			{oracleTypeP, oracleTypeX, oracleTypeR},
		},
		Page: 1, Count: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != 0 || len(got.Counter) != 0 || got.NumFound != 0 || got.Total != 0 {
		t.Fatalf("result = %+v", got)
	}
	if len(got.ID2CI) != 0 {
		t.Fatalf("id2ci = %v; cantor returned five instances here", got.ID2CI)
	}
	// The label maps are still reported: they describe the REQUEST's models,
	// which is what the console needs to render an empty result.
	if got.Type2ShowKey["8"] != "show_P" {
		t.Fatalf("type2show_key = %v", got.Type2ShowKey)
	}
}

// The target's own expression narrows which instances may end a path.
func TestSearchByPathNarrowsByTheTargetQuery(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	got, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP,
		TargetQuery:  "uid_R:no-such-value",
		Paths: [][]model.ID{
			{oracleTypeP, oracleTypeQ, oracleTypeR},
			{oracleTypeP, oracleTypeX, oracleTypeR},
		},
		Page: 1, Count: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.NumFound != 0 {
		t.Fatalf("numfound = %d, want 0", got.NumFound)
	}
}

// A chain the model graph does not declare is refused with a sentence. cantor
// dereferenced the missing relation type and died with an AttributeError, so a
// chain written backwards was a 500.
func TestSearchByPathRefusesAnUndeclaredChain(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	_, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeQ,
		Paths:        [][]model.ID{{oracleTypeQ, oracleTypeP, oracleTypeR}},
		Page:         1, Count: 10,
	})
	if err == nil {
		t.Fatal("a reversed chain was accepted")
	}
}

// A chain longer than the walk's cutoff is refused rather than silently
// answered with nothing. cantor's cutoff was hard-coded and a longer chain
// could never match.
func TestSearchByPathRefusesAChainLongerThanTheCutoff(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	long := make([]model.ID, 0, cibiz.MaxPathEdges+2)
	for i := 0; i <= cibiz.MaxPathEdges+1; i++ {
		long = append(long, model.ID(i+1))
	}
	_, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: long[0], Paths: [][]model.ID{long}, Page: 1, Count: 10,
	})
	if err == nil {
		t.Fatal("an over-long chain was accepted")
	}
}

// The enumeration is bounded, and the bound is an ERROR rather than a
// truncation: `numfound` counts what was found, and reporting a truncated
// number as the total would be a lie. cantor materialised every path it found.
func TestSearchByPathBoundsTheEnumeration(t *testing.T) {
	// A layered graph: each level of the chain forks twice, so the number of
	// simple paths triples per level.
	uc, repo, db := newOracleHierarchy(t)
	_ = repo

	// Fourteen instances per level, each pointing at every instance of the
	// next — the shape a service tree's fan-out makes. The number of simple
	// paths is the product of the layers, so this is 14 * 14 * 14 = 2744 of
	// them from a graph of forty-two instances.
	mk := func(id model.ID, typeID model.ID) {
		row := cimodel.CI{TypeID: typeID, PropsJSON: "{}"}
		row.ID = id
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	rel := func(a, b model.ID) {
		row := cimodel.Relation{FirstCIID: a, SecondCIID: b, RelationTypeID: 20}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("edge: %v", err)
		}
	}
	var layerA, layerB, layerC []model.ID
	for i := model.ID(100); i < 114; i++ {
		mk(i, oracleTypeA)
		layerA = append(layerA, i)
	}
	for i := model.ID(200); i < 214; i++ {
		mk(i, oracleTypeB)
		layerB = append(layerB, i)
	}
	for i := model.ID(300); i < 314; i++ {
		mk(i, oracleTypeC)
		layerC = append(layerC, i)
	}
	for _, a := range layerA {
		for _, b := range layerB {
			rel(a, b)
		}
	}
	for _, b := range layerB {
		for _, c := range layerC {
			rel(b, c)
		}
	}

	_, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeA,
		Paths:        [][]model.ID{{oracleTypeA, oracleTypeB, oracleTypeC}},
		Page:         1, Count: 10,
	})
	if err == nil {
		t.Fatal("the enumeration was not bounded")
	}
	if !strings.Contains(err.Error(), "bound") {
		t.Fatalf("error = %v", err)
	}
}

// The path search reads the PLAIN child map, following an edge recorded under
// any ancestor path. Reproduced from cantor rather than fixed: the model chain
// already constrains the walk, and two endpoints of the same product must agree
// about the same graph.
func TestSearchByPathIgnoresTheAncestorPath(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	got, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeM1,
		Paths:        [][]model.ID{{oracleTypeM1, oracleTypeM2, oracleTypeM3}},
		Page:         1, Count: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both branches: m1a→m2a→m3a and m1b→m2b→m3b. The intermediate edges were
	// recorded under the paths "9" and "12", and neither is consulted here.
	if got.NumFound != 2 {
		t.Fatalf("numfound = %d, want 2; paths = %v", got.NumFound, got.Paths)
	}
}

// The response is JSON with the console's field names, because the console
// indexes `numfound`, `paths`, `id2ci`, `relation_types` and `type2show_key`.
func TestPathSearchJSONShape(t *testing.T) {
	uc, _, _ := newOracleHierarchy(t)
	got, err := uc.SearchByPath(context.Background(), cibiz.PathSearchRequest{
		SourceTypeID: oracleTypeP,
		Paths:        [][]model.ID{{oracleTypeP, oracleTypeQ, oracleTypeR}},
		Page:         1, Count: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"numfound", "total", "page", "counter", "paths", "id2ci", "relation_types", "type2show_key"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("field %q missing from %s", field, raw)
		}
	}
}
