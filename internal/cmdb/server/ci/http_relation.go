package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerRelations attaches the relation routes.
//
// Routes:
//
//	GET    /v1/cmdb/ci-relations/search                  (any authed) — a hierarchy page
//	GET    /v1/cmdb/ci-relations/search/full             (any authed) — the whole tree
//	GET    /v1/cmdb/ci-relations/statistics              (any authed) — descendant counts
//	POST   /v1/cmdb/ci-relations/path/s                  (any authed) — enumerate paths
//	GET    /v1/cmdb/ci-relations/{id}/children           (any authed) — one hop down
//	GET    /v1/cmdb/ci-relations/{id}/parents            (any authed) — one hop up
//	POST   /v1/cmdb/ci-relations/batch                   (any authed) — relate many
//	POST   /v1/cmdb/ci-relations/{first}/{second}        (any authed) — relate two
//	DELETE /v1/cmdb/ci-relations/{first}/{second}        (any authed) — un-relate two
//	DELETE /v1/cmdb/ci-relations/{id}                    (any authed) — remove an edge
//
// NOT ADMIN-GATED, like the instance writes: relating two hosts is the same kind
// of work as adding one. cantor gated these on the caller's ADD or DELETE
// permission for the MODEL PAIR (a resource of its own, `acl_resource_name`), so
// resource-level authorization lands here — the hook is the model-pair
// resolution in Usecase.AddRelation.
//
// BATCH DELETE IS NOT IMPLEMENTED. cantor had a route for it that the console
// never calls, and its version took only `parents` — no `children`, unlike the
// add — and was not atomic. Adding a route with those properties to satisfy an
// unused path would be worse than leaving it out; the single-pair delete covers
// what the console does.
func (h *Handler) registerRelations(r chi.Router) {
	r.Get("/v1/cmdb/ci-relations/search", h.searchHierarchy)
	r.Get("/v1/cmdb/ci-relations/search/full", h.fullTree)
	r.Get("/v1/cmdb/ci-relations/statistics", h.relationStatistics)
	r.Post("/v1/cmdb/ci-relations/path/s", h.searchPaths)
	r.Get("/v1/cmdb/ci-relations/{id}/children", h.listChildren)
	r.Get("/v1/cmdb/ci-relations/{id}/parents", h.listParents)
	r.Post("/v1/cmdb/ci-relations/batch", h.batchRelate)
	r.Post("/v1/cmdb/ci-relations/{first}/{second}", h.addRelation)
	r.Delete("/v1/cmdb/ci-relations/{first}/{second}", h.deleteRelationBetween)
	r.Delete("/v1/cmdb/ci-relations/{id}", h.deleteRelation)
}

// --- reading ---

