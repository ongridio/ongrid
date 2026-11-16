package ci

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Bounds on a path search.
//
// EVERY ONE OF THEM REPLACES SOMETHING CANTOR LEFT UNBOUNDED, and the shape of
// the enumeration is why they are needed. cantor asked networkx for
// `all_simple_paths(cutoff=6)` and MATERIALISED every result before paging it:
// measured on a six-level chain that forks and rejoins once per level, that is
// 64 paths for a graph with eleven instances, and the number of simple paths in
// a layered graph grows with the product of the layer sizes.
const (
	// MaxPathEdges is cantor's `cutoff`, kept because the console's service
	// trees are shallower than it: a chain longer than this can never match,
	// and the request is refused rather than silently answered with nothing.
	MaxPathEdges = 6

	// MaxPathSourceInstances bounds the instances a source expression may
	// select. They seed the graph walk, so an unbounded set is an unbounded
	// traversal.
	MaxPathSourceInstances = 5000

	// MaxPathGraphNodes bounds the graph the walk builds.
	MaxPathGraphNodes = 20000

	// MaxPathsFound bounds the enumeration. cantor held all of them in memory
	// and reported the count it had; truncating here would make the reported
	// total a lie, so the limit is an error.
	MaxPathsFound = 2000
)

// Errors of the path search.
var (
	// ErrPathTooLong is returned for a declared chain longer than the walk's
	// cutoff.
	ErrPathTooLong = fmt.Errorf("%w: a path may name at most %d relations", errs.ErrInvalid, MaxPathEdges)

	// ErrPathUnresolved is returned when a declared chain names an edge the
	// model graph does not have. cantor dereferenced the missing relation type
	// and crashed with an AttributeError.
	ErrPathUnresolved = fmt.Errorf("%w: the path names a relation the models do not declare", errs.ErrInvalid)

	// ErrTooManyPaths is returned when the enumeration exceeds its bound.
	// cantor returned everything it found, which for a layered graph is
	// exponential in the number of layers.
	ErrTooManyPaths = fmt.Errorf("%w: the paths between these instances exceed the search's bound", errs.ErrInvalid)
)

// PathSearchRequest is one path enumeration.
type PathSearchRequest struct {
	// SourceTypeID is the model the paths start from.
	SourceTypeID model.ID

	// SourceQuery and TargetQuery narrow which instances may be an endpoint.
	// Empty selects every instance of the model. A query that already names a
	// model (`_type:...`) is used as the caller wrote it.
	SourceQuery string
	TargetQuery string

	// Paths are the declared chains of models to walk, each a list of model
	// ids from the source's model to the target's. THE ORDER INSIDE A CHAIN IS
	// THE DIRECTION OF THE RELATIONS and is not interchangeable.
	//
	// The target model is READ FROM THE CHAINS, not from a separate field:
	// cantor overwrote its `target.type_ids` with the chains' tails before
	// doing anything, so a caller's own target list was never consulted.
	Paths [][]model.ID

	// Page is 1-based; Count is the page size.
	Page  int
	Count int
}

// PathSearchResult is one page of enumerated paths.
type PathSearchResult struct {
	// NumFound is the number of paths BEFORE paging; Total is the number on
	// this page. Both are cantor's, and the console reads `numfound` for the
	// count and `paths` for the rows.
	NumFound int `json:"numfound"`
	Total    int `json:"total"`
	Page     int `json:"page"`

	// Counter is the number of paths per alias chain, over the page.
	Counter map[string]int `json:"counter"`

	// Paths groups this page's rows by alias chain, each row a list of
	// instance ids from source to target.
	Paths map[string][][]model.ID `json:"paths"`

	// ID2CI describes every instance on the page. A TARGET instance carries its
	// attributes; every other one carries only the model's display value and
	// its model, which is all the console renders for a path's interior nodes.
	ID2CI map[string]map[string]any `json:"id2ci"`

	// RelationTypes is src model alias -> dst model alias -> relation name.
	RelationTypes map[string]map[string]string `json:"relation_types"`

	// Type2ShowKey is model id -> the attribute the paths label that model by.
	Type2ShowKey map[string]string `json:"type2show_key"`
}

