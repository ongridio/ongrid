package pages_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	pagesbiz "github.com/ongridio/ongrid/internal/manager/biz/pages"
	pagesdata "github.com/ongridio/ongrid/internal/manager/data/pages"
	"github.com/ongridio/ongrid/internal/manager/server/pages"
	"github.com/ongridio/ongrid/internal/pkg/auth"
)

func TestPageRoutesAuthenticationSharingAndDeletion(t *testing.T) {
	const secret = "page-test-secret"
	s := pagesdata.FileStore{Dir: t.TempDir()}
	id, path, err := s.SavePage(context.Background(), "Report", "<h1>Report</h1>")
	if err != nil {
		t.Fatal(err)
	}
	signer := auth.NewSigner(secret, time.Hour, time.Hour)
	token, err := signer.SignAccess(auth.Claims{UserID: 1, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	h := pages.NewHandler(s, secret)
	mux := chi.NewRouter()
	mux.Route("/api", func(r chi.Router) {
		h.RegisterPublic(r)
		r.Group(func(protected chi.Router) { protected.Use(auth.Middleware(signer)); h.RegisterProtected(protected) })
	})
	request := func(method, path string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, route := range []struct{ method, path string }{{"GET", path}, {"GET", "/api/v1/pages"}, {"DELETE", "/api/v1/pages/" + id}, {"POST", "/api/v1/pages/" + id + "/share"}} {
		if w := request(route.method, route.path, false); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", route.path, w.Code)
		}
	}
	if w := request("GET", path, true); w.Code != 200 || w.Body.String() != "<h1>Report</h1>" || w.Header().Get("Content-Security-Policy") != "sandbox allow-popups allow-downloads" {
		t.Fatalf("read = %d %s %v", w.Code, w.Body, w.Header())
	}
	w := request("POST", "/api/v1/pages/"+id+"/share", true)
	var share struct {
		Path      string    `json:"path"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &share); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || time.Until(share.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("share = %d %+v", w.Code, share)
	}
	if w := request("GET", share.Path, false); w.Code != 200 {
		t.Fatalf("public share = %d", w.Code)
	}
	expired := pagesbiz.MintShareToken(secret, id, time.Now().Add(-time.Hour))
	for _, invalid := range []string{"invalid", expired} {
		if w := request("GET", "/api/p/"+invalid, false); w.Code != 404 {
			t.Fatalf("invalid share = %d", w.Code)
		}
	}
	if w := request("DELETE", "/api/v1/pages/"+id, true); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	if w := request("GET", share.Path, false); w.Code != 404 {
		t.Fatalf("deleted share = %d", w.Code)
	}
}
