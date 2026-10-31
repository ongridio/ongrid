package citype

import (
	"context"
	"fmt"
	"sort"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// AttributeGroup is a form section as the API presents it: a named, ordered
// list of a model's attributes.
type AttributeGroup struct {
	ID         model.ID        `json:"id"`
	Name       string          `json:"name"`
	TypeID     model.ID        `json:"type_id"`
	Order      model.SmallInt  `json:"order"`
	Inherited  bool            `json:"inherited"`
	Attributes []TypeAttribute `json:"attributes"`

	// IsOther marks the trailing catch-all section a caller can ask for, which
	// collects the attributes no group claims. cantor signalled it by omitting
	// the `name` key; an explicit flag is easier to branch on and survives a
	// group that happens to be nameless in some other way.
	IsOther bool `json:"is_other"`
}

// ListAttributeGroups returns a model's form sections, including the ones it
// inherits.
//
// THE MERGE RULE IS CANTOR'S, and two parts of it are surprising enough to
// state rather than leave to be rediscovered:
//
//   - Sections are merged BY NAME across the inheritance chain, and the merge
//     keeps the FIRST occurrence's position, order and inherited flag; only the
//     owning type is rewritten when the model itself also defines the name. So
//     a section a model defines itself can still be reported as inherited from
//     an ancestor that uses the same name.
//   - An attribute listed by two sections ends up in the LAST one, with that
//     section's inherited flag — not the attribute's own.
//
// Both are reproduced because the console's rendering is built around them, and
// neither can corrupt data. They are recorded here so a future reader does not
// mistake them for bugs in the port.
func (u *Usecase) ListAttributeGroups(ctx context.Context, ref string, needOther bool) ([]AttributeGroup, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}
	typeID := view.Type.ID

	effective, err := u.effectiveAttributes(ctx, view)
	if err != nil {
		return nil, err
	}
	byID := make(map[model.ID]TypeAttribute, len(effective.Attributes))
	for _, a := range effective.Attributes {
		byID[a.ID] = a
	}

	ancestors, err := u.repo.AncestorIDs(ctx, typeID)
	if err != nil {
		return nil, err
	}
	chain := make([]model.ID, 0, len(ancestors)+1)
	chain = append(chain, ancestors...)
	chain = append(chain, typeID)

	groups, err := u.repo.AttributeGroupsForTypes(ctx, chain)
	if err != nil {
		return nil, err
	}
	items, err := u.repo.AttributeGroupItems(ctx, groupIDsOf(groups))
	if err != nil {
		return nil, err
	}
	aliases, err := u.repo.TypeAliases(ctx, ancestors)
	if err != nil {
		return nil, err
	}

	byType := make(map[model.ID][]citypemodel.CITypeAttributeGroup, len(chain))
	for _, g := range groups {
		byType[g.TypeID] = append(byType[g.TypeID], g)
	}
	itemsByGroup := make(map[model.ID][]citypemodel.CITypeAttributeGroupItem, len(groups))
	for _, it := range items {
		itemsByGroup[it.GroupID] = append(itemsByGroup[it.GroupID], it)
	}

	// --- walk the chain, merging sections by name ---
	type section struct {
		id        model.ID
		name      string
		typeID    model.ID
		order     model.SmallInt
		inherited bool
		from      string
		attrs     []TypeAttribute
	}
	var ordered []*section
	posByName := map[string]int{}
	posByAttr := map[model.ID]int{}
	grouped := map[model.ID]bool{}

	for _, chainType := range chain {
		groupsOfType := byType[chainType]
		sort.SliceStable(groupsOfType, func(i, j int) bool { return groupsOfType[i].Order < groupsOfType[j].Order })

		for _, g := range groupsOfType {
			at, seen := posByName[g.Name]
			if !seen {
				at = len(ordered)
				s := &section{id: g.ID, name: g.Name, typeID: g.TypeID, order: g.Order}
				if chainType != typeID {
					s.inherited = true
					s.from = aliases[chainType]
				}
				ordered = append(ordered, s)
				posByName[g.Name] = at
			} else if g.TypeID == typeID {
				// cantor's only merge-time patch: the model's own section
				// reclaims ownership of the shared name.
				ordered[at].typeID = typeID
			}

			groupItems := itemsByGroup[g.ID]
			sort.SliceStable(groupItems, func(i, j int) bool { return groupItems[i].Order < groupItems[j].Order })
			for _, it := range groupItems {
				attr, ok := byID[it.AttrID]
				if !ok {
					// Not in the model's effective attribute set: either the
					// binding is gone or the attribute was deleted. cantor hid
					// these; so does the port.
					continue
				}
				grouped[it.AttrID] = true
				attr.Inherited = ordered[at].inherited
				attr.InheritedFrom = ordered[at].from

				// An attribute claimed by two sections belongs to the later one.
				if prev, dup := posByAttr[it.AttrID]; dup {
					ordered[prev].attrs = removeAttr(ordered[prev].attrs, it.AttrID)
				}
				ordered[at].attrs = append(ordered[at].attrs, attr)
				posByAttr[it.AttrID] = at
			}
		}
	}

	out := make([]AttributeGroup, 0, len(ordered)+1)
	for _, s := range ordered {
		attrs := s.attrs
		if attrs == nil {
			attrs = []TypeAttribute{}
		}
		out = append(out, AttributeGroup{
			ID: s.id, Name: s.name, TypeID: s.typeID, Order: s.order,
			Inherited: s.inherited, Attributes: attrs,
		})
	}

	if needOther {
		rest := make([]TypeAttribute, 0)
		for _, a := range effective.Attributes {
			if !grouped[a.ID] {
				rest = append(rest, a)
			}
		}
		out = append(out, AttributeGroup{Attributes: rest, IsOther: true})
	}
	return out, nil
}

