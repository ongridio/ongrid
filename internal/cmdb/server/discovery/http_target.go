package discovery

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerTargets attaches the target, relation and agent-poll routes.
//
// Routes:
//
//	GET    /v1/cmdb/discovery/ci-types/{ref}/targets               (any authed)
//	POST   /v1/cmdb/discovery/ci-types/{ref}/targets               (any authed; see below)
//	GET    /v1/cmdb/discovery/ci-types/{ref}/discoverable-attributes (any authed)
//	GET    /v1/cmdb/discovery/ci-types/{ref}/target-relations      (any authed)
//	PUT    /v1/cmdb/discovery/ci-types/{ref}/target-relations      (any authed)
//	GET    /v1/cmdb/discovery/targets/{id}                         (any authed)
//	PUT    /v1/cmdb/discovery/targets/{id}                         (any authed; see below)
//	DELETE /v1/cmdb/discovery/targets/{id}                         (any authed)
//	POST   /v1/cmdb/discovery/targets/{id}/test                    (any authed) — 501
//	GET    /v1/cmdb/discovery/targets/{id}/sync-histories          (any authed)
//	POST   /v1/cmdb/discovery/rules/builtin                        (admin)
//	GET    /v1/cmdb/discovery/sync                                 (admin) — the agent's poll
//
// THE TARGET WRITES ARE NOT ADMIN-GATED, with ONE exception the use case
// enforces: a target that names neither an agent nor an expression runs on every
// collector, and creating or keeping one is an administrative act. That is
// cantor's rule, and it is the only authorization cantor had here that this port
// can express — see ErrTargetScopingUnenforced for the half that needs the
// resource-level authorization this port has not built.
//
// THE SYNC ROUTE IS ADMIN-GATED FOR NOW, and that is a PLACEHOLDER: cantor
// admitted a dedicated agent account (`cmdb_agent`) alongside `worker` and
// `admin`, and this deployment has no agent identity at all. Gating it on the
// deployment's administrator is stricter than cantor and wrong for a real
// collector, which would have to be given an administrator's credential. It
// changes when the edge agent is wired to pull from here, and until then the
// endpoint exists so the contract is settled.
func (h *Handler) registerTargets(r chi.Router) {
	r.Get("/v1/cmdb/discovery/ci-types/{ref}/targets", h.listTargets)
	r.Post("/v1/cmdb/discovery/ci-types/{ref}/targets", h.createTarget)
	r.Get("/v1/cmdb/discovery/ci-types/{ref}/discoverable-attributes", h.discoverableAttributes)
	r.Get("/v1/cmdb/discovery/ci-types/{ref}/target-relations", h.listTargetRelations)
	r.Put("/v1/cmdb/discovery/ci-types/{ref}/target-relations", h.replaceTargetRelations)

	r.Get("/v1/cmdb/discovery/targets/{id}", h.getTarget)
	r.Put("/v1/cmdb/discovery/targets/{id}", h.updateTarget)
	r.Delete("/v1/cmdb/discovery/targets/{id}", h.deleteTarget)
	r.Post("/v1/cmdb/discovery/targets/{id}/test", h.testTarget)
	r.Get("/v1/cmdb/discovery/targets/{id}/sync-histories", h.listSyncHistories)

	r.Post("/v1/cmdb/discovery/rules/builtin", h.ensureBuiltinRules)
	r.Get("/v1/cmdb/discovery/sync", h.sync)
}

// targetBody is a target's write body.
//
// THE MAPPINGS AND THE CREDENTIAL BAG ARE POINTERS to a map, so that an absent
// `attributes` (a write that is not about the mapping) is distinguishable from
// `"attributes": {}` (a write that clears it). A plain map field cannot tell
// those apart, and treating the second as the first would make a mapping
// impossible to clear.
type targetBody struct {
	AdrID *model.ID `json:"adr_id"`

	Attributes  *map[string]any `json:"attributes"`
	ExtraOption *map[string]any `json:"extra_option"`

	AutoAccept *bool   `json:"auto_accept"`
	AgentID    *string `json:"agent_id"`
	QueryExpr  *string `json:"query_expr"`
	Cron       *string `json:"cron"`
	Interval   *int32  `json:"interval"`
	Enabled    *bool   `json:"enabled"`
}

