// Package history serves the operation-history reads.
package history

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	historybiz "github.com/ongridio/ongrid/internal/cmdb/biz/history"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// Handler serves the history routes.
type Handler struct {
	uc *historybiz.Usecase
}

// NewHandler builds the handler.
func NewHandler(uc *historybiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the history routes.
//
// Routes:
//
//	GET /v1/cmdb/history/attributes        (admin) — attribute changes
//	GET /v1/cmdb/history/relations         (admin) — relation changes
//	GET /v1/cmdb/history/ci-types          (admin) — model-configuration changes
//	GET /v1/cmdb/history/ci-triggers       (admin) — trigger firings
//	GET /v1/cmdb/history/ci/{id}           (any authed) — one instance's changes
//	GET /v1/cmdb/history/ci/{id}/triggers  (any authed) — one instance's firings
//
// THE GLOBAL READS ARE ADMIN-ONLY. They span every model and every user; the
// per-instance reads are not, because a person who may read an instance may
// read what happened to it. cantor drew the same line with an ACL, and this is
// the closest expression of it without one.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/cmdb/history/attributes", h.listAttributes)
	r.Get("/v1/cmdb/history/relations", h.listRelations)
	r.Get("/v1/cmdb/history/ci-types", h.listCITypes)
	r.Get("/v1/cmdb/history/ci-triggers", h.listCITriggers)
	r.Get("/v1/cmdb/history/ci/{id}", h.listCIHistory)
	r.Get("/v1/cmdb/history/ci/{id}/triggers", h.listCITriggerHistory)
}

// isAdmin reads the caller's role from the request context.
func isAdmin(r *http.Request) bool {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		return false
	}
	return t.IsSuperuser || t.Role == "admin"
}

func (h *Handler) listAttributes(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	f, err := attributeFilter(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ListAttributes(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listRelations(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	start, end, err := timeRange(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	f := historybiz.RelationFilter{
		Start: start, End: end,
		UID:         queryID(r, "uid"),
		TypeID:      queryID(r, "type_id"),
		FirstCIID:   queryID(r, "first_ci_id"),
		SecondCIID:  queryID(r, "second_ci_id"),
		OperateType: strings.TrimSpace(r.URL.Query().Get("operate_type")),
	}
	f.Page, f.PageSize = pageParams(r)
	if err := checkQueryIDs(r, "uid", "type_id", "first_ci_id", "second_ci_id"); err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ListRelations(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listCITypes(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	start, end, err := timeRange(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	f := historybiz.TypeFilter{
		Start: start, End: end,
		UID:         queryID(r, "uid"),
		TypeID:      queryID(r, "type_id"),
		OperateType: strings.TrimSpace(r.URL.Query().Get("operate_type")),
	}
	f.Page, f.PageSize = pageParams(r)
	if err := checkQueryIDs(r, "uid", "type_id"); err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ListTypes(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listCITriggers(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeErr(w, errs.ErrForbidden)
		return
	}
	start, end, err := timeRange(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	f := historybiz.TriggerFilter{
		Start: start, End: end,
		UID:         queryID(r, "uid"),
		TypeID:      queryID(r, "type_id"),
		CIID:        queryID(r, "ci_id"),
		TriggerID:   queryID(r, "trigger_id"),
		OperateType: strings.TrimSpace(r.URL.Query().Get("operate_type")),
	}
	f.Page, f.PageSize = pageParams(r)
	if err := checkQueryIDs(r, "uid", "type_id", "ci_id", "trigger_id"); err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ListTriggers(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listCIHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	page, size := pageParams(r)
	got, err := h.uc.ListCIAttributes(r.Context(), id, page, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) listCITriggerHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	page, size := pageParams(r)
	got, err := h.uc.ListCITriggers(r.Context(), id, page, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// attributeFilter reads the attribute route's filters.
func attributeFilter(r *http.Request) (historybiz.AttributeFilter, error) {
	start, end, err := timeRange(r)
	if err != nil {
		return historybiz.AttributeFilter{}, err
	}
	f := historybiz.AttributeFilter{
		Start: start, End: end,
		UID:         queryID(r, "uid"),
		TypeID:      queryID(r, "type_id"),
		CIID:        queryID(r, "ci_id"),
		AttrID:      queryID(r, "attr_id"),
		OperateType: strings.TrimSpace(r.URL.Query().Get("operate_type")),
	}
	f.Page, f.PageSize = pageParams(r)
	if err := checkQueryIDs(r, "uid", "type_id", "ci_id", "attr_id"); err != nil {
		return historybiz.AttributeFilter{}, err
	}
	return f, nil
}

// timeRange reads `start` and `end`.
//
// TWO FORMATS ARE ACCEPTED: the console's `2006-01-02 15:04:05` and RFC 3339.
// The first is cantor's; the second is what an API client sends. A malformed
// value is a 400 rather than a silently unbounded read, which is what a parse
// error quietly ignored would produce.
func timeRange(r *http.Request) (time.Time, time.Time, error) {
	start, err := parseTimeParam(r, "start")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseTimeParam(r, "end")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, end, nil
}

func parseTimeParam(r *http.Request, name string) (time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: %s", errs.ErrInvalid, name)
}

// queryID reads an optional positive id. A malformed one is caught by
// checkQueryIDs, which is called where the value is used.
func queryID(r *http.Request, name string) model.ID {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return model.ID(n)
}

// checkQueryIDs rejects a malformed id rather than silently dropping the
// filter: a page that quietly shows every user's history when asked for one is
// worse than a 400.
func checkQueryIDs(r *http.Request, names ...string) error {
	for _, name := range names {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n <= 0 {
			return fmt.Errorf("%w: %s", errs.ErrInvalid, name)
		}
	}
	return nil
}

func pageParams(r *http.Request) (page, size int) {
	query := r.URL.Query()
	page = 1
	if raw := query.Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	if raw := query.Get("page_size"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			size = n
		}
	}
	return page, size
}

func pathID(r *http.Request, name string) (model.ID, error) {
	n, err := strconv.ParseInt(chi.URLParam(r, name), 10, 32)
	if err != nil || n <= 0 {
		return 0, errs.ErrInvalid
	}
	return model.ID(n), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.WriteHeader(errs.HTTPStatus(err))
	_, _ = w.Write([]byte(strings.TrimSpace(err.Error())))
}
