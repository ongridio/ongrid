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

// ShowAttributes is a model's visible-field subscription as the API presents
// it.
type ShowAttributes struct {
	TypeID model.ID

	// IsSubscribed distinguishes a saved subscription from the DEFAULT view
	// the console falls back to. The two are different states and cantor
	// reported them with one flag; the port keeps the flag and always returns
	// the fields that are actually shown either way, so a caller can render the
	// list without knowing which state it is in.
	IsSubscribed bool

	Fields []SubscribedField
}

// GetShowAttributes returns the fields a user sees for a model.
//
// With no subscription saved, the default is every attribute the model marks as
// shown by default, followed by the built-in columns. The fallback is not a
// convenience: it is what makes a freshly created model usable before anyone
// has configured anything.
func (u *Usecase) GetShowAttributes(ctx context.Context, uid model.ID, ref string) (*ShowAttributes, error) {
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

	rows, err := u.repo.ShowAttributes(ctx, uid, view.Type.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		fields, err := u.defaultShowAttributes(ctx, view)
		if err != nil {
			return nil, err
		}
		return &ShowAttributes{TypeID: view.Type.ID, IsSubscribed: false, Fields: fields}, nil
	}

	// One batch for every referenced attribute rather than one per row.
	ids := make([]model.ID, 0, len(rows))
	for _, r := range rows {
		if r.AttrID != nil {
			ids = append(ids, *r.AttrID)
		}
	}
	attrs, err := u.repo.AttributesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	choices, err := u.repo.ChoiceValues(ctx, choiceCandidates(attrs))
	if err != nil {
		return nil, err
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Order < rows[j].Order })
	fields := make([]SubscribedField, 0, len(rows))
	for _, r := range rows {
		field := SubscribedField{IsFixed: r.IsFixed}
		switch {
		case r.AttrID != nil:
			attr, ok := attrs[*r.AttrID]
			if !ok {
				// A subscription row that outlived its attribute. The port
				// skips it rather than rendering an empty column named after a
				// number; the unbind cascade now removes these, but an adopted
				// database can still hold one.
				continue
			}
			view := attributeView(attr, choices[attr.ID])
			field.Attribute = &view
		case r.BuiltinAttr != nil:
			if !IsBuiltinAttribute(*r.BuiltinAttr) {
				// cantor indexed its label map directly and answered 500 with a
				// KeyError. Skipping is the honest answer: a column whose name
				// means nothing to this build cannot be rendered, and failing
				// the whole request over one bad row hides the rest.
				continue
			}
			field.Builtin = *r.BuiltinAttr
		default:
			continue
		}
		fields = append(fields, field)
	}
	return &ShowAttributes{TypeID: view.Type.ID, IsSubscribed: true, Fields: fields}, nil
}

// defaultShowAttributes is the view a model presents before anyone subscribes.
func (u *Usecase) defaultShowAttributes(ctx context.Context, view *View) ([]SubscribedField, error) {
	effective, err := u.effectiveAttributes(ctx, view)
	if err != nil {
		return nil, err
	}
	fields := make([]SubscribedField, 0, len(effective.Attributes)+len(BuiltinAttributes))
	for _, a := range effective.Attributes {
		if !a.DefaultShow {
			continue
		}
		attr := a.Attribute
		fields = append(fields, SubscribedField{Attribute: &attr})
	}
	for _, name := range BuiltinAttributes {
		fields = append(fields, SubscribedField{Builtin: name})
	}
	return fields, nil
}

// ShowAttributeInput is one field in a subscription write.
//
// The field is named by an attribute REFERENCE (a numeric id or a name) or by a
// built-in column name, which is what the console has. Resolving it here keeps
// the port's callers free of the id-versus-name distinction.
type ShowAttributeInput struct {
	Ref     string
	IsFixed bool
}

// SetShowAttributes replaces a user's subscription for a model.
//
// The write is one transaction and the whole list is resolved BEFORE anything
// is written. cantor wrote each field and then, in a separate pass that ran
// after all of them had committed, decided which old rows to remove — so a
// failure in the second pass left a subscription with the new fields ADDED to
// the old ones, which is not a state any caller asked for.
func (u *Usecase) SetShowAttributes(ctx context.Context, uid model.ID, ref string, in []ShowAttributeInput) (*ShowAttributes, error) {
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
	if len(in) == 0 {
		// cantor treated an empty list as "unsubscribe", so clearing every
		// field silently dropped the subscription. Saying so is better than
		// guessing: the caller wanted either a default view or nothing, and
		// those are different requests.
		return nil, ErrSubscriptionEmpty
	}

	fields := make([]SubscribedField, 0, len(in))
	for _, item := range in {
		field, err := u.resolveSubscribedField(ctx, item)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}

	spec := ShowAttributesSpec{UID: uid, TypeID: view.Type.ID, Fields: make([]SubscribedFieldSpec, 0, len(fields))}
	for _, f := range fields {
		item := SubscribedFieldSpec{Builtin: f.Builtin, IsFixed: f.IsFixed}
		if f.Attribute != nil {
			id := f.Attribute.ID
			item.AttrID = &id
		}
		spec.Fields = append(spec.Fields, item)
	}
	if err := u.repo.ReplaceShowAttributes(ctx, spec); err != nil {
		return nil, err
	}
	return u.GetShowAttributes(ctx, uid, ref)
}

