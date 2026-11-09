package citype

import (
	"fmt"
	"sort"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Service-tree definition errors.
var (
	// ErrServiceTreeEmpty is returned when a definition names no pair.
	ErrServiceTreeEmpty = fmt.Errorf("%w: a service tree needs at least one model pair", errs.ErrInvalid)

	// ErrServiceTreeCycle is returned when a definition's pairs form a loop.
	//
	// cantor fed the pairs to a topological sort and answered 500 with whatever
	// the library raised. A definition is user data — the pairs come from a
	// picker and nothing validates them against the model graph — so a loop is
	// something the user can create by accident and has to be told about in
	// terms they can act on.
	ErrServiceTreeCycle = fmt.Errorf("%w: the service tree definition contains a loop", errs.ErrConflict)

	// ErrServiceTreeUnknownModel is returned when a definition names a model
	// that does not exist, or a pair that is not a model relation.
	//
	// cantor validated neither. A definition could name any pair at all, and
	// its renderer then looked each model up without a guard — so a definition
	// naming a deleted model took the whole endpoint down with a 500 rather
	// than being reported as the one bad entry it was.
	ErrServiceTreeUnknownModel = fmt.Errorf("%w: the service tree names a model or relation that does not exist", errs.ErrNotFound)
)

// ServiceTree is a rendered service-tree definition.
//
// Everything here is DERIVED from the stored pairs plus the model-relation
// graph. Nothing is stored twice: a definition is the pair list, and the shape
// below is what a console needs to draw it.
type ServiceTree struct {
	Name string

	IsPublic bool
	Option   map[string]any

	// Order is the definition's models in dependency order, parents first.
	Order []model.ID

	// Levels groups those models by dependency depth: every model in a level
	// descends only from models in earlier ones. cantor returned this from its
	// topological-sort library; it is computed here so the walk has a cycle
	// check rather than an exception.
	Levels [][]model.ID

	// Leaf holds the models nothing in the definition descends from — the
	// bottom of the tree, and where the real configuration items are read from.
	Leaf []model.ID

	// LevelConstraints maps a step number to the cardinality of the relation
	// between that step's model and the next one. cantor looked it up from the
	// CONSECUTIVE models in the flatten order rather than from the stored
	// pairs; for a definition that is a chain the two agree, and for a
	// branching one the flatten order is what the tree actually renders.
	LevelConstraints map[int]string

	// LeafShowTypes maps each bottom model to the models the tree shows under
	// it, which are its children in the FULL model-relation graph — the
	// definition chooses the path, not the leaves' extent.
	LeafShowTypes map[model.ID][]model.ID

	// NodeShowTypes is the same, propagated up: a model shows everything its
	// descendants at the bottom show.
	NodeShowTypes map[model.ID][]model.ID

	// ShowTypes is the union of LeafShowTypes, which is what the console offers
	// as the instance types a tree built from this definition will contain.
	ShowTypes []model.ID
}

// BuildServiceTree renders a definition.
//
// graph is the FULL model-relation graph, not just the definition's pairs: the
// leaves' show-types are the models related to the leaf in the whole schema.
//
// constraintOf returns the cardinality code between two models, or "" when
// there is no relation between them.
func BuildServiceTree(
	name string,
	pairs []citypemodel.TypePair,
	graph *RelationGraph,
	constraintOf func(parent, child model.ID) string,
) (*ServiceTree, error) {
	if len(pairs) == 0 {
		return nil, ErrServiceTreeEmpty
	}

	// The definition's own graph: each child's parents, deduplicated. A pair
	// naming the same model twice is a self-loop and is reported as the cycle
	// it is.
	parents := map[model.ID][]model.ID{}
	nodes := map[model.ID]bool{}
	for _, p := range pairs {
		if p.ParentID <= 0 || p.ChildID <= 0 {
			return nil, fmt.Errorf("%w: %d -> %d", ErrServiceTreeUnknownModel, p.ParentID, p.ChildID)
		}
		nodes[p.ParentID] = true
		nodes[p.ChildID] = true
		if !containsID(parents[p.ChildID], p.ParentID) {
			parents[p.ChildID] = append(parents[p.ChildID], p.ParentID)
		}
	}

	levels, err := topologicalLevels(parents, nodes)
	if err != nil {
		return nil, err
	}

	tree := &ServiceTree{
		Name:             name,
		Order:            []model.ID{},
		Levels:           levels,
		Leaf:             []model.ID{},
		LevelConstraints: map[int]string{},
		LeafShowTypes:    map[model.ID][]model.ID{},
		NodeShowTypes:    map[model.ID][]model.ID{},
		ShowTypes:        []model.ID{},
	}
	for _, level := range levels {
		tree.Order = append(tree.Order, level...)
	}

	// A leaf is a model the definition never descends from.
	isParent := map[model.ID]bool{}
	for _, p := range pairs {
		isParent[p.ParentID] = true
	}
	for _, id := range tree.Order {
		if !isParent[id] {
			tree.Leaf = append(tree.Leaf, id)
		}
	}

	// Each leaf shows its children in the FULL relation graph — and the graph
	// knows nothing about which of them exist as models, so the caller filters.
	for _, leaf := range tree.Leaf {
		tree.LeafShowTypes[leaf] = sortedUnique(graph.Children(leaf))
	}
	tree.NodeShowTypes = propagateUp(tree.Leaf, parents, tree.LeafShowTypes)

	for _, id := range tree.Order {
		tree.ShowTypes = append(tree.ShowTypes, tree.NodeShowTypes[id]...)
	}
	tree.ShowTypes = sortedUnique(tree.ShowTypes)

	if constraintOf != nil {
		for i := 0; i+1 < len(tree.Order); i++ {
			tree.LevelConstraints[i+1] = constraintOf(tree.Order[i], tree.Order[i+1])
		}
		// The last step runs from the last model in the order to the first
		// model it shows, which is cantor's rule and the one that gives the
		// bottom of the tree a cardinality at all.
		if last := len(tree.Order); last > 0 {
			if shows := tree.LeafShowTypes[tree.Order[last-1]]; len(shows) > 0 {
				tree.LevelConstraints[last] = constraintOf(tree.Order[last-1], shows[0])
			}
		}
	}
	return tree, nil
}

// topologicalLevels groups nodes by dependency depth.
//
// Iterative Kahn rather than a recursive sort, so a pathological definition
// exhausts neither the stack nor patience, and so a cycle is a returned error
// rather than a library exception. Each level is SORTED: cantor's sort produced
// sets, and a set has no order, so the console's rendering of a branching
// definition was whatever the hash offered that run.
func topologicalLevels(parents map[model.ID][]model.ID, nodes map[model.ID]bool) ([][]model.ID, error) {
	remaining := make(map[model.ID]int, len(nodes))
	for id := range nodes {
		count := 0
		for _, p := range parents[id] {
			if nodes[p] {
				count++
			}
		}
		remaining[id] = count
	}

	children := map[model.ID][]model.ID{}
	for child, ps := range parents {
		for _, p := range ps {
			children[p] = append(children[p], child)
		}
	}

	var levels [][]model.ID
	done := map[model.ID]bool{}
	for len(done) < len(nodes) {
		var ready []model.ID
		for id := range nodes {
			if !done[id] && remaining[id] == 0 {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			// Nothing is ready and nodes remain: the rest is a loop.
			return nil, fmt.Errorf("%w: %v", ErrServiceTreeCycle, unresolved(remaining, done))
		}
		sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
		levels = append(levels, ready)
		for _, id := range ready {
			done[id] = true
			for _, c := range children[id] {
				if remaining[c] > 0 {
					remaining[c]--
				}
			}
		}
	}
	return levels, nil
}

// unresolved lists the models still blocked, for the cycle report.
func unresolved(remaining map[model.ID]int, done map[model.ID]bool) []model.ID {
	var out []model.ID
	for id, n := range remaining {
		if !done[id] && n > 0 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// propagateUp unions each leaf's show-types into all of its ancestors.
//
// THE VISITED SET IS WHAT BOUNDS THE COST. cantor's `_find_parent` recursed on
// the ancestor chain with no guard, so a definition containing a diamond
// re-entered the shared ancestor once per route to it — and a layered diamond
// has exponentially many routes. Each model is dequeued once here, so the work
// is proportional to the number of models however many paths join them.
//
// It did NOT hang in cantor, and this is not a hang fix: a real loop was caught
// earlier by the topological sort, which raised, and a self-loop adjacent to a
// shared node hit the interpreter's recursion limit. The cost is the defect,
// and its only visible symptom is a slow request.
//
// The contributions are accumulated raw and normalised once at the end rather
// than deduplicated on each merge: the walk deletes nothing, so merging is the
// only place the cost could grow, and sorting a small map once is cheaper than
// sorting a growing slice per edge.
//
// topologicalLevels has already refused a loop by this point, so the set is not
// the ONLY defence — but it is what makes the cost independent of the routes.
func propagateUp(leaves []model.ID, parents map[model.ID][]model.ID, leafShow map[model.ID][]model.ID) map[model.ID][]model.ID {
	out := make(map[model.ID][]model.ID, len(parents)+len(leaves))
	for _, leaf := range leaves {
		// The leaf's OWN entry is seeded first. It is the only node whose
		// contribution does not come from a child, so a walk that only assigned
		// to parents left the bottom of every tree showing nothing.
		out[leaf] = sortedUnique(append(out[leaf], leafShow[leaf]...))

		seen := map[model.ID]bool{leaf: true}
		queue := []model.ID{leaf}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			contribution := out[id]
			for _, p := range parents[id] {
				out[p] = append(out[p], contribution...)
				if seen[p] {
					continue
				}
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	for id := range out {
		out[id] = sortedUnique(out[id])
	}
	return out
}

func containsID(list []model.ID, id model.ID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// sortedUnique returns the ids sorted ascending with duplicates removed.
func sortedUnique(ids []model.ID) []model.ID {
	seen := make(map[model.ID]bool, len(ids))
	out := make([]model.ID, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