// SearchByPath enumerates the instance paths that follow a declared chain of
// models.
//
// THE ENUMERATION IS TYPE-DIRECTED, WHICH IS WHAT MAKES IT FINITE. cantor asked
// networkx for every simple path between the source set and the target set and
// THEN discarded the ones whose model sequence was not one of the declared
// chains. Since acceptance depends only on the model sequence, rejecting a
// branch as soon as its next node's model cannot appear at that position yields
// exactly the same set of accepted paths while never generating the rest — and
// the rest are the majority: on a graph whose models mostly do not match the
// chain, cantor's search generated and discarded almost everything it found.
//
// IT DOES NOT NESSE THE MANY-TO-MANY PATH SCOPE. cantor's `_build_graph` reads
// only the plain child map, so an edge recorded under an ancestor path is
// followed here regardless of which path it belongs to. That is reproduced, not
// fixed: the path search is given the chains to follow explicitly, so the model
// sequence already constrains the walk, and changing the edge rule here would
// make two endpoints of the same product disagree about the same graph.
func (u *Usecase) SearchByPath(ctx context.Context, req PathSearchRequest) (*PathSearchResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if req.SourceTypeID <= 0 {
		return nil, fmt.Errorf("%w: source type", errs.ErrInvalid)
	}
	if len(req.Paths) == 0 {
		return nil, fmt.Errorf("%w: path", errs.ErrInvalid)
	}

	plan, err := u.planPaths(ctx, req)
	if err != nil {
		return nil, err
	}

	sourceIDs, err := u.instanceIDs(ctx, scopedQuery("_type", []model.ID{req.SourceTypeID}, req.SourceQuery), nil, MaxPathSourceInstances)
	if err != nil {
		return nil, err
	}
	if len(sourceIDs) == 0 {
		return emptyPathResult(req.Page, plan), nil
	}

	graph, typeOf, err := u.pathGraph(ctx, plan, sourceIDs, req.SourceTypeID)
	if err != nil {
		return nil, err
	}
	if len(graph) == 0 {
		return emptyPathResult(req.Page, plan), nil
	}

	// The targets are the graph's instances of the declared target models,
	// narrowed by the caller's own expression. cantor ran that expression as a
	// search RESTRICTED to the ids it had collected, which is what the
	// restriction here does.
	candidates := make([]model.ID, 0, len(graph))
	for id := range graph {
		if plan.targetTypes[typeOf[id]] {
			candidates = append(candidates, id)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	targetIDs, err := u.instanceIDs(ctx, scopedQuery("_type", plan.targetTypeList(), req.TargetQuery),
		&search.IDTerm{IDs: candidates}, MaxPathGraphNodes)
	if err != nil {
		return nil, err
	}
	if len(targetIDs) == 0 {
		return emptyPathResult(req.Page, plan), nil
	}
	targets := make(map[model.ID]bool, len(targetIDs))
	for _, id := range targetIDs {
		targets[id] = true
	}

	paths, err := findPaths(graph, typeOf, sourceIDs, targets, plan)
	if err != nil {
		return nil, err
	}
	numFound := len(paths)
	if numFound == 0 {
		return emptyPathResult(req.Page, plan), nil
	}

	page, size := clampPage(req.Page, req.Count)
	lo := (page - 1) * size
	if lo > len(paths) {
		lo = len(paths)
	}
	hi := lo + size
	if hi > len(paths) {
		hi = len(paths)
	}
	// numfound counts every path and total counts the page's, which is
	// cantor's pair of names and the reason both are reported.
	return u.renderPathPage(ctx, plan, typeOf, paths[lo:hi], page, numFound)
}

// --- the declared chains ---

// pathPlan is everything the walk and the response need from the declared
// chains, resolved once.
type pathPlan struct {
	// level2type is the set of models allowed at each level, counting from 1
	// one hop below the source.
	level2type map[int]map[model.ID]bool

	// chains are the declared model sequences, kept for the enumeration's
	// pruning.
	chains [][]model.ID

	// targetTypes is the union of the chains' tails.
	targetTypes map[model.ID]bool

	// relationTypes and type2show are the response's two label maps.
	relationTypes map[string]map[string]string
	type2show     map[string]string

	depth int
}

func (p *pathPlan) targetTypeList() []model.ID {
	out := make([]model.ID, 0, len(p.targetTypes))
	for id := range p.targetTypes {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// planPaths resolves the declared chains against the model graph.
//
// AN EDGE THE GRAPH DOES NOT DECLARE IS REFUSED. cantor dereferenced the
// relation type it could not find and died with an AttributeError, so a caller
// who wrote a chain backwards got a 500 rather than a sentence saying so.
func (u *Usecase) planPaths(ctx context.Context, req PathSearchRequest) (*pathPlan, error) {
	sets := newTypeSetCache(u)
	plan := &pathPlan{
		level2type:    map[int]map[model.ID]bool{},
		targetTypes:   map[model.ID]bool{},
		relationTypes: map[string]map[string]string{},
		type2show:     map[string]string{},
	}

	for _, chain := range req.Paths {
		if len(chain) < 2 {
			return nil, fmt.Errorf("%w: a path needs a source model and a target model", errs.ErrInvalid)
		}
		if len(chain)-1 > MaxPathEdges {
			return nil, fmt.Errorf("%w: %d", ErrPathTooLong, len(chain)-1)
		}
		plan.chains = append(plan.chains, chain)
		plan.targetTypes[chain[len(chain)-1]] = true
		if len(chain)-1 > plan.depth {
			plan.depth = len(chain) - 1
		}
		for level, typeID := range chain[1:] {
			if plan.level2type[level+1] == nil {
				plan.level2type[level+1] = map[model.ID]bool{}
			}
			plan.level2type[level+1][typeID] = true
		}
	}

	for _, chain := range plan.chains {
		for idx, parentType := range chain[:len(chain)-1] {
			childType := chain[idx+1]
			rule, ok, err := u.types.RelationBetween(ctx, parentType, childType)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%w: %d → %d", ErrPathUnresolved, parentType, childType)
			}
			parentSet, err := sets.get(ctx, parentType)
			if err != nil {
				return nil, err
			}
			childSet, err := sets.get(ctx, childType)
			if err != nil {
				return nil, err
			}
			parentKey, err := showKeyName(parentType, parentSet)
			if err != nil {
				return nil, err
			}
			childKey, err := showKeyName(childType, childSet)
			if err != nil {
				return nil, err
			}
			plan.type2show[strconv.FormatInt(int64(parentType), 10)] = parentKey
			plan.type2show[strconv.FormatInt(int64(childType), 10)] = childKey

			if plan.relationTypes[parentSet.Type.Alias] == nil {
				plan.relationTypes[parentSet.Type.Alias] = map[string]string{}
			}
			plan.relationTypes[parentSet.Type.Alias][childSet.Type.Alias] = rule.RelationTypeName
		}
	}
	return plan, nil
}

// showKeyName is the attribute a model's instances are labelled by, which is
// `show_id or unique_id` — the same rule the tree read uses.
//
// A MODEL WITH NEITHER IS REFUSED. cantor dereferenced the missing attribute and
// died; a model with no identifier attribute is one the console cannot render
// in a path at all, so reporting which model is the useful answer.
func showKeyName(typeID model.ID, set *AttributeSet) (string, error) {
	if set == nil || set.ShowAttr.ID == 0 || set.ShowAttr.Name == "" {
		return "", fmt.Errorf("%w: model %d has no attribute to label an instance by", ErrPathUnresolved, typeID)
	}
	return set.ShowAttr.Name, nil
}

// scopedQuery builds cantor's `_type:...`-prefixed expression, leaving an
// expression that already names a model alone.
func scopedQuery(prefix string, typeIDs []model.ID, q string) string {
	trimmed := strings.TrimSpace(q)
	if strings.HasPrefix(trimmed, prefix+":") {
		return trimmed
	}
	parts := make([]string, 0, len(typeIDs))
	for _, id := range typeIDs {
		parts = append(parts, strconv.FormatInt(int64(id), 10))
	}
	scope := prefix + ":(" + strings.Join(parts, ";") + ")"
	if trimmed == "" {
		return scope
	}
	return scope + "," + trimmed
}

// --- the graph ---

// pathGraph walks the declared models' instances from the source set and
// returns the reachable edges, together with each node's model.
//
// ONLY NODES OF A DECLARED MODEL ARE FOLLOWED, which is what keeps the walk
// bounded by the chain rather than by the graph.
func (u *Usecase) pathGraph(ctx context.Context, plan *pathPlan, sourceIDs []model.ID, sourceType model.ID) (map[model.ID][]model.ID, map[model.ID]model.ID, error) {
	graph := map[model.ID][]model.ID{}
	typeOf := make(map[model.ID]model.ID, len(sourceIDs))
	frontier := make([]WalkKey, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		typeOf[id] = sourceType
		frontier = append(frontier, WalkKey{Path: strconv.FormatInt(int64(id), 10), Node: id, TypeID: sourceType})
	}

	seen := map[model.ID]bool{}
	for _, id := range sourceIDs {
		seen[id] = true
	}

	for level := 1; level <= plan.depth; level++ {
		allow := plan.level2type[level]
		if len(allow) == 0 || len(frontier) == 0 {
			break
		}
		// The plain child map: see the doc comment on SearchByPath.
		children, err := u.repo.WalkChildren(ctx, frontier, false)
		if err != nil {
			return nil, nil, err
		}
		next := make([]WalkKey, 0, len(children))
		for _, child := range children {
			if !allow[child.TypeID] {
				continue
			}
			graph[child.Parent] = append(graph[child.Parent], child.Node)
			typeOf[child.Node] = child.TypeID
			if !seen[child.Node] {
				seen[child.Node] = true
				if len(seen) > MaxPathGraphNodes {
					return nil, nil, fmt.Errorf("%w: the search reached %d instances, more than the %d it may walk",
						errs.ErrInvalid, len(seen), MaxPathGraphNodes)
				}
			}
			next = append(next, WalkKey{
				Path: child.Path, Node: child.Node, TypeID: child.TypeID, Parent: child.Parent,
			})
		}
		frontier = next
	}
	return graph, typeOf, nil
}

// findPaths enumerates the simple paths that follow one of the declared chains.
//
// SIMPLE MEANS NO REPEATED NODE, which is networkx's rule and the reason a
// cyclic relation graph terminates here. The model sequence is checked as the
// walk descends, so a branch that cannot be a declared chain is never expanded.
func findPaths(graph map[model.ID][]model.ID, typeOf map[model.ID]model.ID, sources []model.ID, targets map[model.ID]bool, plan *pathPlan) ([][]model.ID, error) {
	var out [][]model.ID

	// canFollow reports whether stepping from the current path to `next` keeps
	// it on some declared chain. The whole chain must match up to the current
	// position — a node reached by a route that already left every chain is
	// dead, and this is where the enumeration stops generating it.
	canFollow := func(path []model.ID, next model.ID) bool {
		depth := len(path) - 1
		for _, chain := range plan.chains {
			if len(chain) <= depth+1 {
				continue
			}
			match := true
			for i := 0; i <= depth; i++ {
				if chain[i] != typeOf[path[i]] {
					match = false
					break
				}
			}
			if match && chain[depth+1] == typeOf[next] {
				return true
			}
		}
		return false
	}

	// matchesChain reports whether a completed path is one of the declared
	// chains. Two chains can share a prefix, so this is a separate question
	// from whether the walk could take the step.
	matchesChain := func(path []model.ID) bool {
		for _, chain := range plan.chains {
			if len(chain) != len(path) {
				continue
			}
			match := true
			for i := range chain {
				if chain[i] != typeOf[path[i]] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	}

	var walk func(path []model.ID, onPath map[model.ID]bool) error
	walk = func(path []model.ID, onPath map[model.ID]bool) error {
		node := path[len(path)-1]
		if targets[node] {
			// A TARGET ENDS THE PATH — networkx does not walk through one — and
			// a path that reaches one whose models are not a declared chain is
			// discarded, which is cantor's post-filter applied where it still
			// saves the work of enumerating past the target.
			if matchesChain(path) {
				out = append(out, append([]model.ID(nil), path...))
				if len(out) > MaxPathsFound {
					return ErrTooManyPaths
				}
			}
			return nil
		}
		if len(path)-1 >= MaxPathEdges {
			return nil
		}
		for _, next := range graph[node] {
			if onPath[next] || !canFollow(path, next) {
				continue
			}
			onPath[next] = true
			if err := walk(append(path, next), onPath); err != nil {
				return err
			}
			delete(onPath, next)
		}
		return nil
	}

	for _, source := range sources {
		if err := walk([]model.ID{source}, map[model.ID]bool{source: true}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- the page ---

// emptyPathResult is the answer to a search that found no path.
//
// ITS `id2ci` IS EMPTY, AND CANTOR'S WAS NOT. cantor passed an empty id set
// into its own search, and the guard that would have skipped the query only
// fires when a NON-empty set is filtered down to nothing — so an empty set let
// the expression run unrestricted and the response carried every instance of
// every model the path named. Measured: a search that found no path returned
// five instances. They describe instances that are not on any path, in a
// response whose `paths` is empty.
func emptyPathResult(page int, plan *pathPlan) *PathSearchResult {
	if page < 1 {
		page = 1
	}
	return &PathSearchResult{
		Page:          page,
		Counter:       map[string]int{},
		Paths:         map[string][][]model.ID{},
		ID2CI:         map[string]map[string]any{},
		RelationTypes: plan.relationTypes,
		Type2ShowKey:  plan.type2show,
	}
}

// renderPathPage renders one page of paths.
//
// THE GROUPING KEY IS THE ALIAS CHAIN, while the ENUMERATION matched on the
// model-ID chain — cantor's own split. Two chains whose models have the same
// aliases therefore merge into one group, and paths that share a model
// sequence but differ in the instances they pass through share a counter entry.
// Reproduced: the console renders one section per key, and the sections are
// what a person reads.
func (u *Usecase) renderPathPage(ctx context.Context, plan *pathPlan, typeOf map[model.ID]model.ID, pagePaths [][]model.ID, page, numFound int) (*PathSearchResult, error) {
	sets := newTypeSetCache(u)
	alias := map[model.ID]string{}
	keyName := map[model.ID]string{}
	for _, path := range pagePaths {
		for _, id := range path {
			typeID := typeOf[id]
			if _, done := alias[typeID]; done {
				continue
			}
			set, err := sets.get(ctx, typeID)
			if err != nil {
				return nil, err
			}
			alias[typeID] = set.Type.Alias
			name, err := showKeyName(typeID, set)
			if err != nil {
				return nil, err
			}
			keyName[typeID] = name
		}
	}

	result := &PathSearchResult{
		NumFound:      numFound,
		Total:         len(pagePaths),
		Page:          page,
		Counter:       map[string]int{},
		Paths:         map[string][][]model.ID{},
		ID2CI:         map[string]map[string]any{},
		RelationTypes: plan.relationTypes,
		Type2ShowKey:  plan.type2show,
	}

	seen := map[model.ID]bool{}
	var targets, others []model.ID
	for _, path := range pagePaths {
		group := make([]string, 0, len(path))
		for _, id := range path {
			group = append(group, alias[typeOf[id]])
			if seen[id] {
				continue
			}
			seen[id] = true
			if plan.targetTypes[typeOf[id]] {
				targets = append(targets, id)
			} else {
				others = append(others, id)
			}
		}
		key := strings.Join(group, "-")
		result.Paths[key] = append(result.Paths[key], path)
		result.Counter[key]++
	}

	// The targets carry their attributes; cantor kept the whole row for them
	// and reduced everything else. A target's row is built by the same renderer
	// a search result uses, so a path cannot expose a value a search would not.
	if len(targets) > 0 {
		plan, err := u.compileQuery(ctx, SearchRequest{Query: scopedQuery("_type", plan.targetTypeList(), "")},
			&search.IDTerm{IDs: targets})
		if err != nil {
			return nil, err
		}
		rows, _, err := u.repo.QueryInstances(ctx, *plan, len(targets), 0)
		if err != nil {
			return nil, err
		}
		hits, err := u.renderHits(ctx, rows, nil, RetKeyName)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			row := make(map[string]any, len(hit.Values)+6)
			for k, v := range hit.Values {
				row[k] = v
			}
			row["_id"] = hit.ID
			row["_type"] = hit.TypeID
			row["ci_type"] = hit.TypeName
			row["ci_type_alias"] = hit.TypeAlias
			if hit.UniqueAttr != "" {
				row["unique"] = hit.UniqueAttr
				row["unique_alias"] = hit.UniqueAlias
			}
			result.ID2CI[strconv.FormatInt(int64(hit.ID), 10)] = row
		}
	}

	// Everything else is reduced to its label, its model alias and its model —
	// no attributes, no id, which is what cantor sent and all the console draws
	// for a path's interior nodes.
	if len(others) > 0 {
		byType := map[model.ID][]model.ID{}
		for _, id := range others {
			byType[typeOf[id]] = append(byType[typeOf[id]], id)
		}
		idsByType := make([]model.ID, 0, len(byType))
		for typeID := range byType {
			idsByType = append(idsByType, typeID)
		}
		sort.Slice(idsByType, func(i, j int) bool { return idsByType[i] < idsByType[j] })
		for _, typeID := range idsByType {
			set, err := sets.get(ctx, typeID)
			if err != nil {
				return nil, err
			}
			values, err := u.labels(ctx, set, byType[typeID])
			if err != nil {
				return nil, err
			}
			for _, id := range byType[typeID] {
				row := map[string]any{
					"ci_type_alias": alias[typeID],
					"_type":         typeID,
				}
				if pair := values[id]; pair.title != nil {
					row[keyName[typeID]] = *pair.title
				} else if pair.unique != nil {
					row[keyName[typeID]] = *pair.unique
				} else {
					row[keyName[typeID]] = nil
				}
				result.ID2CI[strconv.FormatInt(int64(id), 10)] = row
			}
		}
	}
	return result, nil
}
