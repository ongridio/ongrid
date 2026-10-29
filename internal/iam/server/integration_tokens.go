package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ongridio/ongrid/internal/iam/biz/integrationtoken"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// TokenHandler serves JWT-authenticated administrator-owned PAT management.
// Secret/hash fields are never included in list, update or delete responses.
type TokenHandler struct {
	tokens        *integrationtoken.Usecase
	enabled       bool
	writesEnabled bool
	endpoint      string
}

func NewTokenHandler(tokens *integrationtoken.Usecase, enabled bool, writesEnabled bool, endpoint string) *TokenHandler {
	return &TokenHandler{tokens: tokens, enabled: enabled, writesEnabled: writesEnabled, endpoint: endpoint}
}

func (h *TokenHandler) Register(r chi.Router) {
	r.Get("/v1/iam/mcp-tokens", h.list)
	r.Post("/v1/iam/mcp-tokens", h.save)
	r.Put("/v1/iam/mcp-tokens/{id}", h.save)
	r.Delete("/v1/iam/mcp-tokens/{id}", h.remove)
}

// GET /v1/iam/mcp-tokens?limit=20&offset=0 returns owned metadata and setup info.
func (h *TokenHandler) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	t, _ := tenantctx.From(r.Context())
	limit, offset := 20, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, errs.ErrInvalid)
			return
		}
		limit = n
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, errs.ErrInvalid)
			return
		}
		offset = n
	}
	items, total, err := h.tokens.List(r.Context(), t.UserID, limit, offset)
	if err != nil {
		tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "scopes": integrationauth.Scopes(), "enabled": h.enabled, "writes_enabled": h.writesEnabled, "endpoint": h.endpoint})
}

// POST /v1/iam/mcp-tokens creates an owned PAT; PUT /{id} updates its name,
// scopes and nullable expires_at. Only creation returns the plaintext token.
func (h *TokenHandler) save(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	t, _ := tenantctx.From(r.Context())
	var in integrationtoken.CreateInput
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		item, err := h.tokens.Update(r.Context(), t.UserID, id, in)
		if err != nil {
			tokenError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
		return
	}
	item, secret, err := h.tokens.Create(r.Context(), t.UserID, in)
	if err != nil {
		tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": item, "token": secret})
}

// DELETE /v1/iam/mcp-tokens/{public_id} deletes only the caller's PAT.
func (h *TokenHandler) remove(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	t, _ := tenantctx.From(r.Context())
	if err := h.tokens.Delete(r.Context(), t.UserID, chi.URLParam(r, "id")); err != nil {
		tokenError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tokenError(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	http.Error(w, http.StatusText(status), status)
}