// AttributeGroupSaveInput creates or updates a form section.
//
// The section is identified by NAME, so saving a section the model already has
// updates it rather than failing: cantor's console has one save button that does
// not know whether the section is new, and both of its endpoints landed on the
// same helper.
type AttributeGroupSaveInput struct {
	TypeRef string
	Name    string

	// Order is the section's position. Nil leaves an existing section's order
	// alone. cantor defaulted it to 0 and wrote it unconditionally, so renaming
	// a section silently moved it to the front.
	Order *model.SmallInt

	// AttrOrder is the membership as a display-ordered list; positions are
	// assigned from it. Nil leaves the membership alone; a non-nil empty slice
	// clears it. cantor could not express "clear" — an empty list was
	// indistinguishable from an omitted one.
	AttrOrder *[]model.ID
}

// SaveAttributeGroup creates or updates a form section and returns its id.
func (u *Usecase) SaveAttributeGroup(ctx context.Context, in AttributeGroupSaveInput) (model.ID, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, in.TypeRef)
	if err != nil {
		return 0, err
	}
	if view == nil {
		return 0, errs.ErrNotFound
	}
	name, err := normalizeGroupName(in.Name)
	if err != nil {
		return 0, err
	}
	if err := u.assertGroupAttrs(ctx, in.AttrOrder); err != nil {
		return 0, err
	}

	group, err := u.repo.SaveAttributeGroup(ctx, AttributeGroupSaveSpec{
		TypeID: view.Type.ID, Name: name, Order: in.Order, AttrOrder: in.AttrOrder,
	})
	if err != nil {
		return 0, err
	}
	return group.ID, nil
}

// AttributeGroupUpdateInput renames, reorders or rewrites one section,
// addressed by id.
type AttributeGroupUpdateInput struct {
	TypeRef string
	GroupID model.ID

	Name      *string
	Order     *model.SmallInt
	AttrOrder *[]model.ID
}

// UpdateAttributeGroup renames, reorders or rewrites one section.
//
// This is the endpoint that can rename a section onto a name another section of
// the same model already uses, which is a conflict rather than a merge — the
// name is the section's identity within the model.
func (u *Usecase) UpdateAttributeGroup(ctx context.Context, in AttributeGroupUpdateInput) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if in.GroupID <= 0 {
		return errs.ErrInvalid
	}
	view, err := u.GetByRef(ctx, in.TypeRef)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}

	group, err := u.repo.AttributeGroupByID(ctx, in.GroupID)
	if err != nil {
		return err
	}
	if group == nil {
		return ErrGroupNotFound
	}
	if group.TypeID != view.Type.ID {
		// cantor resolved groups by id alone, so a request addressed to one
		// model could rewrite another's section.
		return ErrGroupNotInType
	}

	spec := AttributeGroupSaveSpec{
		TypeID:  view.Type.ID,
		GroupID: &in.GroupID,
		Name:    group.Name,
		Order:   in.Order,
	}
	if in.Name != nil {
		name, err := normalizeGroupName(*in.Name)
		if err != nil {
			return err
		}
		spec.Name = name

		other, err := u.repo.AttributeGroupByName(ctx, view.Type.ID, name)
		if err != nil {
			return err
		}
		if other != nil && other.ID != in.GroupID {
			return ErrGroupExists
		}
	}
	if in.AttrOrder != nil {
		if err := u.assertGroupAttrs(ctx, in.AttrOrder); err != nil {
			return err
		}
		spec.AttrOrder = in.AttrOrder
	}

	_, err = u.repo.SaveAttributeGroup(ctx, spec)
	return err
}

