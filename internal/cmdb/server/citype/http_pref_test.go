package citype

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// --- the preference half of the server double ---

func (s *attrStub) ShowAttributes(_ context.Context, uid, typeID model.ID) ([]citypemodel.PreferenceShowAttributes, error) {
	var out []citypemodel.PreferenceShowAttributes
	for _, r := range s.showAttrs {
		if r.UID == uid && r.TypeID == typeID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *attrStub) ReplaceShowAttributes(_ context.Context, in citypebiz.ShowAttributesSpec) error {
	if s.prefErr != nil {
		return s.prefErr
	}
	s.replacedShow = &in
	return nil
}

func (s *attrStub) ClearShowAttributes(context.Context, model.ID, model.ID) error { return nil }

func (s *attrStub) DeleteShowAttributesForType(context.Context, model.ID) error { return nil }

func (s *attrStub) DeleteShowAttributesForAttr(context.Context, []model.ID, model.ID) error {
	return nil
}

func (s *attrStub) SubscribedTypeIDs(context.Context, model.ID) ([]model.ID, error) {
	seen := map[model.ID]bool{}
	var out []model.ID
	for _, r := range s.showAttrs {
		if !seen[r.TypeID] {
			seen[r.TypeID] = true
			out = append(out, r.TypeID)
		}
	}
	return out, nil
}

func (s *attrStub) TypeOrder(context.Context, model.ID) ([]citypemodel.PreferenceCITypeOrder, error) {
	return s.typeOrder, nil
}

func (s *attrStub) UpsertTypeOrder(_ context.Context, in citypebiz.TypeOrderSpec) error {
	if s.prefErr != nil {
		return s.prefErr
	}
	s.upsertedOrder = &in
	return nil
}

func (s *attrStub) AutoSubscription(context.Context, model.ID) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	if s.prefErr != nil {
		return nil, s.prefErr
	}
	if s.autoSub == nil {
		return nil, nil
	}
	copied := *s.autoSub
	return &copied, nil
}

func (s *attrStub) SaveAutoSubscription(_ context.Context, in citypebiz.AutoSubscriptionSpec) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	if s.prefErr != nil {
		return nil, s.prefErr
	}
	s.savedAutoSub = &in
	row := citypemodel.PreferenceAutoSubscriptionConfig{
		UID: in.UID, BaseStrategy: string(in.Strategy),
		GroupIDs: citypemodel.IDList(in.GroupIDs), TypeIDs: citypemodel.IDList(in.TypeIDs),
		Enabled: in.Enabled,
	}
	row.ID = 900
	s.autoSub = &row
	return &row, nil
}

func (s *attrStub) DeleteAutoSubscription(context.Context, model.ID) error {
	if s.prefErr != nil {
		return s.prefErr
	}
	s.autoSub = nil
	return nil
}

func (s *attrStub) TypeGroups(context.Context) ([]citypemodel.CITypeGroup, error) {
	if s.prefErr != nil {
		return nil, s.prefErr
	}
	return s.typeGroups, nil
}

func (s *attrStub) TypeGroupByID(_ context.Context, id model.ID) (*citypemodel.CITypeGroup, error) {
	for _, g := range s.typeGroups {
		if g.ID == id {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) TypeGroupByName(_ context.Context, name string) (*citypemodel.CITypeGroup, error) {
	for _, g := range s.typeGroups {
		if g.Name == name {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) TypeGroupItems(context.Context) ([]citypemodel.CITypeGroupItem, error) {
	return s.typeGroupItems, nil
}

func (s *attrStub) CreateTypeGroup(_ context.Context, in citypebiz.TypeGroupCreateSpec) (*citypemodel.CITypeGroup, error) {
	if s.groupErr != nil {
		return nil, s.groupErr
	}
	row := citypemodel.CITypeGroup{Name: in.Name}
	row.ID = model.ID(len(s.typeGroups) + 300)
	s.typeGroups = append(s.typeGroups, row)
	return &row, nil
}

func (s *attrStub) UpdateTypeGroup(_ context.Context, in citypebiz.TypeGroupUpdateSpec) error {
	if s.groupErr != nil {
		return s.groupErr
	}
	s.updatedGroup = &in
	for i := range s.typeGroups {
		if s.typeGroups[i].ID == in.ID && in.Name != "" {
			s.typeGroups[i].Name = in.Name
		}
	}
	return nil
}

func (s *attrStub) DeleteTypeGroup(_ context.Context, in citypebiz.TypeGroupDeleteSpec) error {
	if s.groupErr != nil {
		return s.groupErr
	}
	s.deletedGroup = &in
	return nil
}

func (s *attrStub) ReorderTypeGroups(_ context.Context, in citypebiz.TypeGroupReorderSpec) error {
	if s.groupErr != nil {
		return s.groupErr
	}
	s.reorderedGroups = &in
	return nil
}

// --- authorization ---

// THE SPLIT THAT MATTERS. A subscription is one person's view of the inventory,
// so gating it on an administrative role would mean nobody but an administrator
// could choose which columns they see. A grouping is the sidebar everyone sees,
// so it is administrative.
func TestPreferenceWritesDoNotRequireAdmin(t *testing.T) {
	stub := prefStub()
	r := attrRouter(t, stub, "user", false, 7)

	cases := []struct{ method, path, body string }{
		{http.MethodPut, "/v1/cmdb/preferences/ci-types/10/fields", `{"fields":[{"ref":"hostname"}]}`},
		{http.MethodPut, "/v1/cmdb/preferences/ci-types/order", `{"type_ids":[10]}`},
		{http.MethodPut, "/v1/cmdb/preferences/auto-subscription", `{"base_strategy":"all"}`},
		{http.MethodDelete, "/v1/cmdb/preferences/auto-subscription", ``},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, r, tc.method, tc.path, tc.body)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("status = 403, want the caller's OWN preference to be writable (body %s)",
					rec.Body.String())
			}
		})
	}
}

