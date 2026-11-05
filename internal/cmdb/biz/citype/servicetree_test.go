package citype

import (
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

func pair(parent, child int) citypemodel.TypePair {
	return citypemodel.TypePair{ParentID: model.ID(parent), ChildID: model.ID(child)}
}

// chain builds a definition from consecutive ids.
func chain(ns ...int) []citypemodel.TypePair {
	out := make([]citypemodel.TypePair, 0, len(ns)-1)
	for i := 0; i+1 < len(ns); i++ {
		out = append(out, pair(ns[i], ns[i+1]))
	}
	return out
}

// graphFrom builds a relation graph from pairs.
func graphFrom(pairs ...citypemodel.TypePair) *RelationGraph {
	rows := make([]citypemodel.CITypeRelation, 0, len(pairs))
	for _, p := range pairs {
		rows = append(rows, citypemodel.CITypeRelation{ParentID: p.ParentID, ChildID: p.ChildID, RelationTypeID: 1})
	}
	return NewRelationGraph(rows)
}

func levelOf(t *testing.T, tree *ServiceTree, id model.ID) int {
	t.Helper()
	for i, level := range tree.Levels {
		for _, x := range level {
			if x == id {
				return i
			}
		}
	}
	t.Fatalf("model %d is not in any level: %v", id, tree.Levels)
	return -1
}

func TestBuildServiceTreeOrdersParentsFirst(t *testing.T) {
	def := chain(1, 2, 3, 4)
	graph := graphFrom(def...)

	tree, err := BuildServiceTree("t", def, graph, nil)
	if err != nil {
		t.Fatalf("BuildServiceTree() error = %v", err)
	}
	if !sameIDs(tree.Order, ids(1, 2, 3, 4)) {
		t.Fatalf("order = %v, want parents first", tree.Order)
	}
	if len(tree.Levels) != 4 {
		t.Fatalf("levels = %v, want one model per level in a chain", tree.Levels)
	}
	if !sameIDs(tree.Leaf, ids(4)) {
		t.Fatalf("leaf = %v, want the bottom of the chain", tree.Leaf)
	}
}

// The leaf's show-types come from the FULL relation graph, not the definition:
// the definition chooses the path, not the leaves' extent.
func TestBuildServiceTreeLeafShowTypesComeFromTheWholeGraph(t *testing.T) {
	def := chain(1, 2)
	// 2 relates to 20 and 30 in the schema, only one of which the definition
	// mentions.
	graph := graphFrom(pair(1, 2), pair(2, 20), pair(2, 30))

	tree, err := BuildServiceTree("t", def, graph, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(tree.LeafShowTypes[2], ids(20, 30)) {
		t.Fatalf("leaf show types = %v, want both children of the leaf", tree.LeafShowTypes[2])
	}
	if !sameIDs(tree.ShowTypes, ids(20, 30)) {
		t.Fatalf("show types = %v, want the union of the leaves'", tree.ShowTypes)
	}
}

// A node shows everything its descendants at the bottom show.
func TestBuildServiceTreePropagatesShowTypesUp(t *testing.T) {
	def := chain(1, 2, 3)
	graph := graphFrom(pair(1, 2), pair(2, 3), pair(3, 40))

	tree, err := BuildServiceTree("t", def, graph, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []model.ID{1, 2, 3} {
		if !sameIDs(tree.NodeShowTypes[id], ids(40)) {
			t.Fatalf("node %d shows %v, want the leaf's children", id, tree.NodeShowTypes[id])
		}
	}
}

// A branching definition puts two models in one level, and the level is SORTED.
//
// cantor's topological sort produced sets, and a set has no order, so a
// branching definition rendered its levels in whatever order the hash offered
// that run.
func TestBuildServiceTreeSortsEachLevel(t *testing.T) {
	def := []citypemodel.TypePair{pair(1, 30), pair(1, 20), pair(1, 10)}
	graph := graphFrom(def...)

	first, err := BuildServiceTree("t", def, graph, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(first.Levels[1], ids(10, 20, 30)) {
		t.Fatalf("level = %v, want ascending", first.Levels[1])
	}
	for i := 0; i < 50; i++ {
		again, err := BuildServiceTree("t", def, graph, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !sameIDs(again.Levels[1], first.Levels[1]) {
			t.Fatalf("level order changed between builds: %v then %v", first.Levels[1], again.Levels[1])
		}
	}
}

// A DIAMOND MUST NOT DO WORK PROPORTIONAL TO ITS NUMBER OF ROUTES. cantor's
// propagation appended rather than unioned and recursed without a visited set,
// so a shared ancestor was re-walked once per route to it.
func TestBuildServiceTreeHandlesADiamond(t *testing.T) {
	// Ten layers of two, fully connected: 2^9 routes from the root to the bottom.
	var def []citypemodel.TypePair
	var graphPairs []citypemodel.TypePair
	nodeAt := func(layer, which int) int { return layer*100 + which }
	for j := 0; j < 2; j++ {
		def = append(def, pair(1, nodeAt(1, j)))
	}
	for l := 1; l < 10; l++ {
		for a := 0; a < 2; a++ {
			for b := 0; b < 2; b++ {
				def = append(def, pair(nodeAt(l, a), nodeAt(l+1, b)))
			}
		}
	}
	graphPairs = append(graphPairs, def...)
	// The bottom layer relates to a single model.
	for j := 0; j < 2; j++ {
		graphPairs = append(graphPairs, pair(nodeAt(10, j), 500))
	}

	tree, err := BuildServiceTree("t", def, graphFrom(graphPairs...), nil)
	if err != nil {
		t.Fatalf("BuildServiceTree() error = %v", err)
	}
	if len(tree.Leaf) != 2 {
		t.Fatalf("leaf = %v, want the bottom layer", tree.Leaf)
	}
	// Every node ends up showing the same single model, reached by 512 routes
	// without any of them being enumerated.
	if !sameIDs(tree.NodeShowTypes[1], ids(500)) {
		t.Fatalf("root shows %v, want the bottom model once", tree.NodeShowTypes[1])
	}
}

func TestBuildServiceTreeRejectsALoop(t *testing.T) {
	// cantor fed this to a topological sort and answered 500 with whatever the
	// library raised.
	def := []citypemodel.TypePair{pair(1, 2), pair(2, 3), pair(3, 1)}
	_, err := BuildServiceTree("t", def, graphFrom(def...), nil)
	if !errors.Is(err, ErrServiceTreeCycle) {
		t.Fatalf("error = %v, want ErrServiceTreeCycle", err)
	}
}

func TestBuildServiceTreeRejectsASelfLoop(t *testing.T) {
	def := []citypemodel.TypePair{pair(1, 1)}
	_, err := BuildServiceTree("t", def, graphFrom(pair(1, 2)), nil)
	if !errors.Is(err, ErrServiceTreeCycle) {
		t.Fatalf("error = %v, want a self-loop reported as the loop it is", err)
	}
}

func TestBuildServiceTreeRejectsAnEmptyDefinition(t *testing.T) {
	if _, err := BuildServiceTree("t", nil, graphFrom(), nil); !errors.Is(err, ErrServiceTreeEmpty) {
		t.Fatalf("error = %v, want ErrServiceTreeEmpty", err)
	}
}

func TestBuildServiceTreeResolvesLevelConstraints(t *testing.T) {
	def := chain(1, 2, 3)
	graph := graphFrom(pair(1, 2), pair(2, 3), pair(3, 40))
	constraintOf := func(parent, child model.ID) string {
		if parent == 2 && child == 3 {
			return string(model.ConstraintMany2Many)
		}
		if parent == 3 && child == 40 {
			return string(model.ConstraintOne2One)
		}
		return string(model.ConstraintOne2Many)
	}

	tree, err := BuildServiceTree("t", def, graph, constraintOf)
	if err != nil {
		t.Fatal(err)
	}
	if tree.LevelConstraints[1] != string(model.ConstraintOne2Many) {
		t.Fatalf("step 1 = %q", tree.LevelConstraints[1])
	}
	if tree.LevelConstraints[2] != string(model.ConstraintMany2Many) {
		t.Fatalf("step 2 = %q", tree.LevelConstraints[2])
	}
	// The last step runs from the last model in the order to the first model it
	// shows, which is what gives the bottom of the tree a cardinality at all.
	if tree.LevelConstraints[3] != string(model.ConstraintOne2One) {
		t.Fatalf("step 3 = %q", tree.LevelConstraints[3])
	}
}

func TestBuildServiceTreeWithoutAConstraintLookup(t *testing.T) {
	def := chain(1, 2)
	tree, err := BuildServiceTree("t", def, graphFrom(def...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.LevelConstraints) != 0 {
		t.Fatalf("constraints = %v, want none without a lookup", tree.LevelConstraints)
	}
}

func TestBuildServiceTreeRejectsANonPositivePair(t *testing.T) {
	def := []citypemodel.TypePair{{ParentID: 0, ChildID: 2}}
	if _, err := BuildServiceTree("t", def, graphFrom(), nil); !errors.Is(err, ErrServiceTreeUnknownModel) {
		t.Fatalf("error = %v, want the bad pair reported", err)
	}
}

// The levels are a dependency depth, not a position in the pair list.
func TestBuildServiceTreeLevelsAreDependencyDepth(t *testing.T) {
	def := []citypemodel.TypePair{pair(1, 2), pair(1, 3), pair(2, 4), pair(3, 4)}
	tree, err := BuildServiceTree("t", def, graphFrom(def...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if levelOf(t, tree, 1) != 0 {
		t.Fatalf("1 is at level %d, want 0", levelOf(t, tree, 1))
	}
	if levelOf(t, tree, 2) != 1 || levelOf(t, tree, 3) != 1 {
		t.Fatalf("levels = %v, want 2 and 3 together", tree.Levels)
	}
	if levelOf(t, tree, 4) != 2 {
		t.Fatalf("4 is at level %d, want 2", levelOf(t, tree, 4))
	}
	if !sameIDs(tree.Leaf, ids(4)) {
		t.Fatalf("leaf = %v", tree.Leaf)
	}
}
