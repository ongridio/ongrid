package citype

import (
	"context"
	"fmt"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Service-tree definition errors.
var (
	// ErrServiceTreeNameRequired is returned when a definition has no name.
	ErrServiceTreeNameRequired = fmt.Errorf("%w: service tree name is required", errs.ErrInvalid)

	// ErrServiceTreeNameTooLong is returned when the name exceeds the column.
	ErrServiceTreeNameTooLong = fmt.Errorf("%w: service tree name is too long", errs.ErrInvalid)

	// ErrServiceTreeNotFound is returned for a missing definition.
	ErrServiceTreeNotFound = fmt.Errorf("%w: service tree does not exist", errs.ErrNotFound)

	// ErrServiceTreeNotOwner refuses editing a definition belonging to someone
	// else. A public definition is readable by everyone and writable by its
	// owner.
	ErrServiceTreeNotOwner = fmt.Errorf("%w: only the definition's owner can change it", errs.ErrForbidden)

	// ErrSearchOptionNotFound is returned for a missing saved search.
	ErrSearchOptionNotFound = fmt.Errorf("%w: saved search does not exist", errs.ErrNotFound)

	// ErrSearchOptionExists is returned when the user already saved a search
	// under that name and scope.
	ErrSearchOptionExists = fmt.Errorf("%w: a saved search with that name already exists here", errs.ErrConflict)

	// ErrSearchOptionNameRequired is returned when a saved search has no name.
	ErrSearchOptionNameRequired = fmt.Errorf("%w: saved search name is required", errs.ErrInvalid)
)

// MaxServiceTreeNameLen is c_prv.name, String(64).
const MaxServiceTreeNameLen = 64

// MaxSearchOptionNameLen is c_pso.name, String(64).
const MaxSearchOptionNameLen = 64

// ServiceTreeDefinition is one definition as the API presents it.
type ServiceTreeDefinition struct {
	ID       model.ID
	Name     string
	IsPublic bool
	Option   map[string]any

	// Pairs is the stored chain, in order, which is what an edit sends back.
	Pairs []citypemodel.TypePair

	// Tree is the rendering of that chain: the order, the levels, the leaves,
	// and the models the resource menu will show under each leaf.
	Tree *ServiceTree
}

// ServiceTreeInput is one definition write.
type ServiceTreeInput struct {
	// ID, when non-zero, rewrites that definition. Zero creates.
	ID model.ID

	Name     string
	Pairs    []citypemodel.TypePair
	IsPublic bool
	Option   map[string]any
}

// ListServiceTrees returns the definitions a user may see, each rendered.
//
// includeAll widens the result to every definition, which is what the
// model-management page needs. The resource menu passes false: a service tree
// is a page, and a user should not be handed the shape of one they cannot open.
func (u *Usecase) ListServiceTrees(ctx context.Context, uid model.ID, includeAll bool) ([]ServiceTreeDefinition, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rows, err := u.repo.ServiceTrees(ctx, uid, includeAll)
	if err != nil {
		return nil, err
	}
	graph, constraints, err := u.relationGraph(ctx)
	if err != nil {
		return nil, err
	}
	known, err := u.knownTypeIDs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]ServiceTreeDefinition, 0, len(rows))
	for _, row := range rows {
		// A definition naming a model that no longer exists is reported WITHOUT
		// its rendering rather than taken as a reason to fail the request.
		// cantor looked each named model up and dereferenced the result, so one
		// stale definition answered 500 for the whole list — and because its
		// write path validated nothing, such a definition was easy to create
		// and permanent.
		item := ServiceTreeDefinition{
			ID: row.ID, Name: row.Name, IsPublic: row.IsPublic, Option: row.Option,
			Pairs: append([]citypemodel.TypePair{}, row.CRIDs...),
		}
		if missing := missingTypes(row.CRIDs, known); len(missing) > 0 {
			item.Tree = &ServiceTree{
				Name: row.Name, Order: []model.ID{}, Levels: [][]model.ID{}, Leaf: []model.ID{},
				LevelConstraints: map[int]string{},
				LeafShowTypes:    map[model.ID][]model.ID{},
				NodeShowTypes:    map[model.ID][]model.ID{},
				ShowTypes:        []model.ID{},
			}
			out = append(out, item)
			continue
		}
		tree, err := BuildServiceTree(row.Name, row.CRIDs, graph, constraints)
		if err != nil {
			return nil, fmt.Errorf("service tree %q: %w", row.Name, err)
		}
		tree.IsPublic = row.IsPublic
		tree.Option = row.Option
		item.Tree = tree
		out = append(out, item)
	}
	return out, nil
}

