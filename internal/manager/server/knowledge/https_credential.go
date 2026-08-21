// https_credential.go — HTTP layer for /v1/knowledge/https-credentials
//
// Security: DTO never contains plaintext token; only HasToken (bool) is
// returned. This satisfies T-03-01 (Information Disclosure) at the REST
// boundary, complementing the biz-layer scrubbing in plan 02.
package knowledge

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// httpsCredentialDTO is the public view of a stored HTTPS credential.
// Token is intentionally absent — callers receive only has_token (bool)
// indicating whether a token is configured. This prevents plaintext PAT
// leakage through the API surface (T-03-01).
type httpsCredentialDTO struct {
	ID         uint64     `json:"id"`
	Name       string     `json:"name"`
	Hosts      []string   `json:"hosts"`
	Username   string     `json:"username"`
	HasToken   bool       `json:"has_token"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func toHTTPSCredentialDTO(row *model.HTTPSCredential) httpsCredentialDTO {
	hosts := []string{}
	if row.HostsJSON != "" {
		_ = json.Unmarshal([]byte(row.HostsJSON), &hosts)
	}
	return httpsCredentialDTO{
		ID:         row.ID,
		Name:       row.Name,
		Hosts:      hosts,
		Username:   row.Username,
		HasToken:   row.HasToken,
		LastUsedAt: row.LastUsedAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

// createHTTPSCredentialReq is the POST /v1/knowledge/https-credentials body.
type createHTTPSCredentialReq struct {
	Name     string   `json:"name"`
	Hosts    []string `json:"hosts"`
	Username string   `json:"username"`
	Token    string   `json:"token"`
}

// updateHTTPSCredentialReq is the PATCH /v1/knowledge/https-credentials/{id} body.
// Token="" means "do not change stored token"; Token!="" rotates to the new value.
type updateHTTPSCredentialReq struct {
	Name     string   `json:"name"`
	Hosts    []string `json:"hosts"`
	Username string   `json:"username"`
	Token    string   `json:"token"`
}

func (h *Handler) listHTTPSCredentials(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.ListHTTPSCredentials(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]httpsCredentialDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, toHTTPSCredentialDTO(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

func (h *Handler) createHTTPSCredential(w http.ResponseWriter, r *http.Request) {
	var req createHTTPSCredentialReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	row, err := h.svc.CreateHTTPSCredential(r.Context(), biz.CreateHTTPSCredentialInput{
		Name:     req.Name,
		Hosts:    req.Hosts,
		Username: req.Username,
		Token:    req.Token,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toHTTPSCredentialDTO(row))
}

func (h *Handler) updateHTTPSCredential(w http.ResponseWriter, r *http.Request) {
	id, err := parseUintParam(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateHTTPSCredentialReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	row, err := h.svc.UpdateHTTPSCredential(r.Context(), id, biz.UpdateHTTPSCredentialInput{
		Name:     req.Name,
		Hosts:    req.Hosts,
		Username: req.Username,
		Token:    req.Token,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toHTTPSCredentialDTO(row))
}

func (h *Handler) deleteHTTPSCredential(w http.ResponseWriter, r *http.Request) {
	id, err := parseUintParam(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.DeleteHTTPSCredential(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
