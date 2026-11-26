package citype

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// maxWriteBody bounds a model-configuration request body. Model definitions
// are small documents; anything larger is a caller bug or an attempt to make
// the server allocate.
const maxWriteBody = 64 << 10

// createRequest is the POST body for a CI model.
//
// Pointers distinguish "absent" from "zero" for the optional fields, so a
// client can send order=0 deliberately. The required fields are plain values.
type createRequest struct {
	Name             string         `json:"name"`
	Alias            string         `json:"alias"`
	UniqueKey        string         `json:"unique_key"`
	Icon             string         `json:"icon"`
	Order            model.SmallInt `json:"order"`
	DefaultOrderAttr string         `json:"default_order_attr"`
	ParentIDs        []model.ID     `json:"parent_ids"`
}

// updateRequest is the PUT body. Every field is optional: an absent field
// leaves the stored value alone, which is what lets the console edit one
// setting without resending the model.
type updateRequest struct {
	Name             *string         `json:"name"`
	Alias            *string         `json:"alias"`
	UniqueKey        *string         `json:"unique_key"`
	Icon             *string         `json:"icon"`
	Order            *model.SmallInt `json:"order"`
	DefaultOrderAttr *string         `json:"default_order_attr"`
}

type enableRequest struct {
	Enabled *bool `json:"enabled"`
}

// registerWrites attaches the mutating model-configuration routes.
//
// Every one is admin-gated, mirroring cantor's `perms_role_required(...,
// admin_name)` on the same operations. Model definitions are the vocabulary the
// rest of CMDB is built from, so changing them is an administrative act even
// though reading them is not.
func (h *Handler) registerWrites(r chi.Router) {
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types", h.create)
	r.With(h.requireAdmin).Put("/v1/cmdb/ci-types/{ref}", h.update)
	r.With(h.requireAdmin).Delete("/v1/cmdb/ci-types/{ref}", h.delete)
	r.With(h.requireAdmin).Post("/v1/cmdb/ci-types/{ref}/enable", h.setEnabled)
}

// requireAdmin 403s callers without the admin role, and 401s a request with no
// identity at all so a misconfigured route group fails closed rather than
// looking like a permissions problem.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok {
			writeErr(w, errs.ErrUnauthorized)
			return
		}
		if t.IsSuperuser || t.Role == roleAdmin {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, errs.ErrForbidden)
	})
}

// actorFrom pulls the caller identity the ownership rules need.
//
// UserID is narrowed to model.ID because every CMDB id column is MySQL int(11)
// to match cantor; a user id that does not fit is reported rather than
// truncated, since a truncated owner would attribute the write to a different
// user.
func actorFrom(r *http.Request) (citypebiz.ActorInfo, error) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		return citypebiz.ActorInfo{}, errs.ErrUnauthorized
	}
	if int64(t.UserID) > int64(maxModelID) {
		return citypebiz.ActorInfo{}, fmt.Errorf("%w: user id does not fit the CMDB id range", errs.ErrInvalid)
	}
	return citypebiz.ActorInfo{UserID: model.ID(t.UserID), IsSuperuser: t.IsSuperuser}, nil
}

// maxModelID is the largest value a CMDB id column can hold, which is the
// signed 32-bit ceiling because cantor declares them int(11).
const maxModelID = int32(2147483647)

func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// resolveRef turns a path {ref} into a CI type id, accepting either a numeric
// id or an exact name — the same dual addressing the read route supports.
func (h *Handler) resolveRef(r *http.Request) (model.ID, error) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		return 0, errs.ErrInvalid
	}
	if view, err := h.uc.GetByRef(r.Context(), ref); err != nil {
		return 0, err
	} else if view == nil {
		return 0, errs.ErrNotFound
	} else {
		return view.Type.ID, nil
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	view, err := h.uc.Create(r.Context(), citypebiz.CreateInput{
		Name: req.Name, Alias: req.Alias, UniqueKey: req.UniqueKey,
		Icon: req.Icon, Order: req.Order, DefaultOrderAttr: req.DefaultOrderAttr,
		ParentIDs: req.ParentIDs, Actor: actor.UserID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(*view))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := h.resolveRef(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	in := citypebiz.UpdateInput{
		ID: id, Actor: actor.UserID,
		Icon: req.Icon, Order: req.Order, DefaultOrderAttr: req.DefaultOrderAttr,
	}
	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.Alias != nil {
		in.Alias = *req.Alias
	}
	if req.UniqueKey != nil {
		in.UniqueKey = *req.UniqueKey
	}

	view, err := h.uc.Update(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(*view))
}

func (h *Handler) setEnabled(w http.ResponseWriter, r *http.Request) {
	id, err := h.resolveRef(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req enableRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if req.Enabled == nil {
		// cantor's enable endpoint had no body and always enabled. Requiring
		// the flag explicitly avoids a request that silently means "enable"
		// when the caller meant the opposite.
		writeErr(w, fmt.Errorf("%w: enabled is required", errs.ErrInvalid))
		return
	}
	if err := h.uc.SetEnabled(r.Context(), id, *req.Enabled); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := h.resolveRef(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.Delete(r.Context(), id, actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
