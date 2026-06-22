package systemupgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	upgradesvc "github.com/ongridio/ongrid/internal/manager/service/systemupgrade"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type stubUpgrade struct {
	called bool
	info   *upgradesvc.Info
	err    error
}

func (s *stubUpgrade) Check(context.Context) (*upgradesvc.Info, error) {
	s.called = true
	if s.info != nil || s.err != nil {
		return s.info, s.err
	}
	return &upgradesvc.Info{
		CurrentVersion:      "v0.8.4",
		LatestVersion:       "v0.8.5",
		UpdateAvailable:     true,
		ComparisonSupported: true,
		CheckedAt:           time.Now().UTC(),
		Commands: []upgradesvc.UpgradeCommand{
			{ID: "auto", Label: "Auto", Arch: "linux", Command: "sudo ./upgrade.sh"},
		},
	}, nil
}

func TestCheckRequiresAdmin(t *testing.T) {
	t.Parallel()
	svc := &stubUpgrade{}
	router := newRouter(NewHandler(svc))

	userReq := httptest.NewRequest(http.MethodPost, "/v1/system/upgrade/check", nil)
	userReq = userReq.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	userRec := httptest.NewRecorder()
	router.ServeHTTP(userRec, userReq)
	if userRec.Code != http.StatusForbidden {
		t.Fatalf("user status = %d body=%s", userRec.Code, userRec.Body.String())
	}
	if svc.called {
		t.Fatalf("service should not be called for non-admin")
	}

	anonReq := httptest.NewRequest(http.MethodPost, "/v1/system/upgrade/check", nil)
	anonRec := httptest.NewRecorder()
	router.ServeHTTP(anonRec, anonReq)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status = %d body=%s", anonRec.Code, anonRec.Body.String())
	}
}

func TestCheckReturnsUpgradeInfo(t *testing.T) {
	t.Parallel()
	svc := &stubUpgrade{}
	router := newRouter(NewHandler(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/system/upgrade/check", nil)
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.called {
		t.Fatalf("service was not called")
	}
	var info upgradesvc.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("response is not upgrade info: %v", err)
	}
	if !info.UpdateAvailable || info.LatestVersion != "v0.8.5" {
		t.Fatalf("info = %+v", info)
	}
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Register(r)
	return r
}
