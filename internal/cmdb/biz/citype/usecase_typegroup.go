package citype

import (
	"context"
	"fmt"
	"sort"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ListTypeGroups returns the console's model groupings.
//
// includeUngrouped appends a trailing bucket of the models that belong to no
// group, flagged rather than left to be inferred from an empty name — cantor
// signalled that bucket by omitting the name key, which is the kind of
// convention that breaks the first time a group is legitimately nameless.
func (u *Usecase) ListTypeGroups(ctx context.Context, includeUngrouped bool) ([]ModelGroup, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	all, err := u.repo.ListTypes(ctx, ListFilter{})
	if err != nil {
		return nil, err
	}
	views, err := u.enrich(ctx, all)
	if err != nil {
		return nil, err
	}
	return u.typeGroupsWith(ctx, views, includeUngrouped)
}

// typeGroupsWith groups already-loaded models.
func (u *Usecase) typeGroupsWith(ctx context.Context, views []View, includeUngrouped bool) ([]ModelGroup, error) {
	groups, err := u.repo.TypeGroups(ctx)
	if err != nil {
		return nil, err
	}
	items, err := u.repo.TypeGroupItems(ctx)
	if err != nil {
		return nil, err
	}

	byGroup := make(map[model.ID][]model.ID, len(groups))
	groupOf := make(map[model.ID]model.ID, len(items))
	for _, it := range items {
		byGroup[it.GroupID] = append(byGroup[it.GroupID], it.TypeID)
		// A model belongs to one group. If rows from two groups exist — which
		// only a direct insert can produce — the first wins and the model is
		// not rendered twice.
		if _, seen := groupOf[it.TypeID]; !seen {
			groupOf[it.TypeID] = it.GroupID
		}
	}
	for gid, ids := range byGroup {
		byGroup[gid] = OrderTypes(ids, groupOrder(items, gid))
	}

	viewByID := make(map[model.ID]View, len(views))
	for _, v := range views {
		viewByID[v.Type.ID] = v
	}

	out := make([]ModelGroup, 0, len(groups)+1)
	for _, g := range groups {
		mg := ModelGroup{ID: g.ID, Name: g.Name, Types: []View{}}
		for _, id := range byGroup[g.ID] {
			if v, ok := viewByID[id]; ok {
				mg.Types = append(mg.Types, v)
			}
		}
		out = append(out, mg)
	}

	if includeUngrouped {
		rest := make([]View, 0)
		for _, v := range views {
			if _, grouped := groupOf[v.Type.ID]; !grouped {
				rest = append(rest, v)
			}
		}
		out = append(out, ModelGroup{Types: rest, IsOther: true})
	}
	return out, nil
}

// groupOrder extracts one group's membership order.
func groupOrder(items []citypemodel.CITypeGroupItem, groupID model.ID) []model.ID {
	type entry struct {
		id    model.ID
		order model.SmallInt
	}
	var entries []entry
	for _, it := range items {
		if it.GroupID == groupID {
			entries = append(entries, entry{id: it.TypeID, order: it.Order})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	out := make([]model.ID, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.id)
	}
	return out
}

// CreateTypeGroup adds a model grouping.
func (u *Usecase) CreateTypeGroup(ctx context.Context, name string) (*ModelGroup, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, err := normalizeTypeGroupName(name)
	if err != nil {
		return nil, err
	}
	if existing, err := u.repo.TypeGroupByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrTypeGroupExists, name)
	}
	created, err := u.repo.CreateTypeGroup(ctx, TypeGroupCreateSpec{Name: name})
	if err != nil {
		return nil, err
	}
	return &ModelGroup{ID: created.ID, Name: created.Name, Types: []View{}}, nil
}

// UpdateTypeGroupInput renames a group and sets its membership.
type UpdateTypeGroupInput struct {
	ID model.ID

	// Name, when non-empty, renames the group.
	Name string

	// TypeIDs, when non-nil, becomes the group's membership. Naming a model
	// that currently sits in ANOTHER group moves it, because a model belongs to
	// one group.
	TypeIDs *[]model.ID
}

// UpdateTypeGroup renames a group and sets which models it holds.
func (u *Usecase) UpdateTypeGroup(ctx context.Context, in UpdateTypeGroupInput) (*ModelGroup, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.ID <= 0 {
		return nil, errs.ErrInvalid
	}
	existing, err := u.repo.TypeGroupByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrTypeGroupNotFound
	}

	spec := TypeGroupUpdateSpec{ID: in.ID, Name: existing.Name}
	if strings.TrimSpace(in.Name) != "" {
		name, err := normalizeTypeGroupName(in.Name)
		if err != nil {
			return nil, err
		}
		other, err := u.repo.TypeGroupByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if other != nil && other.ID != in.ID {
			return nil, fmt.Errorf("%w: %s", ErrTypeGroupExists, name)
		}
		spec.Name = name
	}
	if in.TypeIDs != nil {
		ids := dedupeAttrIDs(*in.TypeIDs)
		if err := u.assertTypesExist(ctx, ids); err != nil {
			return nil, err
		}
		spec.TypeIDs = ids
	}

	if err := u.repo.UpdateTypeGroup(ctx, spec); err != nil {
		return nil, err
	}
	group, err := u.repo.TypeGroupByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, ErrTypeGroupNotFound
	}
	return &ModelGroup{ID: group.ID, Name: group.Name, Types: []View{}}, nil
}

// DeleteTypeGroup removes a group, or detaches some of its models.
//
// The two behaviours are one endpoint because cantor made them one, and the
// distinction is which argument is present: naming models detaches them and
// keeps the group, naming none removes the group and all its membership.
func (u *Usecase) DeleteTypeGroup(ctx context.Context, id model.ID, typeIDs []model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	existing, err := u.repo.TypeGroupByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrTypeGroupNotFound
	}
	return u.repo.DeleteTypeGroup(ctx, TypeGroupDeleteSpec{
		ID: id, TypeIDs: dedupeAttrIDs(typeIDs),
	})
}

// ReorderTypeGroups writes the groups' positions.
//
// Every id is checked to exist. cantor skipped the ones that did not, so a
// request that named a stale id silently reordered the rest to different
// positions than the caller asked for and reported success.
func (u *Usecase) ReorderTypeGroups(ctx context.Context, groupIDs []model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	ids := dedupeAttrIDs(groupIDs)
	if len(ids) == 0 {
		return errs.ErrInvalid
	}
	if err := u.assertTypeGroupsExist(ctx, ids); err != nil {
		return err
	}
	return u.repo.ReorderTypeGroups(ctx, TypeGroupReorderSpec{GroupIDs: ids})
}

// assertTypeGroupsExist reports the first group id that does not exist.
func (u *Usecase) assertTypeGroupsExist(ctx context.Context, ids []model.ID) error {
	for _, id := range ids {
		group, err := u.repo.TypeGroupByID(ctx, id)
		if err != nil {
			return err
		}
		if group == nil {
			return fmt.Errorf("%w: id=%d", ErrTypeGroupNotFound, id)
		}
	}
	return nil
}

// normalizeTypeGroupName validates and trims a group name.
func normalizeTypeGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrTypeGroupNameRequired
	}
	if len(name) > MaxTypeGroupNameLen {
		return "", fmt.Errorf("%w: %d > %d", ErrTypeGroupNameTooLong, len(name), MaxTypeGroupNameLen)
	}
	return name, nil
}
