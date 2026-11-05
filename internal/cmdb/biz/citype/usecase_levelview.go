package citype

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// MaxTreeLevels bounds how many levels a model's tree view may declare.
//
// The resource hierarchy is a fixed-depth structure — the console's menu calls
// it L1–L8 — and the column holds a JSON array with no width of its own, so a
// caller could otherwise store an arbitrarily large list in one request. Twelve
// leaves room for a deeper deployment than any the product describes while
// keeping the row small and the response bounded.
const MaxTreeLevels = 12

// TreeLevelView is one model's saved hierarchy levels, as the console reads
// them.
//
// The model's own identity fields are FLATTENED IN rather than nested, because
// that is the shape cantor returned and the 树视图 page reads `item.name` and
// `item.icon` directly off the same object as `item.levels`.
type TreeLevelView struct {
	ID model.ID `json:"id"`

	TypeID model.ID `json:"type_id"`

	// Name, Alias and Icon are the model's, so the page can label the entry
	// without a second request.
	Name  string `json:"name"`
	Alias string `json:"alias"`
	Icon  string `json:"icon,omitempty"`

	// Levels are the attributes the hierarchy shows, in order. An attribute
	// that has since been removed from the model is SKIPPED rather than
	// rendered as a nameless column — see ListTreeLevels.
	Levels []TypeAttribute `json:"levels"`
}

// ListTreeLevels returns a user's saved hierarchy levels.
//
// ORDER COMES FROM THE TREE ORDERING, not from the rows. cantor sorted by the
// user's c_pcto entries with is_tree set, which is the same ordering the tree
// page's sidebar uses — so the views appear in the order the models do, and a
// view for a model the user has never ordered sorts last rather than first.
// The fallback is "one past the highest position seen", the rule the sidebar
// already uses; sorting with a constant would insert unordered models
// alongside the first ordered one instead of after all of them.
//
// A LEVEL WHOSE ATTRIBUTE IS GONE IS DROPPED. cantor looked the id up in a
// cache and skipped the miss, which is why removing an attribute from a model
// can leave a stale id in this column; the port reproduces the skip and not the
// crash, because a page that renders nine of ten columns is still a page.
func (u *Usecase) ListTreeLevels(ctx context.Context, uid model.ID) ([]TreeLevelView, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rows, err := u.repo.LevelViews(ctx, uid)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []TreeLevelView{}, nil
	}

	orders, err := u.repo.TypeOrder(ctx, uid)
	if err != nil {
		return nil, err
	}
	rank := treeOrderRank(orders)
	sort.SliceStable(rows, func(i, j int) bool {
		ri, iOK := rank[rows[i].TypeID]
		rj, jOK := rank[rows[j].TypeID]
		if !iOK {
			ri = fallbackRank(rank)
		}
		if !jOK {
			rj = fallbackRank(rank)
		}
		if ri != rj {
			return ri < rj
		}
		return rows[i].TypeID < rows[j].TypeID
	})

	typeIDs := make([]model.ID, 0, len(rows))
	for _, row := range rows {
		typeIDs = append(typeIDs, row.TypeID)
	}
	views, err := u.TypesByID(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[model.ID]View, len(views))
	for _, v := range views {
		byID[v.Type.ID] = v
	}

	out := make([]TreeLevelView, 0, len(rows))
	for _, row := range rows {
		view, ok := byID[row.TypeID]
		if !ok {
			// The model is gone. The cascade removes these rows with it, so
			// reaching here means the cascade has not run — skip rather than
			// answer with a nameless entry, and let the cascade be the fix.
			continue
		}
		// One resolution per row, not per level: a model's effective attribute
		// set costs a bounded number of queries and asking for it once per
		// level would multiply that by the depth of the tree.
		eff, err := u.effectiveAttributes(ctx, &view)
		if err != nil {
			return nil, err
		}
		byAttr := make(map[model.ID]TypeAttribute, len(eff.Attributes))
		for _, a := range eff.Attributes {
			byAttr[a.ID] = a
		}

		levels := make([]TypeAttribute, 0, len(row.Levels))
		for _, attrID := range row.Levels {
			if a, ok := byAttr[attrID]; ok {
				levels = append(levels, a)
			}
		}

		item := TreeLevelView{
			ID:     row.ID,
			TypeID: row.TypeID,
			Name:   view.Type.Name,
			Alias:  view.Type.Alias,
			Levels: levels,
		}
		if view.Type.Icon != nil {
			item.Icon = *view.Type.Icon
		}
		out = append(out, item)
	}
	return out, nil
}

