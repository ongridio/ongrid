package citype

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// TypeAttribute is one attribute in a model's EFFECTIVE attribute set: the
// attributes the model carries itself, plus everything it inherits.
type TypeAttribute struct {
	// Attribute is the dictionary entry.
	Attribute

	// IsRequired, Order and DefaultShow come from the BINDING, not the
	// attribute, which is why the same attribute can be required on one model
	// and optional on another.
	IsRequired  bool     `json:"is_required"`
	Order       model.ID `json:"order"`
	DefaultShow bool     `json:"default_show"`

	// Inherited reports that the attribute comes from an ancestor model rather
	// than from this one.
	Inherited bool `json:"inherited"`

	// InheritedFrom is the alias of the ancestor it came from, empty for the
	// model's own attributes.
	InheritedFrom string `json:"inherited_from,omitempty"`
}

// TypeAttributes is a model's effective attribute set.
type TypeAttributes struct {
	TypeID model.ID `json:"type_id"`

	// UniqueID and Unique describe the attribute that identifies instances of
	// this model. Unique is its NAME, because that is what a caller writing a
	// query or an import file needs; UniqueID is what the console needs to
	// address the attribute.
	UniqueID model.ID `json:"unique_id"`
	Unique   string   `json:"unique"`

	Attributes []TypeAttribute `json:"attributes"`
}

// ListTypeAttributes returns a model's effective attribute set.
//
// THE COMPOSITION RULE IS CANTOR'S AND IS NOT OBVIOUS, so it is stated here:
// the ancestors are walked root-most first and then the model itself, and each
// type's bindings are visited in (order, id) sequence. An attribute's POSITION
// in the result comes from the first binding seen for it, while its CONTENT —
// required, order, default-show and the inherited flag — comes from the LAST
// one. So an attribute that a child model overrides keeps the ancestor's slot
// while showing the child's settings. Reproducing that exactly matters: it is
// the field order the console renders, and the port's own model-config UI is
// built against it.
//
// The cost is a bounded number of queries — the ancestor chain, one binding
// query for all of them, one attribute query, one alias query, one choice
// query — rather than a lookup per row.
func (u *Usecase) ListTypeAttributes(ctx context.Context, ref string) (*TypeAttributes, error) {
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
	return u.effectiveAttributes(ctx, view)
}

// effectiveAttributes assembles the attribute set for an already-resolved type.
func (u *Usecase) effectiveAttributes(ctx context.Context, view *View) (*TypeAttributes, error) {
	typeID := view.Type.ID

	ancestors, err := u.repo.AncestorIDs(ctx, typeID)
	if err != nil {
		return nil, err
	}
	// Root-most first, then the model itself — cantor's `base(type_id) +
	// [type_id]`.
	chain := make([]model.ID, 0, len(ancestors)+1)
	chain = append(chain, ancestors...)
	chain = append(chain, typeID)

	bindings, err := u.repo.BindingsForTypes(ctx, chain)
	if err != nil {
		return nil, err
	}

	attrIDs := make([]model.ID, 0, len(bindings))
	choiceCandidates := make([]model.ID, 0, len(bindings))
	seenAttr := map[model.ID]bool{}
	for _, b := range bindings {
		if !seenAttr[b.AttrID] {
			seenAttr[b.AttrID] = true
			attrIDs = append(attrIDs, b.AttrID)
		}
	}
	attrs, err := u.repo.AttributesByID(ctx, attrIDs)
	if err != nil {
		return nil, err
	}
	for id, a := range attrs {
		if a.IsChoice {
			choiceCandidates = append(choiceCandidates, id)
		}
	}
	choices, err := u.repo.ChoiceValues(ctx, choiceCandidates)
	if err != nil {
		return nil, err
	}
	aliases, err := u.repo.TypeAliases(ctx, ancestors)
	if err != nil {
		return nil, err
	}

	out := TypeAttributes{TypeID: typeID}
	out.Attributes = composeAttributes(typeID, chain, bindings, attrs, choices, aliases)

	// The unique attribute's name, from the already-loaded batch when possible.
	out.UniqueID = view.Type.UniqueID
	if view.Type.UniqueID > 0 {
		if a, ok := attrs[view.Type.UniqueID]; ok {
			out.Unique = a.Name
		} else {
			out.Unique = view.UniqueKey
		}
	}
	return &out, nil
}