// SaveServiceTree creates or rewrites a definition.
//
// The pairs ARE validated, against both the models and the model-relation
// graph, and inside the write's transaction. cantor validated neither: a
// definition could name a model that did not exist or a pair that was not a
// relation, it was stored, and the reader then crashed on it — so the console
// could create a definition that broke the endpoint for every user, with no way
// back through the UI.
func (u *Usecase) SaveServiceTree(ctx context.Context, uid model.ID, in ServiceTreeInput) (*ServiceTreeDefinition, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, err := normalizeServiceTreeName(in.Name)
	if err != nil {
		return nil, err
	}
	if len(in.Pairs) == 0 {
		return nil, ErrServiceTreeEmpty
	}

	var previous *citypemodel.PreferenceRelationView
	if in.ID > 0 {
		previous, err = u.repo.ServiceTreeByID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if previous == nil {
			return nil, ErrServiceTreeNotFound
		}
		if previous.UID != uid {
			// A public definition is readable by all and writable by its owner.
			return nil, fmt.Errorf("%w: %q", ErrServiceTreeNotOwner, previous.Name)
		}
	} else {
		// Creating under a name the user already used replaces it, which is
		// cantor's behaviour and what the console's single save button implies.
		existing, err := u.repo.ServiceTreeByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.UID == uid {
			previous = existing
			in.ID = existing.ID
		}
	}

	graph, _, err := u.relationGraph(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.validateServiceTreePairs(ctx, in.Pairs, graph); err != nil {
		return nil, err
	}

	saved, err := u.repo.SaveServiceTree(ctx, ServiceTreeSaveSpec{
		ID: in.ID, UID: uid, Name: name, Pairs: in.Pairs,
		IsPublic: in.IsPublic, Option: in.Option, Previous: previous,
	})
	if err != nil {
		return nil, err
	}

	defs, err := u.ListServiceTrees(ctx, uid, true)
	if err != nil {
		return nil, err
	}
	for i := range defs {
		if defs[i].ID == saved.ID {
			return &defs[i], nil
		}
	}
	return nil, ErrServiceTreeNotFound
}

// DeleteServiceTree removes a definition by id.
//
// BY ID. cantor's delete took a NAME, ignored the id in the URL, and applied no
// user filter — so a request naming a shared name removed every definition with
// that name, other people's included.
func (u *Usecase) DeleteServiceTree(ctx context.Context, uid model.ID, id model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	row, err := u.repo.ServiceTreeByID(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrServiceTreeNotFound
	}
	if row.UID != uid {
		return fmt.Errorf("%w: %q", ErrServiceTreeNotOwner, row.Name)
	}
	return u.repo.DeleteServiceTree(ctx, ServiceTreeDeleteSpec{ID: id, Previous: *row})
}

// CanEditRelation reports whether a relation between two models may be created.
//
// THE RULE IS CANTOR'S, reproduced because the console depends on it rather
// than on any internal detail: a relation is refused when it would conflict
// with a service tree that already contains a many-to-many step. Concretely,
// editing (parent, child) is refused when either
//
//	(a) some definition contains exactly that pair AND that relation is
//	    many-to-many; or
//	(b) some definition contains any many-to-many step, and the CHILD of its
//	    last resolvable pair is `parent`, and `parent` -> `child` is a relation.
//
// The meaning is: you cannot hang a new relation off the bottom of a tree whose
// levels are already many-to-many, because the tree has no single instance to
// hang it from.
//
// What the port does NOT reproduce is the crash. cantor iterated the stored
// pairs and indexed each one as a mapping, so a definition whose pairs were
// stored as anything but objects raised; and it walked the pairs without asking
// whether the models still existed. Both are handled here by skipping what
// cannot be resolved — a definition naming a model that is gone cannot constrain
// a relation that model is not part of.
func (u *Usecase) CanEditRelation(ctx context.Context, parentID, childID model.ID) (bool, error) {
	if u.repo == nil {
		return false, errs.ErrNotWiredYet
	}
	rows, err := u.repo.ServiceTrees(ctx, 0, true)
	if err != nil {
		return false, err
	}
	graph, constraints, err := u.relationGraph(ctx)
	if err != nil {
		return false, err
	}

	for _, row := range rows {
		hasManyToMany := false
		var lastResolvableChild model.ID
		for _, pair := range row.CRIDs {
			if !graph.HasEdge(pair.ParentID, pair.ChildID) {
				continue
			}
			constraint := constraints(pair.ParentID, pair.ChildID)
			if constraint == string(model.ConstraintMany2Many) {
				hasManyToMany = true
				if pair.ParentID == parentID && pair.ChildID == childID {
					return false, nil
				}
			}
			lastResolvableChild = pair.ChildID
		}

		if lastResolvableChild == parentID && hasManyToMany && graph.HasEdge(lastResolvableChild, childID) {
			return false, nil
		}
	}
	return true, nil
}

// relationGraph loads the model-relation graph and a cardinality lookup.
func (u *Usecase) relationGraph(ctx context.Context) (*RelationGraph, func(parent, child model.ID) string, error) {
	rows, err := u.repo.ListTypeRelations(ctx, TypeRelationFilter{})
	if err != nil {
		return nil, nil, err
	}
	byPair := make(map[[2]model.ID]string, len(rows))
	for _, r := range rows {
		byPair[[2]model.ID{r.ParentID, r.ChildID}] = derefString(r.Constraint)
	}
	return NewRelationGraph(rows), func(parent, child model.ID) string {
		return byPair[[2]model.ID{parent, child}]
	}, nil
}

// knownTypeIDs returns the set of models that exist.
func (u *Usecase) knownTypeIDs(ctx context.Context) (map[model.ID]bool, error) {
	all, err := u.repo.ListTypes(ctx, ListFilter{})
	if err != nil {
		return nil, err
	}
	out := make(map[model.ID]bool, len(all))
	for _, ct := range all {
		out[ct.ID] = true
	}
	return out, nil
}

// missingTypes reports the model ids a definition names that do not exist.
func missingTypes(pairs []citypemodel.TypePair, known map[model.ID]bool) []model.ID {
	seen := map[model.ID]bool{}
	var out []model.ID
	for _, p := range pairs {
		for _, id := range []model.ID{p.ParentID, p.ChildID} {
			if !known[id] && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// validateServiceTreePairs refuses a definition naming a model or a relation
// that does not exist.
func (u *Usecase) validateServiceTreePairs(ctx context.Context, pairs []citypemodel.TypePair, graph *RelationGraph) error {
	known, err := u.knownTypeIDs(ctx)
	if err != nil {
		return err
	}
	if missing := missingTypes(pairs, known); len(missing) > 0 {
		return fmt.Errorf("%w: model %v", ErrServiceTreeUnknownModel, missing)
	}
	for _, p := range pairs {
		if p.ParentID == p.ChildID {
			return fmt.Errorf("%w: %d relates to itself", ErrServiceTreeCycle, p.ParentID)
		}
		if !graph.HasEdge(p.ParentID, p.ChildID) {
			return fmt.Errorf("%w: %d -> %d", ErrServiceTreeUnknownModel, p.ParentID, p.ChildID)
		}
	}
	return nil
}

func normalizeServiceTreeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrServiceTreeNameRequired
	}
	if len(name) > MaxServiceTreeNameLen {
		return "", fmt.Errorf("%w: %d > %d", ErrServiceTreeNameTooLong, len(name), MaxServiceTreeNameLen)
	}
	return name, nil
}

// --- saved searches ---

// SavedSearch is one saved search as the API presents it.
type SavedSearch struct {
	ID     model.ID
	Name   string
	Option map[string]any

	PRVID  *model.ID
	PTVID  *model.ID
	TypeID *model.ID
}

// ListSearchOptions returns a user's saved searches.
func (u *Usecase) ListSearchOptions(ctx context.Context, uid model.ID, f SearchOptionFilter) ([]SavedSearch, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	f.UID = uid
	rows, err := u.repo.SearchOptions(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]SavedSearch, 0, len(rows))
	for _, r := range rows {
		out = append(out, savedSearchView(r))
	}
	return out, nil
}

func savedSearchView(r citypemodel.PreferenceSearchOption) SavedSearch {
	view := SavedSearch{ID: r.ID, Option: r.Option, PRVID: r.PRVID, PTVID: r.PTVID, TypeID: r.TypeID}
	if r.Name != nil {
		view.Name = *r.Name
	}
	return view
}

// SearchOptionInput is one saved-search write.
type SearchOptionInput struct {
	Name   string
	Option map[string]any

	PRVID  *model.ID
	PTVID  *model.ID
	TypeID *model.ID
}

// CreateSearchOption saves a search.
//
// The three reserved names are the console's own lists, not user searches:
// __recent__ is the rolling history and is trimmed to its newest entries,
// __favor__ and __relation_favor__ are the user's favourites. Those names are
// persisted data, so they are kept verbatim — a console that renamed them would
// orphan every row already stored.
func (u *Usecase) CreateSearchOption(ctx context.Context, uid model.ID, in SearchOptionInput) (*SavedSearch, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, err := normalizeSearchOptionName(in.Name)
	if err != nil {
		return nil, err
	}
	if in.PRVID != nil && *in.PRVID > 0 {
		row, err := u.repo.ServiceTreeByID(ctx, *in.PRVID)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, ErrServiceTreeNotFound
		}
	}
	if in.TypeID != nil && *in.TypeID > 0 {
		if err := u.assertTypesExist(ctx, []model.ID{*in.TypeID}); err != nil {
			return nil, err
		}
	}

	// A user's own search with the same name and scope already exists.
	if !isReservedSearchName(name) {
		existing, err := u.repo.SearchOptions(ctx, SearchOptionFilter{
			UID: uid, Name: name, PRVID: derefID(in.PRVID), PTVID: derefID(in.PTVID), TypeID: derefID(in.TypeID),
		})
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrSearchOptionExists, name)
		}
	}

	created, err := u.repo.CreateSearchOption(ctx, SearchOptionCreateSpec{
		UID: uid, Name: name, Option: in.Option,
		PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
	})
	if err != nil {
		return nil, err
	}
	view := savedSearchView(*created)
	return &view, nil
}