// SetTreeLevels replaces a user's hierarchy levels for a model.
//
// AN EMPTY LIST REMOVES THE VIEW. That is how the console expresses "stop
// customising this model" through a create-or-update endpoint, and it is why
// the empty list is not stored as an empty array: the page falls back to the
// model's defaults, while a stored empty array would claim the user had chosen
// to display nothing.
//
// EVERY LEVEL IS RESOLVED OR THE WRITE IS REFUSED. cantor rewrote the list in
// place and kept whatever did not match, so a typo was stored verbatim and
// silently dropped at read time — the page then showed fewer levels than the
// user had chosen, with nothing to say why. Naming the unresolvable level is
// the whole difference between a fixable mistake and an invisible one.
func (u *Usecase) SetTreeLevels(ctx context.Context, uid model.ID, typeRef string, levels []string) (*TreeLevelView, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, typeRef)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, fmt.Errorf("%w: model %q", errs.ErrNotFound, typeRef)
	}
	eff, err := u.effectiveAttributes(ctx, view)
	if err != nil {
		return nil, err
	}

	// cantor's list parameters drop empty entries, and the console sends an
	// empty string to mean "unsubscribe"; an empty entry is therefore "no
	// level", not "the attribute whose name is empty".
	cleaned := make([]string, 0, len(levels))
	for _, ref := range levels {
		if ref != "" {
			cleaned = append(cleaned, ref)
		}
	}
	if len(cleaned) > MaxTreeLevels {
		return nil, fmt.Errorf("%w: a tree view may declare at most %d levels, got %d",
			errs.ErrInvalid, MaxTreeLevels, len(cleaned))
	}

	resolved := make([]model.ID, 0, len(cleaned))
	for _, ref := range cleaned {
		attr, ok := findTypeAttribute(eff.Attributes, ref)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not an attribute of model %q",
				errs.ErrInvalid, ref, view.Type.Name)
		}
		resolved = append(resolved, attr.ID)
	}

	existing, err := u.repo.LevelView(ctx, uid, view.Type.ID)
	if err != nil {
		return nil, err
	}
	saved, err := u.repo.SaveLevelView(ctx, LevelViewSaveSpec{
		UID:    uid,
		TypeID: view.Type.ID,
		Levels: resolved,
		// The ordering entry is added only when the view is NEW. Editing an
		// existing view must not move the model in the sidebar: the position is
		// the user's, and it has its own endpoint.
		AppendOrder: existing == nil,
	})
	if err != nil {
		return nil, err
	}

	item := &TreeLevelView{
		TypeID: view.Type.ID,
		Name:   view.Type.Name,
		Alias:  view.Type.Alias,
		Levels: []TypeAttribute{},
	}
	if saved != nil {
		item.ID = saved.ID
		for _, id := range saved.Levels {
			if attr, ok := findTypeAttribute(eff.Attributes, strconv.FormatInt(int64(id), 10)); ok {
				item.Levels = append(item.Levels, attr)
			}
		}
	}
	if view.Type.Icon != nil {
		item.Icon = *view.Type.Icon
	}
	return item, nil
}

// findTypeAttribute resolves a level reference against a model's attributes.
//
// THREE FORMS, in cantor's order: a decimal id, then the name, then the alias.
// The order matters when a model has an attribute named "2024" — addressing it
// by name still works, because the id lookup fails first and falls through.
//
// The search runs over the model's EFFECTIVE set, inherited attributes
// included. cantor resolved against the model's own bindings only, so a
// hierarchy could not display a column the model demonstrably has; refusing
// that is an arbitrary restriction rather than a rule, and the attribute id
// stored here is the same either way.
func findTypeAttribute(attrs []TypeAttribute, ref string) (TypeAttribute, bool) {
	if id, err := strconv.ParseInt(ref, 10, 32); err == nil {
		for _, a := range attrs {
			if int64(a.ID) == id {
				return a, true
			}
		}
	}
	for _, a := range attrs {
		if a.Name == ref {
			return a, true
		}
	}
	for _, a := range attrs {
		if a.Alias != "" && a.Alias == ref {
			return a, true
		}
	}
	return TypeAttribute{}, false
}

// treeOrderRank maps a model to its position in the user's TREE ordering.
//
// The resource ordering is a different list on the same table — see
// PreferenceCITypeOrder.IsTree — and mixing the two would order the tree page
// by how the user arranged a page they were not looking at.
func treeOrderRank(orders []citypemodel.PreferenceCITypeOrder) map[model.ID]int {
	rank := make(map[model.ID]int, len(orders))
	for _, o := range orders {
		if !o.IsTree || o.TypeID == nil {
			continue
		}
		if _, seen := rank[*o.TypeID]; !seen {
			rank[*o.TypeID] = int(o.Order)
		}
	}
	return rank
}

// fallbackRank is where a model with no ordering entry sorts: after every model
// that has one.
func fallbackRank(rank map[model.ID]int) int {
	max := 0
	for _, r := range rank {
		if r > max {
			max = r
		}
	}
	return max + 1
}