// composeAttributes builds a model's effective attribute set from data that has
// already been loaded.
//
// It is separate from the loading because the relation renderer needs the same
// composition for every model it displays, and loading per model there would be
// one round of queries per model. One implementation, two callers — the
// alternative is a second copy that drifts.
//
// THE COMPOSITION RULE IS CANTOR'S AND IS NOT OBVIOUS: the chain is walked
// root-most first and then the model itself, and each type's bindings are
// visited in (order, id) sequence. An attribute's POSITION comes from the first
// binding seen for it, while its CONTENT — required, order, default-show and
// the inherited flag — comes from the LAST. So an attribute a child model
// overrides keeps the ancestor's slot while showing the child's settings, and
// the field order the console renders depends on it.
func composeAttributes(
	typeID model.ID,
	chain []model.ID,
	bindings []citypemodel.CITypeAttribute,
	attrs map[model.ID]citypemodel.Attribute,
	choices map[model.ID][]ChoiceValue,
	aliases map[model.ID]string,
) []TypeAttribute {
	byType := make(map[model.ID][]citypemodel.CITypeAttribute, len(chain))
	for _, b := range bindings {
		byType[b.TypeID] = append(byType[b.TypeID], b)
	}

	out := []TypeAttribute{}
	pos := make(map[model.ID]int, len(bindings))

	for _, chainType := range chain {
		rows := byType[chainType]
		// cantor sorted by (order, id); the id tiebreak is what makes a
		// same-order pair deterministic instead of whatever the database
		// returns.
		sortBindings(rows)

		for _, b := range rows {
			attr, ok := attrs[b.AttrID]
			if !ok {
				// A binding that outlived its attribute. cantor skipped these
				// rather than erroring, and the binding-delete path now cleans
				// them up, but an adopted database can still contain them.
				continue
			}
			item := TypeAttribute{
				Attribute:   attributeView(attr, choices[b.AttrID]),
				IsRequired:  b.IsRequired,
				Order:       b.Order,
				DefaultShow: b.DefaultShow,
				Inherited:   chainType != typeID,
			}
			if item.Inherited {
				item.InheritedFrom = aliases[chainType]
			}

			if at, seen := pos[b.AttrID]; seen {
				out[at] = item
			} else {
				pos[b.AttrID] = len(out)
				out = append(out, item)
			}
		}
	}
	return out
}

// sortBindings orders bindings by (order, id), matching cantor's sort key.
func sortBindings(rows []citypemodel.CITypeAttribute) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if rows[j-1].Order < rows[j].Order ||
				(rows[j-1].Order == rows[j].Order && rows[j-1].ID <= rows[j].ID) {
				break
			}
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

// BindAttributesInput binds attributes to a model.
type BindAttributesInput struct {
	TypeRef string

	// AttrIDs are the attributes to bind. Zero and duplicate entries are
	// dropped, preserving the caller's order for the history rows.
	AttrIDs []model.ID

	// Order, IsRequired and DefaultShow are optional per-binding settings.
	// Nil leaves the schema default: order 0, not required, shown.
	Order       *model.ID
	IsRequired  *bool
	DefaultShow *bool

	Actor model.ID
}

// BindAttributes adds attributes to a model.
//
// Binding is idempotent, which is cantor's behaviour: an attribute that is
// already bound is skipped rather than reported as a conflict, because the
// console's "add selected attributes" flow resends the whole selection and a
// conflict would make it unusable for the second attribute it adds.
func (u *Usecase) BindAttributes(ctx context.Context, in BindAttributesInput) ([]model.ID, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, in.TypeRef)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}

	ids := dedupeAttrIDs(in.AttrIDs)
	if len(ids) == 0 {
		return nil, ErrBindingAttrRequired
	}
	if err := u.assertAttributesExist(ctx, ids); err != nil {
		return nil, err
	}

	// Schema defaults, applied here so the store never has to know them.
	order := model.ID(0)
	if in.Order != nil {
		order = *in.Order
	}
	required := false
	if in.IsRequired != nil {
		required = *in.IsRequired
	}
	show := true
	if in.DefaultShow != nil {
		show = *in.DefaultShow
	}

	return u.repo.AddBindings(ctx, BindingsAddSpec{
		TypeID:      view.Type.ID,
		AttrIDs:     ids,
		Order:       order,
		IsRequired:  required,
		DefaultShow: show,
		Actor:       in.Actor,
	})
}

