package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofHeapHandlerIsRegistered(t *testing.T) {
	rec := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/heap?debug=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