// resolveSubscribedField turns a reference into a field, refusing anything that
// does not exist.
func (u *Usecase) resolveSubscribedField(ctx context.Context, in ShowAttributeInput) (SubscribedField, error) {
	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		return SubscribedField{}, ErrSubscriptionAttrNotFound
	}
	if IsBuiltinAttribute(ref) {
		return SubscribedField{Builtin: ref, IsFixed: in.IsFixed}, nil
	}
	attr, err := u.repo.FindAttribute(ctx, ref)
	if err != nil {
		return SubscribedField{}, err
	}
	if attr == nil {
		return SubscribedField{}, fmt.Errorf("%w: %q", ErrSubscriptionAttrNotFound, ref)
	}
	view := attributeView(*attr, nil)
	return SubscribedField{Attribute: &view, IsFixed: in.IsFixed}, nil
}

// --- the sidebar ---

// SubscribedModels is the console's sidebar: the models a user is subscribed
// to, grouped, plus the order they have arranged them in.
type SubscribedModels struct {
	// Groups holds the model groupings, with an optional trailing bucket of
	// models that belong to none.
	Groups []ModelGroup

	// TypeIDs is every model in the response, in the order the sidebar should
	// render them. cantor returned this as an unordered set; it is ordered here
	// because it is an ordering.
	TypeIDs []model.ID
}

// ListSubscribedModels returns the models a user is subscribed to.
//
// The subscription is resolved in one of two ways, and which one applies is the
// user's configuration, not a parameter:
//
//   - with an enabled auto-subscription rule, the rule decides — see
//     ApplyAutoSubscription;
//   - otherwise, the models the user has actually subscribed to, which are the
//     distinct models named by their c_psa rows.
//
// A model that has been deleted since it was subscribed is dropped rather than
// rendered as a broken entry. cantor looked each one up and dereferenced the
// result without a nil guard, so a subscription to a since-deleted model made
// the whole sidebar answer 500 — and a subscription row pointing at a deleted
// model is reachable, because cantor's own write path never checked that the
// model existed.
func (u *Usecase) ListSubscribedModels(ctx context.Context, uid model.ID, includeUngrouped bool) (*SubscribedModels, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	subscribed, err := u.subscribedTypeIDs(ctx, uid)
	if err != nil {
		return nil, err
	}

	order, err := u.repo.TypeOrder(ctx, uid)
	if err != nil {
		return nil, err
	}
	ordered := OrderTypes(subscribed, resourceOrder(order))

	// Live models only. A model that vanished between the subscription and this
	// read is not an error, it is a row to skip.
	views, err := u.TypesByID(ctx, ordered)
	if err != nil {
		return nil, err
	}
	live := make([]View, 0, len(views))
	liveIDs := make([]model.ID, 0, len(views))
	for _, v := range views {
		live = append(live, v)
		liveIDs = append(liveIDs, v.Type.ID)
	}

	groups, err := u.typeGroupsWith(ctx, live, includeUngrouped)
	if err != nil {
		return nil, err
	}
	return &SubscribedModels{Groups: groups, TypeIDs: liveIDs}, nil
}

// subscribedTypeIDs resolves which models a user is subscribed to.
func (u *Usecase) subscribedTypeIDs(ctx context.Context, uid model.ID) ([]model.ID, error) {
	config, err := u.repo.AutoSubscription(ctx, uid)
	if err != nil {
		return nil, err
	}
	if config == nil || !config.Enabled {
		return u.repo.SubscribedTypeIDs(ctx, uid)
	}

	// The rule needs every model the user may read. Resource-level
	// authorization is not built, so "permitted" is every live model; when it
	// lands, this is the one place that has to consult it.
	all, err := u.repo.ListTypes(ctx, ListFilter{})
	if err != nil {
		return nil, err
	}
	permitted := make([]model.ID, 0, len(all))
	for _, ct := range all {
		permitted = append(permitted, ct.ID)
	}

	items, err := u.repo.TypeGroupItems(ctx)
	if err != nil {
		return nil, err
	}
	groupTypes := map[model.ID][]model.ID{}
	for _, it := range items {
		groupTypes[it.GroupID] = append(groupTypes[it.GroupID], it.TypeID)
	}

	return ApplyAutoSubscription(AutoSubscriptionRule{
		Strategy: SubscriptionStrategy(config.BaseStrategy),
		GroupIDs: []model.ID(config.GroupIDs),
		TypeIDs:  []model.ID(config.TypeIDs),
	}, groupTypes, permitted), nil
}

// resourceOrder extracts the model ordering from a user's preference rows.
func resourceOrder(rows []citypemodel.PreferenceCITypeOrder) []model.ID {
	type entry struct {
		id    model.ID
		order model.SmallInt
	}
	var entries []entry
	for _, r := range rows {
		if r.IsTree || r.TypeID == nil {
			continue
		}
		entries = append(entries, entry{id: *r.TypeID, order: r.Order})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	out := make([]model.ID, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.id)
	}
	return out
}

