package systemhealth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
	healthsvc "github.com/ongridio/ongrid/internal/manager/service/systemhealth"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type stubHealth struct {
	called bool
	caller alertsvc.Caller
	report *healthsvc.Report
	err    error
}

func (s *stubHealth) Check(_ context.Context, caller alertsvc.Caller) (*healthsvc.Report, error) {
	s.called = true
	s.caller = caller
	if s.report != nil || s.err != nil {
		return s.report, s.err
	}
	return &healthsvc.Report{
		Status:    healthsvc.StatusOK,
		CheckedAt: time.Now().UTC(),
		Summary:   healthsvc.Summary{OK: 1},
		Checks: []healthsvc.Check{
			{ID: "manager_api", Status: healthsvc.StatusOK, Message: "ok"},
		},
	}, nil
}

func TestCheckRequiresAdmin(t *testing.T) {
	t.Parallel()
	svc := &stubHealth{}
	router := newRouter(NewHandler(svc))

	userReq := httptest.NewRequest(http.MethodPost, "/v1/system/health/check", nil)
	userReq = userReq.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	userRec := httptest.NewRecorder()
	router.ServeHTTP(userRec, userReq)
	if userRec.Code != http.StatusForbidden {
		t.Fatalf("user status = %d body=%s", userRec.Code, userRec.Body.String())
	}
	if svc.called {
		t.Fatalf("service should not be called for non-admin")
	}

	anonReq := httptest.NewRequest(http.MethodPost, "/v1/system/health/check", nil)
	anonRec := httptest.NewRecorder()
	router.ServeHTTP(anonRec, anonReq)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status = %d body=%s", anonRec.Code, anonRec.Body.String())
	}
}

func TestCheckReturnsReport(t *testing.T) {
	t.Parallel()
	svc := &stubHealth{}
	router := newRouter(NewHandler(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/system/health/check", nil)
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.called || svc.caller.UserID != 1 || svc.caller.Role != "admin" {
		t.Fatalf("caller = %+v called=%v", svc.caller, svc.called)
	}
	var report healthsvc.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("response is not a health report: %v", err)
	}
	if report.Status != healthsvc.StatusOK {
		t.Fatalf("status = %q, want ok", report.Status)
	}
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Register(r)
	return r
}
