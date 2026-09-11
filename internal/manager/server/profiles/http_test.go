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
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/flamegraph?device_id=42&service=orders-api%22%7D&kind=heap&range=15m", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotQuery != `space:inuse_space:bytes:space:bytes{device_id="42",service_name="orders-api\"}",profile_type="heap"}` {
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
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/flamegraph?device_id=42&service=orders-api&kind=unknown", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFlamegraphRequiresDeviceID(t *testing.T) {
	router := chi.NewRouter()
	NewHandler("http://pyroscope.invalid").Register(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/flamegraph?service=orders-api&kind=heap", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "device_id") {
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
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/download?device_id=42&service=orders-api&kind=cpu&range=1h", nil))

	if rec.Code != http.StatusOK || gotFormat != "pprof" || rec.Body.String() != "pprof-data" {
		t.Fatalf("status=%d format=%q body=%q", rec.Code, gotFormat, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="profile.pprof"` {
		t.Fatalf("content-disposition=%q", got)
	}
}

func TestDownloadRejectsOversizedBackendResponse(t *testing.T) {
	handler := NewHandler("http://pyroscope.test")
	handler.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
		}, nil
	})

	router := chi.NewRouter()
	handler.Register(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles/download?device_id=42&service=orders-api&kind=cpu&range=1h", nil))

	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "exceeds 16 MiB") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestProfileAbsoluteTimeAndInstanceScope(t *testing.T) {
	handler := NewHandler("http://pyroscope.test")
	handler.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if q.Get("from") != "1788739200" || q.Get("until") != "1788742800" || !strings.Contains(q.Get("query"), `deployment_environment_name="production",service_namespace="trade",service_instance_id="pod-1",service_version="v2"`) {
			t.Fatalf("incorrect profile query: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	router := chi.NewRouter()
	handler.Register(router)
	base := "/v1/profiles/flamegraph?device_id=42&service=orders&kind=heap&environment=production&service_namespace=trade&instance_id=pod-1&service_version=v2"
	for _, tc := range []struct {
		query  string
		status int
	}{{"&start=2026-09-07T00:00:00Z&end=2026-09-07T01:00:00Z", 200}, {"&start=2026-09-07T00:00:00Z", 400}, {"&start=2026-09-07T00:00:00Z&end=2026-10-07T01:00:00Z", 400}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+tc.query, nil))
		if w.Code != tc.status {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
