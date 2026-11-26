package citype

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// edge builds a relation row for a graph fixture.
func edge(parent, child model.ID) citypemodel.CITypeRelation {
	r := citypemodel.CITypeRelation{ParentID: parent, ChildID: child, RelationTypeID: 1}
	r.ID = parent*100 + child
	return r
}

func TestFindCycleOnAnAcyclicGraph(t *testing.T) {
	// A diamond: a -> b, a -> c, b -> d, c -> d. Acyclic.
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(1, 3), edge(2, 4), edge(3, 4),
	})
	if cycle, found := g.FindCycle(); found {
		t.Fatalf("FindCycle() = %v, want no cycle in a diamond", cycle)
	}
}

func TestFindCycleOnASelfLoop(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{edge(1, 2), edge(2, 2)})
	cycle, found := g.FindCycle()
	if !found {
		t.Fatal("FindCycle() = no cycle, want the self-loop found")
	}
	if len(cycle) < 2 || cycle[0] != 2 || cycle[len(cycle)-1] != 2 {
		t.Fatalf("cycle = %v, want it to start and end at 2", cycle)
	}
}

// The witness is the point of returning a path rather than a boolean: the check
// covers the WHOLE graph, so the cycle may be one the request never touched.
func TestFindCycleNamesTheParticipants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edges []citypemodel.CITypeRelation
		want  []model.ID
	}{
		{"two-cycle", []citypemodel.CITypeRelation{edge(1, 2), edge(2, 1)}, []model.ID{1, 2, 1}},
		{"three-cycle", []citypemodel.CITypeRelation{edge(1, 2), edge(2, 3), edge(3, 1)}, []model.ID{1, 2, 3, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cycle, found := NewRelationGraph(tc.edges).FindCycle()
			if !found {
				t.Fatal("FindCycle() = no cycle, want one")
			}
			if len(cycle) != len(tc.want) {
				t.Fatalf("cycle = %v, want %v", cycle, tc.want)
			}
			for i := range tc.want {
				if cycle[i] != tc.want[i] {
					t.Fatalf("cycle = %v, want %v", cycle, tc.want)
				}
			}
		})
	}
}

// A cycle appended to an otherwise healthy graph must still be found, because
// the check runs over everything rather than from the edited node.
func TestFindCycleSeesAnUnrelatedCycle(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(2, 3),
		edge(70, 71), edge(71, 70), // nothing to do with 1..3
	})
	cycle, found := g.FindCycle()
	if !found {
		t.Fatal("FindCycle() = no cycle, want the unrelated one found")
	}
	for _, id := range cycle {
		if id < 70 {
			t.Fatalf("cycle = %v, want the 70/71 pair", cycle)
		}
	}
}

// THE TERMINATION FIX. cantor's only guard was a self-loop check, so a cycle of
// three or more models recursed until the process died.
func TestDescendantsTerminateOnACycle(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(2, 3), edge(3, 1),
	})
	levels, err := g.DescendantsByLevel(1)
	if err != nil {
		t.Fatalf("DescendantsByLevel() error = %v, want the cycle skipped rather than fatal", err)
	}
	var reached []model.ID
	for _, lv := range levels {
		reached = append(reached, lv.Types...)
	}
	if len(reached) != 2 {
		t.Fatalf("reached %v, want models 2 and 3 once each and 1 not revisited", reached)
	}
}

// cantor reported a model at every depth it was reachable at, and the port
// keeps that: the caller renders routes, and two routes to one model are two
// branches.
func TestDescendantsReportEveryReachableDepth(t *testing.T) {
	// 1 -> 2 -> 4 and 1 -> 3 -> 2 -> 4 ... build a graph where 4 sits at
	// depth 2 via one route and depth 3 via another.
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(2, 4), // 4 at depth 2
		edge(1, 3), edge(3, 5), edge(5, 4), // 4 at depth 3
	})
	levels, err := g.DescendantsByLevel(1)
	if err != nil {
		t.Fatalf("DescendantsByLevel() error = %v", err)
	}
	depthsOf4 := 0
	for _, lv := range levels {
		for _, id := range lv.Types {
			if id == 4 {
				depthsOf4++
			}
		}
	}
	if depthsOf4 != 2 {
		t.Fatalf("model 4 appears at %d depths, want 2 (the caller renders routes, not a set)", depthsOf4)
	}
}

func TestDescendantsAreOrderedByLevel(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(1, 3), edge(2, 4),
	})
	levels, err := g.DescendantsByLevel(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 2 {
		t.Fatalf("levels = %+v, want two", levels)
	}
	if levels[0].Level != 1 || levels[1].Level != 2 {
		t.Fatalf("levels = %+v, want ascending", levels)
	}
	if len(levels[0].Types) != 2 || len(levels[1].Types) != 1 {
		t.Fatalf("levels = %+v, want depth-1 neighbours in row order", levels)
	}
}

