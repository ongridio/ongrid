package aiops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	svc "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type resultHTTPService struct {
	fakeService
	request toolresult.Request
	query   bool
	err     error
}

func (f *resultHTTPService) GetToolResult(context.Context, svc.Caller, string, string) (*model.Result, error) {
	return &model.Result{}, f.err
}
func (f *resultHTTPService) ReadToolResult(_ context.Context, c svc.Caller, _ string, req toolresult.Request, query bool) (*toolresult.Page, error) {
	f.lastCaller, f.request, f.query = c, req, query
	return &toolresult.Page{ResultID: req.ResultID}, f.err
}
func (f *resultHTTPService) DownloadToolResult(context.Context, svc.Caller, string, string) (*model.Result, io.ReadSeekCloser, error) {
	return nil, nil, errs.ErrNotFound
}

func TestToolResultHTTPRouteAndInputBoundary(t *testing.T) {
	const url = "/v1/chat/sessions/session/tool-results/route-id/query"
	f := &resultHTTPService{}
	r := buildRouter(NewHandler(f), tenantctx.Tenant{UserID: 7, Role: "user"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"result_id":"forged","contains":"failure"}`)))
	if w.Code != http.StatusOK || f.request.ResultID != "route-id" || !f.query || f.lastCaller.UserID != 7 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("route=%d %s %+v", w.Code, w.Body.String(), f)
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, `{"contains":"` + strings.Repeat("x", 16<<10) + `"}`} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body accepted: %d", w.Code)
		}
	}
	f.err = errs.ErrForbidden
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("authorization error=%d", w.Code)
	}
	w = httptest.NewRecorder()
	unauthenticated := chi.NewRouter()
	NewHandler(f).Register(unauthenticated)
	unauthenticated.ServeHTTP(w, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request=%d", w.Code)
	}
}
