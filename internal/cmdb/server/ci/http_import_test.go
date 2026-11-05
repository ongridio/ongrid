package ci

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The import endpoints' wire contract. What a sheet MEANS is settled a package
// down and tested there against a database.

func TestImportRoutesAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)
	for _, path := range []string{"/v1/cmdb/import/preview", "/v1/cmdb/import/commit"} {
		routeCtx := chi.NewRouteContext()
		if !r.Match(routeCtx, http.MethodPost, path) {
			t.Fatalf("POST %s did not match any route", path)
		}
		if got := routeCtx.RoutePattern(); got != path {
			t.Fatalf("POST %s matched %s", path, got)
		}
	}
}

// A sheet's body carries cells of any JSON type, and the numbers keep their
// exact form so a 64-bit id survives the round trip.
func TestImportBodyDecodesCells(t *testing.T) {
	body := `{"ci_type":"host","headers":["hostname","cpu","note"],"rows":[["web-01",8,"x"],[2,true,null]],"exist_policy":"replace"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/import/preview", strings.NewReader(body))
	req = withTenant(req)
	in, err := decodeImport(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if in.TypeRef != "host" || len(in.Headers) != 3 || len(in.Rows) != 2 {
		t.Fatalf("decoded %+v", in)
	}
	if in.ExistPolicy != "replace" {
		t.Fatalf("policy = %q", in.ExistPolicy)
	}
	if in.Rows[0][1] != json.Number("8") {
		t.Fatalf("a numeric cell decoded as %T(%v)", in.Rows[0][1], in.Rows[0][1])
	}
	if in.Rows[1][2] != nil {
		t.Fatalf("a null cell decoded as %T(%v)", in.Rows[1][2], in.Rows[1][2])
	}

	// A body with no model is refused, and so is a malformed one.
	for _, bad := range []string{`{"headers":["a"],"rows":[]}`, `{nope`, ``} {
		r := withTenant(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(bad)))
		if _, err := decodeImport(r); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}
