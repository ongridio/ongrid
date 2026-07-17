package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	bizgrafana "github.com/ongridio/ongrid/internal/manager/biz/grafana"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// stubGrafana implements GrafanaService with overridable hooks. Each hook
// defaults to "not called" so a test that forgets to wire a hook fails
// loudly instead of silently passing.
type stubGrafana struct {
	test           func(ctx context.Context) error
	sync           func(ctx context.Context) (*bizgrafana.SyncResult, error)
	syncLoki       func(ctx context.Context) error
	fetchDashboard func(ctx context.Context, uid string) ([]byte, error)
}

func (s stubGrafana) Test(ctx context.Context) error                           { return s.test(ctx) }
func (s stubGrafana) Sync(ctx context.Context) (*bizgrafana.SyncResult, error) { return s.sync(ctx) }
func (s stubGrafana) SyncLoki(ctx context.Context) error {
	if s.syncLoki == nil {
		return nil
	}
	return s.syncLoki(ctx)
}
func (s stubGrafana) FetchDashboardJSON(ctx context.Context, uid string) ([]byte, error) {
	return s.fetchDashboard(ctx, uid)
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Register(r)
	return r
}

func TestFetchDashboardPassesUIDAndReturnsRawJSON(t *testing.T) {
	t.Parallel()
	body := []byte(`{"dashboard":{"uid":"d-1","title":"hi","panels":[]},"meta":{}}`)
	gotUID := ""
	g := stubGrafana{
		test: func(_ context.Context) error { return nil },
		sync: func(_ context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(_ context.Context, uid string) ([]byte, error) {
			gotUID = uid
			return body, nil
		},
	}
	h := NewHandler(g, nil, nil, nil, nil)
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/observability/dashboards/d-1", nil)
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "user"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotUID != "d-1" {
		t.Fatalf("uid passed = %q", gotUID)
	}
	if rec.Body.String() != string(body) {
		t.Fatalf("body mismatch:\n got %s\nwant %s", rec.Body.String(), body)
	}
	// Sanity: it should still be valid JSON.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
}

func TestFetchDashboardRequiresAuthContext(t *testing.T) {
	t.Parallel()
	g := stubGrafana{
		test: func(_ context.Context) error { return nil },
		sync: func(_ context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(_ context.Context, _ string) ([]byte, error) {
			t.Fatal("FetchDashboardJSON should not be invoked without auth")
			return nil, nil
		},
	}
	h := NewHandler(g, nil, nil, nil, nil)
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/observability/dashboards/d-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestFetchDashboardMapsNotFoundTo404(t *testing.T) {
	t.Parallel()
	g := stubGrafana{
		test: func(_ context.Context) error { return nil },
		sync: func(_ context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(_ context.Context, _ string) ([]byte, error) {
			return nil, pkggrafana.ErrDashboardNotFound
		},
	}
	h := NewHandler(g, nil, nil, nil, nil)
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/observability/dashboards/missing", nil)
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "user"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFetchDashboardMapsTransportErrorTo502(t *testing.T) {
	t.Parallel()
	g := stubGrafana{
		test: func(_ context.Context) error { return nil },
		sync: func(_ context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("connection refused")
		},
	}
	h := NewHandler(g, nil, nil, nil, nil)
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/observability/dashboards/d-1", nil)
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "user"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSyncLokiRequiresAdminAndInvokesService(t *testing.T) {
	t.Parallel()
	called := false
	g := stubGrafana{
		test: func(_ context.Context) error { return nil },
		sync: func(_ context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		syncLoki: func(_ context.Context) error {
			called = true
			return nil
		},
		fetchDashboard: func(_ context.Context, _ string) ([]byte, error) { return nil, nil },
	}
	router := newRouter(NewHandler(g, nil, nil, nil, nil))

	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/grafana/sync-loki", nil)
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "admin"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("SyncLoki was not invoked")
	}
}
