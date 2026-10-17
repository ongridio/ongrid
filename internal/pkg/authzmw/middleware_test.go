package authzmw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type mockAuthorizer struct {
	allowAnyOrgFunc func(ctx context.Context, userID uint64, obj, act string) bool
}

func (m *mockAuthorizer) Allow(ctx context.Context, userID, orgID uint64, obj, act string) bool {
	return false
}

func (m *mockAuthorizer) AllowAnyOrg(ctx context.Context, userID uint64, obj, act string) bool {
	if m.allowAnyOrgFunc != nil {
		return m.allowAnyOrgFunc(ctx, userID, obj, act)
	}
	return false
}

func TestRequire(t *testing.T) {
	dummyNext := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		tenant     *tenantctx.Tenant
		authorizer Authorizer
		wantStatus int
	}{
		{
			name:       "unauthenticated returns 401",
			tenant:     nil,
			authorizer: nil,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "superuser bypasses even if authorizer is nil",
			tenant:     &tenantctx.Tenant{UserID: 1, IsSuperuser: true},
			authorizer: nil,
			wantStatus: http.StatusOK,
		},
		{
			name:       "authorizer nil fails closed with 403",
			tenant:     &tenantctx.Tenant{UserID: 2, IsSuperuser: false},
			authorizer: nil,
			wantStatus: http.StatusForbidden,
		},
		{
			name:   "authorized user returns 200",
			tenant: &tenantctx.Tenant{UserID: 3, IsSuperuser: false},
			authorizer: &mockAuthorizer{
				allowAnyOrgFunc: func(ctx context.Context, userID uint64, obj, act string) bool {
					return userID == 3 && obj == "edge:*" && act == "write"
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:   "unauthorized user returns 403",
			tenant: &tenantctx.Tenant{UserID: 4, IsSuperuser: false},
			authorizer: &mockAuthorizer{
				allowAnyOrgFunc: func(ctx context.Context, userID uint64, obj, act string) bool {
					return false
				},
			},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := New(tt.authorizer, nil)
			handler := mw.Require("edge:*", "write")(dummyNext)

			req := httptest.NewRequest(http.MethodPost, "/v1/edges", nil)
			if tt.tenant != nil {
				req = req.WithContext(tenantctx.With(req.Context(), *tt.tenant))
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
