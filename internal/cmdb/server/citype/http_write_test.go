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
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// --- write methods on the existing stub ---
//
// They live in this file so the read-path stub stays readable. Go allows
// methods on a package-local type from any file in the package.

func (s *stubRepo) FindAttribute(_ context.Context, ref string) (*citypemodel.Attribute, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	for id, a := range s.attrs {
		if a.Name == ref || itoa(id) == ref {
			copied := a
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *stubRepo) CreateType(_ context.Context, in citypebiz.CreateTypeInput) (*citypemodel.CIType, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	ct := in.Type
	ct.ID = model.ID(len(s.types) + 1000)
	s.types = append(s.types, ct)
	s.lastCreate = &in
	return &ct, nil
}

func (s *stubRepo) UpdateType(_ context.Context, in citypebiz.UpdateTypeInput) error {
	if s.fail {
		return errors.New("boom")
	}
	for i := range s.types {
		if s.types[i].ID == in.Type.ID {
			s.types[i] = in.Type
			return nil
		}
	}
	return errs.ErrNotFound
}

func (s *stubRepo) SetEnabled(_ context.Context, id model.ID, enabled bool) error {
	if s.fail {
		return errors.New("boom")
	}
	for i := range s.types {
		if s.types[i].ID == id {
			s.types[i].Enabled = enabled
			return nil
		}
	}
	return errs.ErrNotFound
}

func (s *stubRepo) DeleteType(_ context.Context, in citypebiz.DeleteTypeInput) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	for i := range s.types {
		if s.types[i].ID == in.ID {
			s.types = append(s.types[:i], s.types[i+1:]...)
			s.deletedActor = in.Actor
			return nil
		}
	}
	return errs.ErrNotFound
}

func itoa(id model.ID) string {
	if id == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	n := id
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// --- test scaffolding ---

// asIdentity attaches a caller identity the way auth.Middleware would, so the
// admin gate and the ownership rules can be exercised.
func asIdentity(h http.Handler, role string, superuser bool, uid uint64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenantctx.With(r.Context(), tenantctx.Tenant{
			UserID: uid, Email: "a@b.c", Role: role, IsSuperuser: superuser,
		})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeRouter(t *testing.T, repo citypebiz.Repo, role string, superuser bool, uid uint64) chi.Router {
	t.Helper()
	h := NewHandler(citypebiz.NewUsecase(repo, nil))
	r := chi.NewRouter()
	h.Register(r)
	// Re-wrap the router so every request carries an identity.
	wrapped := chi.NewRouter()
	wrapped.Handle("/*", asIdentity(r, role, superuser, uid))
	return wrapped
}

func send(t *testing.T, r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// adminRepo seeds one model owned by user 7 plus a second attribute.
func adminRepo() *stubRepo {
	owner := model.ID(7)
	ct := newCIType(1, "host")
	ct.UniqueID = 10
	ct.UID = &owner
	other := newCIType(2, "kvm")
	other.UniqueID = 10
	return &stubRepo{
		types: []citypemodel.CIType{ct, other},
		attrs: map[model.ID]citypemodel.Attribute{10: newAttr(10, "hostname")},
	}
}

// --- authorization ---

func TestWriteRoutesRequireAdmin(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cmdb/ci-types", `{"name":"x","unique_key":"hostname"}`},
		{http.MethodPut, "/v1/cmdb/ci-types/1", `{"alias":"a"}`},
		{http.MethodDelete, "/v1/cmdb/ci-types/1", ``},
		{http.MethodPost, "/v1/cmdb/ci-types/1/enable", `{"enabled":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, writeRouter(t, adminRepo(), "user", false, 7), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 for a non-admin write (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWriteRoutesWithoutIdentityAreUnauthorized(t *testing.T) {
	repo := adminRepo()
	h := NewHandler(citypebiz.NewUsecase(repo, nil))
	r := chi.NewRouter()
	h.Register(r) // no identity middleware at all

	rec := send(t, r, http.MethodPost, "/v1/cmdb/ci-types", `{"name":"x","unique_key":"hostname"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no identity is on the context", rec.Code)
	}
}

func TestReadRoutesDoNotRequireAdmin(t *testing.T) {
	rec := do(t, writeRouter(t, adminRepo(), "user", false, 7), "/v1/cmdb/ci-types")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — reads are open to any authed caller", rec.Code)
	}
}

// --- create ---

func TestCreateReturns201(t *testing.T) {
	repo := adminRepo()
	rec := send(t, writeRouter(t, repo, "admin", false, 7), http.MethodPost, "/v1/cmdb/ci-types",
		`{"name":"switch","unique_key":"hostname","alias":"交换机"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var dto citTypeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Name != "switch" || dto.Alias != "交换机" {
		t.Fatalf("response = %+v", dto)
	}
	// The caller becomes the owner, which the delete path then gates on.
	if repo.lastCreate == nil || repo.lastCreate.Actor != 7 {
		t.Fatalf("actor = %+v, want 7", repo.lastCreate)
	}
	// And the unique attribute is carried through for binding.
	if repo.lastCreate.UniqueAttributeID != 10 {
		t.Fatalf("unique attribute = %d, want 10", repo.lastCreate.UniqueAttributeID)
	}
}

func TestCreateDuplicateIsConflict(t *testing.T) {
	// "host" already exists in the seeded repo.
	rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPost, "/v1/cmdb/ci-types",
		`{"name":"host","unique_key":"hostname"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateValidationIsBadRequest(t *testing.T) {
	cases := []struct{ name, body string }{
		{"missing name", `{"unique_key":"hostname"}`},
		{"missing unique_key", `{"name":"x"}`},
		{"unknown attribute", `{"name":"x","unique_key":"nope"}`},
		{"unknown field", `{"name":"x","unique_key":"hostname","nope":1}`},
		{"malformed json", `{"name":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPost,
				"/v1/cmdb/ci-types", tc.body)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 400 (or 404 for an unknown attribute); body %s",
					rec.Code, rec.Body.String())
			}
		})
	}
}

// --- update ---

func TestUpdateReturns200AndPartialPatch(t *testing.T) {
	repo := adminRepo()
	rec := send(t, writeRouter(t, repo, "admin", false, 7), http.MethodPut, "/v1/cmdb/ci-types/1",
		`{"alias":"新别名"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var dto citTypeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	// The name was not supplied, so it must be untouched.
	if dto.Name != "host" || dto.Alias != "新别名" {
		t.Fatalf("response = %+v, want the name preserved", dto)
	}
}

func TestUpdateByNumericAndByName(t *testing.T) {
	for _, ref := range []string{"1", "host"} {
		rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPut,
			"/v1/cmdb/ci-types/"+ref, `{"alias":"a"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("ref %q status = %d, want 200 (body %s)", ref, rec.Code, rec.Body.String())
		}
	}
}

func TestUpdateMissingIsNotFound(t *testing.T) {
	rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPut,
		"/v1/cmdb/ci-types/nope", `{"alias":"a"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- enable ---

func TestEnableReturns204(t *testing.T) {
	repo := adminRepo()
	rec := send(t, writeRouter(t, repo, "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/1/enable", `{"enabled":false}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.types[0].Enabled {
		t.Fatal("enabled was not applied")
	}
}

// cantor's enable endpoint always enabled and took no body; requiring the flag
// prevents a request that silently means the opposite of what was intended.
func TestEnableRequiresTheFlag(t *testing.T) {
	rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types/1/enable", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when `enabled` is absent (body %s)", rec.Code, rec.Body.String())
	}
}

// --- delete ---

func TestDeleteReturns204(t *testing.T) {
	repo := adminRepo()
	rec := send(t, writeRouter(t, repo, "admin", false, 7), http.MethodDelete, "/v1/cmdb/ci-types/1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.deletedActor != 7 {
		t.Fatalf("deletedActor = %d, want 7", repo.deletedActor)
	}
}

// A non-owner admin is refused. cantor had only this gate; the port adds the
// superuser bypass.
func TestDeleteByNonOwnerIsForbidden(t *testing.T) {
	rec := send(t, writeRouter(t, adminRepo(), "admin", false, 99), http.MethodDelete,
		"/v1/cmdb/ci-types/1", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "forbidden" {
		t.Fatalf("code = %q, want forbidden", body.Code)
	}
}

func TestDeleteBySuperuserSucceedsDespiteOwnership(t *testing.T) {
	rec := send(t, writeRouter(t, adminRepo(), "user", true, 99), http.MethodDelete,
		"/v1/cmdb/ci-types/1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a superuser (body %s)", rec.Code, rec.Body.String())
	}
}

// A dependency refusal must surface as 409 with the conflict slug, not as a
// 500 that blames the server.
func TestDeleteDependencyRefusalIsConflict(t *testing.T) {
	repo := adminRepo()
	repo.deleteErr = citypebiz.ErrTypeHasInstances

	rec := send(t, writeRouter(t, repo, "admin", false, 7), http.MethodDelete, "/v1/cmdb/ci-types/1", "")
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

func TestDeleteMissingIsNotFound(t *testing.T) {
	rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodDelete,
		"/v1/cmdb/ci-types/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// A request body that reads past the limit must be refused rather than
// buffered.
//
// The bulk goes in `icon`, which is the only string field on this request with
// no length validation of its own. That is what makes the test discriminating:
// padding `name` instead would also produce a 400 (ErrNameTooLong), and the
// test would pass with no size limit in place at all.
func TestOversizedBodyIsRejected(t *testing.T) {
	body := func(iconLen int) string {
		return `{"name":"switch","unique_key":"hostname","icon":"` +
			strings.Repeat("x", iconLen) + `"}`
	}

	// Comfortably under the limit must succeed, proving the payload is
	// otherwise valid and only its size is at issue.
	if rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types", body(maxWriteBody/2)); rec.Code != http.StatusCreated {
		t.Fatalf("under-limit status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	if rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types", body(maxWriteBody)); rec.Code != http.StatusBadRequest {
		t.Fatalf("over-limit status = %d, want 400 for an oversized body (body %s)",
			rec.Code, rec.Body.String())
	}
}

// The limit bounds what the DECODER reads, not the bytes the client sends.
//
// json.Decoder stops as soon as it has one complete value, so trailing bytes
// are never read and never counted against the limit — verified here so the
// limit's scope is pinned rather than assumed. This is benign: unconsumed data
// is not buffered by the decoder, so allocation is still bounded. It does mean
// the limit is a bound on memory, not a bound on request size.
func TestSizeLimitDoesNotCountUnreadTrailingBytes(t *testing.T) {
	body := `{"name":"switch","unique_key":"hostname"}` + strings.Repeat(" ", maxWriteBody)
	if rec := send(t, writeRouter(t, adminRepo(), "admin", false, 7), http.MethodPost,
		"/v1/cmdb/ci-types", body); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: trailing bytes past the first JSON value are not read, "+
			"so they cannot trip the limit (body %s)", rec.Code, rec.Body.String())
	}
}
