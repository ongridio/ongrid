package agenttrace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type emptyQuerier struct{}

func (emptyQuerier) SearchTraces(context.Context, tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	return &tracequery.SearchResult{Traces: json.RawMessage(`[]`)}, nil
}
func (emptyQuerier) GetTrace(context.Context, string) (*tracequery.TraceResult, error) {
	return nil, tracequery.ErrResponseTooLarge
}

func TestAuthenticatedRoutesAndErrors(t *testing.T) {
	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) { NewHandler(biz.New(emptyQuerier{}), nil).Register(r) })
	for _, tc := range []struct {
		path   string
		auth   bool
		status int
	}{
		{"/runs?start=2026-10-14T00:00:00Z&end=2026-10-14T01:00:00Z", false, 401},
		{"/runs?start=bad&end=bad", true, 400},
		{"/runs?start=2026-10-14T00:00:00Z&end=2026-10-14T01:00:00Z", true, 200},
		{"/sessions/detail?start=2026-10-14T00:00:00Z&end=2026-10-14T01:00:00Z", true, 400},
		{"/traces/not-a-trace", true, 400},
		{"/traces/0102030405060708090a0b0c0d0e0f10", true, 413},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-observability"+tc.path, nil)
		if tc.auth {
			req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 1}))
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Errorf("%s: got %d: %s", tc.path, out.Code, out.Body.String())
		}
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("sensitive trace response can be cached")
		}
	}
}
