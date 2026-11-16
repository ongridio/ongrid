package citype

import (
	"context"
	"errors"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// errBoom is the generic failure a repo double reports when a test asks it to.
var errBoom = errors.New("boom")

// attrRepo is the attribute-management half of the test double. It embeds the
// model-configuration double so only the new behaviour has to be written here,
// and so a test can seed attributes through the same `attrs` map the CI-type
// reads already use.
type attrRepo struct {
	*countingRepo

	names   map[string]model.ID
	aliases map[model.ID]string
	choices map[model.ID][]ChoiceValue

	bindings    []citypemodel.CITypeAttribute
	ancestors   []model.ID
	descendants []model.ID

	groups []citypemodel.CITypeAttributeGroup
	items  []citypemodel.CITypeAttributeGroupItem

	constraints []citypemodel.CITypeUniqueConstraint
	triggers    []citypemodel.CITypeTrigger

	relations     []citypemodel.CITypeRelation
	relationTypes map[model.ID]citypemodel.RelationType

	showAttrs      []citypemodel.PreferenceShowAttributes
	typeOrder      []citypemodel.PreferenceCITypeOrder
	autoSubs       []citypemodel.PreferenceAutoSubscriptionConfig
	typeGroups     []citypemodel.CITypeGroup
	typeGroupItems []citypemodel.CITypeGroupItem

	serviceTrees  []citypemodel.PreferenceRelationView
	searchOptions []citypemodel.PreferenceSearchOption

	// Recorded writes, asserted on by the tests.
	createdAttr *AttributeCreateSpec
	updateAttr  *AttributeUpdateSpec
	deletedAttr *AttributeDeleteSpec
	addedBind   *BindingsAddSpec
	updatedBind *BindingsUpdateSpec
	deletedBind *BindingsDeleteSpec
	savedGroup  *AttributeGroupSaveSpec
	movedAttr   *AttributeMoveSpec
	swappedGrp  *AttributeGroupSwapSpec

	// Recorded rule writes.
	createdConstraint *UniqueConstraintCreateSpec
	updatedConstraint *UniqueConstraintUpdateSpec
	deletedConstraint *UniqueConstraintDeleteSpec
	createdTrigger    *TriggerCreateSpec
	updatedTrigger    *TriggerUpdateSpec
	deletedTrigger    *TriggerDeleteSpec

	// Recorded relation writes.
	savedRelation   *TypeRelationSaveSpec
	deletedRelation *TypeRelationDeleteSpec

	// Recorded preference and group writes.
	replacedShow    *ShowAttributesSpec
	upsertedOrder   *TypeOrderSpec
	savedAutoSub    *AutoSubscriptionSpec
	updatedGroup    *TypeGroupUpdateSpec
	deletedGroup    *TypeGroupDeleteSpec
	reorderedGroups *TypeGroupReorderSpec

	savedTree     *ServiceTreeSaveSpec
	deletedTree   *ServiceTreeDeleteSpec
	createdSearch *SearchOptionCreateSpec
	updatedSearch *SearchOptionUpdateSpec
	deletedSearch *model.ID

	prefErr   error
	groupErr  error
	treeErr   error
	searchErr error

	failOn      string
	relationErr error
}

func newAttrRepo() *attrRepo {
	return &attrRepo{
		countingRepo: &countingRepo{
			attrs: map[model.ID]citypemodel.Attribute{},
			types: []citypemodel.CIType{},
		},
		names:         map[string]model.ID{},
		aliases:       map[model.ID]string{},
		choices:       map[model.ID][]ChoiceValue{},
		relationTypes: map[model.ID]citypemodel.RelationType{},
	}
}

// withAttr seeds one attribute into the shared store and the lookups.
func (a *attrRepo) withAttr(attr citypemodel.Attribute) *attrRepo {
	if attr.Alias == "" {
		attr.Alias = attr.Name
	}
	a.attrs[attr.ID] = attr
	a.names[attr.Name] = attr.ID
	a.aliases[attr.ID] = attr.Alias
	return a
}

func (a *attrRepo) ListAttributes(_ context.Context, f AttributeFilter) ([]citypemodel.Attribute, int64, error) {
	if a.failOn == "ListAttributes" {
		return nil, 0, errBoom
	}
	out := make([]citypemodel.Attribute, 0, len(a.attrs))
	var total int64
	for _, attr := range a.attrs {
		total++
		if f.NameLike != "" && attr.Name != f.NameLike {
			continue
		}
		if f.AliasLike != "" && attr.Alias != f.AliasLike {
			continue
		}
		out = append(out, attr)
	}
	// Paging is applied so a test can prove the usecase clamped the page.
	if f.Offset < len(out) {
		out = out[f.Offset:]
	} else {
		out = nil
	}
	if f.Limit < len(out) {
		out = out[:f.Limit]
	}
	return out, total, nil
}

func (a *attrRepo) GetAttribute(_ context.Context, id model.ID) (*citypemodel.Attribute, error) {
	if a.failOn == "GetAttribute" {
		return nil, errBoom
	}
	attr, ok := a.attrs[id]
	if !ok {
		return nil, nil
	}
	return &attr, nil
}

func (a *attrRepo) GetAttributeByName(_ context.Context, name string) (*citypemodel.Attribute, error) {
	if a.failOn == "GetAttributeByName" {
		return nil, errBoom
	}
	id, ok := a.names[name]
	if !ok {
		return nil, nil
	}
	attr := a.attrs[id]
	return &attr, nil
}

func (a *attrRepo) GetAttributeByAlias(_ context.Context, alias string) (*citypemodel.Attribute, error) {
	for id, al := range a.aliases {
		if al == alias {
			attr := a.attrs[id]
			return &attr, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) AttributesByName(_ context.Context, names []string) (map[string]citypemodel.Attribute, error) {
	out := map[string]citypemodel.Attribute{}
	for _, n := range names {
		if id, ok := a.names[n]; ok {
			out[n] = a.attrs[id]
		}
	}
	return out, nil
}

func (a *attrRepo) CreateAttribute(_ context.Context, in AttributeCreateSpec) (*citypemodel.Attribute, error) {
	if a.failOn == "CreateAttribute" {
		return nil, errBoom
	}
	a.createdAttr = &in
	row := in.Attribute
	if row.ID == 0 {
		row.ID = model.ID(len(a.attrs) + 1000)
	}
	a.attrs[row.ID] = row
	a.names[row.Name] = row.ID
	a.aliases[row.ID] = row.Alias
	if len(in.ChoiceValues) > 0 {
		a.choices[row.ID] = in.ChoiceValues
	}
	return &row, nil
}

func (a *attrRepo) UpdateAttribute(_ context.Context, in AttributeUpdateSpec) error {
	if a.failOn == "UpdateAttribute" {
		return errBoom
	}
	a.updateAttr = &in
	a.attrs[in.Attribute.ID] = in.Attribute
	a.aliases[in.Attribute.ID] = in.Attribute.Alias
	if in.ReplaceChoices {
		a.choices[in.Attribute.ID] = in.ChoiceValues
	}
	return nil
}

func (a *attrRepo) DeleteAttribute(_ context.Context, in AttributeDeleteSpec) error {
	if a.failOn == "DeleteAttribute" {
		return errBoom
	}
	a.deletedAttr = &in
	return nil
}

func (a *attrRepo) ChoiceValues(_ context.Context, attrIDs []model.ID) (map[model.ID][]ChoiceValue, error) {
	out := map[model.ID][]ChoiceValue{}
	for _, id := range attrIDs {
		if v, ok := a.choices[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (a *attrRepo) AncestorIDs(context.Context, model.ID) ([]model.ID, error) {
	return a.ancestors, nil
}

func (a *attrRepo) DescendantIDs(context.Context, model.ID) ([]model.ID, error) {
	return a.descendants, nil
}

func (a *attrRepo) BindingsForTypes(_ context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttribute, error) {
	want := map[model.ID]bool{}
	for _, id := range typeIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeAttribute, 0, len(a.bindings))
	for _, b := range a.bindings {
		if want[b.TypeID] {
			out = append(out, b)
		}
	}
	return out, nil
}

func (a *attrRepo) AddBindings(_ context.Context, in BindingsAddSpec) ([]model.ID, error) {
	a.addedBind = &in
	return in.AttrIDs, nil
}

func (a *attrRepo) UpdateBindings(_ context.Context, in BindingsUpdateSpec) ([]model.ID, []model.ID, error) {
	a.updatedBind = &in
	applied := make([]model.ID, 0, len(in.Changes))
	skipped := make([]model.ID, 0)
	for _, c := range in.Changes {
		bound := false
		for _, b := range a.bindings {
			if b.TypeID == in.TypeID && b.AttrID == c.AttrID {
				bound = true
			}
		}
		if bound {
			applied = append(applied, c.AttrID)
		} else {
			skipped = append(skipped, c.AttrID)
		}
	}
	return applied, skipped, nil
}

func (a *attrRepo) DeleteBindings(_ context.Context, in BindingsDeleteSpec) (BindingsDeleteResult, error) {
	a.deletedBind = &in
	return BindingsDeleteResult{
		Unbound:       append([]model.ID{}, in.AttrIDs...),
		AffectedTypes: []model.ID{in.TypeID},
		DeletedValues: 3,
	}, nil
}

func (a *attrRepo) TypeAliases(_ context.Context, ids []model.ID) (map[model.ID]string, error) {
	out := map[model.ID]string{}
	for _, id := range ids {
		if al, ok := a.aliases[id]; ok {
			out[id] = al
		}
	}
	return out, nil
}

func (a *attrRepo) AttributeGroupsForTypes(_ context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttributeGroup, error) {
	want := map[model.ID]bool{}
	for _, id := range typeIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeAttributeGroup, 0, len(a.groups))
	for _, g := range a.groups {
		if want[g.TypeID] {
			out = append(out, g)
		}
	}
	return out, nil
}

func (a *attrRepo) AttributeGroupByID(_ context.Context, id model.ID) (*citypemodel.CITypeAttributeGroup, error) {
	for _, g := range a.groups {
		if g.ID == id {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) AttributeGroupByName(_ context.Context, typeID model.ID, name string) (*citypemodel.CITypeAttributeGroup, error) {
	for _, g := range a.groups {
		if g.TypeID == typeID && g.Name == name {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) AttributeGroupItems(_ context.Context, groupIDs []model.ID) ([]citypemodel.CITypeAttributeGroupItem, error) {
	want := map[model.ID]bool{}
	for _, id := range groupIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeAttributeGroupItem, 0, len(a.items))
	for _, it := range a.items {
		if want[it.GroupID] {
			out = append(out, it)
		}
	}
	return out, nil
}

func (a *attrRepo) SaveAttributeGroup(_ context.Context, in AttributeGroupSaveSpec) (*citypemodel.CITypeAttributeGroup, error) {
	if a.failOn == "SaveAttributeGroup" {
		return nil, errBoom
	}
	a.savedGroup = &in
	group := citypemodel.CITypeAttributeGroup{TypeID: in.TypeID, Name: in.Name}
	if in.GroupID != nil {
		group.ID = *in.GroupID
	} else {
		group.ID = model.ID(len(a.groups) + 500)
	}
	if in.Order != nil {
		group.Order = *in.Order
	}
	return &group, nil
}

func (a *attrRepo) DeleteAttributeGroup(_ context.Context, id model.ID) error {
	if a.failOn == "DeleteAttributeGroup" {
		return errBoom
	}
	for _, g := range a.groups {
		if g.ID == id {
			return nil
		}
	}
	return ErrGroupNotFound
}

func (a *attrRepo) MoveAttribute(_ context.Context, in AttributeMoveSpec) error {
	a.movedAttr = &in
	return nil
}

func (a *attrRepo) SwapAttributeGroups(_ context.Context, in AttributeGroupSwapSpec) error {
	a.swappedGrp = &in
	return nil
}

// notImplemented is what the model-configuration double reports for the
// attribute-management half it does not carry. Tests that exercise attributes
// use attrRepo, which embeds this double and overrides every one of these.
func notImplemented() error {
	return errors.New("this test double does not implement attribute management; use attrRepo")
}

// noAttrRepo satisfies AttributeRepo and AttributeGroupRepo with failures, so
// the model-configuration double stays a complete Repo. Every method is
// overridden by attrRepo.
type noAttrRepo struct{}

func (noAttrRepo) ListAttributes(context.Context, AttributeFilter) ([]citypemodel.Attribute, int64, error) {
	return nil, 0, notImplemented()
}
func (noAttrRepo) GetAttribute(context.Context, model.ID) (*citypemodel.Attribute, error) {
	return nil, notImplemented()
}
func (noAttrRepo) GetAttributeByName(context.Context, string) (*citypemodel.Attribute, error) {
	return nil, notImplemented()
}
func (noAttrRepo) GetAttributeByAlias(context.Context, string) (*citypemodel.Attribute, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AttributesByName(context.Context, []string) (map[string]citypemodel.Attribute, error) {
	return nil, notImplemented()
}
func (noAttrRepo) CreateAttribute(context.Context, AttributeCreateSpec) (*citypemodel.Attribute, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpdateAttribute(context.Context, AttributeUpdateSpec) error {
	return notImplemented()
}
func (noAttrRepo) DeleteAttribute(context.Context, AttributeDeleteSpec) error {
	return notImplemented()
}
func (noAttrRepo) ChoiceValues(context.Context, []model.ID) (map[model.ID][]ChoiceValue, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AncestorIDs(context.Context, model.ID) ([]model.ID, error) {
	return nil, notImplemented()
}
func (noAttrRepo) DescendantIDs(context.Context, model.ID) ([]model.ID, error) {
	return nil, notImplemented()
}
func (noAttrRepo) BindingsForTypes(context.Context, []model.ID) ([]citypemodel.CITypeAttribute, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AddBindings(context.Context, BindingsAddSpec) ([]model.ID, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpdateBindings(context.Context, BindingsUpdateSpec) ([]model.ID, []model.ID, error) {
	return nil, nil, notImplemented()
}
func (noAttrRepo) DeleteBindings(context.Context, BindingsDeleteSpec) (BindingsDeleteResult, error) {
	return BindingsDeleteResult{}, notImplemented()
}
func (noAttrRepo) TypeAliases(context.Context, []model.ID) (map[model.ID]string, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AttributeGroupsForTypes(context.Context, []model.ID) ([]citypemodel.CITypeAttributeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AttributeGroupByID(context.Context, model.ID) (*citypemodel.CITypeAttributeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AttributeGroupByName(context.Context, model.ID, string) (*citypemodel.CITypeAttributeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) AttributeGroupItems(context.Context, []model.ID) ([]citypemodel.CITypeAttributeGroupItem, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SaveAttributeGroup(context.Context, AttributeGroupSaveSpec) (*citypemodel.CITypeAttributeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) DeleteAttributeGroup(context.Context, model.ID) error   { return notImplemented() }
func (noAttrRepo) MoveAttribute(context.Context, AttributeMoveSpec) error { return notImplemented() }
func (noAttrRepo) SwapAttributeGroups(context.Context, AttributeGroupSwapSpec) error {
	return notImplemented()
}

// --- rule half of the double ---

func (noAttrRepo) UniqueConstraintsForType(context.Context, model.ID) ([]citypemodel.CITypeUniqueConstraint, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UniqueConstraintByID(context.Context, model.ID) (*citypemodel.CITypeUniqueConstraint, error) {
	return nil, notImplemented()
}
func (noAttrRepo) CreateUniqueConstraint(context.Context, UniqueConstraintCreateSpec) (*citypemodel.CITypeUniqueConstraint, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpdateUniqueConstraint(context.Context, UniqueConstraintUpdateSpec) error {
	return notImplemented()
}
func (noAttrRepo) DeleteUniqueConstraint(context.Context, UniqueConstraintDeleteSpec) error {
	return notImplemented()
}
func (noAttrRepo) TriggersForType(context.Context, model.ID) ([]citypemodel.CITypeTrigger, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TriggerByID(context.Context, model.ID) (*citypemodel.CITypeTrigger, error) {
	return nil, notImplemented()
}
func (noAttrRepo) CreateTrigger(context.Context, TriggerCreateSpec) (*citypemodel.CITypeTrigger, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpdateTrigger(context.Context, TriggerUpdateSpec) error { return notImplemented() }
func (noAttrRepo) DeleteTrigger(context.Context, TriggerDeleteSpec) error { return notImplemented() }

func (noAttrRepo) ListTypeRelations(context.Context, TypeRelationFilter) ([]citypemodel.CITypeRelation, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TypeRelationByID(context.Context, model.ID) (*citypemodel.CITypeRelation, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TypeRelationByPair(context.Context, model.ID, model.ID) (*citypemodel.CITypeRelation, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SaveTypeRelation(context.Context, TypeRelationSaveSpec) (*citypemodel.CITypeRelation, error) {
	return nil, notImplemented()
}
func (noAttrRepo) DeleteTypeRelation(context.Context, TypeRelationDeleteSpec) error {
	return notImplemented()
}
func (noAttrRepo) RelationTypesByID(context.Context, []model.ID) (map[model.ID]citypemodel.RelationType, error) {
	return nil, notImplemented()
}

func (noAttrRepo) ShowAttributes(context.Context, model.ID, model.ID) ([]citypemodel.PreferenceShowAttributes, error) {
	return nil, notImplemented()
}
func (noAttrRepo) ReplaceShowAttributes(context.Context, ShowAttributesSpec) error {
	return notImplemented()
}
func (noAttrRepo) ClearShowAttributes(context.Context, model.ID, model.ID) error {
	return notImplemented()
}
func (noAttrRepo) DeleteShowAttributesForType(context.Context, model.ID) error {
	return notImplemented()
}
func (noAttrRepo) DeleteShowAttributesForAttr(context.Context, []model.ID, model.ID) error {
	return notImplemented()
}
func (noAttrRepo) SubscribedTypeIDs(context.Context, model.ID) ([]model.ID, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TypeOrder(context.Context, model.ID) ([]citypemodel.PreferenceCITypeOrder, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpsertTypeOrder(context.Context, TypeOrderSpec) error {
	return notImplemented()
}
func (noAttrRepo) AutoSubscription(context.Context, model.ID) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SaveAutoSubscription(context.Context, AutoSubscriptionSpec) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	return nil, notImplemented()
}
func (noAttrRepo) DeleteAutoSubscription(context.Context, model.ID) error {
	return notImplemented()
}
func (noAttrRepo) TypeGroups(context.Context) ([]citypemodel.CITypeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TypeGroupByID(context.Context, model.ID) (*citypemodel.CITypeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TypeGroupByName(context.Context, string) (*citypemodel.CITypeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) TypeGroupItems(context.Context) ([]citypemodel.CITypeGroupItem, error) {
	return nil, notImplemented()
}
func (noAttrRepo) CreateTypeGroup(context.Context, TypeGroupCreateSpec) (*citypemodel.CITypeGroup, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpdateTypeGroup(context.Context, TypeGroupUpdateSpec) error {
	return notImplemented()
}
func (noAttrRepo) DeleteTypeGroup(context.Context, TypeGroupDeleteSpec) error {
	return notImplemented()
}
func (noAttrRepo) ReorderTypeGroups(context.Context, TypeGroupReorderSpec) error {
	return notImplemented()
}

func (noAttrRepo) ServiceTrees(context.Context, model.ID, bool) ([]citypemodel.PreferenceRelationView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) ServiceTreeByID(context.Context, model.ID) (*citypemodel.PreferenceRelationView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) ServiceTreeByName(context.Context, string) (*citypemodel.PreferenceRelationView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SaveServiceTree(context.Context, ServiceTreeSaveSpec) (*citypemodel.PreferenceRelationView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) DeleteServiceTree(context.Context, ServiceTreeDeleteSpec) error {
	return notImplemented()
}
func (noAttrRepo) ServiceTreesReferencing(context.Context, model.ID) ([]citypemodel.PreferenceRelationView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SearchOptions(context.Context, SearchOptionFilter) ([]citypemodel.PreferenceSearchOption, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SearchOptionByID(context.Context, model.ID) (*citypemodel.PreferenceSearchOption, error) {
	return nil, notImplemented()
}
func (noAttrRepo) CreateSearchOption(context.Context, SearchOptionCreateSpec) (*citypemodel.PreferenceSearchOption, error) {
	return nil, notImplemented()
}
func (noAttrRepo) UpdateSearchOption(context.Context, SearchOptionUpdateSpec) error {
	return notImplemented()
}
func (noAttrRepo) DeleteSearchOption(context.Context, model.ID) error {
	return notImplemented()
}

// bindingFixture builds a binding row for a fixture.
func bindingFixture(typeID, attrID model.ID, order model.ID, required bool) citypemodel.CITypeAttribute {
	b := citypemodel.CITypeAttribute{
		TypeID: typeID, AttrID: attrID, Order: order,
		IsRequired: required, DefaultShow: true,
	}
	b.ID = typeID*100 + attrID
	return b
}

// The hierarchy level views. Every method fails, so a test that reaches one
// without overriding it knows it is testing something it did not set up.
func (noAttrRepo) LevelViews(context.Context, model.ID) ([]citypemodel.PreferenceTreeView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) LevelView(context.Context, model.ID, model.ID) (*citypemodel.PreferenceTreeView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) SaveLevelView(context.Context, LevelViewSaveSpec) (*citypemodel.PreferenceTreeView, error) {
	return nil, notImplemented()
}
func (noAttrRepo) DeleteLevelView(context.Context, model.ID, model.ID) error {
	return notImplemented()
}
