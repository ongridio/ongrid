package citype

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The usecase's attribute views carry their own JSON tags and ARE the wire
// shape. Unlike the CI-type view — which exists only so the DTO layer can
// decide the field names — these projections already drop the one field that
// must never be echoed (the choice webhook) and add the two derived answers
// callers actually need (whether the values are indexed, and where they live).
// Restating them as DTOs would duplicate that decision in two places and make
// it possible for the copy to drift back into echoing a credential.

// registerAttributeRoutes attaches the attribute-management routes.
//
// Routes:
//
//	GET    /v1/cmdb/attributes                    (any authed) — dictionary page
//	GET    /v1/cmdb/attributes/{ref}              (any authed) — one, by id/name/alias
//	POST   /v1/cmdb/attributes                    (admin)
//	PUT    /v1/cmdb/attributes/{id}               (admin)
//	DELETE /v1/cmdb/attributes/{id}               (admin)
//	GET    /v1/cmdb/ci-types/{ref}/attributes     (any authed) — effective set
//	POST   /v1/cmdb/ci-types/{ref}/attributes     (admin) — bind
//	PUT    /v1/cmdb/ci-types/{ref}/attributes     (admin) — per-binding settings
//	DELETE /v1/cmdb/ci-types/{ref}/attributes     (admin) — unbind, DESTRUCTIVE
//	POST   /v1/cmdb/ci-types/{ref}/attributes/transfer (admin)
//
// Writes are admin-gated, which is stricter than cantor in one specific place
// worth naming: cantor also let an attribute's CREATOR edit or delete it
// without the configuration role. That exemption belongs to the CMDB
// resource-level authorization work, which this port has not built; requiring
// the role cannot permit anything cantor would have refused.
func (h *Handler) registerAttributeRoutes(r chi.Router) {
	r.Get("/v1/cmdb/attributes", h.listAttributes)
	r.Get("/v1/cmdb/attributes/{ref}", h.getAttribute)
	r.With(h.requireAdmin).Post("/v1/cmdb/attributes", h.createAttribute)
	r.With(h.requireAdmin).Put("/v1/cmdb/attributes/{id}", h.updateAttribute)
	r.With(h.requireAdmin).Delete("/v1/cmdb/attributes/{id}", h.deleteAttribute)

	r.Get("/v1/cmdb/ci-types/{ref}/attributes", h.typeAttributes)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/attributes", h.bindAttributes)
	r.With(h.requireAdmin).Put("/v1/cmdb/ci-types/{ref}/attributes", h.updateBindings)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-types/{ref}/attributes", h.unbindAttributes)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/attributes/transfer", h.transferAttribute)
}

// attributeListResponse is the {items,total} shape used throughout Ongrid.
type attributeListResponse struct {
	Items []citypebiz.Attribute `json:"items"`
	Total int64                 `json:"total"`
}

// choiceValueDTO is one enumerable attribute value.
type choiceValueDTO struct {
	Value  any            `json:"value"`
	Option map[string]any `json:"option"`
}

func toChoiceValues(in []choiceValueDTO) []citypebiz.ChoiceValue {
	if in == nil {
		return nil
	}
	out := make([]citypebiz.ChoiceValue, 0, len(in))
	for _, v := range in {
		out = append(out, citypebiz.ChoiceValue{Value: v.Value, Option: v.Option})
	}
	return out
}

// attributeCreateRequest is the POST /attributes body.
type attributeCreateRequest struct {
	Name      string `json:"name"`
	Alias     string `json:"alias"`
	ValueType string `json:"value_type"`

	IsList     bool `json:"is_list"`
	IsUnique   bool `json:"is_unique"`
	IsIndex    bool `json:"is_index"`
	IsLink     bool `json:"is_link"`
	IsPassword bool `json:"is_password"`
	IsSortable bool `json:"is_sortable"`
	IsDynamic  bool `json:"is_dynamic"`
	IsBool     bool `json:"is_bool"`
	IsFile     bool `json:"is_file"`

	IsReference     bool      `json:"is_reference"`
	ReferenceTypeID *model.ID `json:"reference_type_id"`

	// Default accepts either a bare value or cantor's {"default": ...}
	// envelope; the usecase normalises it.
	Default any `json:"default"`

	IsComputed    bool    `json:"is_computed"`
	ComputeExpr   *string `json:"compute_expr"`
	ComputeScript *string `json:"compute_script"`

	ChoiceValue   []choiceValueDTO `json:"choice_value"`
	ChoiceWebHook map[string]any   `json:"choice_web_hook"`
	ChoiceOther   map[string]any   `json:"choice_other"`

	ReCheck *string        `json:"re_check"`
	Option  map[string]any `json:"option"`
}

