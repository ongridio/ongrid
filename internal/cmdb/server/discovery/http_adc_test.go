package discovery

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The pool routes' wire contract.

// THE STATIC SEGMENTS MUST WIN OVER `/adc/{id}`. A shadowed `/adc/counter`
// would look up a pool row named "counter", and a shadowed `/adc/ci-types`
// would answer a model list with a 404.
func TestADCRoutesAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/discovery/adc", "/v1/cmdb/discovery/adc"},
		{http.MethodPost, "/v1/cmdb/discovery/adc", "/v1/cmdb/discovery/adc"},
		{http.MethodGet, "/v1/cmdb/discovery/adc/counter", "/v1/cmdb/discovery/adc/counter"},
		{http.MethodGet, "/v1/cmdb/discovery/adc/ci-types", "/v1/cmdb/discovery/adc/ci-types"},
		{http.MethodGet, "/v1/cmdb/discovery/adc/ci-types/7/attributes", "/v1/cmdb/discovery/adc/ci-types/{ref}/attributes"},
		{http.MethodGet, "/v1/cmdb/discovery/adc/exec-histories", "/v1/cmdb/discovery/adc/exec-histories"},
		{http.MethodPost, "/v1/cmdb/discovery/adc/exec-histories", "/v1/cmdb/discovery/adc/exec-histories"},
		{http.MethodDelete, "/v1/cmdb/discovery/adc/exec-histories/7", "/v1/cmdb/discovery/adc/exec-histories/{typeID}"},
		{http.MethodGet, "/v1/cmdb/discovery/adc/55", "/v1/cmdb/discovery/adc/{id}"},
		{http.MethodDelete, "/v1/cmdb/discovery/adc/55", "/v1/cmdb/discovery/adc/{id}"},
		{http.MethodPut, "/v1/cmdb/discovery/adc/55/accept", "/v1/cmdb/discovery/adc/{id}/accept"},
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

// An agent's report is decoded without json.Number, so a number read from the
// database and the same number reported again compare equal.
func TestPoolBodyDecodesTheReport(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/discovery/adc",
		strings.NewReader(`{"type_id":7,"adt_id":5,"unique_value":"i-1","instance":{"cpu_num":2},"oneagent_id":"a1","oneagent_name":"agent one"}`))
	got, err := decodePoolBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.TypeID != 7 || got.AdtID != 5 || got.UniqueValue != "i-1" ||
		got.OneagentID != "a1" || got.OneagentName != "agent one" {
		t.Fatalf("pool write = %+v", got)
	}
	if _, ok := got.Instance["cpu_num"].(float64); !ok {
		t.Fatalf("instance number = %T, want float64", got.Instance["cpu_num"])
	}

	bad := httptest.NewRequest(http.MethodPost, "/v1/cmdb/discovery/adc", strings.NewReader(`{bad`))
	if _, err := decodePoolBody(bad); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}
