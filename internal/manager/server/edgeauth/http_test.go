package edgeauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type stubAuthenticator struct {
	identity Identity
	err      error
	user     string
	pass     string
}

func (a *stubAuthenticator) AuthenticateDataPlane(_ context.Context, accessKey, secretKey string) (Identity, error) {
	a.user = accessKey
	a.pass = secretKey
	return a.identity, a.err
}

func TestVerifyReturnsScopedIdentityHeaders(t *testing.T) {
	tests := []struct {
		name      string
		identity  Identity
		wantEdge  string
		wantK8sID string
	}{
		{name: "edge", identity: Identity{EdgeID: 42}, wantEdge: "42"},
		{name: "telemetry", identity: Identity{ClusterID: 7}, wantK8sID: "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn := &stubAuthenticator{identity: tt.identity}
			h := NewHandler(authn, nil)
			req := httptest.NewRequest(http.MethodGet, "/internal/auth/test", nil)
			req.SetBasicAuth("access", "secret")
			resp := httptest.NewRecorder()

			h.verify(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.Code)
			}
			if authn.user != "access" || authn.pass != "secret" {
				t.Fatalf("credentials = %q/%q", authn.user, authn.pass)
			}
			if got := resp.Header().Get("X-Edge-Id"); got != tt.wantEdge {
				t.Fatalf("X-Edge-Id = %q, want %q", got, tt.wantEdge)
			}
			if got := resp.Header().Get("X-Cluster-Id"); got != tt.wantK8sID {
				t.Fatalf("X-Cluster-Id = %q, want %q", got, tt.wantK8sID)
			}
		})
	}
}

func TestVerifyFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		authErr    error
		wantStatus int
	}{
		{name: "missing header", wantStatus: http.StatusUnauthorized},
		{name: "bad credentials", authHeader: "Basic YWNjZXNzOnNlY3JldA==", authErr: errs.ErrUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: "backend failure", authHeader: "Basic YWNjZXNzOnNlY3JldA==", authErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&stubAuthenticator{err: tt.authErr}, nil)
			req := httptest.NewRequest(http.MethodGet, "/internal/auth/test", nil)
			req.Header.Set("Authorization", tt.authHeader)
			resp := httptest.NewRecorder()

			h.verify(resp, req)

			if resp.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.Code, tt.wantStatus)
			}
		})
	}
}
