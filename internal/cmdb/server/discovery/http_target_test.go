package discovery

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// The target routes' wire contract.

// THE STATIC SEGMENTS MUST NOT BE SHADOWED BY THE PARAMETERISED ONES.
//
// `/rules/builtin` sits beside `/rules/{ref}`, and `/sync` beside nothing else —
// but `/ci-types/{ref}/targets` and `/targets/{id}` are two subtrees whose
// parameter names differ, and chi resolves static beats parameter only if the
// route is registered. A shadowed `/rules/builtin` would try to look up a RULE
// named "builtin"; a shadowed `/sync` would be read as a target id.
func TestTargetRoutesAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/discovery/ci-types/7/targets", "/v1/cmdb/discovery/ci-types/{ref}/targets"},
		{http.MethodPost, "/v1/cmdb/discovery/ci-types/host/targets", "/v1/cmdb/discovery/ci-types/{ref}/targets"},
		{http.MethodGet, "/v1/cmdb/discovery/ci-types/7/discoverable-attributes", "/v1/cmdb/discovery/ci-types/{ref}/discoverable-attributes"},
		{http.MethodGet, "/v1/cmdb/discovery/ci-types/7/target-relations", "/v1/cmdb/discovery/ci-types/{ref}/target-relations"},
		{http.MethodPut, "/v1/cmdb/discovery/ci-types/7/target-relations", "/v1/cmdb/discovery/ci-types/{ref}/target-relations"},
		{http.MethodGet, "/v1/cmdb/discovery/targets/55", "/v1/cmdb/discovery/targets/{id}"},
		{http.MethodPut, "/v1/cmdb/discovery/targets/55", "/v1/cmdb/discovery/targets/{id}"},
		{http.MethodDelete, "/v1/cmdb/discovery/targets/55", "/v1/cmdb/discovery/targets/{id}"},
		{http.MethodPost, "/v1/cmdb/discovery/targets/55/test", "/v1/cmdb/discovery/targets/{id}/test"},
		{http.MethodGet, "/v1/cmdb/discovery/targets/55/sync-histories", "/v1/cmdb/discovery/targets/{id}/sync-histories"},

		// The two that must win over a parameter.
		{http.MethodPost, "/v1/cmdb/discovery/rules/builtin", "/v1/cmdb/discovery/rules/builtin"},
		{http.MethodGet, "/v1/cmdb/discovery/sync", "/v1/cmdb/discovery/sync"},

		// And the rule routes the target layer did not disturb.
		{http.MethodGet, "/v1/cmdb/discovery/rules/builtin", "/v1/cmdb/discovery/rules/{ref}"},
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

// A target's body distinguishes an omitted mapping from an empty one, which is
// what makes a write about the agent leave the mapping alone.
func TestTargetBodyDistinguishesAbsentFromEmpty(t *testing.T) {
	decode := func(body string) (discoverybiz.TargetInput, error) {
		req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/discovery/targets/1", strings.NewReader(body))
		return decodeTarget(req)
	}

	absent, err := decode(`{"agent_id":"0xabc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if absent.SetAttributes || absent.SetExtraOption || absent.AdrID != nil {
		t.Fatalf("an omission was read as a value: %+v", absent)
	}
	if absent.AutoAccept != nil || absent.Enabled != nil {
		t.Fatalf("an omitted flag was read as a value: %+v", absent)
	}

	given, err := decode(`{"adr_id":3,"attributes":{},"extra_option":{},"enabled":false,"auto_accept":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !given.SetAttributes || given.Attributes == nil || len(given.Attributes) != 0 {
		t.Fatalf("an empty mapping was not read as supplied: %+v", given)
	}
	if !given.SetExtraOption {
		t.Fatalf("an empty extra_option was not read as supplied: %+v", given)
	}
	if given.AdrID == nil || *given.AdrID != 3 {
		t.Fatalf("adr_id was lost: %+v", given.AdrID)
	}
	// AN EXPLICIT FALSE IS A VALUE, and it is the difference between switching a
	// target off and not mentioning it.
	if given.Enabled == nil || *given.Enabled {
		t.Fatalf("an explicit enabled:false was lost: %+v", given.Enabled)
	}
	if given.AutoAccept == nil || !*given.AutoAccept {
		t.Fatalf("an explicit auto_accept:true was lost: %+v", given.AutoAccept)
	}
}

// An empty string is a VALUE the console sends to mean "not this mode", so it
// must survive decoding rather than collapse into an omission.
func TestEmptyAgentIDIsAValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"agent_id":"","query_expr":""}`))
	got, err := decodeTarget(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID == nil || *got.AgentID != "" {
		t.Fatalf("an empty agent_id was read as an omission: %+v", got.AgentID)
	}
	if got.QueryExpr == nil || *got.QueryExpr != "" {
		t.Fatalf("an empty query_expr was read as an omission: %+v", got.QueryExpr)
	}
}

// THE TEST ENDPOINT ANSWERS 501, not three hard-coded lines.
func TestTargetTestEndpointIsNotImplemented(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/discovery/targets/55/test", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "1\n2\n3") {
		t.Fatalf("the endpoint answered with cantor's fake output: %q", rec.Body.String())
	}
}

// AN AGENT'S POLL IS NOT AN ORDINARY READ, so a caller with no identity is
// refused before anything is assembled.
func TestSyncRequiresAnIdentity(t *testing.T) {
	uc := discoverybiz.NewUsecase(nil)
	h := NewHandler(uc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/cmdb/discovery/sync?oneagent_id=0xabc", nil)
	h.sync(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	// A plain user is refused too: cantor admitted a dedicated agent account and
	// this deployment has none, so for now the poll is an administrative read —
	// see the route table's comment.
	ctx := tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 7, Role: "user"})
	rec = httptest.NewRecorder()
	h.sync(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-administrator", rec.Code)
	}

	// An administrator reaches the use case, which is unwired in this fixture and
	// therefore says so.
	ctx = tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 7, Role: "admin"})
	rec = httptest.NewRecorder()
	h.sync(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 from the unwired use case", rec.Code)
	}
}

// The built-in bootstrap writes rules for the whole deployment, so it is
// administrative.
func TestBuiltinRulesAreAdminGated(t *testing.T) {
	h := NewHandler(discoverybiz.NewUsecase(nil))

	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/discovery/rules/builtin", nil)
	ctx := tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 7, Role: "user"})
	rec := httptest.NewRecorder()
	h.ensureBuiltinRules(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// A malformed sync parameter is not a failure: an agent that sends no instance
// id is an agent whose deployment does not model placement.
func TestOptionalIDIsForgiving(t *testing.T) {
	for _, raw := range []string{"", "abc", "-1", "0", "9999999999999"} {
		if got := parseOptionalID(raw); got != 0 {
			t.Fatalf("parseOptionalID(%q) = %d, want zero", raw, got)
		}
	}
	if got := parseOptionalID("42"); got != 42 {
		t.Fatalf("parseOptionalID(42) = %d", got)
	}
}
