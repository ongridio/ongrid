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

// registerPreferenceRoutes attaches the per-user preference and model-group
// routes.
//
// Routes:
//
//	GET    /v1/cmdb/preferences/ci-types                          (any authed)
//	GET    /v1/cmdb/preferences/model-groups                      (any authed)
//	GET    /v1/cmdb/preferences/ci-types/{ref}/fields             (any authed)
//	PUT    /v1/cmdb/preferences/ci-types/{ref}/fields             (any authed)
//	PUT    /v1/cmdb/preferences/ci-types/order                    (any authed)
//	GET    /v1/cmdb/preferences/auto-subscription                 (any authed)
//	PUT    /v1/cmdb/preferences/auto-subscription                 (any authed)
//	DELETE /v1/cmdb/preferences/auto-subscription                 (any authed)
//	POST   /v1/cmdb/model-groups                                  (admin)
//	PUT    /v1/cmdb/model-groups/{id}                             (admin)
//	DELETE /v1/cmdb/model-groups/{id}                             (admin)
//	PUT    /v1/cmdb/model-groups/order                            (admin)
//
// THE PREFERENCE ROUTES ARE NOT ADMIN-GATED, and that is the point of them: a
// subscription is one person's view of the inventory, so gating it on an
// administrative role would mean nobody but an administrator could choose which
// columns they see. cantor gated them on the caller's READ permission for the
// model, which is what the port will do once resource-level authorization
// lands — the hook is where the subscription is resolved. Nothing here can
// change what anyone else sees.
//
// THE GROUP ROUTES ARE admin-gated, because a grouping is shared: it is the
// sidebar everyone sees, not one person's arrangement.
func (h *Handler) registerPreferenceRoutes(r chi.Router) {
	r.Get("/v1/cmdb/preferences/ci-types", h.listSubscribedModels)
	r.Get("/v1/cmdb/preferences/model-groups", h.listModelGroups)
	r.Get("/v1/cmdb/preferences/ci-types/{ref}/fields", h.getShowAttributes)
	r.Put("/v1/cmdb/preferences/ci-types/{ref}/fields", h.setShowAttributes)
	r.Put("/v1/cmdb/preferences/ci-types/order", h.setSubscriptionOrder)

	r.Get("/v1/cmdb/preferences/auto-subscription", h.getAutoSubscription)
	r.Put("/v1/cmdb/preferences/auto-subscription", h.setAutoSubscription)
	r.Delete("/v1/cmdb/preferences/auto-subscription", h.clearAutoSubscription)

	r.With(h.requireAdmin).Post("/v1/cmdb/model-groups", h.createModelGroup)
	r.With(h.requireAdmin).Put("/v1/cmdb/model-groups/order", h.reorderModelGroups)
	r.With(h.requireAdmin).Put("/v1/cmdb/model-groups/{id}", h.updateModelGroup)
	r.With(h.requireAdmin).Delete("/v1/cmdb/model-groups/{id}", h.deleteModelGroup)
}

// callerID is the identity every preference read and write is scoped to.
//
// A preference belongs to one person, so the user id comes from the caller's
// identity and never from the request body: a body-supplied uid would let one
// user read or rewrite another's subscription.
func callerID(r *http.Request) (model.ID, error) {
	actor, err := actorFrom(r)
	if err != nil {
		return 0, err
	}
	return actor.UserID, nil
}

// --- subscribed fields ---

// subscribedFieldDTO is one entry in a model's visible-field subscription.
//
// A field is either a real attribute or a built-in column, and the two are
// distinguished by which half of the object is populated rather than by a
// discriminator field — which is what cantor's shape was, and what the console
// switches on.
type subscribedFieldDTO struct {
	// AttrID and Name are present for a real attribute.
	AttrID    *model.ID `json:"attr_id,omitempty"`
	Name      string    `json:"name,omitempty"`
	Alias     string    `json:"alias,omitempty"`
	ValueType string    `json:"value_type,omitempty"`
	IsIndexed *bool     `json:"is_indexed,omitempty"`

	// Builtin is the pseudo-column name, empty for a real attribute.
	//
	// No label is returned. cantor shipped English ones; user-facing text
	// belongs to the console's tr('中文','English'), and a label baked into the
	// backend is a string no translation pass can reach.
	Builtin string `json:"builtin,omitempty"`

	IsFixed bool `json:"is_fixed"`
}

