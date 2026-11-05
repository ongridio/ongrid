package citype

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerAttributeGroupRoutes attaches the form-section routes.
//
// Routes:
//
//	GET    /v1/cmdb/ci-types/{ref}/attribute-groups            (any authed)
//	POST   /v1/cmdb/ci-types/{ref}/attribute-groups            (admin) — upsert by name
//	PUT    /v1/cmdb/ci-types/{ref}/attribute-groups/{id}       (admin)
//	DELETE /v1/cmdb/ci-types/{ref}/attribute-groups/{id}       (admin)
//	POST   /v1/cmdb/ci-types/{ref}/attribute-groups/transfer   (admin)
//
// Every write here is admin-gated. In cantor two of these were effectively
// ungated: its PUT and DELETE carried a permission decorator naming `type_id`,
// but the routes expose only a group id, so the decorator found no resource and
// skipped the check entirely — any authenticated caller could rename, reorder
// or delete any model's form sections. The port gates on the role instead,
// which cannot be bypassed by omitting a field.
func (h *Handler) registerAttributeGroupRoutes(r chi.Router) {
	r.Get("/v1/cmdb/ci-types/{ref}/attribute-groups", h.listAttributeGroups)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/attribute-groups", h.saveAttributeGroup)
	r.With(h.requireAdmin).Put("/v1/cmdb/ci-types/{ref}/attribute-groups/{id}", h.updateAttributeGroup)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-types/{ref}/attribute-groups/{id}", h.deleteAttributeGroup)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/attribute-groups/transfer", h.swapAttributeGroups)
}

func (h *Handler) listAttributeGroups(w http.ResponseWriter, r *http.Request) {
	needOther := r.URL.Query().Get("need_other") == "1" ||
		r.URL.Query().Get("need_other") == "true"
	groups, err := h.uc.ListAttributeGroups(r.Context(), chi.URLParam(r, "ref"), needOther)
	if err != nil {
		writeErr(w, err)
		return
	}
	if groups == nil {
		groups = []citypebiz.AttributeGroup{}
	}
	writeJSON(w, http.StatusOK, groups)
}

// saveGroupRequest is the POST body: a section identified by NAME.
type saveGroupRequest struct {
	Name string `json:"name"`

	// Order is optional; absent leaves an existing section's position alone.
	// cantor defaulted it to 0 and wrote it unconditionally, so a request that
	// only renamed a section also moved it to the front.
	Order *model.SmallInt `json:"order"`

	// AttrIDs is the membership in display order; positions are assigned from
	// the list index, so a caller never computes them. Absent leaves the
	// membership alone, and an empty list clears it — a distinction cantor's
	// empty list could not express.
	AttrIDs *[]model.ID `json:"attr_ids"`
}

type groupIDResponse struct {
	GroupID model.ID `json:"group_id"`
}

func (h *Handler) saveAttributeGroup(w http.ResponseWriter, r *http.Request) {
	var req saveGroupRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	id, err := h.uc.SaveAttributeGroup(r.Context(), citypebiz.AttributeGroupSaveInput{
		TypeRef:   chi.URLParam(r, "ref"),
		Name:      req.Name,
		Order:     req.Order,
		AttrOrder: req.AttrIDs,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groupIDResponse{GroupID: id})
}

// updateGroupRequest is the PUT body: a section identified by ID.
type updateGroupRequest struct {
	Name    *string         `json:"name"`
	Order   *model.SmallInt `json:"order"`
	AttrIDs *[]model.ID     `json:"attr_ids"`
}

func (h *Handler) updateAttributeGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateGroupRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	// The model is named in the URL because the section's own row carries it:
	// addressing a section through a model it does not belong to is a
	// not-found, not a silent cross-model edit.
	if err := h.uc.UpdateAttributeGroup(r.Context(), citypebiz.AttributeGroupUpdateInput{
		TypeRef:   chi.URLParam(r, "ref"),
		GroupID:   id,
		Name:      req.Name,
		Order:     req.Order,
		AttrOrder: req.AttrIDs,
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groupIDResponse{GroupID: id})
}

func (h *Handler) deleteAttributeGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteAttributeGroup(r.Context(), chi.URLParam(r, "ref"), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// swapGroupsRequest exchanges two sections' positions.
type swapGroupsRequest struct {
	FromGroupID model.ID `json:"from_group_id"`
	ToGroupID   model.ID `json:"to_group_id"`
}

func (h *Handler) swapAttributeGroups(w http.ResponseWriter, r *http.Request) {
	var req swapGroupsRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.SwapAttributeGroups(r.Context(), chi.URLParam(r, "ref"),
		req.FromGroupID, req.ToGroupID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
