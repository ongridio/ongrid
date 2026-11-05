package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// The discovery routes' wire contract. What a rule or a credential MEANS is
// settled a package down and tested there.

// THE TWO PARAMETERISED SEGMENTS MUST NOT SHADOW EACH OTHER. `/rules/{ref}` and
// `/rules/{ref}/accounts` differ by a trailing segment, and `/accounts/{id}` is
// a separate subtree — a shadowed route would answer an account read by looking
// up a rule.
func TestDiscoveryRoutesAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/discovery/rules", "/v1/cmdb/discovery/rules"},
		{http.MethodPost, "/v1/cmdb/discovery/rules", "/v1/cmdb/discovery/rules"},
		{http.MethodGet, "/v1/cmdb/discovery/rules/aliyun", "/v1/cmdb/discovery/rules/{ref}"},
		{http.MethodPut, "/v1/cmdb/discovery/rules/7", "/v1/cmdb/discovery/rules/{ref}"},
		{http.MethodDelete, "/v1/cmdb/discovery/rules/7", "/v1/cmdb/discovery/rules/{ref}"},
		{http.MethodGet, "/v1/cmdb/discovery/rules/7/accounts", "/v1/cmdb/discovery/rules/{ref}/accounts"},
		{http.MethodPut, "/v1/cmdb/discovery/rules/7/accounts", "/v1/cmdb/discovery/rules/{ref}/accounts"},
		{http.MethodGet, "/v1/cmdb/discovery/accounts/55", "/v1/cmdb/discovery/accounts/{id}"},
		{http.MethodDelete, "/v1/cmdb/discovery/accounts/55", "/v1/cmdb/discovery/accounts/{id}"},
		{http.MethodPost, "/v1/cmdb/discovery/templates/import", "/v1/cmdb/discovery/templates/import"},
		{http.MethodGet, "/v1/cmdb/discovery/templates/export", "/v1/cmdb/discovery/templates/export"},
		{http.MethodGet, "/v1/cmdb/discovery/target-types", "/v1/cmdb/discovery/target-types"},
		{http.MethodGet, "/v1/cmdb/discovery/providers", "/v1/cmdb/discovery/providers"},
		{http.MethodGet, "/v1/cmdb/discovery/catalog/http/aliyun/categories", "/v1/cmdb/discovery/catalog/http/{provider}/categories"},
		{http.MethodGet, "/v1/cmdb/discovery/catalog/http/aliyun/resources", "/v1/cmdb/discovery/catalog/http/{provider}/resources"},
		{http.MethodGet, "/v1/cmdb/discovery/catalog/http/aliyun/attributes", "/v1/cmdb/discovery/catalog/http/{provider}/attributes"},
		{http.MethodGet, "/v1/cmdb/discovery/catalog/http/aliyun/mapping", "/v1/cmdb/discovery/catalog/http/{provider}/mapping"},
		{http.MethodGet, "/v1/cmdb/discovery/catalog/snmp/attributes", "/v1/cmdb/discovery/catalog/snmp/attributes"},
		{http.MethodGet, "/v1/cmdb/discovery/catalog/components/nginx/attributes", "/v1/cmdb/discovery/catalog/components/{name}/attributes"},
	} {
		routeCtx := chi.NewRouteContext()
		if !r.Match(routeCtx, tc.method, tc.path) {
			t.Fatalf("%s %s did not match any route", tc.method, tc.path)
		}
		if got := routeCtx.RoutePattern(); got != tc.wantPattern {
			t.Fatalf("%s %s matched %s, want %s", tc.method, tc.path, got, tc.wantPattern)
		}
	}
}

// A rule's body distinguishes "not mentioned" from "explicitly empty", which is
// what makes a rename not clear the icon and a submitted `{}` clear it.
func TestRuleBodyDistinguishesAbsentFromEmpty(t *testing.T) {
	decode := func(body string) (discoverybiz.RuleInput, error) {
		req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/discovery/rules", strings.NewReader(body))
		return decodeRuleInput(req)
	}

	absent, err := decode(`{"name":"a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Type != nil || absent.IsPlugin != nil || absent.SetOption {
		t.Fatalf("an omission was read as a value: %+v", absent)
	}
	given, err := decode(`{"name":"a","type":"http","is_plugin":false,"option":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	if given.Type == nil || *given.Type != "http" {
		t.Fatalf("a supplied type was lost: %+v", given.Type)
	}
	if !given.SetOption || len(given.Option) != 0 {
		t.Fatalf("an empty option was not read as supplied: %+v", given)
	}
	if given.IsPlugin == nil || *given.IsPlugin {
		t.Fatalf("an explicit false was lost: %+v", given.IsPlugin)
	}
}

// The caller's identity comes from the context and never from the body, and the
// admin flag is what gates a plugin rule.
func TestActorComesFromTheContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"uid":999,"user_id":999}`))
	if _, err := actorOf(req); err == nil {
		t.Fatal("a request with no identity was accepted")
	}

	ctx := tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 7, Role: "admin"})
	got, err := actorOf(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != 7 || !got.IsAdmin {
		t.Fatalf("actor = %+v", got)
	}

	ctx = tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 8, Role: "user"})
	got, err = actorOf(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if got.IsAdmin {
		t.Fatalf("a plain user was read as an admin: %+v", got)
	}
}

// The error body is plain text with a mapped status, which is the shape every
// other CMDB handler uses.
func TestWriteErrUsesPlainText(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, errs.ErrNotWiredYet)
	if ct := rec.Header().Get("content-type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q", ct)
	}
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Fatal("the body is empty")
	}
	var asJSON map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &asJSON); err == nil {
		t.Fatalf("the error body is JSON, which this project's handlers do not emit: %v", asJSON)
	}
}
