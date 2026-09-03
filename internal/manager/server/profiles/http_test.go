package profiles

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestFlamegraphBuildsSafePyroscopeQuery(t *testing.T) {
	var gotQuery string
	handler := NewHandler("http://pyroscope.test")
	handler.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		gotQuery = r.URL.Query().Get("query")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"flamebearer":{"names":["total"],"levels":[[0,4,4,0]],"numTicks":4,"maxSelf":4},"metadata":{"format":"single","units":"samples"}}`)),
		}, nil
	})

	router := chi.NewRouter()
	handler.Register(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/flamegraph?service=orders-api%22%7D&kind=heap&range=15m", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotQuery != `space:inuse_space:bytes:space:bytes{service_name="orders-api\"}",profile_type="heap"}` {
		t.Fatalf("query=%q", gotQuery)
	}
	if !strings.Contains(rec.Body.String(), `"numTicks":4`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (fn roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestFlamegraphRejectsUnsupportedKind(t *testing.T) {
	router := chi.NewRouter()
	NewHandler("http://pyroscope.invalid").Register(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/flamegraph?service=orders-api&kind=unknown", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDownloadReturnsPprofAttachment(t *testing.T) {
	var gotFormat string
	handler := NewHandler("http://pyroscope.test")
	handler.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		gotFormat = r.URL.Query().Get("format")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("pprof-data"))}, nil
	})

	router := chi.NewRouter()
	handler.Register(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/download?service=orders-api&kind=cpu&range=1h", nil))

	if rec.Code != http.StatusOK || gotFormat != "pprof" || rec.Body.String() != "pprof-data" {
		t.Fatalf("status=%d format=%q body=%q", rec.Code, gotFormat, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="profile.pprof"` {
		t.Fatalf("content-disposition=%q", got)
	}
}
