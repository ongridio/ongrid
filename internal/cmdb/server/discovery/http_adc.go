package discovery

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerADC attaches the discovery pool.
//
// Routes:
//
//	GET    /v1/cmdb/discovery/adc                         (any authed)
//	POST   /v1/cmdb/discovery/adc                         (any authed) — an agent report
//	GET    /v1/cmdb/discovery/adc/counter                 (any authed)
//	GET    /v1/cmdb/discovery/adc/ci-types                (any authed)
//	GET    /v1/cmdb/discovery/adc/ci-types/{ref}/attributes (any authed)
//	GET    /v1/cmdb/discovery/adc/exec-histories          (any authed)
//	POST   /v1/cmdb/discovery/adc/exec-histories          (any authed) — an agent log line
//	DELETE /v1/cmdb/discovery/adc/exec-histories/{typeID} (any authed) — clear a model's log
//	GET    /v1/cmdb/discovery/adc/{id}                    (any authed)
//	DELETE /v1/cmdb/discovery/adc/{id}                    (any authed)
//	PUT    /v1/cmdb/discovery/adc/{id}/accept             (any authed)
//
// THE STATIC SEGMENTS ARE REGISTERED FIRST, so chi resolves `/adc/ci-types`
// before it ever considers `/adc/{id}`. The route test pins that.
//
// THE POOL IS NOT ADMIN-GATED, which is cantor's shape: an agent has no
// administrator role, and an accept is the act of admitting a machine the
// deployment's own collector reported. The destructive clear of a log is the
// one route that would benefit from the ACL this port does not have; it is
// recorded as a gap rather than invented as a role check.
func (h *Handler) registerADC(r chi.Router) {
	r.Get("/v1/cmdb/discovery/adc", h.listPool)
	r.Post("/v1/cmdb/discovery/adc", h.upsertPool)
	r.Get("/v1/cmdb/discovery/adc/counter", h.poolCounter)
	r.Get("/v1/cmdb/discovery/adc/ci-types", h.listPoolCITypes)
	r.Get("/v1/cmdb/discovery/adc/ci-types/{ref}/attributes", h.poolAttributes)
	r.Get("/v1/cmdb/discovery/adc/exec-histories", h.listExecHistories)
	r.Post("/v1/cmdb/discovery/adc/exec-histories", h.addExecHistory)
	r.Delete("/v1/cmdb/discovery/adc/exec-histories/{typeID}", h.clearExecHistories)

	r.Get("/v1/cmdb/discovery/adc/{id}", h.getPool)
	r.Delete("/v1/cmdb/discovery/adc/{id}", h.deletePool)
	r.Put("/v1/cmdb/discovery/adc/{id}/accept", h.acceptPool)
}

// poolBody is one agent report.
//
// THE DECODER DOES NOT USE json.Number HERE, unlike the rule and target bodies:
// the store merges the report against the JSON it read back, and a number that
// decodes differently from the one the database returned would make every sync
// look like a change and reset every acceptance.
type poolBody struct {
	TypeID       model.ID       `json:"type_id"`
	AdtID        model.ID       `json:"adt_id"`
	UniqueValue  string         `json:"unique_value"`
	Instance     map[string]any `json:"instance"`
	OneagentID   string         `json:"oneagent_id"`
	OneagentName string         `json:"oneagent_name"`
}

func decodePoolBody(r *http.Request) (discoverybiz.PoolWrite, error) {
	var body poolBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	if err := dec.Decode(&body); err != nil {
		return discoverybiz.PoolWrite{}, errs.ErrInvalid
	}
	return discoverybiz.PoolWrite{
		TypeID: body.TypeID, AdtID: body.AdtID, UniqueValue: body.UniqueValue,
		Instance: body.Instance, OneagentID: body.OneagentID, OneagentName: body.OneagentName,
	}, nil
}

func (h *Handler) listPool(w http.ResponseWriter, r *http.Request) {
	typeID, err := queryID(r, "type_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	page, size := pageParams(r)
	filter := discoverybiz.PoolFilter{
		TypeID: typeID, Page: page, PageSize: size,
		UniqueValue: r.URL.Query().Get("unique_value"),
		OneagentID:  r.URL.Query().Get("oneagent_id"),
	}
	if raw := r.URL.Query().Get("is_accept"); raw != "" {
		accepted, err := strconv.ParseBool(raw)
		if err != nil {
			writeErr(w, errs.ErrInvalid)
			return
		}
		filter.IsAccept = &accepted
	}
	got, err := h.uc.ListPool(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) getPool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetPool(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) upsertPool(w http.ResponseWriter, r *http.Request) {
	in, err := decodePoolBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.UpsertPool(r.Context(), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) deletePool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeletePool(r.Context(), id, actor); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"adc_id": id})
}

func (h *Handler) acceptPool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.AcceptPool(r.Context(), id, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The entry travels with the id so the console can update the row without
	// a second read; `adc_id` keeps cantor's shape for an existing caller.
	writeJSON(w, http.StatusOK, map[string]any{"adc_id": id, "item": got})
}

func (h *Handler) poolCounter(w http.ResponseWriter, r *http.Request) {
	typeID, err := queryID(r, "type_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.PoolCounter(r.Context(), typeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listPoolCITypes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListTargetTypes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) poolAttributes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListPoolAttributes(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) listExecHistories(w http.ResponseWriter, r *http.Request) {
	typeID, err := queryID(r, "type_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	page, size := pageParams(r)
	got, err := h.uc.ListExecHistories(r.Context(), typeID, page, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// execBody is one log line from an agent.
type execBody struct {
	TypeID model.ID `json:"type_id"`
	Stdout string   `json:"stdout"`
}

func (h *Handler) addExecHistory(w http.ResponseWriter, r *http.Request) {
	var body execBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.AddExecHistory(r.Context(), body.TypeID, body.Stdout); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200})
}

func (h *Handler) clearExecHistories(w http.ResponseWriter, r *http.Request) {
	typeID, err := pathID(r, "typeID")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.ClearExecHistories(r.Context(), typeID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200})
}

// queryID reads a required positive id from the query string.
func queryID(r *http.Request, name string) (model.ID, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, errs.ErrInvalid
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return 0, errs.ErrInvalid
	}
	return model.ID(n), nil
}
