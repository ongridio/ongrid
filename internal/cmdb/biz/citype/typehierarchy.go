package citype

import (
	"context"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// The model graph as the INSTANCE hierarchy needs it: which models appear at
// each level, and which levels a many-to-many rule makes path-scoped.
//
// This is the model module answering for the instance module, in that direction
// on purpose — see the TypeResolver port's doc comment. cantor had the same
// split (CITypeRelationManager served the hierarchy search), and the alternative
// would put a second copy of the model-graph rules in the instance layer.

// RelationBetween returns the rule a model pair declares.
func (u *Usecase) RelationBetween(ctx context.Context, parentTypeID, childTypeID model.ID) (cibiz.TypeRelationRule, bool, error) {
	if u.repo == nil {
		return cibiz.TypeRelationRule{}, false, nil
	}
	row, err := u.repo.TypeRelationByPair(ctx, parentTypeID, childTypeID)
	if err != nil {
		return cibiz.TypeRelationRule{}, false, err
	}
	if row == nil {
		return cibiz.TypeRelationRule{}, false, nil
	}
	rule := cibiz.TypeRelationRule{RelationTypeID: row.RelationTypeID}
	if row.Constraint != nil {
		if c := model.Constraint(*row.Constraint); c.Valid() {
			rule.Constraint = c
		}
	}
	// The vocabulary word travels with the rule because a caller that has
	// already asked "what relates these two models" is about to label an edge
	// with it, and resolving it separately is a second question about the same
	// row.
	if names, err := u.repo.RelationTypesByID(ctx, []model.ID{row.RelationTypeID}); err != nil {
		return cibiz.TypeRelationRule{}, false, err
	} else if rt, ok := names[row.RelationTypeID]; ok {
		rule.RelationTypeName = rt.Name
	}
	return rule, true, nil
}

// HierarchyLevels describes the model graph a hierarchy walk needs.
//
// maxLevel IS THE WALK'S DEPTH, and it is the caller's max(level) rather than
// cantor's level[0]. cantor computed the many-to-many levels from the FIRST
// requested level — so `level=1,3` walked three levels with the path scoping
// computed for one, and a many-to-many branch at level 2 was followed WITHOUT
// the path that makes it meaningful, showing children nobody linked there. The
// scoping has to cover the same range as the walk.
//
// The walk follows CHILDREN even for a reversed hierarchy, because that is what
// cantor did: `get_level2constraint` always walked down from the root's model,
// and the reverse walk then used that answer. Reproduced deliberately — a
// reversed hierarchy over a many-to-many graph was path-scoped the same way.
func (u *Usecase) HierarchyLevels(ctx context.Context, rootTypeID model.ID, maxLevel int, reverse bool) (cibiz.HierarchyLevels, error) {
	if u.repo == nil || maxLevel <= 0 {
		return cibiz.HierarchyLevels{}, nil
	}
	rows, err := u.repo.ListTypeRelations(ctx, TypeRelationFilter{})
	if err != nil {
		return cibiz.HierarchyLevels{}, err
	}

	graph := NewRelationGraph(rows)
	edges := indexTypeEdges(rows)

	out := cibiz.HierarchyLevels{TypesAtLevel: map[int][]model.ID{}}

	frontier := []model.ID{rootTypeID}
	for level := 1; level <= maxLevel; level++ {
		next := make([]model.ID, 0, len(frontier))
		seen := map[model.ID]bool{}
		for _, id := range frontier {
			neighbours := graph.Children(id)
			if reverse {
				neighbours = graph.Parents(id)
			}
			for _, n := range neighbours {
				if !seen[n] {
					seen[n] = true
					next = append(next, n)
				}
			}
		}
		out.TypesAtLevel[level] = next
		if len(next) == 0 {
			break
		}
		frontier = next
	}

	out.ManyToManyLevels = manyToManyLevels(rootTypeID, maxLevel, graph, edges)
	return out, nil
}

// manyToManyLevels reproduces `get_level2constraint`: walk down from the root's
// model, and at each level that has a many-to-many child edge, record the level
// and continue from that child.
//
// It advances to the FIRST many-to-many child in id order. cantor took the
// first row the database returned, which is unspecified — so on a model with
// two many-to-many children the two implementations can walk different
// branches. Ordering by id is what makes this one answer instead of an
// accident of storage order.
func manyToManyLevels(rootTypeID model.ID, maxLevel int, graph *RelationGraph, edges map[typeEdgeKey]typeEdge) []int {
	var levels []int
	current := rootTypeID
	for level := 1; level <= maxLevel; level++ {
		advanced := false
		for _, child := range graph.Children(current) {
			if edges[typeEdgeKey{parent: current, child: child}].constraint == model.ConstraintMany2Many {
				current = child
				levels = append(levels, level)
				advanced = true
				break
			}
		}
		if !advanced {
			// Nothing many-to-many below, so no deeper level can be either:
			// the recorded levels are a contiguous prefix, which is what makes
			// "is there a many-to-many level before this one" answerable.
			break
		}
	}
	return levels
}

// typeEdgeKey identifies one model-to-model edge.
type typeEdgeKey struct {
	parent, child model.ID
}

// typeEdge is the rule declared on an edge.
type typeEdge struct {
	relationTypeID model.ID
	constraint     model.Constraint
}

// indexTypeEdges keys the relation rows by their pair.
func indexTypeEdges(rows []citypemodel.CITypeRelation) map[typeEdgeKey]typeEdge {
	out := make(map[typeEdgeKey]typeEdge, len(rows))
	for _, row := range rows {
		e := typeEdge{relationTypeID: row.RelationTypeID}
		if row.Constraint != nil {
			if c := model.Constraint(*row.Constraint); c.Valid() {
				e.constraint = c
			}
		}
		// First row wins, which the store's id ordering makes deterministic.
		key := typeEdgeKey{parent: row.ParentID, child: row.ChildID}
		if _, seen := out[key]; !seen {
			out[key] = e
		}
	}
	return out
}
