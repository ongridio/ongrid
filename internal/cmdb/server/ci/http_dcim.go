package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerDCIM attaches the data-centre routes.
//
// Routes:
//
//	GET    /v1/cmdb/dcim/tree-view                                  (any authed)
//	POST   /v1/cmdb/dcim/{type}                                     (any authed) — create a node
//	PUT    /v1/cmdb/dcim/{type}/{id}                                (any authed) — write node values
//	DELETE /v1/cmdb/dcim/{type}/{id}                                (any authed) — remove a node
//	GET    /v1/cmdb/dcim/server-rooms/{id}/racks                    (any authed) — a room's racks
//	POST   /v1/cmdb/dcim/server-rooms/{id}/racks/{rackID}           (any authed) — mount a device
//	PUT    /v1/cmdb/dcim/server-rooms/{id}/racks/{rackID}/devices/{deviceID}
//	                                                                (any authed) — reposition or migrate
//	DELETE /v1/cmdb/dcim/server-rooms/{id}/racks/{rackID}/devices/{deviceID}
//	                                                                (any authed) — unmount
//	POST   /v1/cmdb/dcim/racks/recalc-units                         (any authed) — recompute capacities
//	GET    /v1/cmdb/dcim/history/operations                         (any authed) — the operation log
//
// THEY LIVE IN THIS HANDLER RATHER THAN ONE OF THEIR OWN because they are built
// on the same use case as everything else here — instances, edges and the model
// vocabulary — and because an instance row is rendered the same way whichever
// endpoint returned it. A second handler package would either import this one's
// renderer or grow a second one, and the second is how the two shapes drift
// apart.
//
// NOT ADMIN-GATED, like the instance writes: putting a server in a rack is the
// same kind of work as editing one. cantor gated the whole module on a DCIM
// role; resource-level authorization lands here when it lands.
func (h *Handler) registerDCIM(r chi.Router) {
	r.Get("/v1/cmdb/dcim/tree-view", h.dcimTree)
	r.Post("/v1/cmdb/dcim/racks/recalc-units", h.dcimRecalcUnits)
	r.Get("/v1/cmdb/dcim/history/operations", h.dcimHistory)

	r.Get("/v1/cmdb/dcim/server-rooms/{roomID}/racks", h.dcimRoomRacks)
	r.Post("/v1/cmdb/dcim/server-rooms/{roomID}/racks/{rackID}/devices/{deviceID}", h.dcimMountDevice)
	r.Put("/v1/cmdb/dcim/server-rooms/{roomID}/racks/{rackID}/devices/{deviceID}", h.dcimMoveDevice)
	r.Delete("/v1/cmdb/dcim/server-rooms/{roomID}/racks/{rackID}/devices/{deviceID}", h.dcimUnmountDevice)

	r.Post("/v1/cmdb/dcim/{type}", h.dcimCreateNode)
	r.Put("/v1/cmdb/dcim/{type}/{id}", h.dcimUpdateNode)
	r.Delete("/v1/cmdb/dcim/{type}/{id}", h.dcimDeleteNode)
}

// --- the tree ---

func (h *Handler) dcimTree(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.DCIMTree(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- nodes ---

// dcimNodeBody is a node's write body.
//
// IT IS FLAT, like an instance write: the keys are attribute names, because
// that is what the console's form produces and what the instance endpoints
// already take. `parent_id` is the one reserved key, and it is reserved by the
// vocabulary rather than by this handler — see the model package's built-in
// keywords.
type dcimNodeBody map[string]any

func decodeNodeBody(r *http.Request) (dcimNodeBody, error) {
	var body dcimNodeBody
	if r.Body == nil {
		return dcimNodeBody{}, nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return dcimNodeBody{}, nil
		}
		return nil, errs.ErrInvalid
	}
	return body, nil
}

// nodeParentID pulls `parent_id` out of a node's body.
func nodeParentID(body dcimNodeBody) (model.ID, error) {
	raw, ok := body["parent_id"]
	if !ok || raw == nil {
		return 0, nil
	}
	delete(body, "parent_id")
	switch v := raw.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil || n <= 0 || n > int64(maxModelID) {
			return 0, fmt.Errorf("%w: parent_id", errs.ErrInvalid)
		}
		return model.ID(n), nil
	case string:
		var n int64
		if _, err := fmt.Sscan(strings.TrimSpace(v), &n); err != nil || n <= 0 || n > int64(maxModelID) {
			return 0, fmt.Errorf("%w: parent_id", errs.ErrInvalid)
		}
		return model.ID(n), nil
	}
	return 0, fmt.Errorf("%w: parent_id", errs.ErrInvalid)
}

