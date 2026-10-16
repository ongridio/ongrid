package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	chi "github.com/go-chi/chi/v5"
	pagesbiz "github.com/ongridio/ongrid/internal/manager/biz/pages"
	pagesmodel "github.com/ongridio/ongrid/internal/manager/model/pages"
)

// Store is the page storage surface consumed by the HTTP handlers.
type Store interface {
	List(context.Context) ([]pagesmodel.Metadata, error)
	Delete(context.Context, string) error
	ReadPageHTML(string) ([]byte, error)
}

type Handler struct {
	store  Store
	secret string
}

func NewHandler(store Store, secret string) *Handler { return &Handler{store: store, secret: secret} }

func (h *Handler) RegisterPublic(r chi.Router) {
	r.Get("/p/{token}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pagesbiz.VerifyShareToken(h.secret, chi.URLParam(r, "token"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := h.store.ReadPageHTML(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writePageHTML(w, b)
	})
}

func (h *Handler) RegisterProtected(r chi.Router) {
	// Hosted-page management and content require authentication. Public
	// sharing uses a signed token at /api/p/{token}.
	r.Get("/v1/pages", func(w http.ResponseWriter, r *http.Request) {
		items, err := h.store.List(r.Context())
		if err != nil {
			items = []pagesmodel.Metadata{}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "total": len(items)})
	})
	r.Delete("/v1/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := h.store.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Authed in-app read of a page (the SPA fetches this with its bearer
	// and renders it via iframe srcdoc — the page is NOT public).
	r.Get("/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !pagesbiz.ValidID(id) {
			http.NotFound(w, r)
			return
		}
		b, err := h.store.ReadPageHTML(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writePageHTML(w, b)
	})
	// Mint a TTL-bounded public share link for a page (off-platform,
	// login-free) — mirrors POST /v1/reports/{id}/share.
	r.Post("/v1/pages/{id}/share", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := h.store.ReadPageHTML(id); err != nil {
			http.NotFound(w, r)
			return
		}
		exp := time.Now().Add(pagesbiz.ShareTTL)
		tok := pagesbiz.MintShareToken(h.secret, id, exp)
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"share_token": tok,
			"path":        "/api/p/" + tok,
			"expires_at":  exp.UTC().Format(time.RFC3339),
		})
	})
}

// writePageHTML serves hosted page bytes with the sandbox CSP. The HTML is
// LLM-generated; the sandbox directive loads it in an opaque origin with
// scripts disabled, so it can't read the SPA's JWT from localStorage. Inline
// styles + images still render.
func writePageHTML(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox allow-popups allow-downloads")
	_, _ = w.Write(b)
}