// --- auto-subscription ---

// GetAutoSubscription returns a user's rule, or nil when they have none.
func (u *Usecase) GetAutoSubscription(ctx context.Context, uid model.ID) (*AutoSubscriptionRule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.repo.AutoSubscription(ctx, uid)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &AutoSubscriptionRule{
		Strategy:    SubscriptionStrategy(row.BaseStrategy),
		GroupIDs:    []model.ID(row.GroupIDs),
		TypeIDs:     []model.ID(row.TypeIDs),
		Enabled:     row.Enabled,
		Description: derefString(row.Description),
	}, nil
}

// SetAutoSubscriptionInput is the usecase-level rule write. Nil pointers leave
// the corresponding field unchanged, which is what lets the console toggle the
// rule without resending it.
type SetAutoSubscriptionInput struct {
	Strategy    *SubscriptionStrategy
	GroupIDs    *[]model.ID
	TypeIDs     *[]model.ID
	Enabled     *bool
	Description *string
}

// SetAutoSubscription saves a user's rule.
//
// Every referenced group is checked to exist, and every referenced model is
// checked to be readable. cantor stored whatever ids arrived, so a rule could
// name a group that had been deleted and then silently select nothing from it —
// a rule that looks configured and does not work.
func (u *Usecase) SetAutoSubscription(ctx context.Context, uid model.ID, in SetAutoSubscriptionInput) (*AutoSubscriptionRule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := u.repo.AutoSubscription(ctx, uid)
	if err != nil {
		return nil, err
	}

	spec := AutoSubscriptionSpec{
		UID:     uid,
		Enabled: true,
	}
	if existing != nil {
		spec.Strategy = SubscriptionStrategy(existing.BaseStrategy)
		spec.GroupIDs = []model.ID(existing.GroupIDs)
		spec.TypeIDs = []model.ID(existing.TypeIDs)
		spec.Enabled = existing.Enabled
		spec.Description = derefString(existing.Description)
	} else {
		spec.Strategy = SubscribeNone
	}

	if in.Strategy != nil {
		if !in.Strategy.Valid() {
			return nil, fmt.Errorf("%w: %q", ErrAutoSubscriptionStrategy, *in.Strategy)
		}
		spec.Strategy = *in.Strategy
	}
	if in.GroupIDs != nil {
		ids := dedupeAttrIDs(*in.GroupIDs)
		if err := u.assertTypeGroupsExist(ctx, ids); err != nil {
			return nil, err
		}
		spec.GroupIDs = ids
	}
	if in.TypeIDs != nil {
		ids := dedupeAttrIDs(*in.TypeIDs)
		if err := u.assertTypesExist(ctx, ids); err != nil {
			return nil, err
		}
		spec.TypeIDs = ids
	}
	if in.Enabled != nil {
		spec.Enabled = *in.Enabled
	}
	if in.Description != nil {
		spec.Description = *in.Description
	}

	if _, err := u.repo.SaveAutoSubscription(ctx, spec); err != nil {
		return nil, err
	}
	// The result is built from the resolved spec, not read back. A re-read
	// could only return what was just written, and if it ever returned nil the
	// caller would have a nil it was entitled to dereference — a crash with no
	// cause. The spec IS the state that was written.
	return &AutoSubscriptionRule{
		Strategy:    spec.Strategy,
		GroupIDs:    spec.GroupIDs,
		TypeIDs:     spec.TypeIDs,
		Enabled:     spec.Enabled,
		Description: spec.Description,
	}, nil
}

// ClearAutoSubscription removes a user's rule.
func (u *Usecase) ClearAutoSubscription(ctx context.Context, uid model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	return u.repo.DeleteAutoSubscription(ctx, uid)
}

// SetSubscriptionOrderInput is one ordering write.
type SetSubscriptionOrderInput struct {
	TypeIDs []model.ID
	IsTree  bool
}

// SetSubscriptionOrder saves a user's model ordering.
func (u *Usecase) SetSubscriptionOrder(ctx context.Context, uid model.ID, in SetSubscriptionOrderInput) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	ids := dedupeAttrIDs(in.TypeIDs)
	if len(ids) > 0 {
		if err := u.assertTypesExist(ctx, ids); err != nil {
			return err
		}
	}
	return u.repo.UpsertTypeOrder(ctx, TypeOrderSpec{UID: uid, TypeIDs: ids, IsTree: in.IsTree})
}

// assertTypesExist reports the first model id that does not exist.
func (u *Usecase) assertTypesExist(ctx context.Context, ids []model.ID) error {
	if len(ids) == 0 {
		return nil
	}
	found, err := u.repo.ListTypes(ctx, ListFilter{IDs: ids})
	if err != nil {
		return err
	}
	seen := make(map[model.ID]bool, len(found))
	for _, ct := range found {
		seen[ct.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			return fmt.Errorf("%w: model %d", errs.ErrNotFound, id)
		}
	}
	return nil
}
