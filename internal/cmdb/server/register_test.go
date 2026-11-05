package server

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/cmdb/server/ci"
	"github.com/ongridio/ongrid/internal/cmdb/server/citype"
	"github.com/ongridio/ongrid/internal/cmdb/server/discovery"
	"github.com/ongridio/ongrid/internal/cmdb/server/history"
	"github.com/ongridio/ongrid/internal/cmdb/server/meta"
	"github.com/ongridio/ongrid/internal/cmdb/server/relationtype"
	"github.com/ongridio/ongrid/internal/cmdb/server/topology"
)

func allHandlers() Handlers {
	return Handlers{
		Meta:         meta.NewHandler(nil),
		RelationType: relationtype.NewHandler(nil),
		Discovery:    discovery.NewHandler(nil),
		History:      history.NewHandler(nil),
		Topology:     topology.NewHandler(nil),
		CIType:       citype.NewHandler(nil),
		CI:           ci.NewHandler(nil),
	}
}

func TestRegisterAllMountsRepresentativeRoutes(t *testing.T) {
	r := chi.NewRouter()
	RegisterAll(r, allHandlers())

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/cmdb/health"},
		{http.MethodGet, "/v1/cmdb/relation-types"},
		{http.MethodGet, "/v1/cmdb/ci-types"},
		{http.MethodGet, "/v1/cmdb/ci-types/1/attributes"},
		{http.MethodGet, "/v1/cmdb/instances/1"},
		{http.MethodGet, "/v1/cmdb/ipam/tree-view"},
		{http.MethodGet, "/v1/cmdb/dcim/tree-view"},
		{http.MethodGet, "/v1/cmdb/discovery/rules"},
		{http.MethodGet, "/v1/cmdb/history/attributes"},
		{http.MethodGet, "/v1/cmdb/topology-views"},
		{http.MethodGet, "/v1/cmdb/preferences/ci-types"},
	} {
		routeCtx := chi.NewRouteContext()
		if !r.Match(routeCtx, tc.method, tc.path) {
			t.Errorf("%s %s did not match any route", tc.method, tc.path)
		}
	}
}

func TestRegisterAllRejectsIncompleteHandlers(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("RegisterAll accepted an incomplete Handlers set")
		}
	}()
	RegisterAll(chi.NewRouter(), Handlers{})
}

// TestRegisterAllRouteSetMatchesGolden is a characterization test. It does not
// prove a route behaves, but it makes a route addition/removal a deliberate
// edit: the CMDB split must preserve every existing path for the SPA.
//
// Regenerate after an intentional route change:
//
//	UPDATE_ROUTES_GOLDEN=1 go test ./internal/cmdb/server -run TestRegisterAllRouteSetMatchesGolden
func TestRegisterAllRouteSetMatchesGolden(t *testing.T) {
	r := chi.NewRouter()
	RegisterAll(r, allHandlers())

	var got []string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(got)

	goldenPath := filepath.Join("testdata", "routes.golden")
	if os.Getenv("UPDATE_ROUTES_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with UPDATE_ROUTES_GOLDEN=1): %v", goldenPath, err)
	}
	want := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CMDB route set changed.\n got: %v\nwant: %v", got, want)
	}
}
