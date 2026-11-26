package ci

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The data-centre routes' wire contract: which paths exist, and how the shared
// pieces of a node's body are read. What the operations MEAN is settled a
// package down and tested there against a database.

// THE STATIC SEGMENTS MUST NOT BE SHADOWED. `/dcim/{type}` and
// `/dcim/{type}/{id}` are parameterised, and every other data-centre path is a
// static word in one of the same positions — so a shadowed route would answer a
// tree read or a device mount by writing an instance of a model named
// "tree-view".
func TestDCIMRoutesAreRegisteredAndNotShadowed(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/dcim/tree-view", "/v1/cmdb/dcim/tree-view"},
		{http.MethodPost, "/v1/cmdb/dcim/racks/recalc-units", "/v1/cmdb/dcim/racks/recalc-units"},
		{http.MethodGet, "/v1/cmdb/dcim/history/operations", "/v1/cmdb/dcim/history/operations"},
		{http.MethodGet, "/v1/cmdb/dcim/server-rooms/7/racks", "/v1/cmdb/dcim/server-rooms/{roomID}/racks"},
		{
			http.MethodPost, "/v1/cmdb/dcim/server-rooms/7/racks/8/devices/9",
			"/v1/cmdb/dcim/server-rooms/{roomID}/racks/{rackID}/devices/{deviceID}",
		},
		{
			http.MethodPut, "/v1/cmdb/dcim/server-rooms/7/racks/8/devices/9",
			"/v1/cmdb/dcim/server-rooms/{roomID}/racks/{rackID}/devices/{deviceID}",
		},
		{
			http.MethodDelete, "/v1/cmdb/dcim/server-rooms/7/racks/8/devices/9",
			"/v1/cmdb/dcim/server-rooms/{roomID}/racks/{rackID}/devices/{deviceID}",
		},
		{http.MethodPost, "/v1/cmdb/dcim/rack", "/v1/cmdb/dcim/{type}"},
		{http.MethodPut, "/v1/cmdb/dcim/rack/12", "/v1/cmdb/dcim/{type}/{id}"},
		{http.MethodDelete, "/v1/cmdb/dcim/server_room/12", "/v1/cmdb/dcim/{type}/{id}"},
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

// A node's body is FLAT — attribute names at the top level — with `parent_id`
// as the one reserved key, and the parent may arrive as a JSON number or as a
// string because the console sends whichever its form library produced.
func TestDCIMNodeBodyReadsTheParent(t *testing.T) {
	for _, body := range []string{
		`{"parent_id":7,"name":"r1","u_count":42}`,
		`{"parent_id":"7","name":"r1","u_count":42}`,
	} {
		req := newJSONRequest(t, body)
		got, err := decodeNodeBody(req)
		if err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		parent, err := nodeParentID(got)
		if err != nil {
			t.Fatalf("parent of %s: %v", body, err)
		}
		if parent != 7 {
			t.Fatalf("parent of %s = %d, want 7", body, parent)
		}
		// The reserved key is CONSUMED, so it cannot be written as an
		// attribute that happens to share the name.
		if _, has := got["parent_id"]; has {
			t.Fatalf("parent_id is still in the values of %s: %v", body, got)
		}
		if got["name"] != "r1" {
			t.Fatalf("values of %s = %v", body, got)
		}
	}

	// Absent is a root, which is what a region is.
	empty, err := decodeNodeBody(newJSONRequest(t, `{"name":"r1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if parent, err := nodeParentID(empty); err != nil || parent != 0 {
		t.Fatalf("parent = %d, %v; want a root", parent, err)
	}

	// A malformed body is refused rather than read as empty.
	if _, err := decodeNodeBody(newJSONRequest(t, "{nope")); err == nil {
		t.Fatal("a malformed body was accepted")
	}
	// So is a parent that is not an id.
	bad, err := decodeNodeBody(newJSONRequest(t, `{"parent_id":"north"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeParentID(bad); err == nil {
		t.Fatal("a non-numeric parent was accepted")
	}
}

// The device body distinguishes "no height given" from "a height of zero".
func TestDCIMDeviceBodyDistinguishesAbsentFromZero(t *testing.T) {
	absent, err := decodeDeviceBody(newJSONRequest(t, `{"u_start":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Units != nil {
		t.Fatalf("units = %v, want absent", *absent.Units)
	}
	zero, err := decodeDeviceBody(newJSONRequest(t, `{"u_start":3,"u_count":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if zero.Units == nil || *zero.Units != 0 {
		t.Fatalf("units = %v, want a given zero", zero.Units)
	}

	move, err := decodeDeviceBody(newJSONRequest(t, `{"to_u_start":12}`))
	if err != nil {
		t.Fatal(err)
	}
	if move.ToStart == nil || *move.ToStart != 12 {
		t.Fatalf("to_u_start = %v", move.ToStart)
	}

	empty, err := decodeDeviceBody(newJSONRequest(t, ""))
	if err != nil {
		t.Fatalf("an empty body is the unmount's: %v", err)
	}
	if empty.Start != 0 || empty.Units != nil || empty.ToRackID != 0 {
		t.Fatalf("an empty body decoded to %+v", empty)
	}
}

// The history's `operate_type` is a comma list in cantor's spelling.
func TestRackHistoryKindParam(t *testing.T) {
	req := newQueryRequest(t, "/v1/cmdb/dcim/history/operations?operate_type=0,2&rack_id=5&page=2&count=10")
	q := req.URL.Query()
	var kinds []string
	for _, raw := range strings.Split(q.Get("operate_type"), ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			kinds = append(kinds, raw)
		}
	}
	if len(kinds) != 2 || kinds[0] != "0" || kinds[1] != "2" {
		t.Fatalf("kinds = %v", kinds)
	}
	if q.Get("rack_id") != "5" {
		t.Fatalf("rack_id = %q", q.Get("rack_id"))
	}
}

// newJSONRequest builds a POST carrying a body.
func newJSONRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/v1/cmdb/dcim/rack", strings.NewReader(body))
}

// newQueryRequest builds a GET from a full path.
func newQueryRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, path, nil)
}
