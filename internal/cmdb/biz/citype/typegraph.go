package citype

import (
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// Bounds on the model-relation graph walks.
//
// cantor's walks had neither a visited set nor a depth cap, so a cycle in
// c_ci_type_relations — which nothing prevented, and which a direct SQL insert
// could create — made the request recurse until the process ran out of stack.
// The node budget is the second guard: a DAG full of diamonds produces
// exponentially many distinct paths, and the caller's tree only ever renders
// them.
const (
	// MaxGraphDepth bounds how far a walk follows one chain.
	MaxGraphDepth = 64

	// MaxGraphNodes bounds the total number of node visits across a walk, so a
	// graph that is acyclic but combinatorially wide fails with an error
	// instead of allocating without limit.
	MaxGraphNodes = 20000

	// MaxGraphPaths bounds how many paths one query returns. A bound is needed
	// because "all simple paths" is exponential in the worst case, and the
	// result is rendered in a panel.
	MaxGraphPaths = 200
)

// RelationGraph is the model-level relation graph held in memory.
//
// It is built from ALL type-relation rows once and walked in Go, rather than
// issuing a query per node as cantor did. The table holds one row per allowed
// (parent model, child model, relation type) triple, so it is small — and
// walking in memory is what makes a visited set cheap enough to insist on.
//
// Neighbour order is the order the rows arrived in, which the store fixes by
// ordering on id. That makes every walk deterministic, which cantor's was not:
// it iterated a database result whose order was unspecified.
type RelationGraph struct {
	children map[model.ID][]model.ID
	parents  map[model.ID][]model.ID

	// nodes is the deterministic iteration order for whole-graph scans.
	nodes []model.ID
	seen  map[model.ID]bool
}

// NewRelationGraph builds the graph from relation rows.
func NewRelationGraph(edges []citypemodel.CITypeRelation) *RelationGraph {
	g := &RelationGraph{
		children: map[model.ID][]model.ID{},
		parents:  map[model.ID][]model.ID{},
		seen:     map[model.ID]bool{},
	}
	for _, e := range edges {
		g.add(e.ParentID, e.ChildID)
	}
	return g
}

func (g *RelationGraph) add(parent, child model.ID) {
	g.touch(parent)
	g.touch(child)
	g.children[parent] = append(g.children[parent], child)
	g.parents[child] = append(g.parents[child], parent)
}

func (g *RelationGraph) touch(id model.ID) {
	if !g.seen[id] {
		g.seen[id] = true
		g.nodes = append(g.nodes, id)
	}
}

// Children returns the models this one points at.
func (g *RelationGraph) Children(id model.ID) []model.ID { return g.children[id] }

// Parents returns the models that point at this one.
func (g *RelationGraph) Parents(id model.ID) []model.ID { return g.parents[id] }

// Nodes returns every model id the graph mentions, in a deterministic order.
func (g *RelationGraph) Nodes() []model.ID { return g.nodes }

// HasEdge reports whether the graph holds the given parent -> child relation.
//
// It replaces a database lookup per pair. cantor asked the relation table one
// pair at a time while validating nothing and while rendering, which is a query
// per step in every stored definition.
func (g *RelationGraph) HasEdge(parent, child model.ID) bool {
	for _, c := range g.children[parent] {
		if c == child {
			return true
		}
	}
	return false
}

// FindCycle returns one cycle in the graph, or false when there is none.
//
// It replaces cantor's `toposort_flatten` call, and it returns the cycle ITSELF
// rather than a boolean. cantor logged the exception and answered "circular
// dependency" with no indication of WHICH models formed it — from a UI that
// shows a graph, that is the least useful possible message.
//
// A witness matters here for a second reason: the check runs over the whole
// graph, as cantor's did, so it can fail because of an edge nobody in this
// request touched. Naming the cycle is what tells the operator whether their
// edit is the problem or the data already was.
//
// Iterative rather than recursive so a pathological graph cannot overflow the
// stack it is trying to validate.
func (g *RelationGraph) FindCycle() ([]model.ID, bool) {
	const (
		white = iota
		grey
		black
	)
	color := make(map[model.ID]int, len(g.nodes))

	type frame struct {
		node model.ID
		next int
	}

	for _, start := range g.nodes {
		if color[start] != white {
			continue
		}
		color[start] = grey
		stack := []frame{{node: start}}

		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			kids := g.children[top.node]

			if top.next >= len(kids) {
				color[top.node] = black
				stack = stack[:len(stack)-1]
				continue
			}
			next := kids[top.next]
			top.next++

			switch color[next] {
			case grey:
				// The cycle is the run of stack frames from `next` onward, plus
				// `next` again to close it.
				cycle := make([]model.ID, 0, len(stack))
				started := false
				for _, f := range stack {
					if f.node == next {
						started = true
					}
					if started {
						cycle = append(cycle, f.node)
					}
				}
				return append(cycle, next), true
			case white:
				color[next] = grey
				stack = append(stack, frame{node: next})
			}
		}
	}
	return nil, false
}

