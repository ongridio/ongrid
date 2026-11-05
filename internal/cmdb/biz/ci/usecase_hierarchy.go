package ci

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Bounds on a hierarchy walk.
//
// BOTH ARE NEW, and both are the fix for a defect cantor had no guard against.
// `level` came straight off the query string and drove a loop that expanded a
// frontier which can double per level, one hash read at a time; the console's
// menu calls the feature L1–L8 and nothing enforced that, so a caller could ask
// for level 1000 and make the service walk until it ran out of memory.
//
// MaxHierarchyInstances is the second ceiling, and it is sized against a
// MEASURED limit rather than a guess: the walked ids become a SQL IN list, and
// SQLite refuses a statement with more than 32766 bound variables (measured —
// it accepts 32766 and fails at 40000). 20000 leaves room for the query's own
// operands and is already far more instances than a hierarchy page renders.
const (
	MaxHierarchyLevel     = 8
	MaxHierarchyInstances = 20000
)

// HierarchyRequest is one hierarchy query.
type HierarchyRequest struct {
	// RootIDs are the instances the walk starts from.
	RootIDs []model.ID

	// Levels are the levels to RETURN, counting from 1 at the root's direct
	// neighbours. They are not a depth: `1,3` walks to level 3 and returns
	// levels 1 and 3, exactly as cantor's parameter did. Empty means level 1.
	Levels []int

	// Reverse walks towards the instances' parents instead of their children.
	Reverse bool

	// AncestorIDs is the path the whole walk is scoped by, for a hierarchy the
	// caller has already descended into.
	AncestorIDs string

	// Search is the CI search run over the walked instances. Its Query narrows
	// further and its projection, sorting, faceting and paging shape the page.
	Search SearchRequest
}

// HierarchyPage is one page of a hierarchy query.
type HierarchyPage struct {
	*SearchPage

	// Levels are the levels that were returned, ascending.
	Levels []int

	// Walked is how many instances the walk reached, before the query narrowed
	// them. It is reported so a caller can tell "the hierarchy has three
	// instances" from "the filter matched none of them".
	Walked int
}

// WalkHierarchy returns the instances at the requested levels of a hierarchy.
//
// THE WALK REPLACES TWO REDIS HASHES. cantor held the child map in
// `CMDB_CI_RELATION`, keyed by instance, and a path-scoped variant in
// `CMDB_CI_RELATION2`, keyed by "ancestor,parent"; both were written by a
// Celery task after the relation row committed, so the map could and did
// disagree with the table. Here each step is a query over c_ci_relations, which
// removes the whole class: there is no cache to go stale, no task to lose, and
// no way for a hierarchy to be missing a branch the database has.
func (u *Usecase) WalkHierarchy(ctx context.Context, req HierarchyRequest) (*HierarchyPage, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if len(req.RootIDs) == 0 {
		return nil, fmt.Errorf("%w: root_ids", errs.ErrInvalid)
	}
	levels, depth, err := normalizeLevels(req.Levels)
	if err != nil {
		return nil, err
	}

	roots := make([]*cimodel.CI, 0, len(req.RootIDs))
	for _, id := range req.RootIDs {
		ci, err := u.loadInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		roots = append(roots, ci)
	}

	// The model graph answers two questions, once per root model: which models
	// belong at each level, and which levels a many-to-many rule makes
	// path-scoped.
	scope, pathScoped, err := u.hierarchyScope(ctx, roots, depth, req.Reverse, req.AncestorIDs)
	if err != nil {
		return nil, err
	}

	walked, err := u.walkHierarchy(ctx, roots, levels, depth, pathScoped, req.Reverse, req.AncestorIDs)
	if err != nil {
		return nil, err
	}
	if len(walked) > MaxHierarchyInstances {
		return nil, fmt.Errorf("%w: the hierarchy reached %d instances, which is more than the %d a page may address; narrow the levels or the query",
			errs.ErrInvalid, len(walked), MaxHierarchyInstances)
	}

	if len(walked) == 0 {
		return &HierarchyPage{SearchPage: &SearchPage{
			Hits: []SearchHit{}, Page: 1, Counter: map[string]int{}, Facets: nil,
		}, Levels: levels}, nil
	}

	var restrict search.Node = &search.IDTerm{IDs: walked}
	if !queryNamesAType(req.Search.Query) {
		// cantor synthesised a type scope from the model graph for every level
		// the caller asked for, and only when their own expression named no
		// type. It acts as a consistency filter: an instance reached through an
		// edge whose model rule has since been removed is excluded, because the
		// graph no longer says that model belongs at that level.
		if types := typesForLevels(scope, levels); len(types) > 0 {
			restrict = &search.And{
				Left:  restrict,
				Right: &search.TypeTerm{Types: types},
			}
		}
	}

	// The page carries the WALK's bound rather than the search page's: the
	// instances are already the walk's, so a tighter ceiling here would drop
	// branches the walk was allowed to find. See searchWithLimit.
	page, err := u.searchWithLimit(ctx, req.Search, restrict, MaxHierarchyInstances)
	if err != nil {
		return nil, err
	}
	return &HierarchyPage{SearchPage: page, Levels: levels, Walked: len(walked)}, nil
}