// searchHierarchy walks a hierarchy and returns the instances at the requested
// levels, as a CI search result.
func (h *Handler) searchHierarchy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	roots, err := idListParam(q.Get("root_ids"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(roots) == 0 {
		// cantor spelled it `root_id` singular on this route and the DCIM page
		// still does; the plural is the same parameter with a list.
		roots, err = idListParam(q.Get("root_id"))
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	if len(roots) == 0 {
		writeErr(w, fmt.Errorf("%w: root_id", errs.ErrInvalid))
		return
	}

	levels, err := levelParams(q.Get("level"))
	if err != nil {
		writeErr(w, err)
		return
	}
	search, err := readSearchParams(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	page, err := h.uc.WalkHierarchy(r.Context(), cibiz.HierarchyRequest{
		RootIDs:     roots,
		Levels:      levels,
		Reverse:     boolParam(q.Get("reverse")),
		AncestorIDs: strings.TrimSpace(q.Get("ancestor_ids")),
		Search: cibiz.SearchRequest{
			Query: search.query, Sort: search.sort, Fields: search.fields,
			Facets: search.facets, RetKey: search.key, Page: search.page, Count: search.count,
		},
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	body := searchResponse(page.SearchPage)
	// The levels that were returned and how many instances the walk reached.
	// Without `walked`, a filter that matched none of them is indistinguishable
	// from a hierarchy with nothing in it.
	body["levels"] = page.Levels
	body["walked"] = page.Walked
	writeJSON(w, http.StatusOK, body)
}

// fullTree returns a whole hierarchy as a nested tree.
//
// `type_ids` NAMES THE MODEL AT EACH LEVEL, indexed from the root, and there
// must be one per level — a shorter list is a 400 rather than the 500 cantor's
// index-out-of-range produced.
func (h *Handler) fullTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	roots, err := idListParam(q.Get("root_ids"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(roots) == 0 {
		roots, err = idListParam(q.Get("root_id"))
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	if len(roots) == 0 {
		writeErr(w, fmt.Errorf("%w: root_ids", errs.ErrInvalid))
		return
	}
	level, err := positiveIntParam(q.Get("level"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	typeIDs, err := idListParam(q.Get("type_ids"))
	if err != nil {
		writeErr(w, err)
		return
	}

	got, err := h.uc.FullTree(r.Context(), cibiz.FullTreeRequest{
		RootIDs: roots, Level: level, TypeIDs: typeIDs,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// relationStatistics counts the instances at one depth below each root.
//
// THE RESPONSE IS FLAT, with the roots' counts as siblings of `detail`, which
// is cantor's shape and the reason it is built here rather than being a struct
// tag: the console reads `stats[String(id)]` off the top level.
func (h *Handler) relationStatistics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	roots, err := idListParam(q.Get("root_ids"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(roots) == 0 {
		roots, err = idListParam(q.Get("root_id"))
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	if len(roots) == 0 {
		writeErr(w, fmt.Errorf("%w: root_ids", errs.ErrInvalid))
		return
	}
	level, err := positiveIntParam(q.Get("level"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	typeIDs, err := idListParam(q.Get("type_ids"))
	if err != nil {
		writeErr(w, err)
		return
	}

	got, err := h.uc.Statistics(r.Context(), cibiz.StatisticsRequest{
		RootIDs: roots, Level: level, TypeIDs: typeIDs,
		AncestorIDs: strings.TrimSpace(q.Get("ancestor_ids")),
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statisticsBody(got))
}

// statisticsBody flattens the counts into cantor's response shape.
//
// IT IS A FUNCTION RATHER THAN A STRUCT TAG because the shape is not a
// struct's: the root ids are the response's TOP-LEVEL keys, siblings of
// `detail`, and the console reads them off there. A struct would need a map
// with a reserved name inside it, which is what this is.
func statisticsBody(stats *cibiz.Statistics) map[string]any {
	body := make(map[string]any, len(stats.Counts)+1)
	for id, count := range stats.Counts {
		body[id] = count
	}
	detail := make(map[string]any, len(stats.Detail))
	for id, perType := range stats.Detail {
		one := make(map[string]any, len(perType))
		for typeID, count := range perType {
			one[typeID] = count
		}
		detail[id] = one
	}
	body["detail"] = detail
	return body
}

// pathSearchBody is the path enumeration's body.
//
// `target.type_ids` IS IGNORED, and it is accepted rather than refused because
// the console sends it: the target models are the declared chains' tails, which
// is what cantor resolved them to before reading anything else. Echoing a field
// whose only effect is to be overwritten would suggest it does something.
type pathSearchBody struct {
	Source struct {
		TypeID model.ID `json:"type_id"`
		Q      string   `json:"q"`
	} `json:"source"`
	Target struct {
		TypeIDs []model.ID `json:"type_ids"`
		Q       string     `json:"q"`
	} `json:"target"`
	Path     [][]model.ID `json:"path"`
	Page     int          `json:"page"`
	Count    int          `json:"count"`
	PageSize int          `json:"page_size"`
}

func (h *Handler) searchPaths(w http.ResponseWriter, r *http.Request) {
	var req pathSearchBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}

	count := req.Count
	if count == 0 {
		count = req.PageSize
	}
	got, err := h.uc.SearchByPath(r.Context(), cibiz.PathSearchRequest{
		SourceTypeID: req.Source.TypeID,
		SourceQuery:  req.Source.Q,
		TargetQuery:  req.Target.Q,
		Paths:        req.Path,
		Page:         req.Page,
		Count:        count,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listChildren(w http.ResponseWriter, r *http.Request) {
	h.listNeighbours(w, r, false)
}

func (h *Handler) listParents(w http.ResponseWriter, r *http.Request) {
	h.listNeighbours(w, r, true)
}

func (h *Handler) listNeighbours(w http.ResponseWriter, r *http.Request, incoming bool) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
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
	relationTypeID, err := idListParam(q.Get("relation_type_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var one model.ID
	if len(relationTypeID) > 0 {
		one = relationTypeID[0]
	}

	var got *cibiz.NeighbourPage
	if incoming {
		got, err = h.uc.Parents(r.Context(), id, one, page, size)
	} else {
		got, err = h.uc.Children(r.Context(), id, one, page, size)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- writing ---

// relationRequest is the write body.
//
// It is FLAT: `ancestor_ids` and `relation_type_id` are the only controls, and
// the endpoints that take them are the two-element path form.
type relationRequest struct {
	AncestorIDs    string   `json:"ancestor_ids"`
	RelationTypeID model.ID `json:"relation_type_id"`
}

func decodeRelation(r *http.Request) (relationRequest, error) {
	var req relationRequest
	if r.Body == nil {
		return req, nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	if err := dec.Decode(&req); err != nil {
		// An empty body is the common case: the pair is in the path and the
		// body carries only the optional ancestor path.
		if errors.Is(err, io.EOF) {
			return req, nil
		}
		return req, errs.ErrInvalid
	}
	req.AncestorIDs = strings.TrimSpace(req.AncestorIDs)
	return req, nil
}

func (h *Handler) addRelation(w http.ResponseWriter, r *http.Request) {
	first, err := pathID(r, "first")
	if err != nil {
		writeErr(w, err)
		return
	}
	second, err := pathID(r, "second")
	if err != nil {
		writeErr(w, err)
		return
	}
	req, err := decodeRelation(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}

	got, err := h.uc.AddRelation(r.Context(), cibiz.AddRelationRequest{
		FirstCIID: first, SecondCIID: second,
		AncestorIDs: req.AncestorIDs, RelationTypeID: req.RelationTypeID,
	}, audit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"cr_id": got.ID})
}

func (h *Handler) deleteRelation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteRelation(r.Context(), id, audit.Actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteRelationBetween(w http.ResponseWriter, r *http.Request) {
	first, err := pathID(r, "first")
	if err != nil {
		writeErr(w, err)
		return
	}
	second, err := pathID(r, "second")
	if err != nil {
		writeErr(w, err)
		return
	}
	req, err := decodeRelation(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}

	// An edge that is not there is not an error: DELETE is idempotent, and a
	// caller un-linking two instances that are already unlinked has got what
	// they asked for. cantor behaved the same way.
	if _, err := h.uc.DeleteRelationBetween(r.Context(), cibiz.RelationRef{
		FirstCIID: first, SecondCIID: second, AncestorIDs: req.AncestorIDs,
	}, audit.Actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// batchRelationRequest is the batch write body.
//
// `ci_ids` are the instances to relate; `parents` are related as their parents
// and `children` as their children, which is cantor's fan-out and the only
// shape the console sends.
type batchRelationRequest struct {
	CIIDs       []model.ID `json:"ci_ids"`
	Parents     []model.ID `json:"parents"`
	Children    []model.ID `json:"children"`
	AncestorIDs string     `json:"ancestor_ids"`
}

func (h *Handler) batchRelate(w http.ResponseWriter, r *http.Request) {
	var req batchRelationRequest
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	if err := dec.Decode(&req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}

	written, err := h.uc.BatchAddRelations(r.Context(), cibiz.RelateInstances{
		CIIDs: req.CIIDs, Parents: req.Parents, Children: req.Children,
		AncestorIDs: strings.TrimSpace(req.AncestorIDs),
	}, audit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"written": written})
}

// --- parameters ---

// idListParam reads a comma-separated id list, tolerating an empty value.
func idListParam(raw string) ([]model.ID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]model.ID, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 32)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%w: %q is not an instance id", errs.ErrInvalid, part)
		}
		out = append(out, model.ID(n))
	}
	return out, nil
}

// levelParams reads the `level` parameter.
//
// It is a LIST OF LEVELS TO RETURN, not a depth, and the separator is cantor's:
// its argument handler split on commas, so `level=1,3` means levels 1 and 3.
// A semicolon is accepted as well because that is the separator the query DSL
// uses inside parentheses, and a caller who has read that far will try it.
func levelParams(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%w: level %q", errs.ErrInvalid, part)
		}
		out = append(out, n)
	}
	return out, nil
}

// boolParam reads cantor's boolean parameter convention.
func boolParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "t", "on":
		return true
	}
	return false
}