// UpdateSearchOption rewrites a saved search.
//
// cantor's version crashed on EVERY call: it looked the duplicate up without
// asking for a single row, got a list back, and read `.id` off the list. The
// duplicate check here is a real one.
func (u *Usecase) UpdateSearchOption(ctx context.Context, uid model.ID, id model.ID, in SearchOptionInput) (*SavedSearch, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id <= 0 {
		return nil, errs.ErrInvalid
	}
	existing, err := u.repo.SearchOptionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSearchOptionNotFound
	}
	if existing.UID == nil || *existing.UID != uid {
		// A saved search is private. cantor reported this as a 400; the port
		// says forbidden, because the request is well formed.
		return nil, fmt.Errorf("%w: %d", errs.ErrForbidden, id)
	}

	name, err := normalizeSearchOptionName(in.Name)
	if err != nil {
		return nil, err
	}
	if !isReservedSearchName(name) {
		dupes, err := u.repo.SearchOptions(ctx, SearchOptionFilter{
			UID: uid, Name: name, PRVID: derefID(in.PRVID), PTVID: derefID(in.PTVID), TypeID: derefID(in.TypeID),
		})
		if err != nil {
			return nil, err
		}
		for _, d := range dupes {
			if d.ID != id {
				return nil, fmt.Errorf("%w: %s", ErrSearchOptionExists, name)
			}
		}
	}

	if err := u.repo.UpdateSearchOption(ctx, SearchOptionUpdateSpec{
		ID: id, UID: uid, Name: name, Option: in.Option,
		PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
	}); err != nil {
		return nil, err
	}
	updated, err := u.repo.SearchOptionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrSearchOptionNotFound
	}
	view := savedSearchView(*updated)
	return &view, nil
}

// DeleteSearchOption removes a saved search.
func (u *Usecase) DeleteSearchOption(ctx context.Context, uid model.ID, id model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	existing, err := u.repo.SearchOptionByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSearchOptionNotFound
	}
	if existing.UID == nil || *existing.UID != uid {
		return fmt.Errorf("%w: %d", errs.ErrForbidden, id)
	}
	return u.repo.DeleteSearchOption(ctx, id)
}

// isReservedSearchName reports whether a name is one of the console's lists.
func isReservedSearchName(name string) bool {
	switch name {
	case citypemodel.SearchOptionRecent,
		citypemodel.SearchOptionFavourite,
		citypemodel.SearchOptionRelationFavourite:
		return true
	}
	return false
}

func normalizeSearchOptionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrSearchOptionNameRequired
	}
	if len(name) > MaxSearchOptionNameLen {
		return "", fmt.Errorf("%w: %d > %d", ErrSearchOptionNameRequired, len(name), MaxSearchOptionNameLen)
	}
	return name, nil
}

func derefID(id *model.ID) model.ID {
	if id == nil {
		return 0
	}
	return *id
}