// The node budget is what stops an acyclic but combinatorially wide graph from
// allocating without limit — and it ERRORS rather than truncating, because a
// caller rendering "3 routes" when there were 900 has been told something
// false.
func TestDescendantsRefuseAnExplosiveGraph(t *testing.T) {
	// A dense layered DAG: each of 40 layers fully connected to the next gives
	// 2^40 distinct routes.
	var edges []citypemodel.CITypeRelation
	const layers, width = 40, 2
	for l := 0; l < layers; l++ {
		for a := 0; a < width; a++ {
			for b := 0; b < width; b++ {
				edges = append(edges, edge(model.ID(l*width+a+1), model.ID((l+1)*width+b+1)))
			}
		}
	}
	_, err := NewRelationGraph(edges).DescendantsByLevel(1)
	if !errors.Is(err, ErrRelationGraphTooLarge) {
		t.Fatalf("DescendantsByLevel() error = %v, want the walk to refuse rather than run away", err)
	}
}

func TestDescendantsOfALeafAreEmpty(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{edge(1, 2)})
	levels, err := g.DescendantsByLevel(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 0 {
		t.Fatalf("levels = %+v, want none", levels)
	}
}

// --- paths ---

func TestPathsFindEverySimpleRoute(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(2, 4),
		edge(1, 3), edge(3, 4),
	})
	paths, err := g.Paths(1, []model.ID{4})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want two routes to model 4", paths)
	}
	for _, p := range paths {
		if len(p) != 3 || p[0] != 1 || p[len(p)-1] != 4 {
			t.Fatalf("path = %v, want 1 -> ... -> 4", p)
		}
	}
}

// cantor built its graph by recursing with no visited set, so this hung before
// networkx was ever reached.
func TestPathsTerminateOnACycle(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{
		edge(1, 2), edge(2, 3), edge(3, 1), edge(3, 4),
	})
	paths, err := g.Paths(1, []model.ID{4})
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths = %v, want exactly the simple route", paths)
	}
	if routeString(paths[0]) != "1,2,3,4" {
		t.Fatalf("path = %v, want the simple route 1,2,3,4 with the cycle not repeated", paths[0])
	}
}

func TestPathsToAModelThatIsTheSource(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{edge(1, 2)})
	paths, err := g.Paths(1, []model.ID{1})
	if err != nil {
		t.Fatal(err)
	}
	// The zero-length path is not a route the caller asked for; reporting the
	// source as a target would be a loop, not a path.
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
}

func TestPathsWithNoRoute(t *testing.T) {
	g := NewRelationGraph([]citypemodel.CITypeRelation{edge(1, 2), edge(3, 4)})
	paths, err := g.Paths(1, []model.ID{4})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
}

func TestPathsRefuseTooManyRoutes(t *testing.T) {
	// A layered diamond: ten layers of two fully connected to the next. Every
	// route through picks one node per layer, so there are 2^9 = 512 routes to
	// the final node — comfortably past the bound, and still far short of the
	// visit budget, so the PATH bound is what fires.
	var edges []citypemodel.CITypeRelation
	nodeAt := func(layer, which int) model.ID { return model.ID(layer*100 + which) }
	const layers = 10
	for j := 0; j < 2; j++ {
		edges = append(edges, edge(1, nodeAt(1, j)))
	}
	for l := 1; l < layers; l++ {
		for a := 0; a < 2; a++ {
			for b := 0; b < 2; b++ {
				edges = append(edges, edge(nodeAt(l, a), nodeAt(l+1, b)))
			}
		}
	}
	target := nodeAt(layers, 0)

	_, err := NewRelationGraph(edges).Paths(1, []model.ID{target})
	if !errors.Is(err, ErrTooManyPaths) {
		t.Fatalf("Paths() error = %v, want the bound to refuse rather than truncate silently", err)
	}
}

func TestGraphNodeOrderIsDeterministic(t *testing.T) {
	edges := []citypemodel.CITypeRelation{edge(3, 1), edge(1, 2), edge(2, 3)}
	first := NewRelationGraph(edges).Nodes()
	for i := 0; i < 50; i++ {
		again := NewRelationGraph(edges).Nodes()
		if len(again) != len(first) {
			t.Fatalf("node count changed between builds")
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("node order changed between builds: %v then %v", first, again)
			}
		}
	}
	// First-appearance order, so the very first edge's parent leads.
	if first[0] != 3 {
		t.Fatalf("nodes = %v, want first-appearance order", first)
	}
}

func routeString(path []model.ID) string {
	parts := make([]string, 0, len(path))
	for _, id := range path {
		parts = append(parts, strconv.FormatInt(int64(id), 10))
	}
	return strings.Join(parts, ",")
}
