package history

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// The history routes' wire contract.

func TestHistoryRoutesAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/history/attributes", "/v1/cmdb/history/attributes"},
		{http.MethodGet, "/v1/cmdb/history/relations", "/v1/cmdb/history/relations"},
		{http.MethodGet, "/v1/cmdb/history/ci-types", "/v1/cmdb/history/ci-types"},
		{http.MethodGet, "/v1/cmdb/history/ci-triggers", "/v1/cmdb/history/ci-triggers"},
		{http.MethodGet, "/v1/cmdb/history/ci/100", "/v1/cmdb/history/ci/{id}"},
		{http.MethodGet, "/v1/cmdb/history/ci/100/triggers", "/v1/cmdb/history/ci/{id}/triggers"},
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

// A GLOBAL HISTORY READ SPANS EVERY MODEL AND EVERY USER, so a plain user is
// refused before the use case is touched. The nil handler proves the check
// happens first: an admin would panic here, which is why only the refusal is
// exercised.
func TestGlobalHistoryIsAdminOnly(t *testing.T) {
	h := NewHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/cmdb/history/attributes", nil)
	ctx := tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 8, Role: "user"})
	rec := httptest.NewRecorder()
	h.listAttributes(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	// With no identity at all it is also a refusal, not a panic.
	rec = httptest.NewRecorder()
	h.listAttributes(rec, httptest.NewRequest(http.MethodGet, "/v1/cmdb/history/attributes", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status without identity = %d, want 403", rec.Code)
	}
}

func TestTimeRangeAcceptsBothFormats(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?start=2026-10-26%2010:00:00&end=2026-10-26T12:00:00Z", nil)
	start, end, err := timeRange(req)
	if err != nil {
		t.Fatalf("timeRange: %v", err)
	}
	if start.Hour() != 10 || end.Hour() != 12 {
		t.Fatalf("range = %v..%v", start, end)
	}

	bad := httptest.NewRequest(http.MethodGet, "/x?start=not-a-time", nil)
	if _, _, err := timeRange(bad); err == nil {
		t.Fatal("a malformed start was accepted")
	}
}
