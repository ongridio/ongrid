package citype

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerRelationRoutes attaches the model-relation routes.
//
// Routes:
//
//	GET    /v1/cmdb/ci-type-relations                       (any authed)
//	GET    /v1/cmdb/ci-type-relations/path                  (any authed)
//	GET    /v1/cmdb/ci-types/{ref}/relation-children        (any authed)
//	GET    /v1/cmdb/ci-types/{ref}/relation-parents         (any authed)
//	GET    /v1/cmdb/ci-types/{ref}/relation-descendants     (any authed)
//	POST   /v1/cmdb/ci-types/{ref}/relation-children/{child} (admin)
//	DELETE /v1/cmdb/ci-types/{ref}/relation-children/{child} (admin)
//	DELETE /v1/cmdb/ci-type-relations/{id}                  (admin)
//
// The reads are open and the writes are admin-gated, which follows cantor: its
// model-relation list carried the console's page-permission decorator, but the
// per-model children and parents reads carried none, and the resource-search
// tree — which expands descendants and finds paths — is a search surface rather
// than a configuration one.
//
// The relation between two models is addressed by the PAIR, because the pair is
// its natural key: a model pair has at most one relation. The id route exists
// for callers that already hold the id from a read, which is what the `ctr_id`
// in every rendered edge is for.
func (h *Handler) registerRelationRoutes(r chi.Router) {
	r.Get("/v1/cmdb/ci-type-relations", h.listTypeRelations)
	r.Get("/v1/cmdb/ci-type-relations/path", h.findTypePaths)
	r.Get("/v1/cmdb/ci-types/{ref}/relation-children", h.listRelationChildren)
	r.Get("/v1/cmdb/ci-types/{ref}/relation-parents", h.listRelationParents)
	r.Get("/v1/cmdb/ci-types/{ref}/relation-descendants", h.listRelationDescendants)

	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/relation-children/{child}", h.saveTypeRelation)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-types/{ref}/relation-children/{child}", h.deleteTypeRelationByPair)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-type-relations/{id}", h.deleteTypeRelation)
}

// relationTypeRefDTO is the relation-type dictionary entry an edge points at.
type relationTypeRefDTO struct {
	ID    model.ID `json:"id"`
	Name  string   `json:"name"`
	Color string   `json:"color"`
}

// relatedTypeDTO is one end of a model relation.
type relatedTypeDTO struct {
	CTRID      model.ID      `json:"ctr_id"`
	Type       citTypeDTO    `json:"type"`
	ShowKey    string        `json:"show_key,omitempty"`
	Attributes []typeAttrDTO `json:"attributes"`

	RelationType relationTypeRefDTO `json:"relation_type"`
	Constraint   string             `json:"constraint"`

	ParentAttrIDs []model.ID `json:"parent_attr_ids"`
	ChildAttrIDs  []model.ID `json:"child_attr_ids"`
}

// typeAttrDTO is the wire shape of one attribute in a model's effective set.
//
// It mirrors the attribute-management DTO because the console renders both in
// the same fields; keeping one shape for "an attribute on a model" is what
// stops the relation page and the attribute page disagreeing about a name.
type typeAttrDTO struct {
	ID        model.ID `json:"id"`
	Name      string   `json:"name"`
	Alias     string   `json:"alias"`
	ValueType string   `json:"value_type"`
	IsIndexed bool     `json:"is_indexed"`

	IsRequired  bool     `json:"is_required"`
	Order       model.ID `json:"order"`
	DefaultShow bool     `json:"default_show"`

	Inherited     bool   `json:"inherited"`
	InheritedFrom string `json:"inherited_from,omitempty"`
}

func toTypeAttrDTO(a citypebiz.TypeAttribute) typeAttrDTO {
	return typeAttrDTO{
		ID: a.ID, Name: a.Name, Alias: a.Alias, ValueType: a.ValueType,
		IsIndexed:     a.IsIndexed,
		IsRequired:    a.IsRequired,
		Order:         a.Order,
		DefaultShow:   a.DefaultShow,
		Inherited:     a.Inherited,
		InheritedFrom: a.InheritedFrom,
	}
}

func toTypeAttrDTOs(in []citypebiz.TypeAttribute) []typeAttrDTO {
	out := make([]typeAttrDTO, 0, len(in))
	for _, a := range in {
		out = append(out, toTypeAttrDTO(a))
	}
	return out
}

func toRelatedTypeDTO(r citypebiz.RelatedType) relatedTypeDTO {
	parentAttrs := r.ParentAttrIDs
	if parentAttrs == nil {
		parentAttrs = []model.ID{}
	}
	childAttrs := r.ChildAttrIDs
	if childAttrs == nil {
		childAttrs = []model.ID{}
	}
	return relatedTypeDTO{
		CTRID:      r.CTRID,
		Type:       toDTO(r.Type),
		ShowKey:    r.ShowKey,
		Attributes: toTypeAttrDTOs(r.Attributes),
		RelationType: relationTypeRefDTO{
			ID: r.RelationType.ID, Name: r.RelationType.Name, Color: r.RelationType.Color,
		},
		Constraint:    r.Constraint,
		ParentAttrIDs: parentAttrs,
		ChildAttrIDs:  childAttrs,
	}
}

