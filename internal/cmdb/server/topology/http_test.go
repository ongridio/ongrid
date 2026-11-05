package topology

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// The topology routes' wire contract.

func TestTopologyRoutesAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/topology-views/groups", "/v1/cmdb/topology-views/groups"},
		{http.MethodPost, "/v1/cmdb/topology-views/groups", "/v1/cmdb/topology-views/groups"},
		{http.MethodPut, "/v1/cmdb/topology-views/groups/order", "/v1/cmdb/topology-views/groups/order"},
		{http.MethodPut, "/v1/cmdb/topology-views/groups/3", "/v1/cmdb/topology-views/groups/{id}"},
		{http.MethodDelete, "/v1/cmdb/topology-views/groups/3", "/v1/cmdb/topology-views/groups/{id}"},
		{http.MethodGet, "/v1/cmdb/topology-views", "/v1/cmdb/topology-views"},
		{http.MethodPost, "/v1/cmdb/topology-views", "/v1/cmdb/topology-views"},
		{http.MethodPut, "/v1/cmdb/topology-views/order", "/v1/cmdb/topology-views/order"},
		{http.MethodPost, "/v1/cmdb/topology-views/preview", "/v1/cmdb/topology-views/preview"},
		{http.MethodGet, "/v1/cmdb/topology-views/type-relations/host", "/v1/cmdb/topology-views/type-relations/{ref}"},
		{http.MethodGet, "/v1/cmdb/topology-views/7", "/v1/cmdb/topology-views/{id}"},
		{http.MethodPut, "/v1/cmdb/topology-views/7", "/v1/cmdb/topology-views/{id}"},
		{http.MethodDelete, "/v1/cmdb/topology-views/7", "/v1/cmdb/topology-views/{id}"},
		{http.MethodGet, "/v1/cmdb/topology-views/7/graph", "/v1/cmdb/topology-views/{id}/graph"},
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

// A topology WRITE changes what every operator sees, so a plain user is refused
// before the use case is touched. The nil handler proves the check comes first.
func TestTopologyWritesAreAdminOnly(t *testing.T) {
	h := NewHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/topology-views", nil)
	ctx := tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 8, Role: "user"})
	rec := httptest.NewRecorder()
	h.createView(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create view status = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.createGroup(rec, httptest.NewRequest(http.MethodPost, "/v1/cmdb/topology-views/groups", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create group status without identity = %d, want 403", rec.Code)
	}
}
