package citype

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerServiceTreeRoutes attaches the service-tree and saved-search routes.
//
// Routes:
//
//	GET    /v1/cmdb/preferences/service-trees                (any authed)
//	POST   /v1/cmdb/preferences/service-trees                (admin)
//	PUT    /v1/cmdb/preferences/service-trees/{id}           (admin)
//	DELETE /v1/cmdb/preferences/service-trees/{id}           (admin)
//	GET    /v1/cmdb/preferences/saved-searches               (any authed)
//	POST   /v1/cmdb/preferences/saved-searches               (any authed)
//	PUT    /v1/cmdb/preferences/saved-searches/{id}          (any authed)
//	DELETE /v1/cmdb/preferences/saved-searches/{id}          (any authed)
//	GET    /v1/cmdb/ci-type-relations/can-edit               (any authed)
//
// A service tree is a page DEFINITION, so shaping it is administrative — that
// is what cantor's Service_Tree_Definition role gate meant. A saved search is
// something a person does to their own searches while looking for things, so
// those routes are not gated and are scoped to the caller by id and ownership.
func (h *Handler) registerServiceTreeRoutes(r chi.Router) {
	r.Get("/v1/cmdb/preferences/service-trees", h.listServiceTrees)
	r.With(h.requireAdmin).Post("/v1/cmdb/preferences/service-trees", h.saveServiceTree)
	r.With(h.requireAdmin).Put("/v1/cmdb/preferences/service-trees/{id}", h.updateServiceTree)
	r.With(h.requireAdmin).Delete("/v1/cmdb/preferences/service-trees/{id}", h.deleteServiceTree)

	r.Get("/v1/cmdb/preferences/saved-searches", h.listSavedSearches)
	r.Post("/v1/cmdb/preferences/saved-searches", h.createSavedSearch)
	r.Put("/v1/cmdb/preferences/saved-searches/{id}", h.updateSavedSearch)
	r.Delete("/v1/cmdb/preferences/saved-searches/{id}", h.deleteSavedSearch)

	r.Get("/v1/cmdb/ci-type-relations/can-edit", h.canEditRelation)
}

// --- service trees ---

// typePairDTO is one step of a definition.
type typePairDTO struct {
	ParentID model.ID `json:"parent_id"`
	ChildID  model.ID `json:"child_id"`
}

// serviceTreeDTO is a definition and its rendering.
//
// The stored pairs are returned alongside the rendering because they are what
// an edit sends back: the rendering is derived, and a console that edited the
// derivation would be editing a cache.
type serviceTreeDTO struct {
	ID       model.ID       `json:"id"`
	Name     string         `json:"name"`
	IsPublic bool           `json:"is_public"`
	Option   map[string]any `json:"option,omitempty"`

	Pairs []typePairDTO `json:"cr_ids"`

	Tree serviceTreeRenderDTO `json:"tree"`
}

// serviceTreeRenderDTO is the derived shape a console draws.
type serviceTreeRenderDTO struct {
	// Order is the definition's models in dependency order, parents first.
	Order []model.ID `json:"topo_flatten"`

	// Levels groups them by dependency depth.
	Levels [][]model.ID `json:"topo"`

	// Leaf is the bottom of the tree.
	Leaf []model.ID `json:"leaf"`

	// LevelConstraints maps a step to the cardinality between that step's model
	// and the next. Keys are numbers, which JSON object keys can only be as
	// strings — so it is a LIST of steps rather than an object, and the order
	// is the order.
	LevelConstraints []levelConstraintDTO `json:"level2constraint"`

	LeafShowTypes []leafShowDTO `json:"leaf2show_types"`
	NodeShowTypes []leafShowDTO `json:"node2show_types"`

	// ShowTypes is every model the tree will contain instances of.
	ShowTypes []model.ID `json:"show_types"`
}

type levelConstraintDTO struct {
	Level      int    `json:"level"`
	Constraint string `json:"constraint"`
}

type leafShowDTO struct {
	TypeID    model.ID   `json:"type_id"`
	ShowTypes []model.ID `json:"show_types"`
}

