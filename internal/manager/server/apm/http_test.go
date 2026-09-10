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
	}{{"unauthenticated", base, false, 401}, {"disabled", base, true, 503}, {"global dependencies", strings.Replace(base, "services", "dependencies", 1), true, 503}, {"partial dependency identity", strings.Replace(base, "services", "dependencies", 1) + "&service_name=orders", true, 400}, {"invalid time", "/v1/apm/services?start=no", true, 400}, {"zero page", base + "&page=0", true, 400}, {"missing full identity", strings.Replace(base, "services", "overview", 1) + "&service_name=orders", true, 400}} {
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
	q, err := parseQuery(httptest.NewRequest(http.MethodGet, base+"&environment=&service_namespace=&device_id=42&cluster_id=7", nil))
	if err != nil || q.DeviceID != "42" || q.ClusterID != "7" || q.Environment == nil || q.ServiceNamespace == nil || *q.Environment != "" {
		t.Fatalf("lost explicit empty scope: %+v %v", q, err)
	}
}

func TestRepositoryBindingAuthorizationAndBody(t *testing.T) {
	router := chi.NewRouter()
	NewHandler(biz.New(nil, nil, nil), nil).Register(router)
	url := "/v1/apm/repository-binding?service_name=orders&service_namespace=&environment="
	for _, tc := range []struct {
		method, body string
		caller       *tenantctx.Tenant
		status       int
	}{
		{"GET", "", nil, 401},
		{"PUT", `{}`, &tenantctx.Tenant{UserID: 1}, 403},
		{"DELETE", "", &tenantctx.Tenant{UserID: 1}, 403},
		{"PUT", `{"repo_id":"1","repo_url":"https://unregistered"}`, &tenantctx.Tenant{IsSuperuser: true}, 400},
		{"PUT", `{"repo_id":"1"} {}`, &tenantctx.Tenant{IsSuperuser: true}, 400},
		{"PUT", `{"repo_id":"1","source_directory":"../escape"}`, &tenantctx.Tenant{Role: "admin"}, 400},
		{"GET", "", &tenantctx.Tenant{UserID: 1}, 503},
	} {
		r := httptest.NewRequest(tc.method, url, strings.NewReader(tc.body))
		if tc.caller != nil {
			r = r.WithContext(tenantctx.With(r.Context(), *tc.caller))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: status %d, want %d: %s", tc.method, w.Code, tc.status, w.Body.String())
		}
	}
}