// hierarchyScope asks the model graph what a walk needs to know: which models
// belong at each level, and whether the walk is addressed by path.
//
// IT IS SHARED BY EVERY WALK, and the rule lives here for that reason. cantor
// computed the second answer in one place (`Search.__init__`, which the
// hierarchy search AND `statistics` both use) and took it as a PARAMETER in
// another (`search_full`, whose caller sent `has_m2m` off the query string).
// Two implementations of one rule is how the two endpoints came to disagree
// about the same hierarchy; here there is one, and it is derived.
func (u *Usecase) hierarchyScope(ctx context.Context, roots []*cimodel.CI, depth int, reverse bool, ancestorIDs string) (map[int]map[model.ID]bool, bool, error) {
	scope := map[int]map[model.ID]bool{}
	pathScoped := ancestorIDs != ""
	for _, root := range roots {
		graph, err := u.types.HierarchyLevels(ctx, root.TypeID, depth, reverse)
		if err != nil {
			return nil, false, err
		}
		for level, ids := range graph.TypesAtLevel {
			if scope[level] == nil {
				scope[level] = map[model.ID]bool{}
			}
			for _, id := range ids {
				scope[level][id] = true
			}
		}
		// cantor decided this from the FIRST requested level, so `level=1,3`
		// walked three levels with the path scoping computed for one. The walk
		// goes to `depth`, and the scoping has to cover the same range.
		for _, level := range graph.ManyToManyLevels {
			if level < depth {
				pathScoped = true
			}
		}
	}
	return scope, pathScoped, nil
}

