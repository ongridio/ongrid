package citype

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// --- the service-tree half of the server double ---

func (s *attrStub) ServiceTrees(_ context.Context, visibleTo model.ID, includeAll bool) ([]citypemodel.PreferenceRelationView, error) {
	if s.treeErr != nil {
		return nil, s.treeErr
	}
	var out []citypemodel.PreferenceRelationView
	for _, v := range s.serviceTrees {
		if includeAll || v.UID == visibleTo || v.IsPublic {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *attrStub) ServiceTreeByID(_ context.Context, id model.ID) (*citypemodel.PreferenceRelationView, error) {
	for _, v := range s.serviceTrees {
		if v.ID == id {
			copied := v
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) ServiceTreeByName(_ context.Context, name string) (*citypemodel.PreferenceRelationView, error) {
	for _, v := range s.serviceTrees {
		if v.Name == name {
			copied := v
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) SaveServiceTree(_ context.Context, in citypebiz.ServiceTreeSaveSpec) (*citypemodel.PreferenceRelationView, error) {
	if s.treeErr != nil {
		return nil, s.treeErr
	}
	s.savedTree = &in
	row := citypemodel.PreferenceRelationView{
		UID: in.UID, Name: in.Name, CRIDs: citypemodel.TypePairList(in.Pairs),
		IsPublic: in.IsPublic, Option: in.Option,
	}
	if in.ID > 0 {
		row.ID = in.ID
	} else {
		row.ID = model.ID(len(s.serviceTrees) + 400)
	}
	s.serviceTrees = append(s.serviceTrees, row)
	return &row, nil
}

func (s *attrStub) DeleteServiceTree(_ context.Context, in citypebiz.ServiceTreeDeleteSpec) error {
	if s.treeErr != nil {
		return s.treeErr
	}
	s.deletedTree = &in
	return nil
}

func (s *attrStub) ServiceTreesReferencing(_ context.Context, typeID model.ID) ([]citypemodel.PreferenceRelationView, error) {
	var out []citypemodel.PreferenceRelationView
	for _, v := range s.serviceTrees {
		for _, p := range v.CRIDs {
			if p.ParentID == typeID || p.ChildID == typeID {
				out = append(out, v)
				break
			}
		}
	}
	return out, nil
}

func (s *attrStub) SearchOptions(_ context.Context, f citypebiz.SearchOptionFilter) ([]citypemodel.PreferenceSearchOption, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	var out []citypemodel.PreferenceSearchOption
	for _, o := range s.searchOptions {
		if o.UID == nil || *o.UID != f.UID {
			continue
		}
		if f.Name != "" && (o.Name == nil || *o.Name != f.Name) {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *attrStub) SearchOptionByID(_ context.Context, id model.ID) (*citypemodel.PreferenceSearchOption, error) {
	for _, o := range s.searchOptions {
		if o.ID == id {
			copied := o
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) CreateSearchOption(_ context.Context, in citypebiz.SearchOptionCreateSpec) (*citypemodel.PreferenceSearchOption, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	s.createdSearch = &in
	uid, name := in.UID, in.Name
	row := citypemodel.PreferenceSearchOption{
		UID: &uid, Name: &name, Option: in.Option,
		PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
	}
	row.ID = model.ID(len(s.searchOptions) + 600)
	s.searchOptions = append(s.searchOptions, row)
	return &row, nil
}

func (s *attrStub) UpdateSearchOption(_ context.Context, in citypebiz.SearchOptionUpdateSpec) error {
	if s.searchErr != nil {
		return s.searchErr
	}
	s.updatedSearch = &in
	for i := range s.searchOptions {
		if s.searchOptions[i].ID == in.ID {
			name := in.Name
			s.searchOptions[i].Name = &name
		}
	}
	return nil
}

func (s *attrStub) DeleteSearchOption(_ context.Context, id model.ID) error {
	if s.searchErr != nil {
		return s.searchErr
	}
	s.deletedSearch = &id
	return nil
}

// --- fixtures ---

// treeStub seeds two models with a relation between them.
func treeStub() *attrStub {
	stub := newAttrStub().seedAttribute(attr(1, "hostname"))
	a := typeRow(10, "idc")
	a.UniqueID = 1
	b := typeRow(20, "rack")
	b.UniqueID = 1
	stub.seedType(a).seedType(b)
	if stub.relationTypes == nil {
		stub.relationTypes = map[model.ID]citypemodel.RelationType{}
	}
	row := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: 5}
	constraint := "0"
	row.Constraint = &constraint
	row.ID = 700
	stub.relations = []citypemodel.CITypeRelation{row}
	return stub
}

// --- authorization ---

// A service tree is a page DEFINITION, so shaping it is administrative — that
// is what cantor's Service_Tree_Definition role gate meant. A saved search is
// something a person does to their own searches, so those are not gated.
func TestServiceTreeWritesRequireAdmin(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cmdb/preferences/service-trees",
			`{"name":"t","cr_ids":[{"parent_id":10,"child_id":20}]}`},
		{http.MethodPut, "/v1/cmdb/preferences/service-trees/400",
			`{"name":"t","cr_ids":[{"parent_id":10,"child_id":20}]}`},
		{http.MethodDelete, "/v1/cmdb/preferences/service-trees/400", ``},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, attrRouter(t, treeStub(), "user", false, 7), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSavedSearchWritesDoNotRequireAdmin(t *testing.T) {
	r := attrRouter(t, treeStub(), "user", false, 7)
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cmdb/preferences/saved-searches", `{"name":"mine","option":{"q":"x"}}`},
		{http.MethodPut, "/v1/cmdb/preferences/saved-searches/600", `{"name":"mine","option":{"q":"y"}}`},
		{http.MethodDelete, "/v1/cmdb/preferences/saved-searches/600", ``},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, r, tc.method, tc.path, tc.body)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("status = 403, want the caller's own searches writable (body %s)", rec.Body.String())
			}
		})
	}
}

func TestServiceTreeRoutesWithoutIdentityAreUnauthorized(t *testing.T) {
	h := NewHandler(citypebiz.NewUsecase(treeStub(), nil))
	r := newTestRouterNoIdentity(h)

	rec := send(t, r, http.MethodGet, "/v1/cmdb/preferences/service-trees", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

// --- service trees ---

func TestSaveServiceTreeReturnsTheRendering(t *testing.T) {
	stub := treeStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/preferences/service-trees",
		`{"name":"机房拓扑","cr_ids":[{"parent_id":10,"child_id":20}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got serviceTreeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Name != "机房拓扑" || len(got.Pairs) != 1 {
		t.Fatalf("response = %+v", got)
	}
	// The stored pairs are returned alongside the rendering, because they are
	// what an edit sends back.
	if got.Pairs[0].ParentID != 10 || got.Pairs[0].ChildID != 20 {
		t.Fatalf("pairs = %+v", got.Pairs)
	}
	if len(got.Tree.Order) != 2 || got.Tree.Order[0] != 10 {
		t.Fatalf("rendering = %+v", got.Tree)
	}
	// Arrays must serialise as [] rather than null.
	if got.Tree.LevelConstraints == nil || got.Tree.LeafShowTypes == nil {
		t.Fatalf("body = %s, want empty arrays rather than null", rec.Body.String())
	}
}

func TestSaveServiceTreeWithABadPairIsNotFound(t *testing.T) {
	stub := treeStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/preferences/service-trees",
		`{"name":"t","cr_ids":[{"parent_id":10,"child_id":999}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	// Nothing was stored.
	if stub.savedTree != nil {
		t.Fatal("the definition reached the store despite the bad pair")
	}
}

func TestSaveServiceTreeNeedsANameAndPairs(t *testing.T) {
	stub := treeStub()
	r := attrRouter(t, stub, "admin", false, 7)
	for _, body := range []string{
		`{"cr_ids":[{"parent_id":10,"child_id":20}]}`,
		`{"name":"t","cr_ids":[]}`,
	} {
		rec := send(t, r, http.MethodPost, "/v1/cmdb/preferences/service-trees", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400 (got %s)", body, rec.Code, rec.Body.String())
		}
	}
}

func TestListServiceTreesShape(t *testing.T) {
	stub := treeStub()
	stub.serviceTrees = []citypemodel.PreferenceRelationView{{
		UID: 7, Name: "t", CRIDs: citypemodel.TypePairList{{ParentID: 10, ChildID: 20}},
	}}
	stub.serviceTrees[0].ID = 400

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/preferences/service-trees")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got serviceTreeListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("response = %+v", got)
	}
	// The level constraints are a LIST, not an object: the keys are numbers,
	// and a JSON object's keys are strings in no defined order.
	if strings.Contains(rec.Body.String(), `"level2constraint":{`) {
		t.Fatalf("body = %s, want an ordered list of steps", rec.Body.String())
	}
}

func TestListServiceTreesVisibility(t *testing.T) {
	stub := treeStub()
	mine := citypemodel.PreferenceRelationView{UID: 7, Name: "mine", CRIDs: citypemodel.TypePairList{{ParentID: 10, ChildID: 20}}}
	mine.ID = 400
	theirs := citypemodel.PreferenceRelationView{UID: 8, Name: "theirs", CRIDs: citypemodel.TypePairList{{ParentID: 10, ChildID: 20}}}
	theirs.ID = 401
	stub.serviceTrees = []citypemodel.PreferenceRelationView{mine, theirs}

	r := attrRouter(t, stub, "user", false, 7)
	rec := do(t, r, "/v1/cmdb/preferences/service-trees")
	var got serviceTreeListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || got.Items[0].Name != "mine" {
		t.Fatalf("response = %+v, want only the caller's own by default", got)
	}

	rec = do(t, r, "/v1/cmdb/preferences/service-trees?include_all=1")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 {
		t.Fatalf("total = %d, want everything for the management page", got.Total)
	}
}

// cantor's route declared a path id and read a NAME from the body, so the id was
// ignored and every definition sharing that name was removed — across users.
func TestDeleteServiceTreeUsesThePathID(t *testing.T) {
	stub := treeStub()
	stub.serviceTrees = []citypemodel.PreferenceRelationView{{
		UID: 7, Name: "t", CRIDs: citypemodel.TypePairList{{ParentID: 10, ChildID: 20}},
	}}
	stub.serviceTrees[0].ID = 400

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/preferences/service-trees/400", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if stub.deletedTree == nil || stub.deletedTree.ID != 400 {
		t.Fatalf("deleted = %+v, want the path id", stub.deletedTree)
	}
}

func TestDeleteServiceTreeMissingIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, treeStub(), "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/preferences/service-trees/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- can-edit ---

func TestCanEditRelationRoute(t *testing.T) {
	r := attrRouter(t, treeStub(), "user", false, 7)
	for _, q := range []string{"", "?parent_id=10", "?parent_id=abc&child_id=20", "?parent_id=0&child_id=20"} {
		rec := do(t, r, "/v1/cmdb/ci-type-relations/can-edit"+q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q status = %d, want 400 (body %s)", q, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, r, "/v1/cmdb/ci-type-relations/can-edit?parent_id=10&child_id=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Result {
		t.Fatal("result = false, want the pair editable with no service tree")
	}
}

// --- saved searches ---

func TestSavedSearchRoundTrip(t *testing.T) {
	stub := treeStub()
	r := attrRouter(t, stub, "user", false, 7)

	rec := send(t, r, http.MethodPost, "/v1/cmdb/preferences/saved-searches",
		`{"name":"my query","option":{"q":"hostname:x"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var created savedSearchDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "my query" {
		t.Fatalf("response = %+v", created)
	}
	if stub.createdSearch.UID != 7 {
		t.Fatal("the search must belong to the caller")
	}

	// cantor's update crashed on every call.
	rec = send(t, r, http.MethodPut, "/v1/cmdb/preferences/saved-searches/600",
		`{"name":"renamed","option":{"q":"hostname:y"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want it to work at all (body %s)", rec.Code, rec.Body.String())
	}

	rec = send(t, r, http.MethodDelete, "/v1/cmdb/preferences/saved-searches/600", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d (body %s)", rec.Code, rec.Body.String())
	}
}

func TestSavedSearchDuplicateIsConflict(t *testing.T) {
	stub := treeStub()
	uid := model.ID(7)
	name := "mine"
	row := citypemodel.PreferenceSearchOption{UID: &uid, Name: &name}
	row.ID = 600
	stub.searchOptions = []citypemodel.PreferenceSearchOption{row}

	rec := send(t, attrRouter(t, stub, "user", false, 7), http.MethodPost,
		"/v1/cmdb/preferences/saved-searches", `{"name":"mine","option":{"q":"x"}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestSavedSearchMissingIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, treeStub(), "user", false, 7), http.MethodDelete,
		"/v1/cmdb/preferences/saved-searches/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestServiceTreeRepoFailureIsInternalError(t *testing.T) {
	stub := treeStub()
	stub.treeErr = errBoom

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/preferences/service-trees")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}
