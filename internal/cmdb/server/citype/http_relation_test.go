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

// --- the relation half of the server double ---

func (s *attrStub) ListTypeRelations(_ context.Context, f citypebiz.TypeRelationFilter) ([]citypemodel.CITypeRelation, error) {
	if s.relationErr != nil {
		return nil, s.relationErr
	}
	want := map[model.ID]bool{}
	for _, id := range f.TypeIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeRelation, 0, len(s.relations))
	for _, r := range s.relations {
		if len(want) > 0 && !want[r.ParentID] && !want[r.ChildID] {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *attrStub) TypeRelationByID(_ context.Context, id model.ID) (*citypemodel.CITypeRelation, error) {
	for _, r := range s.relations {
		if r.ID == id {
			copied := r
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) TypeRelationByPair(_ context.Context, parentID, childID model.ID) (*citypemodel.CITypeRelation, error) {
	for _, r := range s.relations {
		if r.ParentID == parentID && r.ChildID == childID {
			copied := r
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *attrStub) SaveTypeRelation(_ context.Context, in citypebiz.TypeRelationSaveSpec) (*citypemodel.CITypeRelation, error) {
	if s.relationErr != nil {
		return nil, s.relationErr
	}
	s.savedRelation = &in
	row := citypemodel.CITypeRelation{
		ParentID: in.ParentID, ChildID: in.ChildID,
		RelationTypeID: in.RelationTypeID,
		ParentAttrIDs:  citypemodel.IDList(in.ParentAttrIDs),
		ChildAttrIDs:   citypemodel.IDList(in.ChildAttrIDs),
	}
	constraint := in.Constraint
	row.Constraint = &constraint
	row.ID = model.ID(len(s.relations) + 700)
	s.relations = append(s.relations, row)
	return &row, nil
}

func (s *attrStub) DeleteTypeRelation(_ context.Context, in citypebiz.TypeRelationDeleteSpec) error {
	if s.relationErr != nil {
		return s.relationErr
	}
	s.deletedRelation = &in
	return nil
}

func (s *attrStub) RelationTypesByID(_ context.Context, ids []model.ID) (map[model.ID]citypemodel.RelationType, error) {
	out := map[model.ID]citypemodel.RelationType{}
	for _, id := range ids {
		if rt, ok := s.relationTypes[id]; ok {
			out[id] = rt
		}
	}
	return out, nil
}

// --- fixtures ---

// relationStub seeds two models and a relation type.
func relationStub() *attrStub {
	stub := newAttrStub().
		seedAttribute(attr(1, "hostname")).
		seedAttribute(attr(2, "idc_name"))
	host := typeRow(10, "host")
	host.UniqueID = 1
	idc := typeRow(20, "idc")
	idc.UniqueID = 2
	stub.seedType(host).seedType(idc)
	rt := citypemodel.RelationType{Name: "runs_on", Color: "#abc"}
	rt.ID = 5
	stub.relationTypes[5] = rt
	return stub
}

func addedRelation(id, parent, child, rtID model.ID) citypemodel.CITypeRelation {
	row := citypemodel.CITypeRelation{ParentID: parent, ChildID: child, RelationTypeID: rtID}
	constraint := "0"
	row.Constraint = &constraint
	row.ID = id
	return row
}

// --- authorization ---

// The reads are open and the writes are admin-gated, which follows cantor: its
// per-model children and parents reads carried no permission check, and the
// resource-search tree expands descendants and paths.
func TestRelationWriteRoutesRequireAdmin(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cmdb/ci-types/10/relation-children/20", `{"relation_type_id":5}`},
		{http.MethodDelete, "/v1/cmdb/ci-types/10/relation-children/20", ``},
		{http.MethodDelete, "/v1/cmdb/ci-type-relations/700", ``},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, attrRouter(t, relationStub(), "user", false, 7), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRelationWriteRoutesWithoutIdentityAreUnauthorized(t *testing.T) {
	h := NewHandler(citypebiz.NewUsecase(relationStub(), nil))
	r := chi.NewRouter()
	h.Register(r)

	rec := send(t, r, http.MethodPost, "/v1/cmdb/ci-types/10/relation-children/20",
		`{"relation_type_id":5}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

func TestRelationReadRoutesDoNotRequireAdmin(t *testing.T) {
	stub := relationStub()
	stub.relations = []citypemodel.CITypeRelation{addedRelation(700, 10, 20, 5)}
	r := attrRouter(t, stub, "user", false, 7)

	for _, path := range []string{
		"/v1/cmdb/ci-type-relations",
		"/v1/cmdb/ci-types/10/relation-children",
		"/v1/cmdb/ci-types/10/relation-parents",
		"/v1/cmdb/ci-types/10/relation-descendants",
		"/v1/cmdb/ci-type-relations/path?source_type_id=10&target_type_ids=20",
	} {
		if rec := do(t, r, path); rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (body %s)", path, rec.Code, rec.Body.String())
		}
	}
}

// --- reads ---

func TestListTypeRelationsShape(t *testing.T) {
	stub := relationStub()
	stub.relations = []citypemodel.CITypeRelation{addedRelation(700, 10, 20, 5)}

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/ci-type-relations")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got relationListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("response = %+v, want one edge", got)
	}
	edge := got.Items[0]
	if edge.Parent.Type.Name != "host" || edge.Child.Type.Name != "idc" {
		t.Fatalf("edge = %+v", edge)
	}
	if edge.Parent.RelationType.Name != "runs_on" || edge.Parent.RelationType.Color != "#abc" {
		t.Fatalf("relation type = %+v, want the dictionary entry resolved", edge.Parent.RelationType)
	}
	// The ctr_id is what the console needs to address the write endpoints,
	// which is why every rendered edge carries it.
	if edge.Parent.CTRID != 700 || edge.Child.CTRID != 700 {
		t.Fatalf("ctr ids = %d/%d, want the relation's own id on both sides",
			edge.Parent.CTRID, edge.Child.CTRID)
	}
	// Null arrays would make the SPA branch on them.
	if edge.Parent.ParentAttrIDs == nil || edge.Child.ChildAttrIDs == nil {
		t.Fatalf("attribute id arrays must serialise as [] rather than null: %s", rec.Body.String())
	}
}

func TestListRelationChildrenAndParents(t *testing.T) {
	stub := relationStub()
	stub.relations = []citypemodel.CITypeRelation{addedRelation(700, 10, 20, 5)}

	children := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/ci-types/10/relation-children")
	var got struct {
		Items []relatedTypeDTO `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(children.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, children.Body.String())
	}
	if got.Total != 1 || got.Items[0].Type.Name != "idc" {
		t.Fatalf("children = %+v, want idc", got)
	}

	parents := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/ci-types/20/relation-parents")
	if err := json.Unmarshal(parents.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || got.Items[0].Type.Name != "host" {
		t.Fatalf("parents = %+v, want host", got)
	}
}

func TestListRelationDescendantsIsOrderedByLevel(t *testing.T) {
	stub := relationStub()
	third := typeRow(30, "switch")
	third.UniqueID = 1
	stub.seedType(third)
	stub.relations = []citypemodel.CITypeRelation{
		addedRelation(700, 10, 20, 5),
		addedRelation(701, 20, 30, 5),
	}

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/ci-types/10/relation-descendants")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Levels []levelDTO `json:"levels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	// An ordered array rather than cantor's level-keyed object: a JSON object's
	// key order is undefined, so a caller rendering levels in order would have
	// had to sort the keys back.
	if len(got.Levels) != 2 || got.Levels[0].Level != 1 || got.Levels[1].Level != 2 {
		t.Fatalf("levels = %+v, want 1 then 2", got.Levels)
	}
	if got.Levels[0].Types[0].Name != "idc" || got.Levels[1].Types[0].Name != "switch" {
		t.Fatalf("levels = %+v", got.Levels)
	}
}

func TestFindTypePathsShape(t *testing.T) {
	stub := relationStub()
	third := typeRow(30, "switch")
	third.UniqueID = 1
	stub.seedType(third)
	stub.relations = []citypemodel.CITypeRelation{
		addedRelation(700, 10, 20, 5),
		addedRelation(701, 20, 30, 5),
	}

	rec := do(t, attrRouter(t, stub, "user", false, 7),
		"/v1/cmdb/ci-type-relations/path?source_type_id=10&target_type_ids=30")
	var got struct {
		Paths [][]model.ID `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(got.Paths) != 1 || len(got.Paths[0]) != 3 {
		t.Fatalf("paths = %v, want one route of three models", got.Paths)
	}
}

func TestFindTypePathsValidatesItsQuery(t *testing.T) {
	r := attrRouter(t, relationStub(), "user", false, 7)
	for _, q := range []string{
		"?target_type_ids=20",
		"?source_type_id=10",
		"?source_type_id=10&target_type_ids=",
		"?source_type_id=nope&target_type_ids=20",
	} {
		rec := do(t, r, "/v1/cmdb/ci-type-relations/path"+q)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 400 or 404 (body %s)", q, rec.Code, rec.Body.String())
		}
	}
}

func TestListTypeRelationsRejectsABadTypeIDs(t *testing.T) {
	r := attrRouter(t, relationStub(), "user", false, 7)
	for _, q := range []string{"?type_ids=abc", "?type_ids=0", "?type_ids=10,abc"} {
		if rec := do(t, r, "/v1/cmdb/ci-type-relations"+q); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400 (body %s)", q, rec.Code, rec.Body.String())
		}
	}
}

// --- writes ---

func TestSaveTypeRelationReturnsTheEdge(t *testing.T) {
	stub := relationStub()
	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/relation-children/20",
		`{"relation_type_id":5,"constraint":"2","parent_attr_ids":[1],"child_attr_ids":[2]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if stub.savedRelation == nil {
		t.Fatal("the save was not forwarded")
	}
	if stub.savedRelation.Actor != 7 {
		t.Fatal("the caller must be recorded on the history row")
	}
	if stub.savedRelation.Constraint != "2" {
		t.Fatalf("constraint = %q", stub.savedRelation.Constraint)
	}

	var got relationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Parent.Type.Name != "host" || got.Child.Type.Name != "idc" {
		t.Fatalf("response = %+v", got)
	}
}

func TestSaveTypeRelationWithoutARelationTypeIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, relationStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/relation-children/20", `{"relation_type_id":999}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestSaveTypeRelationSelfLoopIsBadRequest(t *testing.T) {
	rec := send(t, attrRouter(t, relationStub(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/10/relation-children/10", `{"relation_type_id":5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// A cycle is a statement about the graph's current state, so it is a 409 rather
// than a malformed request the caller could fix by editing the body.
func TestSaveTypeRelationCycleIsConflict(t *testing.T) {
	stub := relationStub()
	stub.relationErr = &citypebiz.RelationCycleError{Path: []model.ID{10, 20, 10}}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/20/relation-children/10", `{"relation_type_id":5}`)
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
	// The refusal names the models involved, which is what makes a whole-graph
	// check actionable.
	for _, want := range []string{"10", "20"} {
		if !strings.Contains(body.Error, want) {
			t.Fatalf("error = %q, want the cycle's participants named", body.Error)
		}
	}
}

func TestDeleteTypeRelationByPairReturns204(t *testing.T) {
	stub := relationStub()
	stub.relations = []citypemodel.CITypeRelation{addedRelation(700, 10, 20, 5)}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/relation-children/20", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if stub.deletedRelation == nil || stub.deletedRelation.ID != 700 {
		t.Fatalf("deleted = %+v", stub.deletedRelation)
	}
}

// cantor's pair-delete dereferenced a missing row without checking, so this
// answered 500 with a Python attribute error.
func TestDeleteTypeRelationMissingPairIsNotFound(t *testing.T) {
	rec := send(t, attrRouter(t, relationStub(), "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/10/relation-children/20", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteTypeRelationByIDReturns204(t *testing.T) {
	stub := relationStub()
	stub.relations = []citypemodel.CITypeRelation{addedRelation(700, 10, 20, 5)}

	rec := send(t, attrRouter(t, stub, "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-type-relations/700", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestRelationRepoFailureIsInternalError(t *testing.T) {
	stub := relationStub()
	stub.relationErr = errBoom

	rec := do(t, attrRouter(t, stub, "user", false, 7), "/v1/cmdb/ci-type-relations")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}