func decodeTarget(r *http.Request) (discoverybiz.TargetInput, error) {
	var body targetBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return discoverybiz.TargetInput{}, errs.ErrInvalid
		}
		return discoverybiz.TargetInput{}, errs.ErrInvalid
	}
	in := discoverybiz.TargetInput{
		AdrID:      body.AdrID,
		AutoAccept: body.AutoAccept,
		AgentID:    body.AgentID,
		QueryExpr:  body.QueryExpr,
		Cron:       body.Cron,
		Interval:   body.Interval,
		Enabled:    body.Enabled,
	}
	if body.Attributes != nil {
		in.Attributes, in.SetAttributes = *body.Attributes, true
	}
	if body.ExtraOption != nil {
		in.ExtraOption, in.SetExtraOption = *body.ExtraOption, true
	}
	return in, nil
}

// --- targets ---

func (h *Handler) listTargets(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListTargets(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

func (h *Handler) getTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetTarget(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) createTarget(w http.ResponseWriter, r *http.Request) {
	in, err := decodeTarget(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.CreateTarget(r.Context(), chi.URLParam(r, "ref"), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (h *Handler) updateTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	in, err := decodeTarget(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.UpdateTarget(r.Context(), id, in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) deleteTarget(w http.ResponseWriter, r *http.Request) {
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
	got, err := h.uc.DeleteTarget(r.Context(), id, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// testTarget answers 501.
//
// CANTOR ANSWERED WITH THREE HARD-CODED LINES. Its `GET /adt/{id}/test` returned
// `stdout="1\n2\n3"` without dispatching anything, and its POST returned a fresh
// uuid and ran nothing — so a "test" told an operator that their credentials
// worked when nothing had been tried. This port does not run a probe either (the
// collection is an agent's job and the agent pulls its targets), so the honest
// answer is that the test is not implemented rather than an invented success.
func (h *Handler) testTarget(w http.ResponseWriter, r *http.Request) {
	if _, err := pathID(r, "id"); err != nil {
		writeErr(w, err)
		return
	}
	writeErr(w, discoverybiz.ErrDiscoverTestUnsupported)
}

func (h *Handler) discoverableAttributes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ADAttributes(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- relations ---

func (h *Handler) listTargetRelations(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListTargetRelations(r.Context(), chi.URLParam(r, "ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

// relationListBody is the replace body: the model's whole relation set.
type relationListBody struct {
	Relations []struct {
		AdKey      string   `json:"ad_key"`
		PeerTypeID model.ID `json:"peer_type_id"`
		PeerAttrID model.ID `json:"peer_attr_id"`
	} `json:"relations"`
}

func (h *Handler) replaceTargetRelations(w http.ResponseWriter, r *http.Request) {
	var body relationListBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	in := make([]discoverybiz.TargetRelationWrite, 0, len(body.Relations))
	for _, item := range body.Relations {
		in = append(in, discoverybiz.TargetRelationWrite{
			AdKey: item.AdKey, PeerTypeID: item.PeerTypeID, PeerAttrID: item.PeerAttrID,
		})
	}
	got, err := h.uc.ReplaceTargetRelations(r.Context(), chi.URLParam(r, "ref"), in, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- the handover log ---

func (h *Handler) listSyncHistories(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	page, size := pageParams(r)
	got, err := h.uc.ListSyncHistories(r.Context(), id, page, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- models that have targets ---

// listTargetTypes feeds the discovery-pool tree. It returns only models with at
// least one target, with the label and icon the console draws.
func (h *Handler) listTargetTypes(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.ListTargetTypes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": got, "total": len(got)})
}

// --- the built-in rules ---

func (h *Handler) ensureBuiltinRules(w http.ResponseWriter, r *http.Request) {
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !actor.IsAdmin {
		writeErr(w, errs.ErrForbidden)
		return
	}
	got, err := h.uc.EnsureBuiltinRules(r.Context(), actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- the agent's poll ---

func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	actor, err := actorOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !actor.IsAdmin {
		writeErr(w, errs.ErrForbidden)
		return
	}
	query := r.URL.Query()
	got, err := h.uc.Sync(r.Context(), discoverybiz.SyncRequest{
		CIID:         parseOptionalID(query.Get("ci_id")),
		OneagentID:   query.Get("oneagent_id"),
		OneagentName: query.Get("oneagent_name"),
		LastUpdateAt: query.Get("last_update_at"),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// parseOptionalID reads an id a caller may not have sent.
func parseOptionalID(raw string) model.ID {
	id, ok := parsePositiveID(raw)
	if !ok {
		return 0
	}
	return id
}

func parsePositiveID(raw string) (model.ID, bool) {
	if raw == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(raw[i]-'0')
		if n > maxModelID {
			return 0, false
		}
	}
	if n <= 0 {
		return 0, false
	}
	return model.ID(n), true
}
