// Package topology exposes the manager HTTP routes for the
// graph layer: nodes / relations / relation types.
//
// Routes (all under the authed /api/v1 prefix):
//
//	GET /v1/topology/nodes (any authed)
//	POST /v1/topology/nodes (admin)
//	GET /v1/topology/nodes/{id} (any authed)
//	PATCH /v1/topology/nodes/{id} (admin) — name/props
//	DELETE /v1/topology/nodes/{id} (admin)
//
//	GET /v1/topology/relations (any authed) — filter by src/dst/type/src_or_dst
//	POST /v1/topology/relations (admin)
//	GET /v1/topology/relations/{id} (any authed)
//	PATCH /v1/topology/relations/{id} (admin) — props only
//	DELETE /v1/topology/relations/{id} (admin)
//
//	GET /v1/topology/relation-types (any authed)
//	POST /v1/topology/relation-types (admin)
//	GET /v1/topology/relation-types/{name} (any authed)
//	DELETE /v1/topology/relation-types/{name} (admin)
package topology

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	biz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const roleAdmin = "admin"

// Handler exposes /v1/topology/*.
type Handler struct {
	uc *biz.Usecase
}

// NewHandler builds the handler around a topology biz Usecase.
func NewHandler(uc *biz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the topology routes on r.
func (h *Handler) Register(r chi.Router) {
	// Nodes
	r.Get("/v1/topology/nodes", h.listNodes)
	r.Get("/v1/topology/nodes/{id}", h.getNode)
	r.With(h.requireAdmin).Post("/v1/topology/nodes", h.createNode)
	r.With(h.requireAdmin).Patch("/v1/topology/nodes/{id}", h.updateNode)
	r.With(h.requireAdmin).Put("/v1/topology/nodes/{id}/environment", h.setClusterEnvironment)
	r.With(h.requireAdmin).Delete("/v1/topology/nodes/{id}", h.deleteNode)

	// Relations
	r.Get("/v1/topology/relations", h.listRelations)
	r.Get("/v1/topology/relations/{id}", h.getRelation)
	r.With(h.requireAdmin).Post("/v1/topology/relations", h.createRelation)
	r.With(h.requireAdmin).Patch("/v1/topology/relations/{id}", h.updateRelation)
	r.With(h.requireAdmin).Delete("/v1/topology/relations/{id}", h.deleteRelation)

	// Relation types
	r.Get("/v1/topology/relation-types", h.listRelationTypes)
	r.Get("/v1/topology/relation-types/{name}", h.getRelationType)
	r.With(h.requireAdmin).Post("/v1/topology/relation-types", h.createRelationType)
	r.With(h.requireAdmin).Delete("/v1/topology/relation-types/{name}", h.deleteRelationType)

	// Node types — same shape as relation-types. UI uses these to
	// label chips with display_name and to seed the tier layout.
	r.Get("/v1/topology/node-types", h.listNodeTypes)
	r.Get("/v1/topology/node-types/{name}", h.getNodeType)
	r.With(h.requireAdmin).Post("/v1/topology/node-types", h.createNodeType)
	r.With(h.requireAdmin).Delete("/v1/topology/node-types/{name}", h.deleteNodeType)
}

// requireAdmin gates write endpoints behind the admin role.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok {
			writeErr(w, errs.ErrUnauthorized)
			return
		}
		if t.Role != roleAdmin {
			writeErr(w, errs.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- DTOs ------------------------------------------------------------

type nodeItem struct {
	ID        uint64 `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Props     any    `json:"props,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type nodeListResp struct {
	Items []nodeItem `json:"items"`
	Total int64      `json:"total"`
}

type createNodeReq struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Props any    `json:"props,omitempty"`
}

type updateNodeReq struct {
	Name  *string `json:"name,omitempty"`
	Props any     `json:"props,omitempty"`
}

type relationItem struct {
	ID        uint64 `json:"id"`
	SrcID     uint64 `json:"src_id"`
	DstID     uint64 `json:"dst_id"`
	Type      string `json:"type"`
	Props     any    `json:"props,omitempty"`
	CreatedAt string `json:"created_at"`
}

type relationListResp struct {
	Items []relationItem `json:"items"`
	Total int64          `json:"total"`
}

type createRelationReq struct {
	SrcID uint64 `json:"src_id"`
	DstID uint64 `json:"dst_id"`
	Type  string `json:"type"`
	Props any    `json:"props,omitempty"`
}

type updateRelationReq struct {
	Props any `json:"props,omitempty"`
}

type relationTypeItem struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	DisplayNameEN     string `json:"display_name_en,omitempty"`
	Builtin           bool   `json:"builtin"`
	PropagatesFailure bool   `json:"propagates_failure"`
	Direction         string `json:"direction"`
	SemanticsTag      string `json:"semantics_tag"`
	Description       string `json:"description"`
}

