package ci

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// StatisticsRequest is one descendant count over a set of roots.
type StatisticsRequest struct {
	// RootIDs are the instances to count below. Each is counted SEPARATELY —
	// the result is keyed by root, matching cantor's response, which the
	// console reads as `stats[rootId]`.
	RootIDs []model.ID

	// Level is the DEPTH to count at, counting from 1 at a root's direct
	// neighbours. Unlike the hierarchy search's `level` it is a single depth,
	// not a list of levels to return: the answer is the number of instances
	// sitting exactly this far below each root.
	Level int

	// TypeIDs restricts the count to these models. Empty counts every model.
	TypeIDs []model.ID

	// AncestorIDs scopes the walk to a path the caller has already descended
	// into, as in the hierarchy search.
	AncestorIDs string
}

// Statistics is the per-root descendant count.
//
// THE COUNTS ARE KEYED BY STRING because that is what cantor's JSON object was
// keyed by and what the console indexes with `stats[String(id)]`. Keeping the
// numeric key would be tidier in Go and would silently stop matching the
// caller.
type Statistics struct {
	// Counts holds, per root instance, how many instances sit at Level.
	Counts map[string]int64

	// Detail holds the same nodes broken down by model, per root. A model with
	// no instances at that depth is ABSENT rather than zero, which is what
	// cantor's `Counter` produced.
	Detail map[string]map[string]int64
}

// Statistics counts the instances at one depth below each root.
//
// THE COUNT IS PER ROOT AND NOT MERGED, which is why the walk carries a set of
// originating roots on every node instead of being run once per root: the
// console sends EVERY instance of the root model (its root loader asks for ten
// thousand), and a walk per root would be a query per root per level. One walk
// with per-node provenance is a query per level, and it answers the same
// question.
//
// TWO DEVIATIONS FROM CANTOR, both measured against its own output:
//
//   - A DIAMOND COUNTS ONCE. cantor's count was the length of a list built by
//     expanding every path, so an instance reachable two ways was counted
//     twice — on its fixture, `statistics(root=a1, level=2)` returned 3 for the
//     2 instances below a1, and `level=3` returned 2 for the single instance
//     below it. A badge reading "3 hosts" under a node that has two is simply
//     wrong, and it is the same defect the hierarchy search's duplicate rows
//     had.
//   - A MANY-TO-MANY WALK REACHES ITS THIRD LEVEL. cantor's `statistics` built
//     the path-scoped key by joining the PREVIOUS composite key with the next
//     child, producing "9,10,11" for a depth it had addressed as "9,10" the
//     step before; no such key is ever written, so the call returned zero for a
//     subtree that exists. cantor's OWN `search_full` walks that subtree
//     correctly — the two endpoints disagreed about the same hierarchy — and
//     this port walks it, because the walk is one implementation.
func (u *Usecase) Statistics(ctx context.Context, req StatisticsRequest) (*Statistics, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if len(req.RootIDs) == 0 {
		return nil, fmt.Errorf("%w: root_ids", errs.ErrInvalid)
	}
	if req.Level < 1 || req.Level > MaxHierarchyLevel {
		return nil, fmt.Errorf("%w: level %d is outside 1..%d", errs.ErrInvalid, req.Level, MaxHierarchyLevel)
	}

	roots := make([]*cimodel.CI, 0, len(req.RootIDs))
	for _, id := range req.RootIDs {
		ci, err := u.loadInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		roots = append(roots, ci)
	}

	_, pathScoped, err := u.hierarchyScope(ctx, roots, req.Level, false, req.AncestorIDs)
	if err != nil {
		return nil, err
	}

	want := make(map[model.ID]bool, len(req.TypeIDs))
	for _, id := range req.TypeIDs {
		want[id] = true
	}

	out := &Statistics{
		Counts: make(map[string]int64, len(roots)),
		Detail: make(map[string]map[string]int64, len(roots)),
	}

	// The final level's nodes, per root, mapped to their model so the per-model
	// breakdown costs nothing extra. Only the deepest level is counted: cantor
	// REPLACED its accumulator at each level, so the answer is the node set at
	// `Level`, and the type filter — applied only at that level — is a filter
	// on the counted set rather than the defect it looks like on a first read.
	atDepth := make(map[model.ID]map[model.ID]model.ID, len(roots))

	frontier := make([]WalkKey, 0, len(roots))
	// provenance maps a frontier node's PATH to the roots that reached it.
	// Keyed by path rather than by node because a path-scoped walk reaches the
	// same instance once per path, and the next step is addressed by path.
	provenance := make(map[string][]model.ID, len(roots))
	for _, root := range roots {
		path := fmt.Sprint(root.ID)
		if req.AncestorIDs != "" {
			path = req.AncestorIDs + "," + path
		}
		key := WalkKey{Path: path, Node: root.ID, TypeID: root.TypeID}
		frontier = append(frontier, key)
		provenance[path] = append(provenance[path], root.ID)
	}

	for level := 1; level <= req.Level; level++ {
		scoped := pathScoped
		if level == 1 {
			scoped = req.AncestorIDs != ""
		}
		next, err := u.repo.WalkChildren(ctx, frontier, scoped)
		if err != nil {
			return nil, err
		}
		if len(next) > MaxHierarchyInstances {
			return nil, fmt.Errorf("%w: level %d of the hierarchy reached %d instances, which is more than the %d a request may address; narrow the level",
				errs.ErrInvalid, level, len(next), MaxHierarchyInstances)
		}

		nextProvenance := make(map[string][]model.ID, len(next))
		for _, child := range next {
			parent := parentPathOf(child.Path)
			rootsHere := provenance[parent]
			if len(rootsHere) == 0 {
				// The parent was not on this level's frontier. It cannot
				// normally happen — a child is emitted with its parent's path —
				// but attributing an orphan to every root would be worse than
				// skipping it, so it is skipped.
				continue
			}
			nextProvenance[child.Path] = append(nextProvenance[child.Path], rootsHere...)
			if level == req.Level {
				if len(want) > 0 && !want[child.TypeID] {
					continue
				}
				for _, root := range rootsHere {
					if atDepth[root] == nil {
						atDepth[root] = map[model.ID]model.ID{}
					}
					// DEDUPLICATED — see the doc comment. An instance counted
					// once per path is counted once.
					atDepth[root][child.Node] = child.TypeID
				}
			}
		}

		if len(next) == 0 {
			break
		}
		frontier, provenance = next, nextProvenance
	}

	// Every root appears, with zero when nothing is below it: the console does
	// `nodes.forEach(n => n.number += stats[id] ?? 0)` per root, and omitting a
	// root would make "no descendants" indistinguishable from "not asked".
	for _, root := range roots {
		key := fmt.Sprint(root.ID)
		nodes := atDepth[root.ID]
		out.Counts[key] = int64(len(nodes))
		detail := map[string]int64{}
		for _, typeID := range nodes {
			detail[fmt.Sprint(typeID)]++
		}
		out.Detail[key] = detail
	}
	return out, nil
}

// parentPathOf strips the last segment of a walk path, which is the path of the
// node the hop started from.
func parentPathOf(path string) string {
	if idx := lastComma(path); idx >= 0 {
		return path[:idx]
	}
	return ""
}

func lastComma(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ',' {
			return i
		}
	}
	return -1
}