// UpdateBindingsInput changes per-binding settings for a model.
type UpdateBindingsInput struct {
	TypeRef string
	Changes []BindingChange
	Actor   model.ID
}

// UpdateBindingsResult reports which changes landed.
//
// The two lists exist because cantor silently ignored a change naming an
// attribute the model does not bind, and the response echoed the request — so a
// console could show a saved state that was never saved. Reporting the split
// keeps the lenient behaviour while making the discrepancy observable.
type UpdateBindingsResult struct {
	Applied []model.ID `json:"applied"`
	Skipped []model.ID `json:"skipped"`
}

// UpdateBindings applies per-binding settings.
func (u *Usecase) UpdateBindings(ctx context.Context, in UpdateBindingsInput) (*UpdateBindingsResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, in.TypeRef)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}

	seen := map[model.ID]bool{}
	ids := make([]model.ID, 0, len(in.Changes))
	changes := make([]BindingChange, 0, len(in.Changes))
	for _, c := range in.Changes {
		if c.AttrID <= 0 || seen[c.AttrID] {
			continue
		}
		seen[c.AttrID] = true
		ids = append(ids, c.AttrID)
		changes = append(changes, c)
	}
	if len(changes) == 0 {
		return nil, ErrBindingAttrRequired
	}
	if err := u.assertAttributesExist(ctx, ids); err != nil {
		return nil, err
	}

	applied, skipped, err := u.repo.UpdateBindings(ctx, BindingsUpdateSpec{
		TypeID:  view.Type.ID,
		Changes: changes,
		Actor:   in.Actor,
	})
	if err != nil {
		return nil, err
	}
	if applied == nil {
		applied = []model.ID{}
	}
	if skipped == nil {
		skipped = []model.ID{}
	}
	return &UpdateBindingsResult{Applied: applied, Skipped: skipped}, nil
}

// UnbindAttributesInput removes attributes from a model.
type UnbindAttributesInput struct {
	TypeRef string
	AttrIDs []model.ID
	Actor   model.ID
}

// UnbindAttributes removes attributes from a model AND DESTROYS THE VALUES THEY
// HOLD on every model that loses them.
//
// This is the one model-configuration operation that can destroy instance data,
// and it is defined that way rather than by accident: an attribute the model no
// longer carries has nowhere to keep its values, and leaving the rows behind
// would put data in the database that no schema can reach. The guards are that
// the attribute must not be the model's unique identifier and must not be its
// default sort key.
//
// What the port does differently from cantor is bounded and deliberate:
//
//   - The guards run BEFORE the first mutation and the whole request is one
//     transaction. cantor committed once per attribute inside the loop, so
//     `attr_id=10,11` where 11 was guarded deleted 10, committed, and then
//     answered 400 — the caller was told nothing had happened.
//   - A descendant model that binds the attribute ITSELF keeps both the
//     attribute and its data. cantor's erasure loop walked the descendant tree
//     without asking whether the descendant owned the binding, so a model that
//     independently bound the attribute lost every value while keeping the
//     attribute on its form.
//   - The default-sort-key guard also considers descendants, because the
//     cascade reaches them and their sort key is their own.
//   - Nulling a model's show attribute and writing the history row happen in
//     the same transaction. cantor did both afterwards, in two more.
//
// The blast radius is returned rather than left implicit.
func (u *Usecase) UnbindAttributes(ctx context.Context, in UnbindAttributesInput) (*BindingsDeleteResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, in.TypeRef)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}

	ids := dedupeAttrIDs(in.AttrIDs)
	if len(ids) == 0 {
		return nil, ErrBindingAttrRequired
	}
	if err := u.assertAttributesExist(ctx, ids); err != nil {
		return nil, err
	}

	result, err := u.repo.DeleteBindings(ctx, BindingsDeleteSpec{
		TypeID:  view.Type.ID,
		AttrIDs: ids,
		Actor:   in.Actor,
		Type:    view.Type,
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// assertAttributesExist reports the first requested attribute that is not in
// the dictionary, so the caller learns which id was wrong.
//
// It uses ONE query for the whole set rather than a lookup per id, and reports
// in caller order so the error names the id the caller listed first rather than
// whichever the map happened to surface.
func (u *Usecase) assertAttributesExist(ctx context.Context, ids []model.ID) error {
	found, err := u.repo.AttributesByID(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return fmt.Errorf("%w: id=%d", ErrBindingAttrNotFound, id)
		}
	}
	return nil
}
