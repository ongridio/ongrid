// Package flow exposes the workflow-orchestration HTTP surface
// (HLD-016). Mirrors server/report's lean pattern — chi-mounted
// Handler, reads open to any authed user, writes gated on role via
// requireWriter (ADR-022: viewer is read-only).
package flow

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	bizflow "github.com/ongridio/ongrid/internal/manager/biz/flow"
	model "github.com/ongridio/ongrid/internal/manager/model/flow"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const roleViewer = "viewer"

// Handler carries the biz facade.
type Handler struct {
	uc *bizflow.Usecase
}

// NewHandler constructs the HTTP layer.
func NewHandler(uc *bizflow.Usecase) *Handler { return &Handler{uc: uc} }

// Register mounts the authed routes.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/flows", h.list)
	r.With(h.requireWriter).Post("/v1/flows", h.create)
	r.Get("/v1/flows/{id}", h.get)
	r.With(h.requireWriter).Put("/v1/flows/{id}", h.update)
	r.With(h.requireWriter).Delete("/v1/flows/{id}", h.del)
	r.With(h.requireWriter).Post("/v1/flows/{id}/toggle", h.toggle)
	r.With(h.requireWriter).Post("/v1/flows/{id}/run", h.run)
	r.Get("/v1/flows/{id}/runs", h.listRuns)
	r.Get("/v1/flow-runs/{run_id}", h.getRun)
}

func (h *Handler) requireWriter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok {
			writeErr(w, errs.ErrUnauthorized)
			return
		}
		if t.Role == roleViewer {
			writeErr(w, errs.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- wire DTOs ---

type flowDTO struct {
	ID          uint64          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Graph       json.RawMessage `json:"graph"`
	Enabled     bool            `json:"enabled"`
	Version     int             `json:"version"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

func toFlowDTO(f *model.Flow, withGraph bool) flowDTO {
	d := flowDTO{
		ID:          f.ID,
		Name:        f.Name,
		Description: f.Description,
		Enabled:     f.Enabled,
		Version:     f.Version,
		CreatedAt:   f.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   f.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if withGraph {
		d.Graph = json.RawMessage(f.GraphJSON)
	}
	return d
}

type runDTO struct {
	ID          string `json:"id"`
	FlowID      uint64 `json:"flow_id"`
	FlowVersion int    `json:"flow_version"`
	Status      string `json:"status"`
	TriggerType string `json:"trigger_type"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func toRunDTO(r *model.FlowRun) runDTO {
	d := runDTO{
		ID:          r.ID,
		FlowID:      r.FlowID,
		FlowVersion: r.FlowVersion,
		Status:      r.Status,
		TriggerType: r.TriggerType,
		Error:       r.Error,
		CreatedAt:   r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if r.StartedAt != nil {
		d.StartedAt = r.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if r.FinishedAt != nil {
		d.FinishedAt = r.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return d
}

type runNodeDTO struct {
	NodeID     string          `json:"node_id"`
	NodeType   string          `json:"node_type"`
	NodeName   string          `json:"node_name"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	FiredPort  string          `json:"fired_port"`
	Error      string          `json:"error,omitempty"`
	StartedAt  string          `json:"started_at,omitempty"`
	FinishedAt string          `json:"finished_at,omitempty"`
}

// --- handlers ---

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authed(w, r) {
		return
	}
	q := r.URL.Query()
	rows, total, err := h.uc.List(r.Context(), atoiDefault(q.Get("limit"), 50), atoiDefault(q.Get("offset"), 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]flowDTO, 0, len(rows))
	for _, f := range rows {
		items = append(items, toFlowDTO(f, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

type writeBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Graph       json.RawMessage `json:"graph"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	t, _ := tenantctx.From(r.Context())
	var in writeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	f, err := h.uc.Create(r.Context(), bizflow.CreateInput{
		Name:        in.Name,
		Description: in.Description,
		GraphJSON:   string(in.Graph),
		CreatedBy:   &t.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFlowDTO(f, true))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !h.authed(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	f, err := h.uc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlowDTO(f, true))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in writeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	f, err := h.uc.Update(r.Context(), id, bizflow.CreateInput{
		Name:        in.Name,
		Description: in.Description,
		GraphJSON:   string(in.Graph),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlowDTO(f, true))
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := h.uc.SetEnabled(r.Context(), id, in.Enabled); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": in.Enabled})
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	t, _ := tenantctx.From(r.Context())
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Input map[string]any `json:"input"`
	}
	// Body is optional for a bare manual run.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in)
	run, err := h.uc.Trigger(r.Context(), id, in.Input, &t.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toRunDTO(run))
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	if !h.authed(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	runs, err := h.uc.ListRuns(r.Context(), id, atoiDefault(r.URL.Query().Get("limit"), 20))
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]runDTO, 0, len(runs))
	for _, rr := range runs {
		items = append(items, toRunDTO(rr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	if !h.authed(w, r) {
		return
	}
	run, nodes, err := h.uc.GetRun(r.Context(), chi.URLParam(r, "run_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	nds := make([]runNodeDTO, 0, len(nodes))
	for _, n := range nodes {
		d := runNodeDTO{
			NodeID:    n.NodeID,
			NodeType:  n.NodeType,
			NodeName:  n.NodeName,
			Status:    n.Status,
			Input:     json.RawMessage(n.InputJSON),
			Output:    json.RawMessage(n.OutputJSON),
			FiredPort: n.FiredPort,
			Error:     n.Error,
		}
		if n.StartedAt != nil {
			d.StartedAt = n.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		if n.FinishedAt != nil {
			d.FinishedAt = n.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		nds = append(nds, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": toRunDTO(run), "nodes": nds})
}

// --- helpers (mirror server/report) ---

func (h *Handler) authed(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return false
	}
	return true
}

func pathID(r *http.Request) (uint64, error) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return id, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errs.HTTPStatus(err))
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error(), Code: errCode(err)})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	default:
		return "internal"
	}
}