// RelationLevel is one depth of the descendant tree.
type RelationLevel struct {
	Level int        `json:"level"`
	Types []model.ID `json:"type_ids"`
}

// DescendantsByLevel walks down from root, grouping the models it reaches by
// depth.
//
// THE WALK CARRIES AN ON-PATH VISITED SET, which is the difference between this
// and cantor's. cantor's only guard was `if i.child_id != _id` — a self-loop
// check — so any cycle among three or more models recursed until the process
// died. An on-path set forbids exactly the cycles and nothing else.
//
// A model reachable by two different routes is reported at BOTH depths, which
// is cantor's behaviour and the right one here: the caller renders a tree of
// routes, and two routes to the same model are two branches. The node budget is
// what keeps that from running away.
func (g *RelationGraph) DescendantsByLevel(root model.ID) ([]RelationLevel, error) {
	byLevel := map[int][]model.ID{}
	onPath := map[model.ID]bool{root: true}
	visits := 0

	var walk func(id model.ID, depth int) error
	walk = func(id model.ID, depth int) error {
		if depth >= MaxGraphDepth {
			return fmt.Errorf("%w: the model-relation graph is deeper than %d below model %d",
				ErrRelationGraphTooLarge, MaxGraphDepth, root)
		}
		for _, child := range g.children[id] {
			if onPath[child] {
				continue
			}
			visits++
			if visits > MaxGraphNodes {
				return fmt.Errorf("%w: the walk from model %d exceeds %d visits",
					ErrRelationGraphTooLarge, root, MaxGraphNodes)
			}
			byLevel[depth+1] = append(byLevel[depth+1], child)
			onPath[child] = true
			if err := walk(child, depth+1); err != nil {
				return err
			}
			delete(onPath, child)
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}

	out := make([]RelationLevel, 0, len(byLevel))
	for level := 1; level <= MaxGraphDepth; level++ {
		if len(byLevel[level]) == 0 {
			continue
		}
		out = append(out, RelationLevel{Level: level, Types: byLevel[level]})
	}
	return out, nil
}

// Paths returns every simple path from `from` to any of `targets`.
//
// cantor built a networkx DiGraph by recursing with no visited set — the same
// hang — and then asked networkx for all simple paths. The walk here carries an
// on-path set and returns at most MaxGraphPaths paths, reporting an error
// rather than truncating silently: a caller that renders "3 paths" when there
// were 900 has been told something false.
func (g *RelationGraph) Paths(from model.ID, targets []model.ID) ([][]model.ID, error) {
	want := make(map[model.ID]bool, len(targets))
	for _, t := range targets {
		want[t] = true
	}
	onPath := map[model.ID]bool{from: true}
	visited := 0
	var out [][]model.ID

	var walk func(id model.ID, path []model.ID, depth int) error
	walk = func(id model.ID, path []model.ID, depth int) error {
		if depth > MaxGraphDepth {
			return fmt.Errorf("%w: the search from model %d exceeds depth %d",
				ErrRelationGraphTooLarge, from, MaxGraphDepth)
		}
		for _, child := range g.children[id] {
			if onPath[child] {
				continue
			}
			visited++
			if visited > MaxGraphNodes {
				return fmt.Errorf("%w: the search from model %d exceeds %d visits",
					ErrRelationGraphTooLarge, from, MaxGraphNodes)
			}
			next := append(append([]model.ID{}, path...), child)
			if want[child] {
				if len(out) >= MaxGraphPaths {
					return fmt.Errorf("%w: more than %d paths from model %d to the requested models",
						ErrTooManyPaths, MaxGraphPaths, from)
				}
				out = append(out, next)
			}
			onPath[child] = true
			if err := walk(child, next, depth+1); err != nil {
				return err
			}
			delete(onPath, child)
		}
		return nil
	}
	if err := walk(from, []model.ID{from}, 0); err != nil {
		return nil, err
	}
	if out == nil {
		out = [][]model.ID{}
	}
	return out, nil
}