func (h *Handler) dcimCreateNode(w http.ResponseWriter, r *http.Request) {
	nodeType := chi.URLParam(r, "type")
	body, err := decodeNodeBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	parentID, err := nodeParentID(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}

	got, err := h.uc.CreateDCIMNode(r.Context(), cibiz.DCIMNodeInput{
		Type: nodeType, ParentID: parentID, Values: body, Audit: audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ci_id": got.CIID})
}

func (h *Handler) dcimUpdateNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	body, err := decodeNodeBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// A node's container is not an attribute and cannot be changed by writing
	// one; a migration between rooms or data centres is a different operation,
	// and one this module does not offer. The key is consumed rather than
	// passed through, so it cannot be silently written as an attribute that
	// happens to share the name.
	if _, err := nodeParentID(body); err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}

	got, err := h.uc.UpdateDCIMNode(r.Context(), chi.URLParam(r, "type"), id, cibiz.WriteRequest{
		Values: body, ExistPolicy: cibiz.ExistReplace, Audit: audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ci_id": got.CIID})
}

func (h *Handler) dcimDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteDCIMNode(r.Context(), chi.URLParam(r, "type"), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- a room's racks ---

func (h *Handler) dcimRoomRacks(w http.ResponseWriter, r *http.Request) {
	roomID, err := pathID(r, "roomID")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ServerRoomRacks(r.Context(), roomID, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		writeErr(w, err)
		return
	}
	body := searchResponse(got.Page)
	body["counter"] = got.Counter
	writeJSON(w, http.StatusOK, body)
}

// --- rack contents ---

// dcimDeviceBody is the mount and reposition body.
type dcimDeviceBody struct {
	// Start is the unit to place the device at, counting from 1.
	Start int32 `json:"u_start"`

	// Units is the height to record. A pointer, because "not mentioned" and
	// "zero" are different requests: one leaves the device's height alone, the
	// other is a value the arithmetic reads as the default.
	Units *int32 `json:"u_count"`

	// ToStart is the unit to reposition the device to, for the PUT.
	ToStart *int32 `json:"to_u_start"`

	// ToRackID is the rack to migrate the device to, for the PUT.
	ToRackID model.ID `json:"to_rack_id"`
}

func decodeDeviceBody(r *http.Request) (dcimDeviceBody, error) {
	var body dcimDeviceBody
	if r.Body == nil {
		return body, nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return body, nil
		}
		return body, errs.ErrInvalid
	}
	return body, nil
}

func (h *Handler) dcimMountDevice(w http.ResponseWriter, r *http.Request) {
	rackID, deviceID, audit, body, err := h.dcimDeviceArgs(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.AddDeviceToRack(r.Context(), cibiz.RackDeviceInput{
		RackID: rackID, DeviceID: deviceID, Start: body.Start, Units: body.Units, Audit: audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rackChangeBody(rackID, deviceID, got))
}

// dcimMoveDevice repositions a device, or migrates it when `to_rack_id` names a
// different rack.
//
// ONE ENDPOINT FOR BOTH, where cantor had two under different prefixes and the
// console called whichever the dialog it opened happened to use. The request
// carries the destination, so the destination decides.
func (h *Handler) dcimMoveDevice(w http.ResponseWriter, r *http.Request) {
	rackID, deviceID, audit, body, err := h.dcimDeviceArgs(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	start := body.Start
	if body.ToStart != nil {
		start = *body.ToStart
	}

	if body.ToRackID != 0 && body.ToRackID != rackID {
		got, err := h.uc.MigrateDevice(r.Context(), cibiz.RackMigrationInput{
			FromRackID: rackID, ToRackID: body.ToRackID, DeviceID: deviceID,
			Start: start, Units: body.Units, Audit: audit,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rackChangeBody(rackID, deviceID, got))
		return
	}

	got, err := h.uc.MoveDeviceInRack(r.Context(), cibiz.RackDeviceInput{
		RackID: rackID, DeviceID: deviceID, Start: start, Units: body.Units, Audit: audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rackChangeBody(rackID, deviceID, got))
}

func (h *Handler) dcimUnmountDevice(w http.ResponseWriter, r *http.Request) {
	rackID, deviceID, audit, _, err := h.dcimDeviceArgs(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.RemoveDeviceFromRack(r.Context(), cibiz.RackDeviceInput{
		RackID: rackID, DeviceID: deviceID, Audit: audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rackChangeBody(rackID, deviceID, got))
}

// dcimDeviceArgs reads the addressing and the body shared by the three device
// endpoints.
//
// `roomID` is part of the path because the console navigates by room and the
// rack is what it selects within one; it is not used for the write, and a rack
// that is not in that room is not an error the request can express. It is
// carried so the URLs match the shape of the page they come from.
func (h *Handler) dcimDeviceArgs(r *http.Request) (model.ID, model.ID, cibiz.AuditInfo, dcimDeviceBody, error) {
	var empty dcimDeviceBody
	if _, err := pathID(r, "roomID"); err != nil {
		return 0, 0, cibiz.AuditInfo{}, empty, err
	}
	rackID, err := pathID(r, "rackID")
	if err != nil {
		return 0, 0, cibiz.AuditInfo{}, empty, err
	}
	deviceID, err := pathID(r, "deviceID")
	if err != nil {
		return 0, 0, cibiz.AuditInfo{}, empty, err
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		return 0, 0, cibiz.AuditInfo{}, empty, err
	}
	body, err := decodeDeviceBody(r)
	if err != nil {
		return 0, 0, cibiz.AuditInfo{}, empty, err
	}
	return rackID, deviceID, audit, body, nil
}

// rackChangeBody renders what a rack mutation produced.
func rackChangeBody(rackID, deviceID model.ID, got *cibiz.RackChangeResult) map[string]any {
	body := map[string]any{
		"rack_id":      rackID,
		"ci_id":        deviceID,
		"u_count":      got.Units,
		"free_u_count": got.FreeUnits,
	}
	if got.ToFreeChanged {
		body["to_free_u_count"] = got.ToFreeUnits
	}
	return body
}

// --- the recompute and the log ---

func (h *Handler) dcimRecalcUnits(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.RecalcRackUnits(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) dcimHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := positiveIntParam(q.Get("page"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	size, err := positiveIntParam(q.Get("count"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	if size == 0 {
		if size, err = positiveIntParam(q.Get("page_size"), 0); err != nil {
			writeErr(w, err)
			return
		}
	}
	rackID, err := positiveIntParam(q.Get("rack_id"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}

	// `operate_type` is a list in cantor's spelling; a single value is a list
	// of one.
	var kinds []string
	for _, raw := range strings.Split(q.Get("operate_type"), ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			kinds = append(kinds, raw)
		}
	}

	got, err := h.uc.RackHistory(r.Context(), cibiz.RackHistoryQuery{
		RackID: model.ID(rackID), OperateTypes: kinds, Page: page, Count: size,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
