package relationtype

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	relationtypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/relationtype"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// row builds a relation-type fixture. The ID lives on the embedded Base, and
// assigning a promoted field inside a composite literal requires Go 1.27 (this
// module targets 1.25), so fixtures go through a helper.
func row(id int32, name, color string) citypemodel.RelationType {
	rt := citypemodel.RelationType{Name: name, Color: color}
	rt.ID = id
	return rt
}

// stubRepo lets the HTTP tests drive every biz outcome without a database.
type stubRepo struct {
	rows    []citypemodel.RelationType
	byName  map[string]citypemodel.RelationType
	nextID  int32
	failAll bool
}

func newStubRepo() *stubRepo {
	return &stubRepo{byName: map[string]citypemodel.RelationType{}, nextID: 1}
}

func (s *stubRepo) List(context.Context) ([]citypemodel.RelationType, error) {
	if s.failAll {
		return nil, errors.New("boom")
	}
	return s.rows, nil
}

func (s *stubRepo) Get(_ context.Context, id int32) (*citypemodel.RelationType, error) {
	if s.failAll {
		return nil, errors.New("boom")
	}
	for i := range s.rows {
		if s.rows[i].ID == id {
			row := s.rows[i]
			return &row, nil
		}
	}
	return nil, nil
}

func (s *stubRepo) FindByName(_ context.Context, name string) (*citypemodel.RelationType, error) {
	if s.failAll {
		return nil, errors.New("boom")
	}
	if row, ok := s.byName[name]; ok {
		return &row, nil
	}
	return nil, nil
}

func (s *stubRepo) Create(_ context.Context, rt *citypemodel.RelationType) error {
	if s.failAll {
		return errors.New("boom")
	}
	rt.ID = s.nextID
	s.nextID++
	row := *rt
	s.rows = append(s.rows, row)
	s.byName[row.Name] = row
	return nil
}

func (s *stubRepo) Update(_ context.Context, rt *citypemodel.RelationType) error {
	if s.failAll {
		return errors.New("boom")
	}
	for i := range s.rows {
		if s.rows[i].ID == rt.ID {
			delete(s.byName, s.rows[i].Name)
			s.rows[i] = *rt
			s.byName[rt.Name] = *rt
			return nil
		}
	}
	return errs.ErrNotFound
}

