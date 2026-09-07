package apm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/apm"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

func TestAuthenticatedValidationAndDisabledBackend(t *testing.T) {
	router := chi.NewRouter()
	NewHandler(biz.New(nil, nil, nil), nil).Register(router)
	base := "/v1/apm/services?start=2026-09-07T00:00:00Z&end=2026-09-07T01:00:00Z"
	for _, tc := range []struct {
		name, path string
		auth       bool
		status     int
	}{{"unauthenticated", base, false, 401}, {"disabled", base, true, 503}, {"invalid time", "/v1/apm/services?start=no", true, 400}, {"zero page", base + "&page=0", true, 400}, {"missing full identity", strings.Replace(base, "services", "overview", 1) + "&service_name=orders", true, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.auth {
				r = r.WithContext(tenantctx.With(r.Context(), tenantctx.Tenant{UserID: 1}))
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	q, err := parseQuery(httptest.NewRequest(http.MethodGet, base+"&environment=&service_namespace=", nil))
	if err != nil || q.Environment == nil || q.ServiceNamespace == nil || *q.Environment != "" {
		t.Fatalf("lost explicit empty scope: %+v %v", q, err)
	}
}
