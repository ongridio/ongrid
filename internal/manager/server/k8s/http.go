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

const maxListLimit = 500

type Service interface {
	CreateCluster(ctx context.Context, in biz.CreateClusterInput) (*biz.ClusterRegistration, error)
	ListClusters(ctx context.Context, f biz.ListClustersFilter) ([]*model.Cluster, error)
	CountClusters(ctx context.Context, f biz.ListClustersFilter) (int64, error)
	GetCluster(ctx context.Context, id uint64) (*model.Cluster, error)
	ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error)
	CountNodes(ctx context.Context, clusterID uint64) (int64, error)
	GetNodeCoverage(ctx context.Context, clusterID uint64) (biz.NodeCoverage, error)
	GetNodeCoverageByClusterIDs(ctx context.Context, clusterIDs []uint64) (map[uint64]biz.NodeCoverage, error)
	UpgradeCommand(cluster *model.Cluster) string
	ListEdgeAttachments(ctx context.Context, limit, offset int) ([]biz.EdgeAttachment, int64, error)
	GetClusterHealth(ctx context.Context, clusterID uint64) (biz.ClusterHealthSummary, error)
	ListWorkloads(ctx context.Context, f biz.ListWorkloadsFilter) ([]*model.Workload, error)
	CountWorkloads(ctx context.Context, f biz.ListWorkloadsFilter) (int64, error)
	ListPods(ctx context.Context, f biz.ListPodsFilter) ([]*model.Pod, error)
	CountPods(ctx context.Context, f biz.ListPodsFilter) (int64, error)
	ListEvents(ctx context.Context, f biz.ListEventsFilter) ([]*model.Event, error)
	CountEvents(ctx context.Context, f biz.ListEventsFilter) (int64, error)
	RotateBootstrapToken(ctx context.Context, id uint64) (*biz.ClusterRegistration, error)
	DeleteCluster(ctx context.Context, in biz.DeleteClusterInput) error
	Enroll(ctx context.Context, in biz.EnrollInput) (*biz.EnrollResult, error)
}

type Handler struct {
	svc         Service
	enrollSlots chan struct{}
}

func NewHandler(s Service) *Handler {
	return &Handler{svc: s, enrollSlots: make(chan struct{}, 64)}
}

func (h *Handler) RegisterProtected(r chi.Router) {
	r.With(h.requireAdmin).Post("/v1/k8s/clusters", h.createCluster)
	r.Get("/v1/k8s/clusters", h.listClusters)
	r.Get("/v1/k8s/edge-attachments", h.listEdgeAttachments)
	r.Get("/v1/k8s/clusters/{cluster_id}", h.getCluster)
	r.Get("/v1/k8s/clusters/{cluster_id}/health", h.getClusterHealth)
	r.Get("/v1/k8s/clusters/{cluster_id}/nodes", h.listNodes)
	r.Get("/v1/k8s/clusters/{cluster_id}/workloads", h.listWorkloads)
	r.Get("/v1/k8s/clusters/{cluster_id}/pods", h.listPods)
	r.Get("/v1/k8s/clusters/{cluster_id}/events", h.listEvents)
	r.With(h.requireAdmin).Post("/v1/k8s/clusters/{cluster_id}/rotate-token", h.rotateToken)
	r.With(h.requireAdmin).Delete("/v1/k8s/clusters/{cluster_id}", h.deleteCluster)
}