func TestModelGroupWritesRequireAdmin(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cmdb/model-groups", `{"name":"g"}`},
		{http.MethodPut, "/v1/cmdb/model-groups/300", `{"name":"g"}`},
		{http.MethodDelete, "/v1/cmdb/model-groups/300", ``},
		{http.MethodPut, "/v1/cmdb/model-groups/order", `{"group_ids":[300]}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, attrRouter(t, prefStub(), "user", false, 7), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPreferenceRoutesWithoutIdentityAreUnauthorized(t *testing.T) {
	h := NewHandler(citypebiz.NewUsecase(prefStub(), nil))
	r := newTestRouterNoIdentity(h)

	rec := send(t, r, http.MethodGet, "/v1/cmdb/preferences/ci-types", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

// --- the subscription is the caller's own ---

// The uid comes from the caller's identity and never from the request, so one
// user cannot read or rewrite another's subscription by naming them.
func TestSubscriptionIsScopedToTheCaller(t *testing.T) {
	stub := prefStub()
	rec := send(t, attrRouter(t, stub, "user", false, 7), http.MethodPut,
		"/v1/cmdb/preferences/ci-types/10/fields",
		`{"fields":[{"ref":"hostname","is_fixed":true}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if stub.replacedShow == nil || stub.replacedShow.UID != 7 {
		t.Fatalf("uid = %+v, want the caller's own id", stub.replacedShow)
	}
}

// A JSON body with numeric references is what the console sends, and cantor
// answered 500 to it because its id-versus-name test called .isdigit() on a
// number.
func TestSetFieldsAcceptsNumericAndNamedReferences(t *testing.T) {
	stub := prefStub()
	rec := send(t, attrRouter(t, stub, "user", false, 7), http.MethodPut,
		"/v1/cmdb/preferences/ci-types/10/fields",
		`{"fields":[{"ref":"1"},{"ref":"hostname"},{"ref":"_updated_by"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for integer references (body %s)", rec.Code, rec.Body.String())
	}
	if got := len(stub.replacedShow.Fields); got != 3 {
		t.Fatalf("fields = %d, want 3", got)
	}
	if stub.replacedShow.Fields[2].Builtin != "_updated_by" {
		t.Fatalf("third field = %+v", stub.replacedShow.Fields[2])
	}
}

func TestSetFieldsRejectsAnEmptyList(t *testing.T) {
	rec := send(t, attrRouter(t, prefStub(), "user", false, 7), http.MethodPut,
		"/v1/cmdb/preferences/ci-types/10/fields", `{"fields":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: an empty list means two different things (body %s)",
			rec.Code, rec.Body.String())
	}
}

func TestGetFieldsShape(t *testing.T) {
	stub := prefStub()
	stub.showAttrs = []citypemodel.PreferenceShowAttributes{
		{UID: 7, TypeID: 10, AttrID: attrIDPtr(1), Order: 0},
		{UID: 7, TypeID: 10, BuiltinAttr: strptr("_updated_at"), Order: 1},
	}

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/preferences/ci-types/10/fields")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got showAttributesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if !got.IsSubscribed || len(got.Fields) != 2 {
		t.Fatalf("response = %+v", got)
	}
	if got.Fields[0].Name != "hostname" || got.Fields[0].AttrID == nil {
		t.Fatalf("first = %+v", got.Fields[0])
	}
	// No label is returned for a built-in column: user-facing text belongs to
	// the console's tr(), and cantor shipped English strings from the backend.
	if got.Fields[1].Builtin != "_updated_at" {
		t.Fatalf("second = %+v", got.Fields[1])
	}
	if strings.Contains(rec.Body.String(), "Update Time") {
		t.Fatalf("body = %s, want no backend-supplied label", rec.Body.String())
	}
}

// --- the sidebar and the rule ---

func TestSubscribedModelsShape(t *testing.T) {
	stub := prefStub()
	stub.showAttrs = []citypemodel.PreferenceShowAttributes{{UID: 7, TypeID: 10, AttrID: attrIDPtr(1)}}

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/preferences/ci-types")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got subscribedModelsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(got.TypeIDs) != 1 || got.TypeIDs[0] != 10 {
		t.Fatalf("type ids = %v", got.TypeIDs)
	}
	if len(got.Groups) != 1 || !got.Groups[0].IsOther {
		t.Fatalf("groups = %+v, want the ungrouped bucket", got.Groups)
	}
}

// No rule is a state, not an error: the console renders the manual
// subscription instead.
func TestAutoSubscriptionAbsentIsAnEmptyObject(t *testing.T) {
	rec := do(t, attrRouter(t, prefStub(), "user", false, 7), "/v1/cmdb/preferences/auto-subscription")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 rather than 404 (body %s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("response = %+v, want an empty object", got)
	}
}

func TestAutoSubscriptionRoundTrip(t *testing.T) {
	stub := prefStub()
	r := attrRouter(t, stub, "user", false, 7)

	rec := send(t, r, http.MethodPut, "/v1/cmdb/preferences/auto-subscription",
		`{"base_strategy":"none","type_ids":[10],"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if stub.savedAutoSub == nil || stub.savedAutoSub.Strategy != citypebiz.SubscribeNone {
		t.Fatalf("saved = %+v", stub.savedAutoSub)
	}
	if rec := send(t, r, http.MethodDelete, "/v1/cmdb/preferences/auto-subscription", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAutoSubscriptionRejectsABadStrategy(t *testing.T) {
	rec := send(t, attrRouter(t, prefStub(), "user", false, 7), http.MethodPut,
		"/v1/cmdb/preferences/auto-subscription", `{"base_strategy":"sometimes"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAutoSubscriptionRejectsAnUnknownModel(t *testing.T) {
	rec := send(t, attrRouter(t, prefStub(), "user", false, 7), http.MethodPut,
		"/v1/cmdb/preferences/auto-subscription", `{"base_strategy":"none","type_ids":[999]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a model that does not exist (body %s)",
			rec.Code, rec.Body.String())
	}
}

// --- model groups ---

func TestCreateModelGroupReturns201(t *testing.T) {
	stub := prefStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/model-groups", `{"name":"硬件"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got modelGroupDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "硬件" || got.ID == 0 {
		t.Fatalf("response = %+v", got)
	}
	// An empty member list must serialise as [] rather than null.
	if got.Types == nil {
		t.Fatalf("ci_types was null: %s", rec.Body.String())
	}
}

func TestCreateModelGroupDuplicateIsConflict(t *testing.T) {
	stub := prefStub()
	stub.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	stub.typeGroups[0].ID = 300

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/model-groups", `{"name":"硬件"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdateModelGroupSetsMembership(t *testing.T) {
	stub := prefStub()
	stub.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	stub.typeGroups[0].ID = 300

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPut,
		"/v1/cmdb/model-groups/300", `{"name":"网络","type_ids":[10]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if stub.updatedGroup == nil || len(stub.updatedGroup.TypeIDs) != 1 {
		t.Fatalf("updated = %+v", stub.updatedGroup)
	}
	if stub.updatedGroup.Name != "网络" {
		t.Fatalf("name = %q", stub.updatedGroup.Name)
	}
}

// A DELETE with no body removes the group; naming models detaches them and
// keeps it.
func TestDeleteModelGroupBothShapes(t *testing.T) {
	stub := prefStub()
	stub.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	stub.typeGroups[0].ID = 300
	r := attrRouter(t, stub, "admin", false, 7)

	if rec := send(t, r, http.MethodDelete, "/v1/cmdb/model-groups/300", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("bodyless delete status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if stub.deletedGroup == nil || len(stub.deletedGroup.TypeIDs) != 0 {
		t.Fatalf("deleted = %+v, want the whole group", stub.deletedGroup)
	}

	if rec := send(t, r, http.MethodDelete, "/v1/cmdb/model-groups/300", `{"type_ids":[10]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("detach status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if len(stub.deletedGroup.TypeIDs) != 1 || stub.deletedGroup.TypeIDs[0] != 10 {
		t.Fatalf("deleted = %+v, want just the named model detached", stub.deletedGroup)
	}
}

func TestDeleteModelGroupMissingIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, prefStub(), "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/model-groups/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestReorderModelGroupsRefusesAStaleID(t *testing.T) {
	stub := prefStub()
	stub.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	stub.typeGroups[0].ID = 300

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPut,
		"/v1/cmdb/model-groups/order", `{"group_ids":[300,999]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: cantor skipped stale ids and reordered the rest "+
			"(body %s)", rec.Code, rec.Body.String())
	}
	if stub.reorderedGroups != nil {
		t.Fatal("the reorder ran despite the stale id")
	}
}

func TestPreferenceRepoFailureIsInternalError(t *testing.T) {
	stub := prefStub()
	stub.prefErr = errBoom

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/preferences/ci-types")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- fixtures ---

// prefStub seeds one model with one attribute.
func prefStub() *attrStub {
	stub := newAttrStub().seedAttribute(attr(1, "hostname"))
	ct := typeRow(10, "host")
	ct.UniqueID = 1
	return stub.seedType(ct)
}

func attrIDPtr(id model.ID) *model.ID { return &id }

func newTestRouterNoIdentity(h *Handler) chi.Router {
	r := chi.NewRouter()
	h.Register(r) // no identity middleware at all
	return r
}
