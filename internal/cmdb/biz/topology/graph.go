package topology

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The bounds on one assembled graph.
//
// THEY ARE THE CONSOLE'S, NOT THE DATABASE'S. A view that would draw more than
// this is not a picture a person can read, and assembling it costs a query per
// level over an unbounded frontier. cantor had no bound and could be asked for
// a million roots; the port refuses with a message that names the limit.
const (
	MaxRoots = 200
	MaxNodes = 2000
	MaxDepth = 5
)

// ErrGraphTooLarge is returned when a view exceeds one of the bounds.
var ErrGraphTooLarge = fmt.Errorf("%w: the topology view is too large to draw", errs.ErrInvalid)

// Node is one instance in a graph.
type Node struct {
	ID     model.ID `json:"id"`
	TypeID model.ID `json:"type_id"`
	Name   string   `json:"name"`
}

// Link is one edge, directed from parent to child.
type Link struct {
	From model.ID `json:"from"`
	To   model.ID `json:"to"`
}

// Graph is one assembled view.
type Graph struct {
	Nodes     []Node              `json:"nodes"`
	Links     []Link              `json:"links"`
	Type2Meta map[model.ID]string `json:"type2meta"`
}

// GraphEdge is one relation row as the walk sees it.
type GraphEdge struct {
	From       model.ID
	To         model.ID
	FromTypeID model.ID
	ToTypeID   model.ID
}

// GraphQueries answers the instance-side questions a graph needs.
type GraphQueries interface {
	// Roots returns the instances matching the view's expression, capped at
	// `limit`. The caller asks for one more than MaxRoots so an overflow can
	// be reported rather than silently truncated.
	Roots(ctx context.Context, typeID model.ID, expr string, limit int) ([]Node, error)

	// Children returns every edge leaving any of the ids.
	Children(ctx context.Context, ids []model.ID) ([]GraphEdge, error)

	// Parents returns every edge arriving at any of the ids.
	Parents(ctx context.Context, ids []model.ID) ([]GraphEdge, error)

	// Names labels a batch of instances.
	Names(ctx context.Context, ids []model.ID) (map[model.ID]string, error)

	// Icons maps a model to its stored icon value.
	Icons(ctx context.Context, typeIDs []model.ID) (map[model.ID]string, error)
}

// GraphInput is what a saved view or a preview supplies.
type GraphInput struct {
	CentralNodeType      model.ID
	CentralNodeInstances string
	Path                 map[string][]model.ID
}

