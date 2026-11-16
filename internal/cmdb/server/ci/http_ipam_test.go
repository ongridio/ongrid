package ci

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// The address-management routes' wire contract. What the rules MEAN is settled
// a package down and tested there against a database.

// THE `hosts` STATIC SEGMENT MUST NOT BE SHADOWED BY `{id}`. It occupies the
// same position, and a shadowed route would answer a range's host list by
// looking up a subnet whose id is the word "hosts".
func TestIPAMRoutesAreRegisteredAndNotShadowed(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/ipam/tree-view", "/v1/cmdb/ipam/tree-view"},
		{http.MethodGet, "/v1/cmdb/ipam/stats", "/v1/cmdb/ipam/stats"},
		{http.MethodGet, "/v1/cmdb/ipam/history/operations", "/v1/cmdb/ipam/history/operations"},
		{http.MethodGet, "/v1/cmdb/ipam/history/scans", "/v1/cmdb/ipam/history/scans"},
		{http.MethodPost, "/v1/cmdb/ipam/history/scans", "/v1/cmdb/ipam/history/scans"},
		{http.MethodGet, "/v1/cmdb/ipam/subnets/hosts", "/v1/cmdb/ipam/subnets/hosts"},
		{http.MethodGet, "/v1/cmdb/ipam/subnets/12", "/v1/cmdb/ipam/subnets/{id}"},
		{http.MethodPost, "/v1/cmdb/ipam/subnets", "/v1/cmdb/ipam/subnets"},
		{http.MethodPut, "/v1/cmdb/ipam/subnets/12", "/v1/cmdb/ipam/subnets/{id}"},
		{http.MethodDelete, "/v1/cmdb/ipam/subnets/12", "/v1/cmdb/ipam/subnets/{id}"},
		{http.MethodPut, "/v1/cmdb/ipam/subnets/12/move", "/v1/cmdb/ipam/subnets/{id}/move"},
		{http.MethodPost, "/v1/cmdb/ipam/scopes", "/v1/cmdb/ipam/scopes"},
		{http.MethodDelete, "/v1/cmdb/ipam/scopes/3", "/v1/cmdb/ipam/scopes/{id}"},
		{http.MethodGet, "/v1/cmdb/ipam/addresses", "/v1/cmdb/ipam/addresses"},
		{http.MethodPost, "/v1/cmdb/ipam/addresses", "/v1/cmdb/ipam/addresses"},
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

// A subnet's body consumes the scan fields rather than writing them as
// attributes.
//
// THE SCAN IS NOT A PROPERTY OF A RANGE. It lives in its own table because it
// belongs to whoever watches the range, and a `cron` left in the values would be
// written as an attribute no model declares — which the instance write path
// would then drop or refuse depending on the policy, silently either way.
func TestSubnetBodyConsumesTheScanFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/ipam/subnets",
		strings.NewReader(`{"cidr":"10.0.0.0/24","subnet_name":"a","agent_id":"edge-1","cron":"0 * * * *","scan_enabled":1}`))
	body, err := decodeNodeBody(req)
	if err != nil {
		t.Fatal(err)
	}
	req = withTenant(httptest.NewRequest(http.MethodPost, "/v1/cmdb/ipam/subnets",
		strings.NewReader(`{"cidr":"10.0.0.0/24","subnet_name":"a","agent_id":"edge-1","cron":"0 * * * *","scan_enabled":1}`)))
	in, err := subnetScanInput(req, body)
	if err != nil {
		t.Fatal(err)
	}
	if !in.SetScan || in.AgentID != "edge-1" || in.Cron != "0 * * * *" || !in.Enabled {
		t.Fatalf("scan input = %+v", in)
	}
	if _, has := in.Values["cron"]; has {
		t.Fatalf("cron leaked into the attributes: %v", in.Values)
	}
	if in.Values["cidr"] != "10.0.0.0/24" || in.Values["subnet_name"] != "a" {
		t.Fatalf("the attributes lost a real field: %v", in.Values)
	}

	// A body with no scan fields leaves any existing rule alone.
	plain := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"cidr":"10.0.0.0/24"}`))
	got, err := decodeNodeBody(plain)
	if err != nil {
		t.Fatal(err)
	}
	plain = withTenant(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"cidr":"10.0.0.0/24"}`)))
	bare, err := subnetScanInput(plain, got)
	if err != nil {
		t.Fatal(err)
	}
	if bare.SetScan {
		t.Fatalf("a body with no scan fields asked to write one: %+v", bare)
	}
}

// The assignment body distinguishes "not mentioned" from "explicitly zero", for
// both the status and the in-use flag.
func TestAddressBodyDistinguishesAbsentFromZero(t *testing.T) {
	decode := func(body string) (ipamAddressBody, error) {
		req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/ipam/addresses", strings.NewReader(body))
		var got ipamAddressBody
		if err := jsonDecode(req, &got); err != nil {
			return got, err
		}
		return got, nil
	}

	absent, err := decode(`{"ips":["10.0.0.1"],"subnet_id":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if absent.AssignStatus != nil || absent.IsUsed != nil {
		t.Fatalf("absent fields decoded as set: %+v", absent)
	}
	given, err := decode(`{"ips":["10.0.0.1"],"subnet_id":3,"assign_status":0,"is_used":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if given.AssignStatus == nil || *given.AssignStatus != 0 {
		t.Fatalf("an explicit zero was lost: %+v", given.AssignStatus)
	}
	if given.IsUsed == nil || *given.IsUsed {
		t.Fatalf("an explicit false was lost: %+v", given.IsUsed)
	}
	if len(given.IPs) != 1 || given.IPs[0] != "10.0.0.1" || given.SubnetID != 3 {
		t.Fatalf("decoded %+v", given)
	}
}

// withTenant attaches the caller identity the audit fields come from, which is
// what a request through the protected route group already carries.
func withTenant(r *http.Request) *http.Request {
	ctx := tenantctx.With(r.Context(), tenantctx.Tenant{UserID: 7, Email: "t@example.com"})
	return r.WithContext(ctx)
}

// jsonDecode is the decoder the handler uses, so the test reads the body the
// same way.
func jsonDecode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	return dec.Decode(v)
}