type createRelationTypeReq struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	DisplayNameEN     string `json:"display_name_en,omitempty"`
	PropagatesFailure bool   `json:"propagates_failure"`
	Direction         string `json:"direction"`
	SemanticsTag      string `json:"semantics_tag"`
	Description       string `json:"description"`
}

// ---------- Node handlers ---------------------------------------------------

func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	f := biz.NodeListFilter{
		Type: q.Get("type"),
		Q:    q.Get("q"),
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Limit = n
		}
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Offset = n
		}
	}
	rows, total, err := h.uc.ListNodes(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]nodeItem, 0, len(rows))
	for _, n := range rows {
		out = append(out, toNodeItem(n))
	}
	writeJSON(w, http.StatusOK, nodeListResp{Items: out, Total: total})
}

func (h *Handler) getNode(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := h.uc.GetNode(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNodeItem(n))
}

func (h *Handler) createNode(w http.ResponseWriter, r *http.Request) {
	var in createNodeReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	propsStr, err := encodeProps(in.Props)
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := h.uc.CreateNode(r.Context(), in.Type, in.Name, propsStr)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toNodeItem(n))
}

func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in updateNodeReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	cur, err := h.uc.GetNode(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := cur.Name
	if in.Name != nil {
		name = *in.Name
	}
	propsStr := cur.PropsJSON
	if in.Props != nil {
		s, err := encodeProps(in.Props)
		if err != nil {
			writeErr(w, err)
			return
		}
		propsStr = s
	}
	if err := h.uc.UpdateNode(r.Context(), id, name, propsStr); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setClusterEnvironment updates a cluster's default capture environment.
// @Summary Set cluster default environment
// @Router /api/v1/topology/nodes/{id}/environment [put]
// @Success 204
func (h *Handler) setClusterEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Environment *string `json:"environment"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Environment == nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.uc.SetClusterEnvironment(r.Context(), id, *in.Environment); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteNode(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Relation handlers -----------------------------------------------

func (h *Handler) listRelations(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	f := biz.RelationListFilter{
		Type: q.Get("type"),
	}
	if s := q.Get("src_id"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			f.SrcID = n
		}
	}
	if s := q.Get("dst_id"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			f.DstID = n
		}
	}
	if s := q.Get("src_or_dst_id"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			f.SrcOrDstID = n
		}
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Limit = n
		}
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Offset = n
		}
	}
	rows, total, err := h.uc.ListRelations(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]relationItem, 0, len(rows))
	for _, rel := range rows {
		out = append(out, toRelationItem(rel))
	}
	writeJSON(w, http.StatusOK, relationListResp{Items: out, Total: total})
}

func (h *Handler) getRelation(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	rel, err := h.uc.GetRelation(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRelationItem(rel))
}

func (h *Handler) createRelation(w http.ResponseWriter, r *http.Request) {
	var in createRelationReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	propsStr, err := encodeProps(in.Props)
	if err != nil {
		writeErr(w, err)
		return
	}
	rel, err := h.uc.CreateRelation(r.Context(), in.SrcID, in.DstID, in.Type, propsStr)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRelationItem(rel))
}

func (h *Handler) updateRelation(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in updateRelationReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	propsStr, err := encodeProps(in.Props)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.UpdateRelation(r.Context(), id, propsStr); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteRelation(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteRelation(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- RelationType handlers ------------------------------------------

func (h *Handler) listRelationTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	rows, err := h.uc.ListRelationTypes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]relationTypeItem, 0, len(rows))
	for _, rt := range rows {
		out = append(out, toRelationTypeItem(rt))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) getRelationType(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	name := chi.URLParam(r, "name")
	rt, err := h.uc.GetRelationType(r.Context(), name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRelationTypeItem(rt))
}

func (h *Handler) createRelationType(w http.ResponseWriter, r *http.Request) {
	var in createRelationTypeReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	rt, err := h.uc.RegisterRelationType(r.Context(), model.RelationType{
		Name:              in.Name,
		DisplayName:       in.DisplayName,
		DisplayNameEN:     in.DisplayNameEN,
		PropagatesFailure: in.PropagatesFailure,
		Direction:         in.Direction,
		SemanticsTag:      in.SemanticsTag,
		Description:       in.Description,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRelationTypeItem(rt))
}

func (h *Handler) deleteRelationType(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := h.uc.DeleteRelationType(r.Context(), name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- NodeType handlers ----------------------------------------------

type nodeTypeItem struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	DisplayNameEN string `json:"display_name_en,omitempty"`
	Builtin       bool   `json:"builtin"`
	Tier          int    `json:"tier"`
	Description   string `json:"description"`
}

type createNodeTypeReq struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	DisplayNameEN string `json:"display_name_en,omitempty"`
	Tier          int    `json:"tier"`
	Description   string `json:"description"`
}

func (h *Handler) listNodeTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	rows, err := h.uc.ListNodeTypes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]nodeTypeItem, 0, len(rows))
	for _, nt := range rows {
		out = append(out, toNodeTypeItem(nt))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) getNodeType(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	name := chi.URLParam(r, "name")
	nt, err := h.uc.GetNodeType(r.Context(), name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNodeTypeItem(nt))
}

func (h *Handler) createNodeType(w http.ResponseWriter, r *http.Request) {
	var in createNodeTypeReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	nt, err := h.uc.RegisterNodeType(r.Context(), model.NodeType{
		Name:          in.Name,
		DisplayName:   in.DisplayName,
		DisplayNameEN: in.DisplayNameEN,
		Tier:          in.Tier,
		Description:   in.Description,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toNodeTypeItem(nt))
}

func (h *Handler) deleteNodeType(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := h.uc.DeleteNodeType(r.Context(), name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toNodeTypeItem(nt *model.NodeType) nodeTypeItem {
	return nodeTypeItem{
		Name:          nt.Name,
		DisplayName:   nt.DisplayName,
		DisplayNameEN: nt.DisplayNameEN,
		Builtin:       nt.Builtin,
		Tier:          nt.Tier,
		Description:   nt.Description,
	}
}

// ---------- helpers ---------------------------------------------------------

func toNodeItem(n *model.Node) nodeItem {
	return nodeItem{
		ID:        n.ID,
		Type:      n.Type,
		Name:      n.Name,
		Props:     decodeProps(n.PropsJSON),
		CreatedAt: n.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt: n.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func toRelationItem(r *model.Relation) relationItem {
	return relationItem{
		ID:        r.ID,
		SrcID:     r.SrcID,
		DstID:     r.DstID,
		Type:      r.Type,
		Props:     decodeProps(r.PropsJSON),
		CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func toRelationTypeItem(rt *model.RelationType) relationTypeItem {
	return relationTypeItem{
		Name:              rt.Name,
		DisplayName:       rt.DisplayName,
		DisplayNameEN:     rt.DisplayNameEN,
		Builtin:           rt.Builtin,
		PropagatesFailure: rt.PropagatesFailure,
		Direction:         rt.Direction,
		SemanticsTag:      rt.SemanticsTag,
		Description:       rt.Description,
	}
}

// encodeProps serialises the request-time `props` field (any JSON value
// the client sent — typically an object) back to a JSON string for
// storage. nil maps to "".
func encodeProps(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", errors.Join(errs.ErrInvalid, err)
	}
	return string(b), nil
}

// decodeProps inverts encodeProps for the response side. Returns nil
// (so the field can be omitted via omitempty) when storage is empty.
func decodeProps(s string) any {
	if s == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		// Store raw string fallback — better than dropping silently if
		// somebody bypassed the API and inserted garbage.
		return s
	}
	return out
}

func parseID(r *http.Request, key string) (uint64, error) {
	raw := chi.URLParam(r, key)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error(), Code: errCode(err)})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	default:
		return "internal"
	}
}