// BuildGraph assembles a view from the current relations.
func BuildGraph(ctx context.Context, q GraphQueries, in GraphInput) (*Graph, error) {
	if q == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.CentralNodeType <= 0 {
		return nil, fmt.Errorf("%w: central_node_type", errs.ErrInvalid)
	}
	levels, err := levelsOf(in.Path)
	if err != nil {
		return nil, err
	}
	roots, err := q.Roots(ctx, in.CentralNodeType, in.CentralNodeInstances, MaxRoots+1)
	if err != nil {
		return nil, err
	}
	if len(roots) > MaxRoots {
		return nil, fmt.Errorf("%w: more than %d root instances", ErrGraphTooLarge, MaxRoots)
	}

	graph := &Graph{Nodes: []Node{}, Links: []Link{}, Type2Meta: map[model.ID]string{}}
	if len(roots) == 0 {
		return graph, nil
	}

	visited := make(map[model.ID]bool, len(roots))
	seenLinks := map[string]bool{}
	nodes := make(map[model.ID]Node, len(roots))
	frontier := make([]model.ID, 0, len(roots))
	for _, root := range roots {
		root.TypeID = in.CentralNodeType
		nodes[root.ID] = root
		// NOT VISITED: a root may also be a child of another root when the
		// central model relates to itself. cantor seeded only the node map and
		// deduplicated at the end; marking roots visited drops the edge into
		// them and leaves the roots as isolated dots.
		frontier = append(frontier, root.ID)
	}
	// THE TWO DIRECTIONS EACH START AT THE ROOTS. levelsOf puts positive levels
	// first, so by the time the first negative level runs the frontier holds the
	// deepest downward children; walking up from there would draw grandparents
	// of the children instead of the roots' parents. cantor reset its id list to
	// root_ids before the negative loop for the same reason.
	rootFrontier := append([]model.ID(nil), frontier...)
	lastLevel := 0

	for _, level := range levels {
		if level.Level < 0 && lastLevel > 0 {
			frontier = rootFrontier
		}
		if len(frontier) == 0 {
			break
		}
		allowed := make(map[model.ID]bool, len(level.TypeIDs))
		for _, typeID := range level.TypeIDs {
			allowed[typeID] = true
		}

		var edges []GraphEdge
		if level.Level > 0 {
			edges, err = q.Children(ctx, frontier)
		} else {
			edges, err = q.Parents(ctx, frontier)
		}
		if err != nil {
			return nil, err
		}

		next := make([]model.ID, 0, len(edges))
		for _, edge := range edges {
			if edge.From == edge.To {
				// A self-loop is always a mapping mistake; drawing it as an
				// edge from a node to itself is noise no operator asked for.
				continue
			}
			if level.Level > 0 {
				if !allowed[edge.ToTypeID] || visited[edge.To] {
					continue
				}
				key := linkKey(edge.From, edge.To)
				if seenLinks[key] {
					continue
				}
				seenLinks[key] = true
				graph.Links = append(graph.Links, Link{From: edge.From, To: edge.To})
				nodes[edge.To] = Node{ID: edge.To, TypeID: edge.ToTypeID}
				visited[edge.To] = true
				next = append(next, edge.To)
			} else {
				if !allowed[edge.FromTypeID] || visited[edge.From] {
					continue
				}
				key := linkKey(edge.From, edge.To)
				if seenLinks[key] {
					continue
				}
				seenLinks[key] = true
				graph.Links = append(graph.Links, Link{From: edge.From, To: edge.To})
				nodes[edge.From] = Node{ID: edge.From, TypeID: edge.FromTypeID}
				visited[edge.From] = true
				next = append(next, edge.From)
			}
			if len(nodes) > MaxNodes {
				return nil, fmt.Errorf("%w: more than %d instances", ErrGraphTooLarge, MaxNodes)
			}
		}
		frontier = next
		lastLevel = level.Level
	}

	ids := make([]model.ID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	names, err := q.Names(ctx, ids)
	if err != nil {
		return nil, err
	}
	typeIDs := map[model.ID]bool{}
	for _, id := range ids {
		node := nodes[id]
		if name, ok := names[id]; ok && name != "" {
			node.Name = name
		}
		if node.Name == "" {
			node.Name = fmt.Sprintf("#%d", id)
		}
		graph.Nodes = append(graph.Nodes, node)
		typeIDs[node.TypeID] = true
	}
	list := make([]model.ID, 0, len(typeIDs))
	for id := range typeIDs {
		list = append(list, id)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	icons, err := q.Icons(ctx, list)
	if err != nil {
		return nil, err
	}
	if icons != nil {
		graph.Type2Meta = icons
	}
	return graph, nil
}

// graphLevel is one declared path level.
type graphLevel struct {
	Level   int
	TypeIDs []model.ID
}

// levelsOf parses and orders the path.
//
// THE KEYS ARE STRINGS because they are JSON object keys; the sign is the
// direction and the magnitude is the distance from the roots.
func levelsOf(path map[string][]model.ID) ([]graphLevel, error) {
	out := make([]graphLevel, 0, len(path))
	for key, typeIDs := range path {
		level, err := strconv.Atoi(key)
		if err != nil || level == 0 {
			return nil, fmt.Errorf("%w: path level %q", errs.ErrInvalid, key)
		}
		if level > MaxDepth || level < -MaxDepth {
			return nil, fmt.Errorf("%w: path level %d exceeds %d", ErrGraphTooLarge, level, MaxDepth)
		}
		out = append(out, graphLevel{Level: level, TypeIDs: typeIDs})
	}
	// POSITIVE LEVELS FIRST, THEN NEGATIVE, EACH OUTWARD FROM THE ROOTS.
	// A numeric sort would start at -5 and walk the upward direction before the
	// roots had any downward children to walk back from — the frontier would be
	// the roots for one direction and then empty for the other. cantor's two
	// separate loops did the same thing; this keeps that order explicit.
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].Level, out[j].Level
		if (left > 0) != (right > 0) {
			return left > 0
		}
		return abs(left) < abs(right)
	})
	return out, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// linkKey is the undirected identity of an edge, so A→B and B→A are one link.
func linkKey(a, b model.ID) string {
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("%d:%d", a, b)
}
