// Package skill is the manager-side HTTP layer for the skill framework.
// Routes:
//
//	GET  /v1/skills                    list all skills (optional ?category=)
//	GET  /v1/skills/{key}              one skill's metadata
//	POST /v1/skills/{key}/execute      execute on a target edge
//	                                   body: { edge_id, params }
package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	svc "github.com/ongridio/ongrid/internal/manager/biz/skill"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// Service is the narrow contract the handler depends on. Production
// passes the wired biz/skill.Service; tests inject a fake.
type Service interface {
	List(ctx context.Context, caller svc.Caller, category string) []svc.SkillSummary
	Get(ctx context.Context, caller svc.Caller, key string) (*svc.SkillSummary, error)
	Execute(ctx context.Context, caller svc.Caller, in svc.ExecuteInput) (*svc.ExecuteOutput, error)
}

// Handler holds the wired service.
type Handler struct{ svc Service }

// NewHandler builds the HTTP handler.
func NewHandler(s Service) *Handler { return &Handler{svc: s} }

// Register attaches routes to r. Caller must wrap r in the JWT auth
// middleware before calling this.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/skills", h.list)
	r.Get("/v1/skills/{key}", h.get)
	r.Post("/v1/skills/{key}/execute", h.execute)
}

type listResp struct {
	Items []svc.SkillSummary `json:"items"`
	Total int                `json:"total"`
}

type executeReq struct {
	DeviceID uint64          `json:"device_id"`
	EdgeID   uint64          `json:"edge_id"`
	Params   json.RawMessage `json:"params,omitempty"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	items := h.svc.List(r.Context(), caller, r.URL.Query().Get("category"))
	writeJSON(w, http.StatusOK, listResp{Items: items, Total: len(items)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	key := chi.URLParam(r, "key")
	item, err := h.svc.Get(r.Context(), caller, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// execute 执行技能，以 Host 身份解析当前通道。
// @Summary Execute a skill on a host or manager
// @Router /v1/skills/{key}/execute [post]
// @Success 200 {object} svc.ExecuteOutput
func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	key := chi.URLParam(r, "key")
	if key == "" {
		writeErr(w, fmt.Errorf("%w: skill key required", errs.ErrInvalid))
		return
	}
	var req executeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	// edge_id requirement is scope-dependent — let the service layer
	// decide. ScopeManager skills (web_search / subprocess packs) skip
	// the check there; ScopeHost skills still 400 when edge_id == 0.
	out, err := h.svc.Execute(r.Context(), caller, svc.ExecuteInput{
		Key:      key,
		EdgeID:   req.EdgeID,
		DeviceID: req.DeviceID,
		Params:   req.Params,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func callerFromRequest(r *http.Request) (svc.Caller, bool) {
	tenant, ok := tenantctx.From(r.Context())
	if !ok {
		return svc.Caller{}, false
	}
	return svc.Caller{UserID: tenant.UserID, Role: tenant.Role}, true
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error(), Code: errCode(err)})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	default:
		return "internal"
	}
}

// parseUint64 is a small helper for tests that need to convert URL
// params manually. Not currently used by the handler but exported in
// case future routes (e.g. by-id) pick it up.
func parseUint64(s string) (uint64, error) {
	return strconv.ParseUint(s, 10, 64)
}