func toSubscribedFieldDTO(f citypebiz.SubscribedField) subscribedFieldDTO {
	dto := subscribedFieldDTO{IsFixed: f.IsFixed, Builtin: f.Builtin}
	if f.Attribute != nil {
		id := f.Attribute.ID
		indexed := f.Attribute.IsIndexed
		dto.AttrID = &id
		dto.Name = f.Attribute.Name
		dto.Alias = f.Attribute.Alias
		dto.ValueType = f.Attribute.ValueType
		dto.IsIndexed = &indexed
	}
	return dto
}

type showAttributesDTO struct {
	TypeID       model.ID             `json:"type_id"`
	IsSubscribed bool                 `json:"is_subscribed"`
	Fields       []subscribedFieldDTO `json:"fields"`
}

func toShowAttributesDTO(in citypebiz.ShowAttributes) showAttributesDTO {
	fields := make([]subscribedFieldDTO, 0, len(in.Fields))
	for _, f := range in.Fields {
		fields = append(fields, toSubscribedFieldDTO(f))
	}
	return showAttributesDTO{TypeID: in.TypeID, IsSubscribed: in.IsSubscribed, Fields: fields}
}

func (h *Handler) getShowAttributes(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetShowAttributes(r.Context(), uid, chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toShowAttributesDTO(*got))
}

// fieldRefRequest is one field as the console sends it: a reference and whether
// it is pinned.
//
// The reference is a string so the same field can be a numeric attribute id, an
// attribute name, or a built-in column name. cantor's shape was a two-element
// array whose first element was sometimes a string and sometimes a number, and
// its id-versus-name test was a `.isdigit()` call that raised on the numeric
// form — so a JSON body with integer ids answered 500.
type fieldRefRequest struct {
	Ref     string `json:"ref"`
	IsFixed bool   `json:"is_fixed"`
}

type setShowAttributesRequest struct {
	Fields []fieldRefRequest `json:"fields"`
}

func (h *Handler) setShowAttributes(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req setShowAttributesRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	fields := make([]citypebiz.ShowAttributeInput, 0, len(req.Fields))
	for _, f := range req.Fields {
		fields = append(fields, citypebiz.ShowAttributeInput{Ref: f.Ref, IsFixed: f.IsFixed})
	}
	got, err := h.uc.SetShowAttributes(r.Context(), uid, chi.URLParam(r, "ref"), fields)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toShowAttributesDTO(*got))
}

type setOrderRequest struct {
	TypeIDs []model.ID `json:"type_ids"`
	IsTree  bool       `json:"is_tree"`
}

func (h *Handler) setSubscriptionOrder(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req setOrderRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.SetSubscriptionOrder(r.Context(), uid, citypebiz.SetSubscriptionOrderInput{
		TypeIDs: req.TypeIDs, IsTree: req.IsTree,
	}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- the sidebar ---

type modelGroupDTO struct {
	ID      model.ID     `json:"id,omitempty"`
	Name    string       `json:"name,omitempty"`
	Types   []citTypeDTO `json:"ci_types"`
	IsOther bool         `json:"is_other,omitempty"`
}

type subscribedModelsDTO struct {
	Groups  []modelGroupDTO `json:"groups"`
	TypeIDs []model.ID      `json:"type_ids"`
}

func toModelGroupDTOs(groups []citypebiz.ModelGroup) []modelGroupDTO {
	out := make([]modelGroupDTO, 0, len(groups))
	for _, g := range groups {
		types := make([]citTypeDTO, 0, len(g.Types))
		for _, v := range g.Types {
			types = append(types, toDTO(v))
		}
		out = append(out, modelGroupDTO{ID: g.ID, Name: g.Name, Types: types, IsOther: g.IsOther})
	}
	return out
}

func (h *Handler) listSubscribedModels(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	includeUngrouped := boolParam(r, "include_ungrouped", true)
	got, err := h.uc.ListSubscribedModels(r.Context(), uid, includeUngrouped)
	if err != nil {
		writeErr(w, err)
		return
	}
	ids := got.TypeIDs
	if ids == nil {
		ids = []model.ID{}
	}
	writeJSON(w, http.StatusOK, subscribedModelsDTO{
		Groups: toModelGroupDTOs(got.Groups), TypeIDs: ids,
	})
}

func (h *Handler) listModelGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.uc.ListTypeGroups(r.Context(), boolParam(r, "include_ungrouped", false))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": toModelGroupDTOs(groups), "total": len(groups)})
}

// boolParam reads a query flag, defaulting when absent.
func boolParam(r *http.Request, name string, fallback bool) bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		// cantor tested membership in its BOOL_TRUE set, so "0" was truthy. The
		// port parses, and treats an unparsable value as the default rather
		// than as false — silently inverting a flag is worse than ignoring it.
		return fallback
	}
	return v
}

