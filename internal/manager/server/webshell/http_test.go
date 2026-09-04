package webshell

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestShellTicketIsOneTimeAndBound(t *testing.T) {
	h := &Handler{tickets: map[string]shellTicket{}, failures: map[string]authFailures{}}
	token, _, err := h.mintTicket(shellTicket{UserID: 7, DeviceID: 9, ClientIP: "127.0.0.1", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.consumeTicket(token, 8, "127.0.0.1"); ok {
		t.Fatal("ticket accepted for another device")
	}
	if _, ok := h.consumeTicket(token, 9, "127.0.0.1"); ok {
		t.Fatal("failed binding attempt did not consume ticket")
	}

	token, _, err = h.mintTicket(shellTicket{UserID: 7, DeviceID: 9, ClientIP: "127.0.0.1", SaveCredential: true})
	if err != nil {
		t.Fatal(err)
	}
	ticket, ok := h.consumeTicket(token, 9, "127.0.0.1")
	if !ok {
		t.Fatal("valid ticket rejected")
	}
	if !ticket.SaveCredential {
		t.Fatal("save intent was not carried by the one-time ticket")
	}
	if _, ok := h.consumeTicket(token, 9, "127.0.0.1"); ok {
		t.Fatal("ticket reused")
	}
}

func TestRegisterDoesNotExposeUnverifiedCredentialCreate(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/devices/9/shell/credentials", nil))
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unverified credential create route returned %d", rec.Code)
	}
}

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest("GET", "https://ops.example/api/v1/devices/1/shell", nil)
	r.Host = "ops.example"
	r.Header.Set("Origin", "https://ops.example")
	if !sameOrigin(r) {
		t.Fatal("same origin rejected")
	}
	r.Host = "ops.example:8443"
	r.Header.Set("Origin", "https://ops.example:8443")
	if !sameOrigin(r) {
		t.Fatal("same origin with a custom port rejected")
	}
	r.Host = "ops.example"
	r.Header.Set("Origin", "https://evil.example")
	if sameOrigin(r) {
		t.Fatal("cross origin accepted")
	}
	r.Header.Set("Origin", "http://ops.example")
	if sameOrigin(r) {
		t.Fatal("cross-scheme origin accepted")
	}
	r.Header.Set("Origin", "https://ops.example/path")
	if sameOrigin(r) {
		t.Fatal("origin with a path accepted")
	}
	r.Header.Del("Origin")
	if sameOrigin(r) {
		t.Fatal("missing origin accepted")
	}
}
