// Package approval is the HTTP surface for the propose-confirm inbox
// (HLD-017). Read + decide routes are admin-only. Strictly additive.
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	bizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	externalbiz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	approvalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	externalsvc "github.com/ongridio/ongrid/internal/manager/service/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type operationReader interface {
	ApprovalOperation(context.Context, *approvalmodel.Approval) (externalbiz.OperationResult, error)
}

// Handler serves /v1/approvals.
type Handler struct {
	uc         *bizapproval.Usecase
	operations operationReader
}

func (h *Handler) SetExternalOperations(reader operationReader) { h.operations = reader }

func (h *Handler) detail(w http.ResponseWriter, r *http.Request, a *approvalmodel.Approval) {
	view, err := presentApproval(r, a)
	if err != nil {
		writeErr(w, err)
		return
	}
	var op *externalbiz.OperationResult
	_, external := integrationauth.From(r.Context())
	if a.Kind == externalbiz.ApprovalKind && h.operations != nil && !external {
		result, err := h.operations.ApprovalOperation(r.Context(), a)
		if err != nil && !(errors.Is(err, errs.ErrNotFound) && a.Status != approvalmodel.StatusPending) {
			writeErr(w, err)
			return
		}
		if err == nil {
			op = &result
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		*approvalmodel.Approval
		Operation *externalbiz.OperationResult `json:"operation,omitempty"`
	}{view, op})
}

// NewHandler wires the usecase.
func NewHandler(uc *bizapproval.Usecase) *Handler { return &Handler{uc: uc} }

// Register mounts the routes under an auth'd chi.Router.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/approvals", h.list)
	r.Get("/v1/approvals/count", h.count)
	r.Get("/v1/approvals/{id}", h.get)
	r.Post("/v1/approvals/{id}/approve", h.approve)
	r.Post("/v1/approvals/{id}/reject", h.reject)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	status := r.URL.Query().Get("status")
	items, err := h.uc.List(r.Context(), status, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	for i, item := range items {
		items[i], err = presentApproval(r, item)
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) count(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	n, err := h.uc.CountPending(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": n})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	a, err := h.uc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.detail(w, r, a)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	if _, external := integrationauth.From(r.Context()); external {
		writeErr(w, errs.ErrForbidden)
		return
	}
	c, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	a, err := h.uc.Approve(r.Context(), c.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.detail(w, r, a)
}

func presentApproval(r *http.Request, a *approvalmodel.Approval) (*approvalmodel.Approval, error) {
	owner, ok := tenantctx.From(r.Context())
	if !ok || owner.Role != "admin" {
		return nil, errs.ErrForbidden
	}
	if a.Kind != externalbiz.ApprovalKind {
		return a, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(a.PayloadJSON), &payload); err != nil {
		return nil, err
	}
	ciphertext, ok := payload["encrypted_request"].(string)
	if !ok {
		return a, nil
	}
	delete(payload, "encrypted_request")
	// The MCP API bridge may read approval metadata, never decrypted arguments.
	if _, external := integrationauth.From(r.Context()); !external {
		plain, err := secretbox.Decrypt(ciphertext)
		if err != nil {
			return nil, err
		}
		var request struct {
			Arguments externalbiz.Arguments `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(plain), &request); err != nil {
			return nil, err
		}
		payload["arguments"] = externalsvc.ReviewArguments(request.Arguments)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	copy := *a
	copy.PayloadJSON = string(raw)
	return &copy, nil
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	if _, external := integrationauth.From(r.Context()); external {
		writeErr(w, errs.ErrForbidden)
		return
	}
	c, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in)
	if err := h.uc.Reject(r.Context(), c.UserID, chi.URLParam(r, "id"), in.Reason); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- auth + json helpers (mirrors server/secret) ---

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
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	slug := "internal"
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		code, slug = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		code, slug = http.StatusForbidden, "forbidden"
	case errors.Is(err, errs.ErrNotFound):
		code, slug = http.StatusNotFound, "not_found"
	case errors.Is(err, errs.ErrInvalid):
		code, slug = http.StatusBadRequest, "invalid"
	}
	writeJSON(w, code, errorBody{Error: err.Error(), Code: slug})
}