// walkHierarchy performs the traversal itself.
//
// THE RESULT IS DEDUPLICATED, which cantor's was not. cantor's `merge_ids` was
// built by appending each level's ids and could list the same instance once per
// path that reached it — a diamond produced it twice, and the `numfound` it
// reported counted the duplicates. Deduplicating cannot lose an instance, only
// the repetition, and a result list that shows one host twice is a rendering
// bug in every page that consumes it.
//
// The ORDER is first-seen, level by level, and within a level the order the
// store returned (by relation id). cantor's was the iteration order of a Redis
// hash, which it did not define.
func (u *Usecase) walkHierarchy(ctx context.Context, roots []*cimodel.CI, levels []int, depth int, pathScoped, reverse bool, ancestorIDs string) ([]model.ID, error) {
	want := map[int]bool{}
	for _, level := range levels {
		want[level] = true
	}

	var merged []model.ID
	seen := map[model.ID]bool{}
	collect := func(keys []WalkKey, level int) {
		if !want[level] {
			return
		}
		for _, key := range keys {
			if seen[key.Node] {
				continue
			}
			seen[key.Node] = true
			merged = append(merged, key.Node)
		}
	}

	if reverse {
		return u.walkUpwards(ctx, roots, depth, collect)
	}

	frontier := make([]WalkKey, 0, len(roots))
	for _, root := range roots {
		// The path a walk starts from: the caller's own ancestor path followed
		// by the root, or just the root. A single-element path is what makes the
		// first step a PLAIN child lookup even in a path-scoped walk — cantor's
		// behaviour, and the reason the second step of a many-to-many walk is
		// path-scoped without the caller having supplied a path.
		path := fmt.Sprint(root.ID)
		if ancestorIDs != "" {
			path = ancestorIDs + "," + path
		}
		frontier = append(frontier, WalkKey{Path: path, Node: root.ID, TypeID: root.TypeID})
	}

	for level := 1; level <= depth; level++ {
		// WHICH MAP THIS LEVEL READS. Level 1 is addressed by instance id
		// unless the caller named an ancestor path; from level 2 the walk is
		// addressed by the path it has built, whenever the walk is path-scoped
		// at all. See WalkChildren — inferring this from the frontier's keys
		// instead is what let a path-scoped walk read the plain map.
		scoped := pathScoped
		if level == 1 {
			scoped = ancestorIDs != ""
		}
		next, err := u.repo.WalkChildren(ctx, frontier, scoped)
		if err != nil {
			return nil, err
		}
		collect(next, level)
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	return merged, nil
}

// walkUpwards is cantor's `_get_reverse_ids`, reproduced.
//
// IT IS NOT THE MIRROR OF THE DOWNWARD WALK, and that is cantor's shape rather
// than a port decision. Each step also collects the last element of every
// ancestor path it crossed, and starting at the SECOND level a step's result is
// INTERSECTED with the paths collected on the previous one — so a hierarchy
// whose edges carry paths only walks up through the ones that agree. An
// intersection with an empty or absent set is skipped, which is why a plain
// hierarchy walks up normally.
func (u *Usecase) walkUpwards(ctx context.Context, roots []*cimodel.CI, depth int, collect func([]WalkKey, int)) ([]model.ID, error) {
	nodes := make([]model.ID, 0, len(roots))
	for _, root := range roots {
		nodes = append(nodes, root.ID)
	}

	var previousAncestors []model.ID
	havePrevious := false
	for level := 1; level <= depth; level++ {
		parents, ancestors, err := u.repo.WalkParents(ctx, nodes)
		if err != nil {
			return nil, err
		}

		if havePrevious && len(previousAncestors) > 0 {
			allowed := make(map[model.ID]bool, len(previousAncestors))
			for _, id := range previousAncestors {
				allowed[id] = true
			}
			kept := make([]WalkKey, 0, len(parents))
			for _, parent := range parents {
				if allowed[parent.Node] {
					kept = append(kept, parent)
				}
			}
			collect(kept, level)
		} else {
			collect(parents, level)
		}

		previousAncestors, havePrevious = ancestors, true
		nodes = nodes[:0]
		for _, parent := range parents {
			nodes = append(nodes, parent.Node)
		}
		if len(nodes) == 0 {
			break
		}
	}
	return nil, nil
}

// normalizeLevels validates the requested levels and returns them sorted, with
// the depth the walk must reach.
func normalizeLevels(requested []int) ([]int, int, error) {
	if len(requested) == 0 {
		return []int{1}, 1, nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(requested))
	for _, level := range requested {
		if level < 1 || level > MaxHierarchyLevel {
			return nil, 0, fmt.Errorf("%w: level %d is outside 1..%d", errs.ErrInvalid, level, MaxHierarchyLevel)
		}
		if seen[level] {
			continue
		}
		seen[level] = true
		out = append(out, level)
	}
	sort.Ints(out)
	return out, out[len(out)-1], nil
}

// queryNamesAType reports whether a caller's expression already scopes by
// model, in which case cantor used THEIRS rather than synthesising one.
func queryNamesAType(query string) bool {
	return strings.Contains(query, "_type:") ||
		strings.Contains(query, "type_id:") ||
		strings.Contains(query, "ci_type:")
}

// typesForLevels unions the models the graph assigns to the requested levels.
func typesForLevels(scope map[int]map[model.ID]bool, levels []int) []model.ID {
	seen := map[model.ID]bool{}
	var out []model.ID
	for _, level := range levels {
		ids := make([]model.ID, 0, len(scope[level]))
		for id := range scope[level] {
			ids = append(ids, id)
		}
		// Sorted so the synthesised scope is reproducible; the walk over a map
		// is not.
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
