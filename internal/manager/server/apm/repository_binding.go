package apm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	biz "github.com/ongridio/ongrid/internal/manager/biz/apm"
	audit "github.com/ongridio/ongrid/internal/manager/biz/audit"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	auditmw "github.com/ongridio/ongrid/internal/manager/server/middleware"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// @Summary Read, save or remove a service's source repository binding
// @Router /api/v1/apm/repository-binding [get]
// @Router /api/v1/apm/repository-binding [put]
// @Router /api/v1/apm/repository-binding [delete]
// @Success 200 {object} apm.RepositoryBinding
func (h *Handler) repositoryBinding(w http.ResponseWriter, r *http.Request) {
	caller, ok := tenantctx.From(r.Context())
	if !ok {
		h.respond(w, r, nil, errs.ErrUnauthorized)
		return
	}
	if r.Method != http.MethodGet && !caller.IsSuperuser && caller.Role != "admin" {
		h.respond(w, r, nil, errs.ErrForbidden)
		return
	}
	q := r.URL.Query()
	if !q.Has("environment") || !q.Has("service_namespace") {
		h.respond(w, r, nil, fmt.Errorf("%w: full service identity required", errs.ErrInvalid))
		return
	}
	id := biz.Identity{ServiceName: q.Get("service_name"), ServiceNamespace: q.Get("service_namespace"), Environment: q.Get("environment")}
	if r.Method == http.MethodGet {
		b, err := h.svc.GetRepositoryBinding(r.Context(), id)
		h.respond(w, r, b, err)
		return
	}
	action := auditmodel.ActionSettingUpdate
	if r.Method == http.MethodDelete {
		action = auditmodel.ActionSettingDelete
	}
	auditmw.SetAuditEvent(r, audit.Event{Action: action, ResourceType: auditmodel.ResourceSetting, ResourceName: id.ServiceName, Payload: map[string]any{"category": "apm_repository", "identity": id}})
	if r.Method == http.MethodDelete {
		h.respond(w, r, nil, h.svc.DeleteRepositoryBinding(r.Context(), id))
		return
	}
	var in struct {
		RepoID          uint64 `json:"repo_id,string"`
		SourceDirectory string `json:"source_directory"`
		TagPattern      string `json:"tag_pattern"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		h.respond(w, r, nil, fmt.Errorf("%w: invalid repository binding body", errs.ErrInvalid))
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		h.respond(w, r, nil, fmt.Errorf("%w: expected one JSON object", errs.ErrInvalid))
		return
	}
	b, err := h.svc.PutRepositoryBinding(r.Context(), biz.RepositoryBinding{Identity: id, RepoID: in.RepoID, SourceDirectory: in.SourceDirectory, TagPattern: in.TagPattern})
	h.respond(w, r, b, err)
}
