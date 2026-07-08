package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const roleAdmin = "admin"

const clusterOnlineTTL = 90 * time.Second

type Service interface {
	CreateCluster(ctx context.Context, in biz.CreateClusterInput) (*biz.ClusterRegistration, error)
	ListClusters(ctx context.Context, f biz.ListClustersFilter) ([]*model.Cluster, error)
	GetCluster(ctx context.Context, id uint64) (*model.Cluster, error)
	ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error)
	CountNodes(ctx context.Context, clusterID uint64) (int64, error)
	GetNodeCoverage(ctx context.Context, clusterID uint64) (biz.NodeCoverage, error)
	ListWorkloads(ctx context.Context, f biz.ListWorkloadsFilter) ([]*model.Workload, error)
	CountWorkloads(ctx context.Context, f biz.ListWorkloadsFilter) (int64, error)
	ListPods(ctx context.Context, f biz.ListPodsFilter) ([]*model.Pod, error)
	CountPods(ctx context.Context, f biz.ListPodsFilter) (int64, error)
	ListEvents(ctx context.Context, f biz.ListEventsFilter) ([]*model.Event, error)
	CountEvents(ctx context.Context, f biz.ListEventsFilter) (int64, error)
	RotateBootstrapToken(ctx context.Context, id uint64) (*biz.ClusterRegistration, error)
	DeleteCluster(ctx context.Context, id uint64) error
	Enroll(ctx context.Context, in biz.EnrollInput) (*biz.EnrollResult, error)
}

type Handler struct {
	svc Service
}

func NewHandler(s Service) *Handler {
	return &Handler{svc: s}
}

func (h *Handler) RegisterProtected(r chi.Router) {
	r.With(h.requireAdmin).Post("/v1/k8s/clusters", h.createCluster)
	r.Get("/v1/k8s/clusters", h.listClusters)
	r.Get("/v1/k8s/clusters/{cluster_id}", h.getCluster)
	r.Get("/v1/k8s/clusters/{cluster_id}/nodes", h.listNodes)
	r.Get("/v1/k8s/clusters/{cluster_id}/workloads", h.listWorkloads)
	r.Get("/v1/k8s/clusters/{cluster_id}/pods", h.listPods)
	r.Get("/v1/k8s/clusters/{cluster_id}/events", h.listEvents)
	r.With(h.requireAdmin).Post("/v1/k8s/clusters/{cluster_id}/rotate-token", h.rotateToken)
	r.With(h.requireAdmin).Delete("/v1/k8s/clusters/{cluster_id}", h.deleteCluster)
}

func (h *Handler) RegisterInternal(r chi.Router) {
	r.Post("/internal/k8s/enroll", h.enroll)
}