// DeleteAttributeGroup removes a form section.
func (u *Usecase) DeleteAttributeGroup(ctx context.Context, typeRef string, groupID model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if groupID <= 0 {
		return errs.ErrInvalid
	}
	view, err := u.GetByRef(ctx, typeRef)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}
	group, err := u.repo.AttributeGroupByID(ctx, groupID)
	if err != nil {
		return err
	}
	if group == nil {
		return ErrGroupNotFound
	}
	if group.TypeID != view.Type.ID {
		return ErrGroupNotInType
	}
	return u.repo.DeleteAttributeGroup(ctx, groupID)
}

// AttributeMoveInput moves one attribute between positions.
type AttributeMoveInput struct {
	TypeRef string
	AttrID  model.ID

	// FromGroupID and ToGroupID are nil for "not in any section".
	FromGroupID *model.ID
	ToGroupID   *model.ID

	Order model.SmallInt
}

// MoveAttribute moves one attribute to a position, within a section or between
// two.
func (u *Usecase) MoveAttribute(ctx context.Context, in AttributeMoveInput) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, in.TypeRef)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}
	if in.AttrID <= 0 {
		return errs.ErrInvalid
	}
	if err := u.assertGroupAttrs(ctx, &[]model.ID{in.AttrID}); err != nil {
		return err
	}
	// Both ends must be sections of THIS model, or "no section".
	if err := u.assertGroupsInType(ctx, view.Type.ID, in.FromGroupID, in.ToGroupID); err != nil {
		return err
	}
	return u.repo.MoveAttribute(ctx, AttributeMoveSpec{
		TypeID: view.Type.ID, AttrID: in.AttrID,
		FromGroupID: in.FromGroupID, ToGroupID: in.ToGroupID, Order: in.Order,
	})
}

// SwapAttributeGroups exchanges two sections' positions.
func (u *Usecase) SwapAttributeGroups(ctx context.Context, typeRef string, fromID, toID model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if fromID <= 0 || toID <= 0 {
		return errs.ErrInvalid
	}
	view, err := u.GetByRef(ctx, typeRef)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}
	if err := u.assertGroupsInType(ctx, view.Type.ID, &fromID, &toID); err != nil {
		return err
	}
	return u.repo.SwapAttributeGroups(ctx, AttributeGroupSwapSpec{
		TypeID: view.Type.ID, FromGroupID: fromID, ToGroupID: toID,
	})
}

// assertGroupsInType rejects group ids that belong to another model.
func (u *Usecase) assertGroupsInType(ctx context.Context, typeID model.ID, ids ...*model.ID) error {
	for _, id := range ids {
		if id == nil {
			continue
		}
		if *id <= 0 {
			return fmt.Errorf("%w: group id", errs.ErrInvalid)
		}
		group, err := u.repo.AttributeGroupByID(ctx, *id)
		if err != nil {
			return err
		}
		if group == nil {
			return ErrGroupNotFound
		}
		if group.TypeID != typeID {
			return ErrGroupNotInType
		}
	}
	return nil
}

// assertGroupAttrs checks that every attribute named in a section exists.
//
// cantor stored whatever ids the request carried, so a section could list an
// attribute that does not exist — invisible on read, since the read path drops
// ids that are not in the model's attribute set, but a row that serves no
// purpose and cannot be cleaned up through the API.
func (u *Usecase) assertGroupAttrs(ctx context.Context, ids *[]model.ID) error {
	if ids == nil {
		return nil
	}
	clean := dedupeAttrIDs(*ids)
	if len(clean) == 0 {
		return nil
	}
	return u.assertAttributesExist(ctx, clean)
}

func groupIDsOf(groups []citypemodel.CITypeAttributeGroup) []model.ID {
	if len(groups) == 0 {
		return nil
	}
	out := make([]model.ID, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.ID)
	}
	return out
}

func removeAttr(list []TypeAttribute, id model.ID) []TypeAttribute {
	for i := range list {
		if list[i].ID == id {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}
