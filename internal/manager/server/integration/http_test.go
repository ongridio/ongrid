package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	bizgrafana "github.com/ongridio/ongrid/internal/manager/biz/grafana"
	bizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// stubGrafana implements GrafanaService with overridable hooks. Each hook
// defaults to "not called" so a test that forgets to wire a hook fails
// loudly instead of silently passing.
type stubGrafana struct {
	test           func(ctx context.Context) error
	sync           func(ctx context.Context) (*bizgrafana.SyncResult, error)
	fetchDashboard func(ctx context.Context, uid string) ([]byte, error)
}

type stubLLMConfigProbe struct {
	probe func(context.Context, bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error)
	save  func(context.Context, bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error)
}

func (s stubLLMConfigProbe) Probe(ctx context.Context, in bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
	return s.probe(ctx, in)
}

func (s stubLLMConfigProbe) Save(ctx context.Context, in bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
	if s.save == nil {
		return bizsetting.LLMProbeResult{}, errors.New("unexpected llm configuration save")
	}
	return s.save(ctx, in)
}

type stubLLMRouterInvalidator struct{ calls int }

func (s *stubLLMRouterInvalidator) Invalidate() { s.calls++ }

func (s stubGrafana) Test(ctx context.Context) error                           { return s.test(ctx) }
func (s stubGrafana) Sync(ctx context.Context) (*bizgrafana.SyncResult, error) { return s.sync(ctx) }
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

func TestLLMConfigurationProbePassesDraftAndReturnsTypedResult(t *testing.T) {
	t.Parallel()

	g := stubGrafana{
		test:           func(context.Context) error { return nil },
		sync:           func(context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(context.Context, string) ([]byte, error) { return nil, nil },
	}
	var got bizsetting.LLMProbeInput
	h := NewHandler(g, nil, nil, nil, nil)
	h.SetLLMProbe(stubLLMConfigProbe{probe: func(_ context.Context, in bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
		got = in
		return bizsetting.LLMProbeResult{
			Valid: true, Code: bizsetting.LLMProbeCodeOK, Provider: in.Provider, Model: in.DefaultModel, LatencyMS: 42,
		}, nil
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/llm/test", strings.NewReader(`{
		"provider":"deepseek","api_key":"secret-value","base_url":"https://api.example/v1","default_model":"deepseek-chat","models":["deepseek-chat","deepseek-reasoner"]
	}`))
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "admin"}))
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got.Provider != "deepseek" || got.APIKey != "secret-value" || got.DefaultModel != "deepseek-chat" || len(got.Models) != 2 {
		t.Fatalf("probe input = %+v", got)
	}
	if strings.Contains(rec.Body.String(), "secret-value") {
		t.Fatalf("response leaked API key: %s", rec.Body.String())
	}
	var result bizsetting.LLMProbeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.Valid || result.Code != bizsetting.LLMProbeCodeOK || result.LatencyMS != 42 {
		t.Fatalf("result = %+v", result)
	}
}

func TestLLMConfigurationProbeReturnsValidationFailureAs200(t *testing.T) {
	t.Parallel()

	g := stubGrafana{
		test:           func(context.Context) error { return nil },
		sync:           func(context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(context.Context, string) ([]byte, error) { return nil, nil },
	}
	h := NewHandler(g, nil, nil, nil, nil)
	h.SetLLMProbe(stubLLMConfigProbe{probe: func(_ context.Context, in bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
		return bizsetting.LLMProbeResult{Code: bizsetting.LLMProbeCodeModelNotFound, Provider: in.Provider, Model: in.DefaultModel}, nil
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/llm/test", strings.NewReader(`{
		"provider":"openai","api_key":"bad-key","default_model":"missing-model","models":["missing-model"]
	}`))
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "admin"}))
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"model-not-found"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestLLMConfigurationProbeRejectsUnknownJSONField(t *testing.T) {
	t.Parallel()

	g := stubGrafana{
		test:           func(context.Context) error { return nil },
		sync:           func(context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(context.Context, string) ([]byte, error) { return nil, nil },
	}
	h := NewHandler(g, nil, nil, nil, nil)
	h.SetLLMProbe(stubLLMConfigProbe{probe: func(context.Context, bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
		t.Fatal("probe must not be called")
		return bizsetting.LLMProbeResult{}, nil
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/llm/test", strings.NewReader(`{
		"provider":"openai","api_key":"key","default_model":"gpt-test","models":["gpt-test"],"unexpected":true
	}`))
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "admin"}))
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLLMConfigurationProbeRequiresAdmin(t *testing.T) {
	t.Parallel()

	g := stubGrafana{
		test:           func(context.Context) error { return nil },
		sync:           func(context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(context.Context, string) ([]byte, error) { return nil, nil },
	}
	h := NewHandler(g, nil, nil, nil, nil)
	h.SetLLMProbe(stubLLMConfigProbe{probe: func(context.Context, bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
		t.Fatal("probe must not be called")
		return bizsetting.LLMProbeResult{}, nil
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/llm/test", strings.NewReader(`{}`))
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "user"}))
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLLMConfigurationValidateAndSaveUsesOneDraftAndInvalidatesRouter(t *testing.T) {
	t.Parallel()

	g := stubGrafana{
		test:           func(context.Context) error { return nil },
		sync:           func(context.Context) (*bizgrafana.SyncResult, error) { return nil, nil },
		fetchDashboard: func(context.Context, string) ([]byte, error) { return nil, nil },
	}
	var got bizsetting.LLMProbeInput
	routerInvalidator := &stubLLMRouterInvalidator{}
	h := NewHandler(g, nil, nil, nil, nil)
	h.SetLLMRouter(routerInvalidator)
	h.SetLLMProbe(stubLLMConfigProbe{
		probe: func(context.Context, bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
			return bizsetting.LLMProbeResult{}, errors.New("unexpected standalone probe")
		},
		save: func(_ context.Context, in bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error) {
			got = in
			return bizsetting.LLMProbeResult{
				Valid: true, Saved: true, Code: bizsetting.LLMProbeCodeOK,
				Provider: in.Provider, Model: in.DefaultModel, LatencyMS: 31,
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/llm/validate-and-save", strings.NewReader(`{
		"provider":"openai","api_key":"secret-value","base_url":"https://api.example/v1",
		"default_model":"model-a","models":["model-a","model-b"]
	}`))
	req = req.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7, Role: "admin"}))
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got.APIKey != "secret-value" || got.DefaultModel != "model-a" || len(got.Models) != 2 {
		t.Fatalf("save input = %+v", got)
	}
	if routerInvalidator.calls != 1 {
		t.Fatalf("router invalidations = %d, want 1", routerInvalidator.calls)
	}
	if strings.Contains(rec.Body.String(), "secret-value") || !strings.Contains(rec.Body.String(), `"saved":true`) {
		t.Fatalf("response = %s", rec.Body.String())
	}
}