// attributeUpdateRequest is the PUT /attributes/{id} body. Every field is
// optional and a nil pointer means "leave unchanged".
//
// The pointers are the whole point. cantor's update compared each field of the
// request against the stored row with a lookup that yielded nil when the field
// was absent, so an omitted field always "differed" — which made value_type
// mandatory on every update and made a request that only renamed an attribute
// delete its enumerated values. Making absence explicit is what fixes both.
type attributeUpdateRequest struct {
	Name  *string `json:"name"`
	Alias *string `json:"alias"`

	ValueType *string `json:"value_type"`

	IsList     *bool `json:"is_list"`
	IsUnique   *bool `json:"is_unique"`
	IsIndex    *bool `json:"is_index"`
	IsLink     *bool `json:"is_link"`
	IsPassword *bool `json:"is_password"`
	IsSortable *bool `json:"is_sortable"`
	IsDynamic  *bool `json:"is_dynamic"`
	IsBool     *bool `json:"is_bool"`
	IsFile     *bool `json:"is_file"`

	IsReference     *bool     `json:"is_reference"`
	ReferenceTypeID *model.ID `json:"reference_type_id"`

	// Default is a raw value, not a map: a client may send either a bare
	// default or cantor's {"default": ...} envelope, and null to clear it.
	Default       *any    `json:"default"`
	IsComputed    *bool   `json:"is_computed"`
	ComputeExpr   *string `json:"compute_expr"`
	ComputeScript *string `json:"compute_script"`

	ChoiceValue   *[]choiceValueDTO `json:"choice_value"`
	ChoiceWebHook *map[string]any   `json:"choice_web_hook"`
	ChoiceOther   *map[string]any   `json:"choice_other"`

	ReCheck *string         `json:"re_check"`
	Option  *map[string]any `json:"option"`
}

func (h *Handler) listAttributes(w http.ResponseWriter, r *http.Request) {
	f, err := parseAttributeFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	items, total, err := h.uc.ListAttributes(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	if items == nil {
		items = []citypebiz.Attribute{}
	}
	writeJSON(w, http.StatusOK, attributeListResponse{Items: items, Total: total})
}

// parseAttributeFilter reads ?name=, ?alias=, ?page= and ?page_size=.
//
// cantor used page/page_size here, unlike the id-list style its other endpoints
// used, so the port keeps those parameter names for this resource and converts
// them to the internal limit/offset. The page size is clamped by the usecase
// rather than trusted: cantor took whatever it was given and paged in Python,
// so an unbounded page was a memory amplification the caller controlled.
func parseAttributeFilter(r *http.Request) (citypebiz.AttributeFilter, error) {
	q := r.URL.Query()
	var f citypebiz.AttributeFilter
	f.NameLike = strings.TrimSpace(q.Get("name"))
	f.AliasLike = strings.TrimSpace(q.Get("alias"))

	page := int64(1)
	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			return f, fmt.Errorf("%w: page", errs.ErrInvalid)
		}
		page = n
	}
	size := int64(citypebiz.DefaultAttributePageSize)
	if raw := strings.TrimSpace(q.Get("page_size")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			return f, fmt.Errorf("%w: page_size", errs.ErrInvalid)
		}
		size = n
	}
	if size > citypebiz.MaxAttributePageSize {
		size = citypebiz.MaxAttributePageSize
	}

	offset := (page - 1) * size
	if offset > int64(maxModelID) {
		return f, fmt.Errorf("%w: page", errs.ErrInvalid)
	}
	f.Limit = int(size)
	f.Offset = int(offset)
	return f, nil
}