// --- auto-subscription ---

type autoSubscriptionDTO struct {
	BaseStrategy string     `json:"base_strategy"`
	GroupIDs     []model.ID `json:"group_ids"`
	TypeIDs      []model.ID `json:"type_ids"`
	Enabled      bool       `json:"enabled"`
	Description  string     `json:"description,omitempty"`
}

func toAutoSubscriptionDTO(in *citypebiz.AutoSubscriptionRule) autoSubscriptionDTO {
	groups := in.GroupIDs
	if groups == nil {
		groups = []model.ID{}
	}
	types := in.TypeIDs
	if types == nil {
		types = []model.ID{}
	}
	return autoSubscriptionDTO{
		BaseStrategy: string(in.Strategy),
		GroupIDs:     groups,
		TypeIDs:      types,
		Enabled:      in.Enabled,
		Description:  in.Description,
	}
}

func (h *Handler) getAutoSubscription(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetAutoSubscription(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if got == nil {
		// No rule is a state, not an error: the console renders the manual
		// subscription instead.
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, toAutoSubscriptionDTO(got))
}

type setAutoSubscriptionRequest struct {
	BaseStrategy *string     `json:"base_strategy"`
	GroupIDs     *[]model.ID `json:"group_ids"`
	TypeIDs      *[]model.ID `json:"type_ids"`
	Enabled      *bool       `json:"enabled"`
	Description  *string     `json:"description"`
}

func (h *Handler) setAutoSubscription(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req setAutoSubscriptionRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	in := citypebiz.SetAutoSubscriptionInput{
		GroupIDs: req.GroupIDs, TypeIDs: req.TypeIDs,
		Enabled: req.Enabled, Description: req.Description,
	}
	if req.BaseStrategy != nil {
		strategy := citypebiz.SubscriptionStrategy(*req.BaseStrategy)
		in.Strategy = &strategy
	}
	got, err := h.uc.SetAutoSubscription(r.Context(), uid, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if got == nil {
		// The use case returns what it saved, so this is unreachable. Reported
		// rather than dereferenced: a nil here means the contract changed, and
		// a panic in a handler is a worse way to learn that.
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	writeJSON(w, http.StatusOK, toAutoSubscriptionDTO(got))
}

func (h *Handler) clearAutoSubscription(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.ClearAutoSubscription(r.Context(), uid); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- model groups ---

type createModelGroupRequest struct {
	Name string `json:"name"`
}

func (h *Handler) createModelGroup(w http.ResponseWriter, r *http.Request) {
	var req createModelGroupRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	group, err := h.uc.CreateTypeGroup(r.Context(), req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toModelGroupDTOs([]citypebiz.ModelGroup{*group})[0])
}

type updateModelGroupRequest struct {
	Name    *string     `json:"name"`
	TypeIDs *[]model.ID `json:"type_ids"`
}

func (h *Handler) updateModelGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateModelGroupRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	in := citypebiz.UpdateTypeGroupInput{ID: id, TypeIDs: req.TypeIDs}
	if req.Name != nil {
		in.Name = *req.Name
	}
	group, err := h.uc.UpdateTypeGroup(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toModelGroupDTOs([]citypebiz.ModelGroup{*group})[0])
}

type deleteModelGroupRequest struct {
	// TypeIDs, when non-empty, detaches only these models and keeps the group.
	TypeIDs []model.ID `json:"type_ids"`
}

func (h *Handler) deleteModelGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req deleteModelGroupRequest
	// A DELETE with no body is the common case: it removes the group.
	if r.ContentLength > 0 {
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, errs.ErrInvalid)
			return
		}
	}
	if err := h.uc.DeleteTypeGroup(r.Context(), id, req.TypeIDs); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type reorderModelGroupsRequest struct {
	GroupIDs []model.ID `json:"group_ids"`
}

func (h *Handler) reorderModelGroups(w http.ResponseWriter, r *http.Request) {
	var req reorderModelGroupsRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.ReorderTypeGroups(r.Context(), req.GroupIDs); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