// @Summary List Kubernetes-managed Edge attachments
// @Router /api/v1/k8s/edge-attachments [get]
// @Success 200 {object} listEdgeAttachmentsResponse
func (h *Handler) listEdgeAttachments(w http.ResponseWriter, r *http.Request) {
	limit := parseListLimit(r.URL.Query().Get("limit"), maxListLimit)
	offset := parseListOffset(r.URL.Query().Get("offset"))
	items, total, err := h.svc.ListEdgeAttachments(r.Context(), limit, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]edgeAttachmentDTO, 0, len(items))
	for _, item := range items {
		dto = append(dto, edgeAttachmentDTO{
			EdgeID:      item.EdgeID,
			ClusterID:   item.ClusterID,
			ClusterName: item.ClusterName,
			ClusterMode: item.ClusterMode,
			NodeName:    item.NodeName,
			Kind:        item.Kind,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  dto,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// @Summary Get exact Kubernetes cluster health counters
// @Router /api/v1/k8s/clusters/{cluster_id}/health [get]
// @Success 200 {object} clusterHealthDTO
func (h *Handler) getClusterHealth(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := h.svc.GetClusterHealth(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterHealthDTO{
		DegradedWorkloads:    out.DegradedWorkloads,
		PendingPods:          out.PendingPods,
		CrashLoopBackOffPods: out.CrashLoopBackOffPods,
		OOMKilledPods:        out.OOMKilledPods,
		ImagePullBackOffPods: out.ImagePullBackOffPods,
		NotReadyNodes:        out.NotReadyNodes,
	})
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
	limit := parseListLimit(r.URL.Query().Get("limit"), 50)
	offset := parseListOffset(r.URL.Query().Get("offset"))
	filter := biz.ListClustersFilter{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Name:   strings.TrimSpace(r.URL.Query().Get("name")),
		Mode:   strings.TrimSpace(r.URL.Query().Get("mode")),
		Limit:  limit,
		Offset: offset,
	}
	items, err := h.svc.ListClusters(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	countFilter := filter
	countFilter.Limit = 0
	countFilter.Offset = 0
	total, err := h.svc.CountClusters(r.Context(), countFilter)
	if err != nil {
		writeErr(w, err)
		return
	}
	clusterIDs := make([]uint64, 0, len(items))
	for _, item := range items {
		clusterIDs = append(clusterIDs, item.ID)
	}
	coverageByCluster, err := h.svc.GetNodeCoverageByClusterIDs(r.Context(), clusterIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	dto := make([]clusterDTO, 0, len(items))
	for _, item := range items {
		coverage := coverageByCluster[item.ID]
		clusterDTO := clusterDTOFromModelWithCoverage(item, &coverage)
		clusterDTO.UpgradeCommand = h.svc.UpgradeCommand(item)
		dto = append(dto, clusterDTO)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  dto,
		"total":  total,
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
	dto := clusterDTOFromModelWithCoverage(c, &coverage)
	dto.UpgradeCommand = h.svc.UpgradeCommand(c)
	writeJSON(w, http.StatusOK, dto)
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
	limit := parseListLimit(r.URL.Query().Get("limit"), 100)
	offset := parseListOffset(r.URL.Query().Get("offset"))
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
	limit := parseListLimit(r.URL.Query().Get("limit"), 100)
	offset := parseListOffset(r.URL.Query().Get("offset"))
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
	limit := parseListLimit(r.URL.Query().Get("limit"), 100)
	offset := parseListOffset(r.URL.Query().Get("offset"))
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
	if err := h.svc.DeleteCluster(r.Context(), biz.DeleteClusterInput{
		ID:    id,
		Force: parseBoolDefault(r.URL.Query().Get("force"), false),
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// @Summary Enroll Kubernetes edge node or controller
// @Router /internal/k8s/enroll [post]
// @Success 200 {object} enrollResponse
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	select {
	case h.enrollSlots <- struct{}{}:
		defer func() { <-h.enrollSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeErr(w, errs.ErrTooManyAttempts)
		return
	}
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
	UpgradeCommand                string                 `json:"upgrade_command,omitempty"`
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
	Cluster            clusterDTO `json:"cluster"`
	BootstrapToken     string     `json:"bootstrap_token"`
	NodeBootstrapToken string     `json:"node_bootstrap_token"`
	InstallCommand     string     `json:"install_command"`
}

type clusterHealthDTO struct {
	DegradedWorkloads    int64 `json:"degraded_workloads"`
	PendingPods          int64 `json:"pending_pods"`
	CrashLoopBackOffPods int64 `json:"crash_loop_back_off_pods"`
	OOMKilledPods        int64 `json:"oom_killed_pods"`
	ImagePullBackOffPods int64 `json:"image_pull_back_off_pods"`
	NotReadyNodes        int64 `json:"not_ready_nodes"`
}

type edgeAttachmentDTO struct {
	EdgeID      uint64 `json:"edge_id"`
	ClusterID   uint64 `json:"cluster_id"`
	ClusterName string `json:"cluster_name"`
	ClusterMode string `json:"cluster_mode"`
	NodeName    string `json:"node_name,omitempty"`
	Kind        string `json:"kind"`
}

type listEdgeAttachmentsResponse struct {
	Items []edgeAttachmentDTO `json:"items"`
	Total int                 `json:"total"`
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
		Cluster:            clusterDTOFromModel(in.Cluster),
		BootstrapToken:     in.BootstrapToken,
		NodeBootstrapToken: in.NodeBootstrapToken,
		InstallCommand:     in.InstallCommand,
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
	status := biz.EffectiveClusterStatus(c, time.Now().UTC())
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
	capabilityStatusReady       = "ready"
	capabilityStatusQueryReady  = "query-ready"
	capabilityStatusDegraded    = "degraded"
	capabilityStatusUnavailable = "unavailable"
)

func clusterCapabilitiesFromModel(c *model.Cluster) []clusterCapabilityDTO {
	return clusterCapabilitiesFromModelWithCoverage(c, nil)
}

func clusterCapabilitiesFromModelWithCoverage(c *model.Cluster, coverage *biz.NodeCoverage) []clusterCapabilityDTO {
	if c == nil {
		return nil
	}
	online := biz.EffectiveClusterStatus(c, time.Now().UTC()) == model.ClusterStatusOnline
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
			Key:    "events",
			Label:  "Events",
			Status: eventsCapabilityStatus(hasController),
			Reason: eventsCapabilityReason(hasController),
		},
		{
			Key:    "telemetry",
			Label:  "Telemetry",
			Status: telemetryCapabilityStatus(hasController),
			Reason: telemetryCapabilityReason(hasController),
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

func telemetryCapabilityReason(hasController bool) string {
	if !hasController {
		return "controller is not connected"
	}
	return "queries are scoped by cluster_id"
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

func parseListLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if n <= 0 {
		return fallback
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

func parseListOffset(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
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