func (s *stubRepo) SoftDelete(_ context.Context, id int32) error {
	if s.failAll {
		return errors.New("boom")
	}
	for i := range s.rows {
		if s.rows[i].ID == id {
			delete(s.byName, s.rows[i].Name)
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return errs.ErrNotFound
}

// asUser runs fn with a caller identity attached, the way auth.Middleware
// would have. admin=false exercises the 403 path.
func asUser(t *testing.T, role string, superuser bool, fn func(r chi.Router)) {
	t.Helper()
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := tenantctx.With(r.Context(), tenantctx.Tenant{
				UserID: 7, Email: "a@b.c", Role: role, IsSuperuser: superuser,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	fn(router)
}

func newRouter(t *testing.T, repo relationtypebiz.Repo, role string, superuser bool) chi.Router {
	t.Helper()
	h := NewHandler(relationtypebiz.NewUsecase(repo, nil))
	var router chi.Router
	asUser(t, role, superuser, func(r chi.Router) {
		h.Register(r)
		router = r
	})
	return router
}

func do(t *testing.T, router chi.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// --- read ---

func TestListReturnsItemsAndTotal(t *testing.T) {
	repo := newStubRepo()
	repo.rows = []citypemodel.RelationType{
		row(1, "runs_on", "#1890ff"),
		row(2, "belongs_to", "#52c41a"),
	}
	router := newRouter(t, repo, "user", false)

	rec := do(t, router, http.MethodGet, "/v1/cmdb/relation-types", "")
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
	if got.Items[0].Name != "runs_on" {
		t.Fatalf("first item = %+v", got.Items[0])
	}
}

// An empty dictionary must serialise as [] rather than null: a null would make
// the SPA's list rendering special-case the empty state.
func TestListWithNoRowsSerialisesEmptyArray(t *testing.T) {
	router := newRouter(t, newStubRepo(), "user", false)

	rec := do(t, router, http.MethodGet, "/v1/cmdb/relation-types", "")
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("body = %s, want an empty array not null", rec.Body.String())
	}
}

// The list route must not leak storage bookkeeping (delete_marker, deleted_at).
func TestListDoesNotExposeStorageColumns(t *testing.T) {
	repo := newStubRepo()
	repo.rows = []citypemodel.RelationType{row(1, "runs_on", "#1890ff")}
	router := newRouter(t, repo, "user", false)

	body := do(t, router, http.MethodGet, "/v1/cmdb/relation-types", "").Body.String()
	for _, leaked := range []string{"delete_marker", "deleted_at", "deleted"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("body leaks %q: %s", leaked, body)
		}
	}
}

// --- write authorization ---

func TestWritesRequireAdmin(t *testing.T) {
	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/cmdb/relation-types", `{"name":"runs_on"}`},
		{http.MethodPut, "/v1/cmdb/relation-types/1", `{"name":"runs_on"}`},
		{http.MethodDelete, "/v1/cmdb/relation-types/1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			router := newRouter(t, newStubRepo(), "user", false)
			rec := do(t, router, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 for a non-admin write", rec.Code)
			}
			var body errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.Code != "forbidden" {
				t.Fatalf("code = %q, want forbidden", body.Code)
			}
		})
	}
}

func TestReadsAllowedForNonAdmin(t *testing.T) {
	router := newRouter(t, newStubRepo(), "user", false)
	if rec := do(t, router, http.MethodGet, "/v1/cmdb/relation-types", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (reads are open to any authed caller)", rec.Code)
	}
}

func TestSuperuserBypassesAdminGate(t *testing.T) {
	router := newRouter(t, newStubRepo(), "user", true)
	rec := do(t, router, http.MethodPost, "/v1/cmdb/relation-types", `{"name":"runs_on"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a superuser (body %s)", rec.Code, rec.Body.String())
	}
}

// A missing identity must read as unauthenticated, not forbidden — otherwise a
// misconfigured route group looks like a permissions bug.
func TestMissingIdentityIsUnauthorized(t *testing.T) {
	h := NewHandler(relationtypebiz.NewUsecase(newStubRepo(), nil))
	router := chi.NewRouter()
	h.Register(router) // no identity middleware

	rec := do(t, router, http.MethodPost, "/v1/cmdb/relation-types", `{"name":"runs_on"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

// --- create / update / delete ---

func TestCreateReturns201AndTheRow(t *testing.T) {
	router := newRouter(t, newStubRepo(), "admin", false)

	rec := do(t, router, http.MethodPost, "/v1/cmdb/relation-types", `{"name":"runs_on","color":"#ff0000"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got relationTypeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == 0 || got.Name != "runs_on" || got.Color != "#ff0000" {
		t.Fatalf("response = %+v", got)
	}
}

func TestCreateWithNameOnlyUsesDefaultColour(t *testing.T) {
	router := newRouter(t, newStubRepo(), "admin", false)

	rec := do(t, router, http.MethodPost, "/v1/cmdb/relation-types", `{"name":"runs_on"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got relationTypeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Color != relationtypebiz.DefaultColor {
		t.Fatalf("color = %q, want %q", got.Color, relationtypebiz.DefaultColor)
	}
}

// A duplicate name must map to 409, which is the whole point of the biz error
// wrapping errs.ErrConflict rather than defining its own sentinel.
func TestCreateDuplicateIsConflict(t *testing.T) {
	repo := newStubRepo()
	repo.rows = []citypemodel.RelationType{row(1, "runs_on", "")}
	repo.byName["runs_on"] = repo.rows[0]
	repo.nextID = 2
	router := newRouter(t, repo, "admin", false)

	rec := do(t, router, http.MethodPost, "/v1/cmdb/relation-types", `{"name":"runs_on"}`)
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

func TestCreateValidationIsBadRequest(t *testing.T) {
	cases := []struct{ name, body string }{
		{"missing name", `{}`},
		{"empty name", `{"name":""}`},
		{"name too long", `{"name":"12345678901234567"}`},
		{"bad colour", `{"name":"ok","color":"#gggggg"}`},
		{"unknown field", `{"name":"ok","nope":1}`},
		{"malformed json", `{"name":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := newRouter(t, newStubRepo(), "admin", false)
			rec := do(t, router, http.MethodPost, "/v1/cmdb/relation-types", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUpdateReturnsTheRow(t *testing.T) {
	repo := newStubRepo()
	repo.rows = []citypemodel.RelationType{row(5, "runs_on", "#1890ff")}
	repo.byName["runs_on"] = repo.rows[0]
	repo.nextID = 6
	router := newRouter(t, repo, "admin", false)

	rec := do(t, router, http.MethodPut, "/v1/cmdb/relation-types/5", `{"name":"hosted_on","color":"#00ff00"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got relationTypeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "hosted_on" || got.Color != "#00ff00" {
		t.Fatalf("response = %+v", got)
	}
}

func TestUpdateMissingIsNotFound(t *testing.T) {
	router := newRouter(t, newStubRepo(), "admin", false)

	rec := do(t, router, http.MethodPut, "/v1/cmdb/relation-types/999", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// A non-numeric or non-positive path id must be rejected before it reaches the
// usecase.
func TestMalformedPathIDIsBadRequest(t *testing.T) {
	for _, path := range []string{
		"/v1/cmdb/relation-types/abc",
		"/v1/cmdb/relation-types/0",
		"/v1/cmdb/relation-types/-1",
		"/v1/cmdb/relation-types/9999999999999",
	} {
		t.Run(path, func(t *testing.T) {
			router := newRouter(t, newStubRepo(), "admin", false)
			rec := do(t, router, http.MethodDelete, path, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeleteReturnsNoContent(t *testing.T) {
	repo := newStubRepo()
	repo.rows = []citypemodel.RelationType{row(5, "runs_on", "")}
	repo.byName["runs_on"] = repo.rows[0]
	repo.nextID = 6
	router := newRouter(t, repo, "admin", false)

	rec := do(t, router, http.MethodDelete, "/v1/cmdb/relation-types/5", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty for 204", rec.Body.String())
	}
}

func TestDeleteMissingIsNotFound(t *testing.T) {
	router := newRouter(t, newStubRepo(), "admin", false)

	rec := do(t, router, http.MethodDelete, "/v1/cmdb/relation-types/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// An unwired usecase reports 501 rather than an empty 200, so an operator can
// tell "not configured" from "configured and empty".
func TestUnwiredUsecaseReportsNotImplemented(t *testing.T) {
	h := NewHandler(relationtypebiz.NewUsecase(nil, nil))
	var router chi.Router
	asUser(t, "admin", false, func(r chi.Router) {
		h.Register(r)
		router = r
	})

	rec := do(t, router, http.MethodGet, "/v1/cmdb/relation-types", "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body.String())
	}
}

// A repo failure must be a 500 with the internal slug, not a 400 that would
// blame the caller.
func TestRepoFailureIsInternalError(t *testing.T) {
	repo := newStubRepo()
	repo.failAll = true
	router := newRouter(t, repo, "admin", false)

	rec := do(t, router, http.MethodGet, "/v1/cmdb/relation-types", "")
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

// errCode must not report an empty slug, which would produce an unparseable
// error body for the SPA.
func TestErrCodeAlwaysReturnsASlug(t *testing.T) {
	for _, err := range []error{
		errs.ErrNotFound, errs.ErrUnauthorized, errs.ErrForbidden,
		errs.ErrConflict, errs.ErrInvalid, errs.ErrTenantMismatch,
		errs.ErrNotWiredYet, errors.New("unclassified"),
	} {
		if got := errCode(err); got == "" {
			t.Errorf("errCode(%v) = empty, want a slug", err)
		}
	}
}
