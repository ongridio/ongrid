package citype

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerRuleRoutes attaches the unique-constraint and trigger routes.
//
// Routes:
//
//	GET    /v1/cmdb/ci-types/{ref}/unique-constraints              (admin)
//	POST   /v1/cmdb/ci-types/{ref}/unique-constraints              (admin)
//	PUT    /v1/cmdb/ci-types/{ref}/unique-constraints/{id}         (admin)
//	DELETE /v1/cmdb/ci-types/{ref}/unique-constraints/{id}         (admin)
//	GET    /v1/cmdb/ci-types/{ref}/triggers                        (admin)
//	POST   /v1/cmdb/ci-types/{ref}/triggers                        (admin)
//	PUT    /v1/cmdb/ci-types/{ref}/triggers/{id}                   (admin)
//	DELETE /v1/cmdb/ci-types/{ref}/triggers/{id}                   (admin)
//	POST   /v1/cmdb/ci-types/{ref}/triggers/{id}/test              (admin) — 501
//
// THE READS ARE ADMIN-GATED TOO, which is deliberate and does not match the
// attribute routes. cantor gated these two on CONFIG as well, and for a reason:
// a trigger's option document names the endpoints the product calls out to and
// the filters it matches on, which is deployment configuration rather than
// something a reader of the model needs.
//
// Every write names the model in the path because the constraint or trigger
// belongs to one. cantor's PUT and DELETE took only the row id and used the path
// segment solely to resolve the permission, so a caller authorised for one model
// could rewrite or delete another's rule; here a mismatch is a 404.
func (h *Handler) registerRuleRoutes(r chi.Router) {
	r.With(h.requireAdmin).Get("/v1/cmdb/ci-types/{ref}/unique-constraints", h.listConstraints)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/unique-constraints", h.createConstraint)
	r.With(h.requireAdmin).Put("/v1/cmdb/ci-types/{ref}/unique-constraints/{id}", h.updateConstraint)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-types/{ref}/unique-constraints/{id}", h.deleteConstraint)

	r.With(h.requireAdmin).Get("/v1/cmdb/ci-types/{ref}/triggers", h.listTriggers)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/triggers", h.createTrigger)
	r.With(h.requireAdmin).Put("/v1/cmdb/ci-types/{ref}/triggers/{id}", h.updateTrigger)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-types/{ref}/triggers/{id}", h.deleteTrigger)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/triggers/{id}/test", h.testTrigger)
}

// constraintListResponse is the {items,total} shape used throughout Ongrid.
type constraintListResponse struct {
	Items []citypebiz.UniqueConstraint `json:"items"`
	Total int                          `json:"total"`
}

// constraintRequest is the POST and PUT body. Both take the whole attribute
// set, because a constraint IS its attribute set — there is no partial edit.
type constraintRequest struct {
	AttrIDs []model.ID `json:"attr_ids"`
}

func (h *Handler) listConstraints(w http.ResponseWriter, r *http.Request) {
	items, err := h.uc.ListUniqueConstraints(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if items == nil {
		items = []citypebiz.UniqueConstraint{}
	}
	writeJSON(w, http.StatusOK, constraintListResponse{Items: items, Total: len(items)})
}

func (h *Handler) createConstraint(w http.ResponseWriter, r *http.Request) {
	var req constraintRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := h.uc.CreateUniqueConstraint(r.Context(), chi.URLParam(r, "ref"), req.AttrIDs, actor.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, constraintListResponse{Items: items, Total: len(items)})
}

func (h *Handler) updateConstraint(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req constraintRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := h.uc.UpdateUniqueConstraint(r.Context(), chi.URLParam(r, "ref"), id, req.AttrIDs, actor.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, constraintListResponse{Items: items, Total: len(items)})
}

func (h *Handler) deleteConstraint(w http.ResponseWriter, r *http.Request) {
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
	if err := h.uc.DeleteUniqueConstraint(r.Context(), chi.URLParam(r, "ref"), id, actor.UserID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// triggerListResponse is the {items,total} shape.
type triggerListResponse struct {
	Items []citypebiz.Trigger `json:"items"`
	Total int                 `json:"total"`
}

// triggerRequest is the POST and PUT body.
//
// Option is decoded as an open-ended document rather than a typed struct: it
// is an adopted database's JSON, and a struct would silently drop any key a
// later cantor added the next time the trigger is edited.
type triggerRequest struct {
	AttrID *model.ID      `json:"attr_id"`
	Option map[string]any `json:"option"`
}

func (h *Handler) listTriggers(w http.ResponseWriter, r *http.Request) {
	items, err := h.uc.ListTriggers(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if items == nil {
		items = []citypebiz.Trigger{}
	}
	writeJSON(w, http.StatusOK, triggerListResponse{Items: items, Total: len(items)})
}

func (h *Handler) createTrigger(w http.ResponseWriter, r *http.Request) {
	var req triggerRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	view, err := h.uc.CreateTrigger(r.Context(), chi.URLParam(r, "ref"), citypebiz.TriggerInput{
		AttrID: req.AttrID, Option: req.Option, Actor: actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (h *Handler) updateTrigger(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req triggerRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	view, err := h.uc.UpdateTrigger(r.Context(), chi.URLParam(r, "ref"), id, citypebiz.TriggerInput{
		AttrID: req.AttrID, Option: req.Option, Actor: actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) deleteTrigger(w http.ResponseWriter, r *http.Request) {
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
	if err := h.uc.DeleteTrigger(r.Context(), chi.URLParam(r, "ref"), id, actor.UserID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testTrigger reports that dispatch is not implemented.
//
// cantor's route fired the trigger's webhook or notification immediately. The
// port has neither sender nor the CI hook that decides when a trigger fires, so
// the route answers 501 with a slug rather than 200 with nothing sent — an
// operator told "test sent" would spend the afternoon debugging their webhook
// endpoint instead of the missing implementation.
func (h *Handler) testTrigger(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.TestTrigger(r.Context(), chi.URLParam(r, "ref"), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