func (h *Handler) getAttribute(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	view, err := h.uc.GetAttribute(r.Context(), ref)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) createAttribute(w http.ResponseWriter, r *http.Request) {
	var req attributeCreateRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	view, err := h.uc.CreateAttribute(r.Context(), citypebiz.CreateAttributeInput{
		Name: req.Name, Alias: req.Alias, ValueType: req.ValueType,
		IsList: req.IsList, IsUnique: req.IsUnique, IsIndex: req.IsIndex,
		IsLink: req.IsLink, IsPassword: req.IsPassword, IsSortable: req.IsSortable,
		IsDynamic: req.IsDynamic, IsBool: req.IsBool, IsFile: req.IsFile,
		IsReference:     req.IsReference,
		ReferenceTypeID: req.ReferenceTypeID,
		Default:         req.Default,
		IsComputed:      req.IsComputed,
		ComputeExpr:     req.ComputeExpr,
		ComputeScript:   req.ComputeScript,
		ChoiceValue:     toChoiceValues(req.ChoiceValue),
		ChoiceWebHook:   req.ChoiceWebHook,
		ChoiceOther:     req.ChoiceOther,
		ReCheck:         req.ReCheck,
		Option:          req.Option,
		Actor:           actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (h *Handler) updateAttribute(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req attributeUpdateRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	in := citypebiz.UpdateAttributeInput{
		ID: id, Actor: actor.UserID, ActorIsSuperuser: actor.IsSuperuser,
		Name: req.Name, Alias: req.Alias, ValueType: req.ValueType,
		IsList: req.IsList, IsUnique: req.IsUnique, IsIndex: req.IsIndex,
		IsLink: req.IsLink, IsPassword: req.IsPassword, IsSortable: req.IsSortable,
		IsDynamic: req.IsDynamic, IsBool: req.IsBool, IsFile: req.IsFile,
		IsReference:     req.IsReference,
		ReferenceTypeID: req.ReferenceTypeID,
		IsComputed:      req.IsComputed,
		ComputeExpr:     req.ComputeExpr,
		ComputeScript:   req.ComputeScript,
		ChoiceWebHook:   req.ChoiceWebHook,
		ChoiceOther:     req.ChoiceOther,
		ReCheck:         req.ReCheck,
		Option:          req.Option,
	}
	if req.Default != nil {
		normalised := citypebiz.NormalizeDefault(*req.Default)
		in.Default = &normalised
	}
	if req.ChoiceValue != nil {
		values := toChoiceValues(*req.ChoiceValue)
		in.ChoiceValue = &values
	}

	view, err := h.uc.UpdateAttribute(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) deleteAttribute(w http.ResponseWriter, r *http.Request) {
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
	if err := h.uc.DeleteAttribute(r.Context(), id, actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- effective attribute set for a model ---

func (h *Handler) typeAttributes(w http.ResponseWriter, r *http.Request) {
	view, err := h.uc.ListTypeAttributes(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// bindRequest is the POST /ci-types/{ref}/attributes body.
type bindRequest struct {
	AttrIDs []model.ID `json:"attr_ids"`

	Order       *model.ID `json:"order"`
	IsRequired  *bool     `json:"is_required"`
	DefaultShow *bool     `json:"default_show"`
}

func (h *Handler) bindAttributes(w http.ResponseWriter, r *http.Request) {
	var req bindRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	bound, err := h.uc.BindAttributes(r.Context(), citypebiz.BindAttributesInput{
		TypeRef:     chi.URLParam(r, "ref"),
		AttrIDs:     req.AttrIDs,
		Order:       req.Order,
		IsRequired:  req.IsRequired,
		DefaultShow: req.DefaultShow,
		Actor:       actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if bound == nil {
		bound = []model.ID{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bound": bound})
}

// bindingChangeDTO is one element of the PUT /ci-types/{ref}/attributes body.
type bindingChangeDTO struct {
	AttrID      model.ID  `json:"attr_id"`
	Order       *model.ID `json:"order"`
	IsRequired  *bool     `json:"is_required"`
	DefaultShow *bool     `json:"default_show"`
}

type updateBindingsRequest struct {
	Attributes []bindingChangeDTO `json:"attributes"`
}

func (h *Handler) updateBindings(w http.ResponseWriter, r *http.Request) {
	var req updateBindingsRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	changes := make([]citypebiz.BindingChange, 0, len(req.Attributes))
	for _, c := range req.Attributes {
		changes = append(changes, citypebiz.BindingChange{
			AttrID:      c.AttrID,
			Order:       c.Order,
			IsRequired:  c.IsRequired,
			DefaultShow: c.DefaultShow,
		})
	}
	res, err := h.uc.UpdateBindings(r.Context(), citypebiz.UpdateBindingsInput{
		TypeRef: chi.URLParam(r, "ref"),
		Changes: changes,
		Actor:   actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type unbindRequest struct {
	AttrIDs []model.ID `json:"attr_ids"`
}

// unbindAttributes removes attributes from a model and erases their values.
//
// The response reports what happened rather than echoing the request. cantor
// echoed the request list, which said nothing about whether the values had
// gone, which models had lost the attribute, or how much data was destroyed.
func (h *Handler) unbindAttributes(w http.ResponseWriter, r *http.Request) {
	var req unbindRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.uc.UnbindAttributes(r.Context(), citypebiz.UnbindAttributesInput{
		TypeRef: chi.URLParam(r, "ref"),
		AttrIDs: req.AttrIDs,
		Actor:   actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// transferAttributeRequest moves one attribute between form sections.
type transferAttributeRequest struct {
	AttrID model.ID `json:"attr_id"`

	// FromGroupID and ToGroupID are absent for "not in any section".
	FromGroupID *model.ID `json:"from_group_id"`
	ToGroupID   *model.ID `json:"to_group_id"`

	Order model.SmallInt `json:"order"`
}

func (h *Handler) transferAttribute(w http.ResponseWriter, r *http.Request) {
	var req transferAttributeRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	err := h.uc.MoveAttribute(r.Context(), citypebiz.AttributeMoveInput{
		TypeRef:     chi.URLParam(r, "ref"),
		AttrID:      req.AttrID,
		FromGroupID: req.FromGroupID,
		ToGroupID:   req.ToGroupID,
		Order:       req.Order,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathID reads a positive numeric path parameter.
func pathID(r *http.Request, name string) (model.ID, error) {
	raw := chi.URLParam(r, name)
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s", errs.ErrInvalid, name)
	}
	return model.ID(n), nil
}

// positiveQueryID reads a required positive numeric query parameter.
func positiveQueryID(raw string) (model.ID, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil || n <= 0 {
		return 0, errs.ErrInvalid
	}
	return model.ID(n), nil
}

// optionalQueryID reads an optional positive numeric query parameter, where an
// absent or zero value means "any".
func optionalQueryID(raw string) (model.ID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		return 0, errs.ErrInvalid
	}
	return model.ID(n), nil
}
