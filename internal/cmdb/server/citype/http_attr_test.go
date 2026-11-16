package citype

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// noAttrStub satisfies the attribute-management half of the port with failures,
// so the CI-type double stays a complete Repo for the read tests. Every method
// is overridden by attrStub.
type noAttrStub struct{}

func stubNotImplemented() error {
	return errors.New("this test double does not implement attribute management; use attrStub")
}

func (noAttrStub) ListAttributes(context.Context, citypebiz.AttributeFilter) ([]citypemodel.Attribute, int64, error) {
	return nil, 0, stubNotImplemented()
}
func (noAttrStub) GetAttribute(context.Context, model.ID) (*citypemodel.Attribute, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) GetAttributeByName(context.Context, string) (*citypemodel.Attribute, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) GetAttributeByAlias(context.Context, string) (*citypemodel.Attribute, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AttributesByName(context.Context, []string) (map[string]citypemodel.Attribute, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) CreateAttribute(context.Context, citypebiz.AttributeCreateSpec) (*citypemodel.Attribute, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpdateAttribute(context.Context, citypebiz.AttributeUpdateSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteAttribute(context.Context, citypebiz.AttributeDeleteSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) ChoiceValues(context.Context, []model.ID) (map[model.ID][]citypebiz.ChoiceValue, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AncestorIDs(context.Context, model.ID) ([]model.ID, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) DescendantIDs(context.Context, model.ID) ([]model.ID, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) BindingsForTypes(context.Context, []model.ID) ([]citypemodel.CITypeAttribute, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AddBindings(context.Context, citypebiz.BindingsAddSpec) ([]model.ID, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpdateBindings(context.Context, citypebiz.BindingsUpdateSpec) ([]model.ID, []model.ID, error) {
	return nil, nil, stubNotImplemented()
}
func (noAttrStub) DeleteBindings(context.Context, citypebiz.BindingsDeleteSpec) (citypebiz.BindingsDeleteResult, error) {
	return citypebiz.BindingsDeleteResult{}, stubNotImplemented()
}
func (noAttrStub) TypeAliases(context.Context, []model.ID) (map[model.ID]string, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AttributeGroupsForTypes(context.Context, []model.ID) ([]citypemodel.CITypeAttributeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AttributeGroupByID(context.Context, model.ID) (*citypemodel.CITypeAttributeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AttributeGroupByName(context.Context, model.ID, string) (*citypemodel.CITypeAttributeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) AttributeGroupItems(context.Context, []model.ID) ([]citypemodel.CITypeAttributeGroupItem, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SaveAttributeGroup(context.Context, citypebiz.AttributeGroupSaveSpec) (*citypemodel.CITypeAttributeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) DeleteAttributeGroup(context.Context, model.ID) error {
	return stubNotImplemented()
}
func (noAttrStub) MoveAttribute(context.Context, citypebiz.AttributeMoveSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) SwapAttributeGroups(context.Context, citypebiz.AttributeGroupSwapSpec) error {
	return stubNotImplemented()
}

func (noAttrStub) UniqueConstraintsForType(context.Context, model.ID) ([]citypemodel.CITypeUniqueConstraint, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UniqueConstraintByID(context.Context, model.ID) (*citypemodel.CITypeUniqueConstraint, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) CreateUniqueConstraint(context.Context, citypebiz.UniqueConstraintCreateSpec) (*citypemodel.CITypeUniqueConstraint, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpdateUniqueConstraint(context.Context, citypebiz.UniqueConstraintUpdateSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteUniqueConstraint(context.Context, citypebiz.UniqueConstraintDeleteSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) TriggersForType(context.Context, model.ID) ([]citypemodel.CITypeTrigger, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TriggerByID(context.Context, model.ID) (*citypemodel.CITypeTrigger, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) CreateTrigger(context.Context, citypebiz.TriggerCreateSpec) (*citypemodel.CITypeTrigger, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpdateTrigger(context.Context, citypebiz.TriggerUpdateSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteTrigger(context.Context, citypebiz.TriggerDeleteSpec) error {
	return stubNotImplemented()
}

func (noAttrStub) ListTypeRelations(context.Context, citypebiz.TypeRelationFilter) ([]citypemodel.CITypeRelation, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TypeRelationByID(context.Context, model.ID) (*citypemodel.CITypeRelation, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TypeRelationByPair(context.Context, model.ID, model.ID) (*citypemodel.CITypeRelation, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SaveTypeRelation(context.Context, citypebiz.TypeRelationSaveSpec) (*citypemodel.CITypeRelation, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) DeleteTypeRelation(context.Context, citypebiz.TypeRelationDeleteSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) RelationTypesByID(context.Context, []model.ID) (map[model.ID]citypemodel.RelationType, error) {
	return nil, stubNotImplemented()
}

func (noAttrStub) ShowAttributes(context.Context, model.ID, model.ID) ([]citypemodel.PreferenceShowAttributes, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) ReplaceShowAttributes(context.Context, citypebiz.ShowAttributesSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) ClearShowAttributes(context.Context, model.ID, model.ID) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteShowAttributesForType(context.Context, model.ID) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteShowAttributesForAttr(context.Context, []model.ID, model.ID) error {
	return stubNotImplemented()
}
func (noAttrStub) SubscribedTypeIDs(context.Context, model.ID) ([]model.ID, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TypeOrder(context.Context, model.ID) ([]citypemodel.PreferenceCITypeOrder, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpsertTypeOrder(context.Context, citypebiz.TypeOrderSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) AutoSubscription(context.Context, model.ID) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SaveAutoSubscription(context.Context, citypebiz.AutoSubscriptionSpec) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) DeleteAutoSubscription(context.Context, model.ID) error {
	return stubNotImplemented()
}
func (noAttrStub) TypeGroups(context.Context) ([]citypemodel.CITypeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TypeGroupByID(context.Context, model.ID) (*citypemodel.CITypeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TypeGroupByName(context.Context, string) (*citypemodel.CITypeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) TypeGroupItems(context.Context) ([]citypemodel.CITypeGroupItem, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) CreateTypeGroup(context.Context, citypebiz.TypeGroupCreateSpec) (*citypemodel.CITypeGroup, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpdateTypeGroup(context.Context, citypebiz.TypeGroupUpdateSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteTypeGroup(context.Context, citypebiz.TypeGroupDeleteSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) ReorderTypeGroups(context.Context, citypebiz.TypeGroupReorderSpec) error {
	return stubNotImplemented()
}

func (noAttrStub) ServiceTrees(context.Context, model.ID, bool) ([]citypemodel.PreferenceRelationView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) ServiceTreeByID(context.Context, model.ID) (*citypemodel.PreferenceRelationView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) ServiceTreeByName(context.Context, string) (*citypemodel.PreferenceRelationView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SaveServiceTree(context.Context, citypebiz.ServiceTreeSaveSpec) (*citypemodel.PreferenceRelationView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) DeleteServiceTree(context.Context, citypebiz.ServiceTreeDeleteSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) ServiceTreesReferencing(context.Context, model.ID) ([]citypemodel.PreferenceRelationView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SearchOptions(context.Context, citypebiz.SearchOptionFilter) ([]citypemodel.PreferenceSearchOption, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SearchOptionByID(context.Context, model.ID) (*citypemodel.PreferenceSearchOption, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) CreateSearchOption(context.Context, citypebiz.SearchOptionCreateSpec) (*citypemodel.PreferenceSearchOption, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) UpdateSearchOption(context.Context, citypebiz.SearchOptionUpdateSpec) error {
	return stubNotImplemented()
}
func (noAttrStub) DeleteSearchOption(context.Context, model.ID) error {
	return stubNotImplemented()
}

// attrStub is the attribute-management half of the server test double. It
// embeds the CI-type stub so a route test can seed models through the same maps
// the read tests already use.
type attrStub struct {
	*stubRepo

	byName  map[string]model.ID
	aliases map[string]model.ID
	choices map[model.ID][]citypebiz.ChoiceValue

	bindings    []citypemodel.CITypeAttribute
	ancestors   []model.ID
	descendants []model.ID
	aliasOf     map[model.ID]string

	groups []citypemodel.CITypeAttributeGroup
	items  []citypemodel.CITypeAttributeGroupItem

	constraints []citypemodel.CITypeUniqueConstraint
	triggers    []citypemodel.CITypeTrigger

	relations     []citypemodel.CITypeRelation
	relationTypes map[model.ID]citypemodel.RelationType

	showAttrs      []citypemodel.PreferenceShowAttributes
	typeOrder      []citypemodel.PreferenceCITypeOrder
	autoSub        *citypemodel.PreferenceAutoSubscriptionConfig
	typeGroups     []citypemodel.CITypeGroup
	typeGroupItems []citypemodel.CITypeGroupItem

	serviceTrees  []citypemodel.PreferenceRelationView
	searchOptions []citypemodel.PreferenceSearchOption

	// Drives a failure the route must map to a specific status.
	deleteAttrErr error
	unbindResult  citypebiz.BindingsDeleteResult
	saveGroupErr  error

	// Recorded writes.
	createdAttr *citypebiz.AttributeCreateSpec
	updateAttr  *citypebiz.AttributeUpdateSpec
	deletedAttr *citypebiz.AttributeDeleteSpec
	addedBind   *citypebiz.BindingsAddSpec
	deletedBind *citypebiz.BindingsDeleteSpec
	savedGroup  *citypebiz.AttributeGroupSaveSpec

	createdConstraint *citypebiz.UniqueConstraintCreateSpec
	updatedConstraint *citypebiz.UniqueConstraintUpdateSpec
	deletedConstraint *citypebiz.UniqueConstraintDeleteSpec
	createdTrigger    *citypebiz.TriggerCreateSpec
	updatedTrigger    *citypebiz.TriggerUpdateSpec
	deletedTrigger    *citypebiz.TriggerDeleteSpec

	// Drives a specific status out of the rule routes.
	ruleErr error

	// Recorded relation writes, and a driver for their failures.
	savedRelation   *citypebiz.TypeRelationSaveSpec
	deletedRelation *citypebiz.TypeRelationDeleteSpec
	relationErr     error

	// Recorded preference and group writes, and drivers for their failures.
	replacedShow    *citypebiz.ShowAttributesSpec
	upsertedOrder   *citypebiz.TypeOrderSpec
	savedAutoSub    *citypebiz.AutoSubscriptionSpec
	updatedGroup    *citypebiz.TypeGroupUpdateSpec
	deletedGroup    *citypebiz.TypeGroupDeleteSpec
	reorderedGroups *citypebiz.TypeGroupReorderSpec
	prefErr         error
	groupErr        error

	savedTree     *citypebiz.ServiceTreeSaveSpec
	deletedTree   *citypebiz.ServiceTreeDeleteSpec
	createdSearch *citypebiz.SearchOptionCreateSpec
	updatedSearch *citypebiz.SearchOptionUpdateSpec
	deletedSearch *model.ID
	treeErr       error
	searchErr     error
}

func newAttrStub() *attrStub {
	return &attrStub{
		stubRepo: &stubRepo{
			attrs: map[model.ID]citypemodel.Attribute{},
			types: []citypemodel.CIType{},
		},
		byName:        map[string]model.ID{},
		aliases:       map[string]model.ID{},
		choices:       map[model.ID][]citypebiz.ChoiceValue{},
		aliasOf:       map[model.ID]string{},
		relationTypes: map[model.ID]citypemodel.RelationType{},
	}
}

func (s *attrStub) seedAttribute(a citypemodel.Attribute) *attrStub {
	if a.Alias == "" {
		a.Alias = a.Name
	}
	s.attrs[a.ID] = a
	s.byName[a.Name] = a.ID
	s.aliases[a.Alias] = a.ID
	return s
}

func (s *attrStub) seedType(ct citypemodel.CIType) *attrStub {
	s.types = append(s.types, ct)
	s.aliasOf[ct.ID] = ct.Alias
	return s
}

func (s *attrStub) ListAttributes(context.Context, citypebiz.AttributeFilter) ([]citypemodel.Attribute, int64, error) {
	if s.fail {
		return nil, 0, errBoom
	}
	out := make([]citypemodel.Attribute, 0, len(s.attrs))
	for _, a := range s.attrs {
		out = append(out, a)
	}
	return out, int64(len(out)), nil
}

func (s *attrStub) GetAttribute(_ context.Context, id model.ID) (*citypemodel.Attribute, error) {
	if s.fail {
		return nil, errBoom
	}
	a, ok := s.attrs[id]
	if !ok {
		return nil, nil
	}
	return &a, nil
}

func (s *attrStub) GetAttributeByName(_ context.Context, name string) (*citypemodel.Attribute, error) {
	id, ok := s.byName[name]
	if !ok {
		return nil, nil
	}
	a := s.attrs[id]
	return &a, nil
}

func (s *attrStub) GetAttributeByAlias(_ context.Context, alias string) (*citypemodel.Attribute, error) {
	id, ok := s.aliases[alias]
	if !ok {
		return nil, nil
	}
	a := s.attrs[id]
	return &a, nil
}

func (s *attrStub) AttributesByName(_ context.Context, names []string) (map[string]citypemodel.Attribute, error) {
	out := map[string]citypemodel.Attribute{}
	for _, n := range names {
		if id, ok := s.byName[n]; ok {
			out[n] = s.attrs[id]
		}
	}
	return out, nil
}

func (s *attrStub) CreateAttribute(_ context.Context, in citypebiz.AttributeCreateSpec) (*citypemodel.Attribute, error) {
	if s.fail {
		return nil, errBoom
	}
	s.createdAttr = &in
	row := in.Attribute
	row.ID = model.ID(len(s.attrs) + 1)
	s.attrs[row.ID] = row
	return &row, nil
}

func (s *attrStub) UpdateAttribute(_ context.Context, in citypebiz.AttributeUpdateSpec) error {
	if s.fail {
		return errBoom
	}
	s.updateAttr = &in
	s.attrs[in.Attribute.ID] = in.Attribute
	return nil
}

func (s *attrStub) DeleteAttribute(_ context.Context, in citypebiz.AttributeDeleteSpec) error {
	if s.deleteAttrErr != nil {
		return s.deleteAttrErr
	}
	if s.fail {
		return errBoom
	}
	s.deletedAttr = &in
	return nil
}

func (s *attrStub) ChoiceValues(_ context.Context, ids []model.ID) (map[model.ID][]citypebiz.ChoiceValue, error) {
	out := map[model.ID][]citypebiz.ChoiceValue{}
	for _, id := range ids {
		if v, ok := s.choices[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (s *attrStub) AncestorIDs(context.Context, model.ID) ([]model.ID, error) {
	return s.ancestors, nil
}

func (s *attrStub) DescendantIDs(context.Context, model.ID) ([]model.ID, error) {
	return s.descendants, nil
}

func (s *attrStub) BindingsForTypes(_ context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttribute, error) {
	want := map[model.ID]bool{}
	for _, id := range typeIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeAttribute, 0, len(s.bindings))
	for _, b := range s.bindings {
		if want[b.TypeID] {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *attrStub) AddBindings(_ context.Context, in citypebiz.BindingsAddSpec) ([]model.ID, error) {
	if s.fail {
		return nil, errBoom
	}
	s.addedBind = &in
	return in.AttrIDs, nil
}

func (s *attrStub) UpdateBindings(_ context.Context, in citypebiz.BindingsUpdateSpec) ([]model.ID, []model.ID, error) {
	applied := make([]model.ID, 0, len(in.Changes))
	for _, c := range in.Changes {
		applied = append(applied, c.AttrID)
	}
	return applied, []model.ID{}, nil
}

func (s *attrStub) DeleteBindings(_ context.Context, in citypebiz.BindingsDeleteSpec) (citypebiz.BindingsDeleteResult, error) {
	if s.fail {
		return citypebiz.BindingsDeleteResult{}, errBoom
	}
	s.deletedBind = &in
	if s.unbindResult.Unbound != nil {
		return s.unbindResult, nil
	}
	return citypebiz.BindingsDeleteResult{
		Unbound:       append([]model.ID{}, in.AttrIDs...),
		AffectedTypes: []model.ID{in.TypeID},
		DeletedValues: 4,
	}, nil
}

func (s *attrStub) TypeAliases(_ context.Context, ids []model.ID) (map[model.ID]string, error) {
	out := map[model.ID]string{}
	for _, id := range ids {
		if al, ok := s.aliasOf[id]; ok {
			out[id] = al
		}
	}
	return out, nil
}

func (s *attrStub) AttributeGroupsForTypes(_ context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttributeGroup, error) {
	want := map[model.ID]bool{}
	for _, id := range typeIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeAttributeGroup, 0, len(s.groups))
	for _, g := range s.groups {
		if want[g.TypeID] {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *attrStub) AttributeGroupByID(_ context.Context, id model.ID) (*citypemodel.CITypeAttributeGroup, error) {
	for _, g := range s.groups {
		if g.ID == id {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) AttributeGroupByName(_ context.Context, typeID model.ID, name string) (*citypemodel.CITypeAttributeGroup, error) {
	for _, g := range s.groups {
		if g.TypeID == typeID && g.Name == name {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) AttributeGroupItems(_ context.Context, groupIDs []model.ID) ([]citypemodel.CITypeAttributeGroupItem, error) {
	want := map[model.ID]bool{}
	for _, id := range groupIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeAttributeGroupItem, 0, len(s.items))
	for _, it := range s.items {
		if want[it.GroupID] {
			out = append(out, it)
		}
	}
	return out, nil
}

func (s *attrStub) SaveAttributeGroup(_ context.Context, in citypebiz.AttributeGroupSaveSpec) (*citypemodel.CITypeAttributeGroup, error) {
	if s.saveGroupErr != nil {
		return nil, s.saveGroupErr
	}
	s.savedGroup = &in
	g := citypemodel.CITypeAttributeGroup{TypeID: in.TypeID, Name: in.Name}
	if in.GroupID != nil {
		g.ID = *in.GroupID
	} else {
		g.ID = 700
	}
	if in.Order != nil {
		g.Order = *in.Order
	}
	return &g, nil
}

func (s *attrStub) DeleteAttributeGroup(_ context.Context, id model.ID) error {
	for _, g := range s.groups {
		if g.ID == id {
			return nil
		}
	}
	return citypebiz.ErrGroupNotFound
}

func (s *attrStub) MoveAttribute(context.Context, citypebiz.AttributeMoveSpec) error { return nil }
func (s *attrStub) SwapAttributeGroups(context.Context, citypebiz.AttributeGroupSwapSpec) error {
	return nil
}

var errBoom = errors.New("boom")

// attrRouter builds a router over the attribute double with an identity
// attached, mirroring writeRouter.
func attrRouter(t *testing.T, repo citypebiz.Repo, role string, superuser bool, uid uint64) chi.Router {
	t.Helper()
	h := NewHandler(citypebiz.NewUsecase(repo, nil))
	r := chi.NewRouter()
	h.Register(r)
	wrapped := chi.NewRouter()
	wrapped.Handle("/*", asIdentity(r, role, superuser, uid))
	return wrapped
}

// --- authorization ---

func TestAttributeWriteRoutesRequireAdmin(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cmdb/attributes", `{"name":"x","value_type":"2"}`},
		{http.MethodPut, "/v1/cmdb/attributes/1", `{"alias":"a"}`},
		{http.MethodDelete, "/v1/cmdb/attributes/1", ``},
		{http.MethodPost, "/v1/cmdb/ci-types/1/attributes", `{"attr_ids":[1]}`},
		{http.MethodPut, "/v1/cmdb/ci-types/1/attributes", `{"attributes":[]}`},
		{http.MethodDelete, "/v1/cmdb/ci-types/1/attributes", `{"attr_ids":[1]}`},
		{http.MethodPost, "/v1/cmdb/ci-types/1/attributes/transfer", `{"attr_id":1}`},
		{http.MethodPost, "/v1/cmdb/ci-types/1/attribute-groups", `{"name":"g"}`},
		{http.MethodPut, "/v1/cmdb/ci-types/1/attribute-groups/2", `{"name":"g"}`},
		{http.MethodDelete, "/v1/cmdb/ci-types/1/attribute-groups/2", ``},
		{http.MethodPost, "/v1/cmdb/ci-types/1/attribute-groups/transfer", `{"from_group_id":1,"to_group_id":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, attrRouter(t, newAttrStub(), "user", false, 7), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAttributeWriteRoutesWithoutIdentityAreUnauthorized(t *testing.T) {
	h := NewHandler(citypebiz.NewUsecase(newAttrStub(), nil))
	r := chi.NewRouter()
	h.Register(r) // no identity middleware at all

	rec := send(t, r, http.MethodPost, "/v1/cmdb/attributes", `{"name":"x","value_type":"2"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

func TestAttributeReadRoutesDoNotRequireAdmin(t *testing.T) {
	stub := newAttrStub().seedAttribute(attr(1, "hostname"))
	stub.seedType(typeRow(1, "host"))
	r := attrRouter(t, stub, "user", false, 7)

	for _, path := range []string{
		"/v1/cmdb/attributes",
		"/v1/cmdb/attributes/1",
		"/v1/cmdb/ci-types/1/attributes",
		"/v1/cmdb/ci-types/1/attribute-groups",
	} {
		if rec := do(t, r, path); rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (body %s)", path, rec.Code, rec.Body.String())
		}
	}
}

// --- dictionary writes ---

func TestCreateAttributeReturns201AndRecordsTheActor(t *testing.T) {
	stub := newAttrStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost, "/v1/cmdb/attributes",
		`{"name":"env","value_type":"2","alias":"环境","choice_value":[{"value":"prod"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if stub.createdAttr == nil {
		t.Fatal("the create was not forwarded")
	}
	if stub.createdAttr.Attribute.Name != "env" || stub.createdAttr.Attribute.Alias != "环境" {
		t.Fatalf("row = %+v", stub.createdAttr.Attribute)
	}
	if stub.createdAttr.Attribute.UID == nil || *stub.createdAttr.Attribute.UID != 7 {
		t.Fatal("the caller must become the creator: the delete path gates on it")
	}
	if !stub.createdAttr.Attribute.IsChoice {
		t.Fatal("supplying values must derive is_choice")
	}
	if len(stub.createdAttr.ChoiceValues) != 1 {
		t.Fatalf("choices = %+v, want the supplied value", stub.createdAttr.ChoiceValues)
	}
}

func TestCreateAttributeRequiresName(t *testing.T) {
	rec := send(t, attrRouter(t, newAttrStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/attributes", `{"value_type":"2"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateAttributeRejectsAnUnknownField(t *testing.T) {
	rec := send(t, attrRouter(t, newAttrStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/attributes", `{"name":"x","value_type":"2","nope":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdateAttributeAcceptsABareDefault(t *testing.T) {
	stub := newAttrStub().seedAttribute(attr(1, "cpu"))
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPut,
		"/v1/cmdb/attributes/1", `{"default":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// cantor's column also carries computed default tokens, told apart from
	// literals by the envelope, so a bare value has to be wrapped.
	if stub.updateAttr.Attribute.Default["default"] != float64(4) {
		t.Fatalf("default = %#v, want it wrapped in cantor's envelope", stub.updateAttr.Attribute.Default)
	}
}

func TestUpdateAttributeRejectsANonNumericID(t *testing.T) {
	rec := send(t, attrRouter(t, newAttrStub(), "admin", false, 7), http.MethodPut,
		"/v1/cmdb/attributes/abc", `{"alias":"a"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdateAttributeMissingIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, newAttrStub(), "admin", false, 7), http.MethodPut,
		"/v1/cmdb/attributes/404", `{"alias":"a"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// A dependency refusal is a statement about current state, not a malformed
// request, so it must not surface as a 400 the caller could try to fix.
func TestDeleteAttributeInUseIsConflict(t *testing.T) {
	owner := model.ID(7)
	a := attr(1, "hostname")
	a.UID = &owner // so the ownership gate passes and the state refusal is what surfaces
	stub := newAttrStub().seedAttribute(a)
	stub.deleteAttrErr = citypebiz.ErrAttributeInUse

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete, "/v1/cmdb/attributes/1", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "conflict" {
		t.Fatalf("code = %q, want conflict", body.Code)
	}
}

func TestDeleteAttributeByNonOwnerIsForbidden(t *testing.T) {
	owner := model.ID(7)
	a := attr(1, "hostname")
	a.UID = &owner
	stub := newAttrStub().seedAttribute(a)

	rec := send(t, attrRouter(t, stub, "admin", false, 99), http.MethodDelete, "/v1/cmdb/attributes/1", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteAttributeReturns204(t *testing.T) {
	owner := model.ID(7)
	a := attr(1, "hostname")
	a.UID = &owner
	stub := newAttrStub().seedAttribute(a)

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete, "/v1/cmdb/attributes/1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- bindings ---

func TestBindAttributesReturnsTheBoundIDs(t *testing.T) {
	stub := newAttrStub().seedAttribute(attr(1, "hostname"))
	stub.seedType(typeRow(10, "host"))

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/attributes", `{"attr_ids":[1],"is_required":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if stub.addedBind == nil || !stub.addedBind.IsRequired {
		t.Fatalf("spec = %+v, want is_required carried through", stub.addedBind)
	}
}

// The unbind response reports what actually happened. cantor echoed the request
// list, which said nothing about how much data had gone or which models had
// lost the attribute.
func TestUnbindAttributesReportsTheBlastRadius(t *testing.T) {
	stub := newAttrStub().seedAttribute(attr(1, "cpu"))
	stub.seedType(typeRow(10, "host"))
	stub.descendants = []model.ID{11}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/attributes", `{"attr_ids":[1]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got citypebiz.BindingsDeleteResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.DeletedValues != 4 {
		t.Fatalf("deleted_values = %d, want the erased count surfaced", got.DeletedValues)
	}
	if len(got.Unbound) != 1 || got.Unbound[0] != 1 {
		t.Fatalf("unbound = %v", got.Unbound)
	}
}

func TestUnbindAttributesMissingIsNotFound(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "host"))
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/attributes", `{"attr_ids":[404]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdateBindingsRequiresAttributes(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "host"))
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPut,
		"/v1/cmdb/ci-types/10/attributes", `{"attributes":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- attribute groups ---

func TestSaveAttributeGroupReturnsTheID(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "host")).
		seedAttribute(attr(1, "a")).seedAttribute(attr(2, "b"))
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/attribute-groups", `{"name":"网络","attr_ids":[1,2]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got groupIDResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GroupID == 0 {
		t.Fatal("group_id = 0, want the created section's id")
	}
	if stub.savedGroup.AttrOrder == nil || len(*stub.savedGroup.AttrOrder) != 2 {
		t.Fatalf("membership = %v, want both attributes forwarded", stub.savedGroup.AttrOrder)
	}
}

func TestSaveAttributeGroupLeavesOrderAbsent(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "host"))
	send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/attribute-groups", `{"name":"网络"}`)
	if stub.savedGroup.Order != nil {
		t.Fatal("order = non-nil, want absent so an existing section is not moved to the front")
	}
}

func TestGroupDuplicateNameIsConflict(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "host"))
	stub.saveGroupErr = citypebiz.ErrGroupExists

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/attribute-groups", `{"name":"网络"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteAttributeGroupReturns204(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "host"))
	g := citypemodel.CITypeAttributeGroup{Name: "网络", TypeID: 10}
	g.ID = 700
	stub.groups = []citypemodel.CITypeAttributeGroup{g}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/attribute-groups/700", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteAttributeGroupOfAnotherModelIsNotFound(t *testing.T) {
	stub := newAttrStub().seedType(typeRow(10, "mine"))
	stub.seedType(typeRow(20, "theirs"))
	theirs := citypemodel.CITypeAttributeGroup{Name: "s", TypeID: 20}
	theirs.ID = 700
	stub.groups = []citypemodel.CITypeAttributeGroup{theirs}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/attribute-groups/700", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- the response shape ---

// The webhook carries an encrypted authorization map. It is never echoed,
// whatever the caller's role, so the route tests assert it on the wire rather
// than trusting the view to omit it.
func TestAttributeResponseNeverContainsTheWebhook(t *testing.T) {
	a := attr(1, "owner")
	a.IsChoice = true
	a.ChoiceWebHook = map[string]any{
		"url":           "https://example.invalid",
		"authorization": map[string]any{"token": "v1:supersecret"},
	}
	a.ComputeScript = strptr("print('x')")
	stub := newAttrStub().seedAttribute(a)

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/attributes/1")
	body := rec.Body.String()
	// Matched with the JSON key syntax rather than as bare substrings: the
	// boolean that reports a script's PRESENCE is named `has_compute_script`,
	// so a substring check would flag the honest field as a leak.
	for _, leak := range []string{
		"supersecret", "print(",
		`"authorization"`, `"compute_script":`, `"choice_web_hook":`,
	} {
		if strings.Contains(body, leak) {
			t.Fatalf("body leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"has_choice_web_hook":true`) {
		t.Fatalf("body = %s, want the presence of a webhook reported instead of its content", body)
	}
}

func TestListAttributesUsesTheEnvelopeShape(t *testing.T) {
	stub := newAttrStub().seedAttribute(attr(1, "hostname"))
	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/attributes")
	var got attributeListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("response = %+v, want one item and total 1", got)
	}
	// The project's list convention is {items,total}; a {code,message,data}
	// wrapper would be a contract break.
	if strings.Contains(rec.Body.String(), `"code"`) {
		t.Fatalf("body = %s, want no envelope beyond items/total", rec.Body.String())
	}
}

func TestListAttributesRejectsABadPage(t *testing.T) {
	r := attrRouter(t, newAttrStub(), "user", false, 7)
	for _, q := range []string{"?page=0", "?page=-1", "?page=abc", "?page_size=0"} {
		if rec := do(t, r, "/v1/cmdb/attributes"+q); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400 (body %s)", q, rec.Code, rec.Body.String())
		}
	}
}

func TestTypeAttributesRouteReturnsTheEffectiveSet(t *testing.T) {
	stub := newAttrStub().seedAttribute(attr(1, "hostname"))
	ct := typeRow(10, "host")
	ct.UniqueID = 1
	stub.seedType(ct)
	stub.bindings = []citypemodel.CITypeAttribute{bindRow(10, 1, 0, true)}

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/ci-types/10/attributes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got citypebiz.TypeAttributes
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Unique != "hostname" || len(got.Attributes) != 1 {
		t.Fatalf("response = %+v", got)
	}
}

// --- fixtures ---

func attr(id model.ID, name string) citypemodel.Attribute {
	a := citypemodel.Attribute{Name: name, Alias: name, ValueType: "2"}
	a.ID = id
	return a
}

func typeRow(id model.ID, name string) citypemodel.CIType {
	ct := citypemodel.CIType{Name: name, Alias: name, Enabled: true}
	ct.ID = id
	return ct
}

func bindRow(typeID, attrID model.ID, order model.ID, required bool) citypemodel.CITypeAttribute {
	b := citypemodel.CITypeAttribute{
		TypeID: typeID, AttrID: attrID, Order: order,
		IsRequired: required, DefaultShow: true,
	}
	b.ID = typeID*100 + attrID
	return b
}

func strptr(s string) *string { return &s }

// The hierarchy level views, unimplemented: a test that reaches one of these
// without overriding it is testing something it did not set up.
func (noAttrStub) LevelViews(context.Context, model.ID) ([]citypemodel.PreferenceTreeView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) LevelView(context.Context, model.ID, model.ID) (*citypemodel.PreferenceTreeView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) SaveLevelView(context.Context, citypebiz.LevelViewSaveSpec) (*citypemodel.PreferenceTreeView, error) {
	return nil, stubNotImplemented()
}
func (noAttrStub) DeleteLevelView(context.Context, model.ID, model.ID) error {
	return stubNotImplemented()
}