// relationDTO is one model-level relation.
type relationDTO struct {
	ID     model.ID       `json:"id"`
	Parent relatedTypeDTO `json:"parent"`
	Child  relatedTypeDTO `json:"child"`
}

type relationListResponse struct {
	Items []relationDTO `json:"items"`
	Total int           `json:"total"`
}

func parseTypeIDs(raw string) ([]model.ID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxIDList {
		return nil, errs.ErrInvalid
	}
	out := make([]model.ID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil || n <= 0 {
			return nil, errs.ErrInvalid
		}
		out = append(out, model.ID(n))
	}
	return out, nil
}

func (h *Handler) listTypeRelations(w http.ResponseWriter, r *http.Request) {
	ids, err := parseTypeIDs(r.URL.Query().Get("type_ids"))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := h.uc.ListTypeRelations(r.Context(), citypebiz.TypeRelationFilter{TypeIDs: ids})
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]relationDTO, 0, len(rows))
	for _, rel := range rows {
		items = append(items, relationDTO{
			ID:     rel.ID,
			Parent: toRelatedTypeDTO(rel.Parent),
			Child:  toRelatedTypeDTO(rel.Child),
		})
	}
	writeJSON(w, http.StatusOK, relationListResponse{Items: items, Total: len(items)})
}

func (h *Handler) listRelationChildren(w http.ResponseWriter, r *http.Request) {
	h.listRelated(w, r, true)
}

func (h *Handler) listRelationParents(w http.ResponseWriter, r *http.Request) {
	h.listRelated(w, r, false)
}

func (h *Handler) listRelated(w http.ResponseWriter, r *http.Request, children bool) {
	rows, err := h.uc.ListRelatedTypes(r.Context(), chi.URLParam(r, "ref"), children)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]relatedTypeDTO, 0, len(rows))
	for _, rel := range rows {
		items = append(items, toRelatedTypeDTO(rel))
	}
	// One shape for both directions, with the direction named rather than
	// implied by which key the response carries.
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// levelDTO is one depth of the descendant tree.
type levelDTO struct {
	Level int          `json:"level"`
	Types []citTypeDTO `json:"types"`
}

func (h *Handler) listRelationDescendants(w http.ResponseWriter, r *http.Request) {
	levels, err := h.uc.ListTypeDescendants(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Ordered levels rather than cantor's level-keyed object: a JSON object's
	// key order is not defined, so a caller rendering "level 1, then level 2"
	// would have had to sort the keys back into order itself.
	out := make([]levelDTO, 0, len(levels))
	for _, lv := range levels {
		types, err := h.uc.TypesByID(r.Context(), lv.Types)
		if err != nil {
			writeErr(w, err)
			return
		}
		dtos := make([]citTypeDTO, 0, len(types))
		for _, ct := range types {
			dtos = append(dtos, toDTO(ct))
		}
		out = append(out, levelDTO{Level: lv.Level, Types: dtos})
	}
	writeJSON(w, http.StatusOK, map[string]any{"levels": out})
}

func (h *Handler) findTypePaths(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := strings.TrimSpace(q.Get("source_type_id"))
	if source == "" {
		writeErr(w, errs.ErrInvalid)
		return
	}
	targets := splitRefs(q.Get("target_type_ids"))
	if len(targets) == 0 {
		writeErr(w, errs.ErrInvalid)
		return
	}
	paths, err := h.uc.FindTypePaths(r.Context(), source, targets)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
}

func splitRefs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) > maxIDList {
		return nil
	}
	return out
}

// saveRelationRequest is the POST body for an edge.
type saveRelationRequest struct {
	RelationTypeID model.ID `json:"relation_type_id"`
	Constraint     string   `json:"constraint"`

	ParentAttrIDs []model.ID `json:"parent_attr_ids"`
	ChildAttrIDs  []model.ID `json:"child_attr_ids"`
}

func (h *Handler) saveTypeRelation(w http.ResponseWriter, r *http.Request) {
	var req saveRelationRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	saved, err := h.uc.SaveTypeRelation(r.Context(), citypebiz.TypeRelationInput{
		ParentRef:      chi.URLParam(r, "ref"),
		ChildRef:       chi.URLParam(r, "child"),
		RelationTypeID: req.RelationTypeID,
		Constraint:     req.Constraint,
		ParentAttrIDs:  req.ParentAttrIDs,
		ChildAttrIDs:   req.ChildAttrIDs,
		Actor:          actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, relationDTO{
		ID:     saved.ID,
		Parent: toRelatedTypeDTO(saved.Parent),
		Child:  toRelatedTypeDTO(saved.Child),
	})
}

func (h *Handler) deleteTypeRelationByPair(w http.ResponseWriter, r *http.Request) {
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteTypeRelationByPair(r.Context(),
		chi.URLParam(r, "ref"), chi.URLParam(r, "child"), actor.UserID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteTypeRelation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteTypeRelation(r.Context(), id, actor.UserID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
