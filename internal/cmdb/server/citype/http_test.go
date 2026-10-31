package citype

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type stubRepo struct {
	// The attribute-management half is a separate double; see http_attr_test.go.
	noAttrStub

	types  []citypemodel.CIType
	attrs  map[model.ID]citypemodel.Attribute
	parent map[model.ID][]model.ID
	icons  map[model.ID]string
	fail   bool

	// deleteErr lets a test make the delete path report a dependency refusal.
	deleteErr error

	// Recorded by the write methods, which live in http_write_test.go.
	lastCreate   *citypebiz.CreateTypeInput
	deletedActor model.ID
}

func (s *stubRepo) ListTypes(_ context.Context, f citypebiz.ListFilter) ([]citypemodel.CIType, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	out := make([]citypemodel.CIType, 0, len(s.types))
	for _, ct := range s.types {
		if len(f.IDs) > 0 {
			found := false
			for _, id := range f.IDs {
				if id == ct.ID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, ct)
	}
	return out, nil
}

func (s *stubRepo) GetType(_ context.Context, id model.ID) (*citypemodel.CIType, error) {
	for i := range s.types {
		if s.types[i].ID == id {
			ct := s.types[i]
			return &ct, nil
		}
	}
	return nil, nil
}

func (s *stubRepo) GetTypeByName(_ context.Context, name string) (*citypemodel.CIType, error) {
	for i := range s.types {
		if s.types[i].Name == name {
			ct := s.types[i]
			return &ct, nil
		}
	}
	return nil, nil
}

func (s *stubRepo) AttributesByID(_ context.Context, ids []model.ID) (map[model.ID]citypemodel.Attribute, error) {
	out := map[model.ID]citypemodel.Attribute{}
	for _, id := range ids {
		if a, ok := s.attrs[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (s *stubRepo) ParentIDsByChild(_ context.Context, ids []model.ID) (map[model.ID][]model.ID, error) {
	out := map[model.ID][]model.ID{}
	for _, id := range ids {
		if p, ok := s.parent[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

func (s *stubRepo) TypeIcons(context.Context) (map[model.ID]string, error) { return s.icons, nil }

func newCIType(id model.ID, name string) citypemodel.CIType {
	ct := citypemodel.CIType{Name: name, Alias: name, Enabled: true}
	ct.ID = id
	return ct
}

func newAttr(id model.ID, name string) citypemodel.Attribute {
	a := citypemodel.Attribute{Name: name, ValueType: "2"}
	a.ID = id
	return a
}

func router(t *testing.T, repo citypebiz.Repo) chi.Router {
	t.Helper()
	h := NewHandler(citypebiz.NewUsecase(repo, nil))
	r := chi.NewRouter()
	h.Register(r)
	return r
}

func do(t *testing.T, r chi.Router, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func seedRepo() *stubRepo {
	ct := newCIType(1, "host")
	ct.UniqueID = 10
	show := model.ID(11)
	ct.ShowID = &show
	return &stubRepo{
		types:  []citypemodel.CIType{ct, newCIType(2, "kvm")},
		attrs:  map[model.ID]citypemodel.Attribute{10: newAttr(10, "hostname"), 11: newAttr(11, "display_name")},
		parent: map[model.ID][]model.ID{2: {1}},
		icons:  map[model.ID]string{1: "icon-host$$"},
	}
}

func TestListReturnsItemsAndTotal(t *testing.T) {
	rec := do(t, router(t, seedRepo()), "/v1/cmdb/ci-types")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Fatalf("response = %+v, want 2 items and total 2", got)
	}
	// Derived fields must be present, since the SPA does not resolve attr ids.
	if got.Items[0].UniqueKey != "hostname" || got.Items[0].ShowName != "display_name" {
		t.Fatalf("first item = %+v, want derived unique_key and show_name", got.Items[0])
	}
	if len(got.Items[1].ParentIDs) != 1 || got.Items[1].ParentIDs[0] != 1 {
		t.Fatalf("second item parents = %v, want [1]", got.Items[1].ParentIDs)
	}
}

// A null array would make the SPA branch on it; the empty list must serialise
// as [].
func TestListEmptySerialisesArrays(t *testing.T) {
	body := do(t, router(t, &stubRepo{attrs: map[model.ID]citypemodel.Attribute{}}), "/v1/cmdb/ci-types").Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Fatalf("body = %s, want an empty array", body)
	}
	// parent_ids must also be [] rather than null for a type with no parents.
	repo := seedRepo()
	repo.parent = nil
	body = do(t, router(t, repo), "/v1/cmdb/ci-types").Body.String()
	if strings.Contains(body, `"parent_ids":null`) {
		t.Fatalf("body = %s, want parent_ids [] rather than null", body)
	}
}

func TestListDoesNotLeakStorageColumns(t *testing.T) {
	body := do(t, router(t, seedRepo()), "/v1/cmdb/ci-types").Body.String()
	for _, leaked := range []string{"delete_marker", "deleted_at", "unique_id\":", "show_id\":"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("body leaks %q: %s", leaked, body)
		}
	}
}

func TestListRejectsMalformedIDs(t *testing.T) {
	r := router(t, seedRepo())
	for _, q := range []string{
		"?ids=abc",
		"?ids=0",
		"?ids=-1",
		"?ids=1,abc",
		"?ids=9999999999999",
	} {
		if rec := do(t, r, "/v1/cmdb/ci-types"+q); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400 (body %s)", q, rec.Code, rec.Body.String())
		}
	}
}

func TestListRejectsOversizedIDList(t *testing.T) {
	ids := make([]string, 0, maxIDList+1)
	for i := 0; i < maxIDList+1; i++ {
		ids = append(ids, "1")
	}
	rec := do(t, router(t, seedRepo()), "/v1/cmdb/ci-types?ids="+strings.Join(ids, ","))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an oversized id list", rec.Code)
	}
}

func TestListAcceptsValidIDList(t *testing.T) {
	rec := do(t, router(t, seedRepo()), "/v1/cmdb/ci-types?ids=2")
	var got listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || got.Items[0].ID != 2 {
		t.Fatalf("response = %+v, want only type 2", got)
	}
}

// The static `icons` segment must win over the `{ref}` parameter — chi
// prioritises static routes, and getting this wrong would make the icon map
// unreachable (it would be parsed as a type name).
func TestIconsRouteWinsOverParamRoute(t *testing.T) {
	rec := do(t, router(t, seedRepo()), "/v1/cmdb/ci-types/icons")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the static route must win over {ref} (body %s)",
			rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	// Type 1 has an explicit icon; type 2 falls back to its name.
	if got["1"] != "icon-host$$" {
		t.Fatalf("icons[1] = %q, want the stored icon", got["1"])
	}
	if got["2"] != "kvm" {
		t.Fatalf("icons[2] = %q, want the name fallback", got["2"])
	}
}

func TestGetByIDAndByName(t *testing.T) {
	r := router(t, seedRepo())

	byID := do(t, r, "/v1/cmdb/ci-types/1")
	if byID.Code != http.StatusOK {
		t.Fatalf("by id status = %d (body %s)", byID.Code, byID.Body.String())
	}
	var dto citTypeDTO
	if err := json.Unmarshal(byID.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ID != 1 || dto.Name != "host" {
		t.Fatalf("by id = %+v", dto)
	}

	byName := do(t, r, "/v1/cmdb/ci-types/kvm")
	if byName.Code != http.StatusOK {
		t.Fatalf("by name status = %d (body %s)", byName.Code, byName.Body.String())
	}
	if err := json.Unmarshal(byName.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ID != 2 || dto.Name != "kvm" {
		t.Fatalf("by name = %+v", dto)
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	r := router(t, seedRepo())
	for _, path := range []string{"/v1/cmdb/ci-types/999", "/v1/cmdb/ci-types/nope"} {
		if rec := do(t, r, path); rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 (body %s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestGetRejectsZeroID(t *testing.T) {
	// A numeric ref that parses but is not a valid id.
	rec := do(t, router(t, seedRepo()), "/v1/cmdb/ci-types/0")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUnwiredUsecaseReportsNotImplemented(t *testing.T) {
	h := NewHandler(citypebiz.NewUsecase(nil, nil))
	r := chi.NewRouter()
	h.Register(r)
	for _, path := range []string{"/v1/cmdb/ci-types", "/v1/cmdb/ci-types/icons", "/v1/cmdb/ci-types/1"} {
		if rec := do(t, r, path); rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s status = %d, want 501 (body %s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestRepoFailureIsInternalError(t *testing.T) {
	rec := do(t, router(t, &stubRepo{fail: true, attrs: map[model.ID]citypemodel.Attribute{}}), "/v1/cmdb/ci-types")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "internal" {
		t.Fatalf("code = %q, want internal", body.Code)
	}
}

func TestErrCodeAlwaysReturnsASlug(t *testing.T) {
	for _, err := range []error{
		errs.ErrNotFound, errs.ErrUnauthorized, errs.ErrForbidden, errs.ErrConflict,
		errs.ErrInvalid, errs.ErrNotWiredYet, errors.New("unclassified"),
	} {
		if got := errCode(err); got == "" {
			t.Errorf("errCode(%v) = empty, want a slug", err)
		}
	}
}
