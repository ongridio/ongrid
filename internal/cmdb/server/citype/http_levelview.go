package citype

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerLevelViews attaches the hierarchy level-view routes.
//
// Routes:
//
//	GET /v1/cmdb/preferences/tree-levels        (any authed) — the caller's views
//	PUT /v1/cmdb/preferences/ci-types/{ref}/tree-levels (any authed) — set or clear
//
// NOT ADMIN-GATED, like the other preference routes. A level view is "which
// columns this person's hierarchy shows", which is a view of the resource tree
// rather than a change to the model everyone shares — the same distinction the
// show-attribute subscription draws. cantor asked for READ on the model only
// when the level list was non-empty; here the model is read to resolve the
// levels at all, so the requirement is satisfied by the time the write happens.
func (h *Handler) registerLevelViews(r chi.Router) {
	r.Get("/v1/cmdb/preferences/tree-levels", h.listTreeLevels)
	r.Put("/v1/cmdb/preferences/ci-types/{ref}/tree-levels", h.setTreeLevels)
}

func (h *Handler) listTreeLevels(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := h.uc.ListTreeLevels(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	// A bare list, which is what cantor returned and what the console iterates.
	writeJSON(w, http.StatusOK, items)
}

// treeLevelsRequest is the write body.
//
// `levels` is a list of attribute references — ids, names or aliases — in
// hierarchy order. An empty list clears the view; that is how a
// create-or-update endpoint expresses "stop customising this model".
type treeLevelsRequest struct {
	Levels []json.RawMessage `json:"levels"`
}

func (h *Handler) setTreeLevels(w http.ResponseWriter, r *http.Request) {
	uid, err := callerID(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	var body treeLevelsRequest
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}

	// Each entry is read as a RAW value and rendered to text, because the
	// console sends names while a scripted client sends ids and a JSON body may
	// carry either as a string or as a number. cantor read the same parameter
	// through its form handler, where a number arrived as text, and a port that
	// accepted only strings would reject half of what the console already
	// sends.
	levels := make([]string, 0, len(body.Levels))
	for _, raw := range body.Levels {
		level, err := levelRef(raw)
		if err != nil {
			writeErr(w, err)
			return
		}
		levels = append(levels, level)
	}

	item, err := h.uc.SetTreeLevels(r.Context(), uid, chi.URLParam(r, "ref"), levels)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// levelRef renders one level entry as the reference text the resolver expects.
func levelRef(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), nil
	}
	return "", errs.ErrInvalid
}
