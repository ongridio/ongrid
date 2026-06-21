// Package secret is the HTTP surface for the generic secret vault
// (HLD-017). All routes are admin-only; values are write-only — the list
// API returns redacted views (has_value) and never the secret material.
package secret

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	bizsecret "github.com/ongridio/ongrid/internal/manager/biz/secret"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// Handler serves /v1/secrets.
type Handler struct{ uc *bizsecret.Usecase }

// NewHandler wires the usecase.
func NewHandler(uc *bizsecret.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches routes under a chi.Router that already has auth in
// front of it.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/secrets", h.list)
	r.Post("/v1/secrets", h.create)
	r.Put("/v1/secrets/{id}", h.update)
	r.Delete("/v1/secrets/{id}", h.del)
	r.Get("/v1/credential-types", h.types)
}

// types lists the reusable credential types (fields + which type injects how)
// so the create-credential UI can render the right form.
func (h *Handler) types(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": bizsecret.AllCredTypes()})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	items, err := h.uc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Name        string            `json:"name"`
		Type        string            `json:"type"`
		Description string            `json:"description"`
		Fields      map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	v, err := h.uc.Create(r.Context(), in.Name, in.Type, in.Description, in.Fields)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	var in struct {
		Description string            `json:"description"`
		Fields      map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := h.uc.Update(r.Context(), id, in.Description, in.Fields); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- auth + json helpers (mirrors server/setting) ---

type caller struct {
	UserID uint64
	Role   string
}

func requireAdmin(w http.ResponseWriter, r *http.Request) (caller, bool) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return caller{}, false
	}
	if t.Role != "admin" {
		writeErr(w, errs.ErrForbidden)
		return caller{}, false
	}
	return caller{UserID: t.UserID, Role: t.Role}, true
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	writeJSON(w, errCode(err), errorBody{Error: err.Error(), Code: errSlug(err)})
}

func errCode(err error) int {
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, errs.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, errs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, errs.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, errs.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func errSlug(err error) string {
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrNotFound):
		return "not_found"
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	default:
		return "internal"
	}
}