func toServiceTreeDTO(in citypebiz.ServiceTreeDefinition) serviceTreeDTO {
	pairs := make([]typePairDTO, 0, len(in.Pairs))
	for _, p := range in.Pairs {
		pairs = append(pairs, typePairDTO{ParentID: p.ParentID, ChildID: p.ChildID})
	}
	dto := serviceTreeDTO{
		ID: in.ID, Name: in.Name, IsPublic: in.IsPublic, Option: in.Option, Pairs: pairs,
	}
	if in.Tree == nil {
		dto.Tree = emptyServiceTreeRender()
		return dto
	}
	tree := in.Tree
	dto.Tree = serviceTreeRenderDTO{
		Order:            emptyIDs(tree.Order),
		Levels:           emptyLevels(tree.Levels),
		Leaf:             emptyIDs(tree.Leaf),
		LevelConstraints: renderLevelConstraints(tree),
		LeafShowTypes:    renderLeafShow(tree.LeafShowTypes),
		NodeShowTypes:    renderLeafShow(tree.NodeShowTypes),
		ShowTypes:        emptyIDs(tree.ShowTypes),
	}
	return dto
}

func emptyServiceTreeRender() serviceTreeRenderDTO {
	return serviceTreeRenderDTO{
		Order: []model.ID{}, Levels: [][]model.ID{}, Leaf: []model.ID{},
		LevelConstraints: []levelConstraintDTO{},
		LeafShowTypes:    []leafShowDTO{},
		NodeShowTypes:    []leafShowDTO{},
		ShowTypes:        []model.ID{},
	}
}

func emptyIDs(ids []model.ID) []model.ID {
	if ids == nil {
		return []model.ID{}
	}
	return ids
}

func emptyLevels(levels [][]model.ID) [][]model.ID {
	if levels == nil {
		return [][]model.ID{}
	}
	return levels
}

func renderLevelConstraints(tree *citypebiz.ServiceTree) []levelConstraintDTO {
	// A list rather than a JSON object: the keys are numbers, and a JSON
	// object's keys are strings in no defined order, so a caller rendering the
	// steps in sequence would have had to sort them back. cantor shipped the
	// object and the console did exactly that.
	out := make([]levelConstraintDTO, 0, len(tree.LevelConstraints))
	for level := 1; level <= len(tree.Order); level++ {
		if c, ok := tree.LevelConstraints[level]; ok {
			out = append(out, levelConstraintDTO{Level: level, Constraint: c})
		}
	}
	return out
}

func renderLeafShow(byID map[model.ID][]model.ID) []leafShowDTO {
	ids := make([]model.ID, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	// Sorted so the rendering does not shuffle between two identical requests.
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	out := make([]leafShowDTO, 0, len(ids))
	for _, id := range ids {
		out = append(out, leafShowDTO{TypeID: id, ShowTypes: emptyIDs(byID[id])})
	}
	return out
}

type serviceTreeListResponse struct {
	Items []serviceTreeDTO `json:"items"`
	Total int              `json:"total"`
}

func (h *Handler) listServiceTrees(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// include_all is what the model-management page needs and what the resource
	// menu must not ask for: a service tree is a page, and a user should not be
	// handed the shape of one they cannot open.
	includeAll := boolParam(r, "include_all", false)
	defs, err := h.uc.ListServiceTrees(r.Context(), uid, includeAll)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]serviceTreeDTO, 0, len(defs))
	for _, d := range defs {
		items = append(items, toServiceTreeDTO(d))
	}
	writeJSON(w, http.StatusOK, serviceTreeListResponse{Items: items, Total: len(items)})
}

// serviceTreeRequest is the POST and PUT body.
type serviceTreeRequest struct {
	Name     string         `json:"name"`
	CRIDs    []typePairDTO  `json:"cr_ids"`
	IsPublic bool           `json:"is_public"`
	Option   map[string]any `json:"option"`
}

