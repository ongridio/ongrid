package ci

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerSearch attaches the search routes.
//
// Routes:
//
//	GET  /v1/cmdb/ci/s        (any authed) — search instances
//	GET  /v1/cmdb/ci/search   (any authed) — the same, cantor's other spelling
//	POST /v1/cmdb/ci/s        (any authed) — the same, with the expression in
//	POST /v1/cmdb/ci/search   (any authed)   the body
//
// Both spellings exist because cantor registered both and a stored expression
// or a bookmark may use either. The POST form exists for an expression too long
// for a request line; the console uses GET.
//
// NOT ADMIN-GATED, like the instance writes and unlike the model ones: reading
// the inventory is what the product is for. cantor scoped the result to the
// models the caller could READ and, per model, to an attribute and instance
// filter; that is resource-level authorization, which this port has not built
// yet — see the plan. The hook is the model resolution in Usecase.Search, so
// the scoping lands there when it does.
func (h *Handler) registerSearch(r chi.Router) {
	r.Get("/v1/cmdb/ci/s", h.searchInstances)
	r.Get("/v1/cmdb/ci/search", h.searchInstances)
	r.Post("/v1/cmdb/ci/s", h.searchInstances)
	r.Post("/v1/cmdb/ci/search", h.searchInstances)
}

// searchParams is a search request read off the wire.
type searchParams struct {
	query  string
	sort   string
	fields []string
	facets []string
	page   int
	count  int
	key    cibiz.RetKey
}

func (h *Handler) searchInstances(w http.ResponseWriter, r *http.Request) {
	params, err := readSearchParams(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.Search(r.Context(), cibiz.SearchRequest{
		Query:  params.query,
		Sort:   params.sort,
		Fields: params.fields,
		Facets: params.facets,
		RetKey: params.key,
		Page:   params.page,
		Count:  params.count,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, searchResponse(got))
}

// readSearchParams reads a search's parameters.
//
// A POST body is consulted first and the query string second, so a client can
// put the expression in the body and the paging in the URL. An EMPTY body is
// not an error — it is a POST with everything in the query string, which is
// what cantor's POST route did when it forwarded to its GET handler.
//
// `count=all` is refused rather than ignored. cantor accepted it and then
// formatted it into a LIMIT clause, which is a syntax error on its own MySQL
// backend: the value was never usable, so answering "no" is not a regression,
// and silently substituting the default would answer a different question than
// the one asked.
func readSearchParams(r *http.Request) (searchParams, error) {
	body, err := decodeOptionalBody(r)
	if err != nil {
		return searchParams{}, err
	}

	// The value is read from the body when it carries the key and from the
	// query string otherwise. One accessor rather than two code paths, because
	// every parameter has to behave the same way and a per-parameter choice is
	// how they drift apart.
	get := func(key string) string {
		if v, ok := body[key]; ok {
			// A body is JSON, so `count` and `page` arrive as numbers while the
			// query string always spells them as text. Both forms have to work:
			// a number silently falling through to the query string would read
			// as "no page size given" and quietly page by the default.
			switch t := v.(type) {
			case string:
				return t
			case json.Number:
				return t.String()
			}
		}
		return r.URL.Query().Get(key)
	}
	getList := func(key string) []string {
		if v, ok := body[key]; ok {
			if list, ok := v.([]any); ok {
				out := make([]string, 0, len(list))
				for _, item := range list {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						out = append(out, strings.TrimSpace(s))
					}
				}
				return out
			}
		}
		return splitList(r.URL.Query().Get(key))
	}

	page, err := positiveIntParam(get("page"), 1)
	if err != nil {
		return searchParams{}, err
	}
	// cantor accepted the page size under either name.
	rawCount := get("count")
	if strings.TrimSpace(rawCount) == "" {
		rawCount = get("page_size")
	}
	count, err := positiveIntParam(rawCount, 0)
	if err != nil {
		return searchParams{}, err
	}

	key := cibiz.RetKey(strings.TrimSpace(get("ret_key")))
	if !key.Valid() {
		// A display preference, not an authorisation; cantor fell back to name
		// and so does this.
		key = cibiz.RetKeyName
	}

	return searchParams{
		query:  strings.TrimSpace(get("q")),
		sort:   strings.TrimSpace(get("sort")),
		fields: getList("fl"),
		facets: getList("facet"),
		page:   page,
		count:  count,
		key:    key,
	}, nil
}

// decodeOptionalBody reads a JSON object body, treating an empty one as no
// body at all.
func decodeOptionalBody(r *http.Request) (map[string]any, error) {
	if r.Method != http.MethodPost || r.Body == nil {
		return nil, nil
	}
	var body map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, errs.ErrInvalid
	}
	return body, nil
}

// searchResponse renders a page of results.
//
// THE ROW SHAPE IS CANTOR'S: the identifying fields first, then every attribute
// value FLAT alongside them rather than nested. The console reads a row's
// attributes directly (`row.hostname`), so nesting them under `values` would
// mean rewriting every consumer. The metadata keys cannot collide with an
// attribute name — `_id`, `_type`, `ci_type` and `ci_id` are all in cantor's
// BUILTIN_KEYWORDS, which the model-configuration path refuses to create an
// attribute under — and where one nevertheless does, the value wins, which is
// what cantor did.
func searchResponse(got *cibiz.SearchPage) map[string]any {
	result := make([]map[string]any, 0, len(got.Hits))
	for _, hit := range got.Hits {
		row := map[string]any{
			"_id":     hit.ID,
			"_type":   hit.TypeID,
			"ci_id":   hit.ID,
			"ci_type": hit.TypeName,
		}
		if hit.TypeAlias != "" {
			row["ci_type_alias"] = hit.TypeAlias
		}
		if hit.UniqueAttr != "" {
			row["unique"] = hit.UniqueAttr
		}
		if hit.UniqueAlias != "" {
			row["unique_alias"] = hit.UniqueAlias
		}
		if !hit.UpdatedAt.IsZero() {
			row["_updated_at"] = hit.UpdatedAt.Format("2006-01-02 15:04:05")
		}
		if hit.UpdatedBy != "" {
			row["_updated_by"] = hit.UpdatedBy
		}
		for k, v := range hit.Values {
			row[k] = v
		}
		result = append(result, row)
	}

	body := map[string]any{
		// numfound is the whole match set; total is what came back. cantor
		// named them the other way round from what a reader expects, and the
		// console's pager uses numfound, so both are kept as cantor spelled
		// them.
		"numfound": got.NumFound,
		"total":    len(result),
		"page":     got.Page,
		"counter":  got.Counter,
		"result":   result,
	}
	if got.Facets != nil {
		facet := make(map[string]any, len(got.Facets))
		for key, buckets := range got.Facets {
			// Each bucket is a triple: the value, its count, and the field it
			// came from. The console reads the third element as the column key.
			items := make([][3]any, 0, len(buckets))
			for _, b := range buckets {
				items = append(items, [3]any{b.Value, b.Count, key})
			}
			facet[key] = items
		}
		body["facet"] = facet
	}
	return body
}