// @Summary Create Kubernetes cluster enrollment
// @Router /api/v1/k8s/clusters [post]
// @Success 201 {object} clusterRegistrationDTO
func (h *Handler) createCluster(w http.ResponseWriter, r *http.Request) {
	var req createClusterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var createdBy *uint64
	if t, ok := tenantctx.From(r.Context()); ok && t.UserID != 0 {
		createdBy = &t.UserID
	}
	out, err := h.svc.CreateCluster(r.Context(), biz.CreateClusterInput{
		Name:      req.Name,
		UID:       req.UID,
		Mode:      req.Mode,
		CreatedBy: createdBy,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, registrationDTO(out))
}

// @Summary List Kubernetes clusters
// @Router /api/v1/k8s/clusters [get]
// @Success 200 {object} listClustersResponse
func (h *Handler) listClusters(w http.ResponseWriter, r *http.Request) {
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	items, err := h.svc.ListClusters(r.Context(), biz.ListClustersFilter{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Name:   strings.TrimSpace(r.URL.Query().Get("name")),
		Mode:   strings.TrimSpace(r.URL.Query().Get("mode")),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]clusterDTO, 0, len(items))
	for _, item := range items {
		coverage, err := h.svc.GetNodeCoverage(r.Context(), item.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		dto = append(dto, clusterDTOFromModelWithCoverage(item, &coverage))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  dto,
		"total":  len(dto),
		"limit":  limit,
		"offset": offset,
	})
}

// @Summary Get Kubernetes cluster
// @Router /api/v1/k8s/clusters/{cluster_id} [get]
// @Success 200 {object} clusterDTO
func (h *Handler) getCluster(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	c, err := h.svc.GetCluster(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	coverage, err := h.svc.GetNodeCoverage(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterDTOFromModelWithCoverage(c, &coverage))
}

// @Summary List Kubernetes nodes
// @Router /api/v1/k8s/clusters/{cluster_id}/nodes [get]
// @Success 200 {object} listNodesResponse
func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := h.svc.ListNodes(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	total, err := h.svc.CountNodes(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]nodeDTO, 0, len(items))
	for _, item := range items {
		dto = append(dto, nodeDTOFromModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": dto,
		"total": total,
	})
}

// @Summary List Kubernetes workloads
// @Router /api/v1/k8s/clusters/{cluster_id}/workloads [get]
// @Success 200 {object} listWorkloadsResponse
func (h *Handler) listWorkloads(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	filter := biz.ListWorkloadsFilter{
		ClusterID: id,
		Namespace: strings.TrimSpace(r.URL.Query().Get("namespace")),
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		Query:     strings.TrimSpace(r.URL.Query().Get("q")),
		IssueOnly: parseBoolDefault(r.URL.Query().Get("issue_only"), false),
		Limit:     limit,
		Offset:    offset,
	}
	items, err := h.svc.ListWorkloads(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	countFilter := filter
	countFilter.Limit = 0
	countFilter.Offset = 0
	total, err := h.svc.CountWorkloads(r.Context(), countFilter)
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]workloadDTO, 0, len(items))
	for _, item := range items {
		dto = append(dto, workloadDTOFromModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  dto,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// @Summary List Kubernetes pods
// @Router /api/v1/k8s/clusters/{cluster_id}/pods [get]
// @Success 200 {object} listPodsResponse
func (h *Handler) listPods(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	filter := biz.ListPodsFilter{
		ClusterID: id,
		Namespace: strings.TrimSpace(r.URL.Query().Get("namespace")),
		NodeName:  strings.TrimSpace(r.URL.Query().Get("node_name")),
		Phase:     strings.TrimSpace(r.URL.Query().Get("phase")),
		Reason:    strings.TrimSpace(r.URL.Query().Get("reason")),
		Query:     strings.TrimSpace(r.URL.Query().Get("q")),
		IssueOnly: parseBoolDefault(r.URL.Query().Get("issue_only"), false),
		Limit:     limit,
		Offset:    offset,
	}
	items, err := h.svc.ListPods(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	countFilter := filter
	countFilter.Limit = 0
	countFilter.Offset = 0
	total, err := h.svc.CountPods(r.Context(), countFilter)
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]podDTO, 0, len(items))
	for _, item := range items {
		dto = append(dto, podDTOFromModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  dto,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// @Summary List Kubernetes events
// @Router /api/v1/k8s/clusters/{cluster_id}/events [get]
// @Success 200 {object} listEventsResponse
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	filter := biz.ListEventsFilter{
		ClusterID:    id,
		Namespace:    strings.TrimSpace(r.URL.Query().Get("namespace")),
		Type:         strings.TrimSpace(r.URL.Query().Get("type")),
		Reason:       strings.TrimSpace(r.URL.Query().Get("reason")),
		InvolvedKind: strings.TrimSpace(r.URL.Query().Get("involved_kind")),
		InvolvedName: strings.TrimSpace(r.URL.Query().Get("involved_name")),
		Query:        strings.TrimSpace(r.URL.Query().Get("q")),
		IssueOnly:    parseBoolDefault(r.URL.Query().Get("issue_only"), false),
		Limit:        limit,
		Offset:       offset,
	}
	items, err := h.svc.ListEvents(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	countFilter := filter
	countFilter.Limit = 0
	countFilter.Offset = 0
	total, err := h.svc.CountEvents(r.Context(), countFilter)
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]eventDTO, 0, len(items))
	for _, item := range items {
		dto = append(dto, eventDTOFromModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  dto,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// @Summary Rotate Kubernetes bootstrap token
// @Router /api/v1/k8s/clusters/{cluster_id}/rotate-token [post]
// @Success 200 {object} clusterRegistrationDTO
func (h *Handler) rotateToken(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := h.svc.RotateBootstrapToken(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, registrationDTO(out))
}

// @Summary Delete Kubernetes cluster
// @Router /api/v1/k8s/clusters/{cluster_id} [delete]
// @Success 204
func (h *Handler) deleteCluster(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.DeleteCluster(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// @Summary Enroll Kubernetes edge node or controller
// @Router /internal/k8s/enroll [post]
// @Success 200 {object} enrollResponse
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	var req enrollRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := h.svc.Enroll(r.Context(), biz.EnrollInput{
		BootstrapToken: token,
		ClusterID:      req.ClusterID,
		ClusterUID:     req.ClusterUID,
		Role:           req.Role,
		NodeName:       req.NodeName,
		NodeUID:        req.NodeUID,
		ProviderID:     req.ProviderID,
		Namespace:      req.Namespace,
		AgentVersion:   req.AgentVersion,
		Capabilities:   req.Capabilities,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, enrollResponse{
		ClusterID:        out.ClusterID,
		Role:             out.Role,
		Mode:             out.Mode,
		EdgeID:           out.EdgeID,
		AccessKey:        out.AccessKey,
		SecretKey:        out.SecretKey,
		CloudAddr:        out.CloudAddr,
		ManagerPublicURL: out.ManagerPublicURL,
	})
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok || (!t.IsSuperuser && t.Role != roleAdmin) {
			writeErr(w, errs.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type createClusterRequest struct {
	Name string `json:"name"`
	UID  string `json:"uid,omitempty"`
	Mode string `json:"mode,omitempty"`
}

type enrollRequest struct {
	ClusterID    uint64   `json:"cluster_id"`
	ClusterUID   string   `json:"cluster_uid,omitempty"`
	Role         string   `json:"role"`
	NodeName     string   `json:"node_name,omitempty"`
	NodeUID      string   `json:"node_uid,omitempty"`
	ProviderID   string   `json:"provider_id,omitempty"`
	Namespace    string   `json:"namespace,omitempty"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type clusterDTO struct {
	ID                            uint64                 `json:"id"`
	Name                          string                 `json:"name"`
	UID                           string                 `json:"uid,omitempty"`
	Mode                          string                 `json:"mode"`
	Status                        string                 `json:"status"`
	Capabilities                  []clusterCapabilityDTO `json:"capabilities,omitempty"`
	NodeEdgeCoverage              *nodeEdgeCoverageDTO   `json:"node_edge_coverage,omitempty"`
	ControllerEdgeID              *uint64                `json:"controller_edge_id,omitempty"`
	ControllerNodeName            string                 `json:"controller_node_name,omitempty"`
	ControllerNamespace           string                 `json:"controller_namespace,omitempty"`
	ControllerPodName             string                 `json:"controller_pod_name,omitempty"`
	Version                       string                 `json:"version,omitempty"`
	LastSeenAt                    *time.Time             `json:"last_seen_at,omitempty"`
	InventoryResourceVersion      string                 `json:"inventory_resource_version,omitempty"`
	InventoryResourceVersionsJSON string                 `json:"inventory_resource_versions_json,omitempty"`
	InventoryScope                string                 `json:"inventory_scope,omitempty"`
	InventoryNamespace            string                 `json:"inventory_namespace,omitempty"`
	InventorySyncDurationMS       int64                  `json:"inventory_sync_duration_ms,omitempty"`
	InventoryWatchLagSeconds      int64                  `json:"inventory_watch_lag_seconds,omitempty"`
	InventorySyncedAt             *time.Time             `json:"inventory_synced_at,omitempty"`
	BootstrapTokenExpiresAt       *time.Time             `json:"bootstrap_token_expires_at,omitempty"`
	CreatedAt                     time.Time              `json:"created_at"`
	UpdatedAt                     time.Time              `json:"updated_at"`
}

type clusterCapabilityDTO struct {
	Key    string `json:"key"`
	Label  string `json:"label,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type nodeEdgeCoverageDTO struct {
	Total        int64 `json:"total"`
	EdgeLinked   int64 `json:"edge_linked"`
	DeviceLinked int64 `json:"device_linked"`
	Missing      int64 `json:"missing"`
	Percent      int   `json:"percent"`
}

type clusterRegistrationDTO struct {
	Cluster        clusterDTO `json:"cluster"`
	BootstrapToken string     `json:"bootstrap_token"`
	InstallCommand string     `json:"install_command"`
}

type nodeDTO struct {
	ID             uint64          `json:"id"`
	ClusterID      uint64          `json:"cluster_id"`
	NodeName       string          `json:"node_name"`
	NodeUID        string          `json:"node_uid"`
	ProviderID     string          `json:"provider_id,omitempty"`
	EdgeID         *uint64         `json:"edge_id,omitempty"`
	DeviceID       *uint64         `json:"device_id,omitempty"`
	Labels         json.RawMessage `json:"labels,omitempty"`
	Taints         json.RawMessage `json:"taints,omitempty"`
	Conditions     json.RawMessage `json:"conditions,omitempty"`
	Capacity       json.RawMessage `json:"capacity,omitempty"`
	Allocatable    json.RawMessage `json:"allocatable,omitempty"`
	KubeletVersion string          `json:"kubelet_version,omitempty"`
	LastSeenAt     *time.Time      `json:"last_seen_at,omitempty"`
}

type workloadDTO struct {
	ID              uint64          `json:"id"`
	ClusterID       uint64          `json:"cluster_id"`
	Namespace       string          `json:"namespace"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	UID             string          `json:"uid,omitempty"`
	DesiredReplicas int             `json:"desired_replicas"`
	ReadyReplicas   int             `json:"ready_replicas"`
	Labels          json.RawMessage `json:"labels,omitempty"`
	Annotations     json.RawMessage `json:"annotations,omitempty"`
	Conditions      json.RawMessage `json:"conditions,omitempty"`
	LastSeenAt      *time.Time      `json:"last_seen_at,omitempty"`
}

type podDTO struct {
	ID           uint64     `json:"id"`
	ClusterID    uint64     `json:"cluster_id"`
	Namespace    string     `json:"namespace"`
	Name         string     `json:"name"`
	UID          string     `json:"uid,omitempty"`
	NodeName     string     `json:"node_name,omitempty"`
	Phase        string     `json:"phase,omitempty"`
	OwnerKind    string     `json:"owner_kind,omitempty"`
	OwnerName    string     `json:"owner_name,omitempty"`
	RestartCount int        `json:"restart_count"`
	Reason       string     `json:"reason,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
}

type eventDTO struct {
	ID                  uint64     `json:"id"`
	ClusterID           uint64     `json:"cluster_id"`
	Namespace           string     `json:"namespace"`
	Name                string     `json:"name"`
	UID                 string     `json:"uid,omitempty"`
	Type                string     `json:"type,omitempty"`
	Reason              string     `json:"reason,omitempty"`
	Message             string     `json:"message,omitempty"`
	InvolvedKind        string     `json:"involved_kind,omitempty"`
	InvolvedNamespace   string     `json:"involved_namespace,omitempty"`
	InvolvedName        string     `json:"involved_name,omitempty"`
	InvolvedUID         string     `json:"involved_uid,omitempty"`
	SourceComponent     string     `json:"source_component,omitempty"`
	SourceHost          string     `json:"source_host,omitempty"`
	ReportingController string     `json:"reporting_controller,omitempty"`
	ReportingInstance   string     `json:"reporting_instance,omitempty"`
	Action              string     `json:"action,omitempty"`
	Count               int        `json:"count"`
	FirstTimestamp      *time.Time `json:"first_timestamp,omitempty"`
	LastTimestamp       *time.Time `json:"last_timestamp,omitempty"`
	EventTime           *time.Time `json:"event_time,omitempty"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
}

type enrollResponse struct {
	ClusterID        uint64 `json:"cluster_id"`
	Role             string `json:"role"`
	Mode             string `json:"mode"`
	EdgeID           uint64 `json:"edge_id"`
	AccessKey        string `json:"access_key"`
	SecretKey        string `json:"secret_key"`
	CloudAddr        string `json:"cloud_addr,omitempty"`
	ManagerPublicURL string `json:"manager_public_url,omitempty"`
}

func registrationDTO(in *biz.ClusterRegistration) clusterRegistrationDTO {
	return clusterRegistrationDTO{
		Cluster:        clusterDTOFromModel(in.Cluster),
		BootstrapToken: in.BootstrapToken,
		InstallCommand: in.InstallCommand,
	}
}

func clusterDTOFromModel(c *model.Cluster) clusterDTO {
	return clusterDTOFromModelWithCoverage(c, nil)
}

func clusterDTOFromModelWithCoverage(c *model.Cluster, coverage *biz.NodeCoverage) clusterDTO {
	if c == nil {
		return clusterDTO{}
	}
	var uid string
	if c.UID != nil {
		uid = *c.UID
	}
	status := clusterEffectiveStatus(c, time.Now().UTC())
	return clusterDTO{
		ID:                            c.ID,
		Name:                          c.Name,
		UID:                           uid,
		Mode:                          c.Mode,
		Status:                        status,
		Capabilities:                  clusterCapabilitiesFromModelWithCoverage(c, coverage),
		NodeEdgeCoverage:              nodeEdgeCoverageDTOFromBiz(coverage),
		ControllerEdgeID:              c.ControllerEdgeID,
		ControllerNodeName:            c.ControllerNodeName,
		ControllerNamespace:           c.ControllerNamespace,
		ControllerPodName:             c.ControllerPodName,
		Version:                       c.Version,
		LastSeenAt:                    c.LastSeenAt,
		InventoryResourceVersion:      c.InventoryResourceVersion,
		InventoryResourceVersionsJSON: c.InventoryResourceVersionsJSON,
		InventoryScope:                c.InventoryScope,
		InventoryNamespace:            c.InventoryNamespace,
		InventorySyncDurationMS:       c.InventorySyncDurationMS,
		InventoryWatchLagSeconds:      c.InventoryWatchLagSeconds,
		InventorySyncedAt:             c.InventorySyncedAt,
		BootstrapTokenExpiresAt:       c.BootstrapTokenExpiresAt,
		CreatedAt:                     c.CreatedAt,
		UpdatedAt:                     c.UpdatedAt,
	}
}

func clusterEffectiveStatus(c *model.Cluster, now time.Time) string {
	if c == nil {
		return ""
	}
	status := strings.TrimSpace(c.Status)
	if status != model.ClusterStatusOnline {
		return status
	}
	last := clusterLastActivityAt(c)
	if last == nil {
		return model.ClusterStatusOffline
	}
	if now.Sub(last.UTC()) > clusterOnlineTTL {
		return model.ClusterStatusOffline
	}
	return status
}

func clusterLastActivityAt(c *model.Cluster) *time.Time {
	if c == nil {
		return nil
	}
	var out *time.Time
	if c.LastSeenAt != nil {
		out = c.LastSeenAt
	}
	if c.InventorySyncedAt != nil && (out == nil || c.InventorySyncedAt.After(*out)) {
		out = c.InventorySyncedAt
	}
	return out
}

func nodeEdgeCoverageDTOFromBiz(coverage *biz.NodeCoverage) *nodeEdgeCoverageDTO {
	if coverage == nil {
		return nil
	}
	missing := coverage.Total - coverage.EdgeLinked
	if missing < 0 {
		missing = 0
	}
	percent := 0
	if coverage.Total > 0 {
		percent = int(coverage.EdgeLinked * 100 / coverage.Total)
	}
	return &nodeEdgeCoverageDTO{
		Total:        coverage.Total,
		EdgeLinked:   coverage.EdgeLinked,
		DeviceLinked: coverage.DeviceLinked,
		Missing:      missing,
		Percent:      percent,
	}
}

const (
	capabilityStatusReady         = "ready"
	capabilityStatusQueryReady    = "query-ready"
	capabilityStatusDegraded      = "degraded"
	capabilityStatusUnavailable   = "unavailable"
	capabilityStatusNotApplicable = "not-applicable"
)

func clusterCapabilitiesFromModel(c *model.Cluster) []clusterCapabilityDTO {
	return clusterCapabilitiesFromModelWithCoverage(c, nil)
}

func clusterCapabilitiesFromModelWithCoverage(c *model.Cluster, coverage *biz.NodeCoverage) []clusterCapabilityDTO {
	if c == nil {
		return nil
	}
	mode := strings.TrimSpace(c.Mode)
	serverless := mode == model.ModeServerless
	online := clusterEffectiveStatus(c, time.Now().UTC()) == model.ClusterStatusOnline
	hasController := online && c.ControllerEdgeID != nil && *c.ControllerEdgeID != 0
	hasInventory := online && strings.TrimSpace(c.InventoryResourceVersion) != ""

	return []clusterCapabilityDTO{
		{
			Key:    "inventory",
			Label:  "Inventory",
			Status: inventoryCapabilityStatus(hasController, hasInventory),
			Reason: inventoryCapabilityReason(hasController, hasInventory),
		},
		{
			Key:    "node-metrics",
			Label:  "Node metrics",
			Status: nodeMetricsCapabilityStatus(serverless, hasController, hasInventory, coverage),
			Reason: nodeMetricsCapabilityReason(serverless, hasController, hasInventory, coverage),
		},
		{
			Key:    "events",
			Label:  "Events",
			Status: eventsCapabilityStatus(hasController),
			Reason: eventsCapabilityReason(hasController),
		},
		{
			Key:    "telemetry",
			Label:  "Telemetry",
			Status: telemetryCapabilityStatus(hasController),
			Reason: telemetryCapabilityReason(serverless, hasController),
		},
		{
			Key:    "host-access",
			Label:  "Host access",
			Status: hostAccessCapabilityStatus(serverless, hasController, hasInventory, coverage),
			Reason: hostAccessCapabilityReason(serverless, hasController, hasInventory, coverage),
		},
	}
}

func inventoryCapabilityStatus(hasController, hasInventory bool) string {
	switch {
	case hasInventory:
		return capabilityStatusReady
	case hasController:
		return capabilityStatusDegraded
	default:
		return capabilityStatusUnavailable
	}
}

func inventoryCapabilityReason(hasController, hasInventory bool) string {
	switch {
	case hasInventory:
		return "inventory snapshot is available"
	case hasController:
		return "waiting for the first inventory snapshot"
	default:
		return "controller is not connected"
	}
}

func nodeMetricsCapabilityStatus(serverless, hasController, hasInventory bool, coverage *biz.NodeCoverage) string {
	switch {
	case serverless:
		return capabilityStatusNotApplicable
	case !hasController:
		return capabilityStatusUnavailable
	case coverage != nil && coverage.Total > 0 && coverage.EdgeLinked >= coverage.Total:
		return capabilityStatusReady
	case coverage != nil && coverage.Total > 0 && coverage.EdgeLinked > 0:
		return capabilityStatusDegraded
	case coverage != nil && coverage.Total > 0:
		return capabilityStatusUnavailable
	case !hasInventory:
		return capabilityStatusDegraded
	default:
		return capabilityStatusReady
	}
}

func nodeMetricsCapabilityReason(serverless, hasController, hasInventory bool, coverage *biz.NodeCoverage) string {
	switch {
	case serverless:
		return "serverless mode does not collect host metrics"
	case !hasController:
		return "controller is not connected"
	case coverage != nil && coverage.Total > 0:
		return "node metrics follow node edge coverage"
	case !hasInventory:
		return "waiting for node inventory"
	default:
		return "node metrics are available after node edge coverage is loaded"
	}
}

func eventsCapabilityStatus(hasController bool) string {
	if hasController {
		return capabilityStatusReady
	}
	return capabilityStatusUnavailable
}

func eventsCapabilityReason(hasController bool) string {
	if hasController {
		return "kubernetes events are watched by the controller"
	}
	return "controller is not connected"
}

func telemetryCapabilityStatus(hasController bool) string {
	if hasController {
		return capabilityStatusQueryReady
	}
	return capabilityStatusUnavailable
}

func telemetryCapabilityReason(serverless, hasController bool) string {
	if !hasController {
		return "controller is not connected"
	}
	if serverless {
		return "queries are scoped by namespace and workload"
	}
	return "queries are scoped by cluster_id"
}

func hostAccessCapabilityStatus(serverless, hasController, hasInventory bool, coverage *biz.NodeCoverage) string {
	switch {
	case serverless:
		return capabilityStatusNotApplicable
	case !hasController:
		return capabilityStatusUnavailable
	case coverage != nil && coverage.Total > 0 && coverage.EdgeLinked >= coverage.Total:
		return capabilityStatusReady
	case coverage != nil && coverage.Total > 0 && coverage.EdgeLinked > 0:
		return capabilityStatusDegraded
	case coverage != nil && coverage.Total > 0:
		return capabilityStatusUnavailable
	case !hasInventory:
		return capabilityStatusDegraded
	default:
		return capabilityStatusDegraded
	}
}

func hostAccessCapabilityReason(serverless, hasController, hasInventory bool, coverage *biz.NodeCoverage) string {
	switch {
	case serverless:
		return "serverless mode does not expose host operations"
	case !hasController:
		return "controller is not connected"
	case coverage != nil && coverage.Total > 0:
		return "host access follows node edge coverage"
	case !hasInventory:
		return "waiting for node inventory"
	default:
		return "host access depends on per-node edge coverage"
	}
}

func nodeDTOFromModel(n *model.Node) nodeDTO {
	if n == nil {
		return nodeDTO{}
	}
	return nodeDTO{
		ID:             n.ID,
		ClusterID:      n.ClusterID,
		NodeName:       n.NodeName,
		NodeUID:        n.NodeUID,
		ProviderID:     n.ProviderID,
		EdgeID:         n.EdgeID,
		DeviceID:       n.DeviceID,
		Labels:         rawJSON(n.LabelsJSON, "{}"),
		Taints:         rawJSON(n.TaintsJSON, "[]"),
		Conditions:     rawJSON(n.ConditionsJSON, "[]"),
		Capacity:       rawJSON(n.CapacityJSON, "{}"),
		Allocatable:    rawJSON(n.AllocatableJSON, "{}"),
		KubeletVersion: n.KubeletVersion,
		LastSeenAt:     n.LastSeenAt,
	}
}

func workloadDTOFromModel(item *model.Workload) workloadDTO {
	if item == nil {
		return workloadDTO{}
	}
	return workloadDTO{
		ID:              item.ID,
		ClusterID:       item.ClusterID,
		Namespace:       item.Namespace,
		Kind:            item.Kind,
		Name:            item.Name,
		UID:             item.UID,
		DesiredReplicas: item.DesiredReplicas,
		ReadyReplicas:   item.ReadyReplicas,
		Labels:          rawJSON(item.LabelsJSON, "{}"),
		Annotations:     rawJSON(item.AnnotationsJSON, "{}"),
		Conditions:      rawJSON(item.ConditionsJSON, "[]"),
		LastSeenAt:      item.LastSeenAt,
	}
}

func podDTOFromModel(item *model.Pod) podDTO {
	if item == nil {
		return podDTO{}
	}
	return podDTO{
		ID:           item.ID,
		ClusterID:    item.ClusterID,
		Namespace:    item.Namespace,
		Name:         item.Name,
		UID:          item.UID,
		NodeName:     item.NodeName,
		Phase:        item.Phase,
		OwnerKind:    item.OwnerKind,
		OwnerName:    item.OwnerName,
		RestartCount: item.RestartCount,
		Reason:       item.Reason,
		LastSeenAt:   item.LastSeenAt,
	}
}

func eventDTOFromModel(item *model.Event) eventDTO {
	if item == nil {
		return eventDTO{}
	}
	return eventDTO{
		ID:                  item.ID,
		ClusterID:           item.ClusterID,
		Namespace:           item.Namespace,
		Name:                item.Name,
		UID:                 item.UID,
		Type:                item.Type,
		Reason:              item.Reason,
		Message:             item.Message,
		InvolvedKind:        item.InvolvedKind,
		InvolvedNamespace:   item.InvolvedNamespace,
		InvolvedName:        item.InvolvedName,
		InvolvedUID:         item.InvolvedUID,
		SourceComponent:     item.SourceComponent,
		SourceHost:          item.SourceHost,
		ReportingController: item.ReportingController,
		ReportingInstance:   item.ReportingInstance,
		Action:              item.Action,
		Count:               item.Count,
		FirstTimestamp:      item.FirstTimestamp,
		LastTimestamp:       item.LastTimestamp,
		EventTime:           item.EventTime,
		LastSeenAt:          item.LastSeenAt,
	}
}

func rawJSON(s, fallback string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		s = fallback
	}
	return json.RawMessage(s)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.Join(errs.ErrInvalid, err)
	}
	return nil
}

func parseClusterID(r *http.Request) (uint64, error) {
	id, err := strconv.ParseUint(chi.URLParam(r, "cluster_id"), 10, 64)
	if err != nil || id == 0 {
		if err == nil {
			err = errors.New("cluster_id is required")
		}
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return id, nil
}

func parseIntDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func parseBoolDefault(raw string, fallback bool) bool {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func bearerToken(raw string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
	return token, token != ""
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
	_ = json.NewEncoder(w).Encode(errorBody{
		Error: err.Error(),
		Code:  errCode(err),
	})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrNotFound):
		return "not_found"
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
	default:
		return "internal"
	}
}