func toTypePairs(in []typePairDTO) []citypemodel.TypePair {
	out := make([]citypemodel.TypePair, 0, len(in))
	for _, p := range in {
		out = append(out, citypemodel.TypePair{ParentID: p.ParentID, ChildID: p.ChildID})
	}
	return out
}

func (h *Handler) saveServiceTree(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req serviceTreeRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.SaveServiceTree(r.Context(), uid, citypebiz.ServiceTreeInput{
		Name: req.Name, Pairs: toTypePairs(req.CRIDs), IsPublic: req.IsPublic, Option: req.Option,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toServiceTreeDTO(*got))
}

func (h *Handler) updateServiceTree(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req serviceTreeRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.SaveServiceTree(r.Context(), uid, citypebiz.ServiceTreeInput{
		ID: id, Name: req.Name, Pairs: toTypePairs(req.CRIDs),
		IsPublic: req.IsPublic, Option: req.Option,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toServiceTreeDTO(*got))
}

// deleteServiceTree removes one definition BY ID.
//
// cantor's route declared a path id and then read a NAME from the body, so the
// id was ignored and every definition sharing that name was removed — across
// users, because its lookup had no owner filter either.
func (h *Handler) deleteServiceTree(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteServiceTree(r.Context(), uid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// canEditRelation reports whether a relation between two models may be created.
func (h *Handler) canEditRelation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parent, err := positiveQueryID(q.Get("parent_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	child, err := positiveQueryID(q.Get("child_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ok, err := h.uc.CanEditRelation(r.Context(), parent, child)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"result": ok})
}

// --- saved searches ---

type savedSearchDTO struct {
	ID     model.ID       `json:"id"`
	Name   string         `json:"name"`
	Option map[string]any `json:"option,omitempty"`
	PRVID  *model.ID      `json:"prv_id,omitempty"`
	PTVID  *model.ID      `json:"ptv_id,omitempty"`
	TypeID *model.ID      `json:"type_id,omitempty"`
}

func toSavedSearchDTO(in citypebiz.SavedSearch) savedSearchDTO {
	return savedSearchDTO{
		ID: in.ID, Name: in.Name, Option: in.Option,
		PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
	}
}

type savedSearchListResponse struct {
	Items []savedSearchDTO `json:"items"`
	Total int              `json:"total"`
}

func (h *Handler) listSavedSearches(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	prvID, err := optionalQueryID(q.Get("prv_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ptvID, err := optionalQueryID(q.Get("ptv_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	typeID, err := optionalQueryID(q.Get("type_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := h.uc.ListSearchOptions(r.Context(), uid, citypebiz.SearchOptionFilter{
		Name: q.Get("name"), PRVID: prvID, PTVID: ptvID, TypeID: typeID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]savedSearchDTO, 0, len(rows))
	for _, x := range rows {
		items = append(items, toSavedSearchDTO(x))
	}
	writeJSON(w, http.StatusOK, savedSearchListResponse{Items: items, Total: len(items)})
}

type savedSearchRequest struct {
	Name   string         `json:"name"`
	Option map[string]any `json:"option"`
	PRVID  *model.ID      `json:"prv_id"`
	PTVID  *model.ID      `json:"ptv_id"`
	TypeID *model.ID      `json:"type_id"`
}

func (h *Handler) createSavedSearch(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req savedSearchRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.CreateSearchOption(r.Context(), uid, citypebiz.SearchOptionInput{
		Name: req.Name, Option: req.Option, PRVID: req.PRVID, PTVID: req.PTVID, TypeID: req.TypeID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSavedSearchDTO(*got))
}

func (h *Handler) updateSavedSearch(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req savedSearchRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	got, err := h.uc.UpdateSearchOption(r.Context(), uid, id, citypebiz.SearchOptionInput{
		Name: req.Name, Option: req.Option, PRVID: req.PRVID, PTVID: req.PTVID, TypeID: req.TypeID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSavedSearchDTO(*got))
}

func (h *Handler) deleteSavedSearch(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteSearchOption(r.Context(), uid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
