// Package topology serves the saved topology views.
package topology

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	topologybiz "github.com/ongridio/ongrid/internal/cmdb/biz/topology"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// Handler serves the topology routes.
type Handler struct {
	uc *topologybiz.Usecase
}

// NewHandler builds the handler.
func NewHandler(uc *topologybiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the topology routes.
//
// Routes:
//
//	GET    /v1/cmdb/topology-views/groups              (any authed)
//	POST   /v1/cmdb/topology-views/groups              (admin)
//	PUT    /v1/cmdb/topology-views/groups/order        (admin)
//	PUT    /v1/cmdb/topology-views/groups/{id}         (admin)
//	DELETE /v1/cmdb/topology-views/groups/{id}         (admin)
//	GET    /v1/cmdb/topology-views                     (any authed)
//	POST   /v1/cmdb/topology-views                     (admin)
//	PUT    /v1/cmdb/topology-views/order               (admin)
//	POST   /v1/cmdb/topology-views/preview             (any authed)
//	GET    /v1/cmdb/topology-views/type-relations/{ref} (any authed)
//	GET    /v1/cmdb/topology-views/{id}                (any authed)
//	PUT    /v1/cmdb/topology-views/{id}                (admin)
//	DELETE /v1/cmdb/topology-views/{id}                (admin)
//	GET    /v1/cmdb/topology-views/{id}/graph          (any authed)
//
// THE STATIC SEGMENTS ARE REGISTERED BEFORE `{id}`. `/groups`, `/order`,
// `/preview` and `/type-relations` would otherwise be read as view ids — the
// route test pins each one.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/cmdb/topology-views/groups", h.listGroups)
	r.Post("/v1/cmdb/topology-views/groups", h.createGroup)
	r.Put("/v1/cmdb/topology-views/groups/order", h.reorderGroups)
	r.Put("/v1/cmdb/topology-views/groups/{id}", h.updateGroup)
	r.Delete("/v1/cmdb/topology-views/groups/{id}", h.deleteGroup)

	r.Get("/v1/cmdb/topology-views", h.listViews)
	r.Post("/v1/cmdb/topology-views", h.createView)
	r.Put("/v1/cmdb/topology-views/order", h.reorderViews)
	r.Post("/v1/cmdb/topology-views/preview", h.preview)
	r.Get("/v1/cmdb/topology-views/type-relations/{ref}", h.typeRelations)
	r.Get("/v1/cmdb/topology-views/{id}", h.getView)
	r.Put("/v1/cmdb/topology-views/{id}", h.updateView)
	r.Delete("/v1/cmdb/topology-views/{id}", h.deleteView)
	r.Get("/v1/cmdb/topology-views/{id}/graph", h.viewGraph)
}

func isAdmin(r *http.Request) bool {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		return false
	}
	return t.IsSuperuser || t.Role == "admin"
}

// --- groups ---

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListGroups(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

type groupBody struct {
	Name    *string     `json:"name"`
	ViewIDs *[]model.ID `json:"view_ids"`
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	var body groupBody
	if err := decode(r, &body); err != nil || body.Name == nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.CreateGroup(r.Context(), *body.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var body groupBody
	if err := decode(r, &body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	in := topologybiz.GroupInput{Name: body.Name}
	if body.ViewIDs != nil {
		in.ViewIDs, in.SetViews = *body.ViewIDs, true
	}
	got, err := h.uc.UpdateGroup(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteGroup(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200})
}

type orderBody struct {
	GroupIDs []model.ID `json:"group_ids"`
	ViewIDs  []model.ID `json:"view_ids"`
}

func (h *Handler) reorderGroups(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	var body orderBody
	if err := decode(r, &body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.ReorderGroups(r.Context(), body.GroupIDs); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group_ids": body.GroupIDs})
}

// --- views ---

type viewBody struct {
	Name                 string                `json:"name"`
	GroupID              *model.ID             `json:"group_id"`
	Category             string                `json:"category"`
	CentralNodeType      model.ID              `json:"central_node_type"`
	CentralNodeInstances string                `json:"central_node_instances"`
	Path                 map[string][]model.ID `json:"path"`
	Option               map[string]any        `json:"option"`
}

func (b viewBody) input() topologybiz.ViewInput {
	return topologybiz.ViewInput{
		Name: b.Name, GroupID: b.GroupID, Category: b.Category,
		CentralNodeType: b.CentralNodeType, CentralNodeInstances: b.CentralNodeInstances,
		Path: b.Path, Option: b.Option,
	}
}

func (h *Handler) listViews(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListViews(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) getView(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetView(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) createView(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	var body viewBody
	if err := decode(r, &body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.CreateView(r.Context(), body.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (h *Handler) updateView(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var body viewBody
	if err := decode(r, &body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.UpdateView(r.Context(), id, body.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) deleteView(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteView(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200})
}

func (h *Handler) reorderViews(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	var body orderBody
	if err := decode(r, &body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.ReorderViews(r.Context(), body.ViewIDs); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"view_ids": body.ViewIDs})
}

// --- the graph ---

func (h *Handler) viewGraph(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.Graph(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	var body viewBody
	if err := decode(r, &body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.Preview(r.Context(), body.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) typeRelations(w http.ResponseWriter, r *http.Request) {
	children, parents, err := h.uc.TypeRelations(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"children": children, "parents": parents})
}

// --- plumbing ---

func decode(r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.UseNumber()
	return dec.Decode(out)
}

func pathID(r *http.Request, name string) (model.ID, error) {
	n, err := strconv.ParseInt(chi.URLParam(r, name), 10, 32)
	if err != nil || n <= 0 {
		return 0, errs.ErrInvalid
	}
	return model.ID(n), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.WriteHeader(errs.HTTPStatus(err))
	_, _ = w.Write([]byte(strings.TrimSpace(err.Error())))
}
