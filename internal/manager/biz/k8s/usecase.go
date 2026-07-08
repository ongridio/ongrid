package k8s

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/passwd"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	defaultBootstrapTokenTTL = 90 * 24 * time.Hour
	bootstrapTokenBytes      = 32
)

// Repository is the k8s bounded context persistence contract.
type Repository interface {
	CreateCluster(ctx context.Context, c *model.Cluster) error
	GetCluster(ctx context.Context, id uint64) (*model.Cluster, error)
	GetClusterByControllerEdge(ctx context.Context, edgeID uint64) (*model.Cluster, error)
	ListClusters(ctx context.Context, f ListClustersFilter) ([]*model.Cluster, error)
	UpdateClusterToken(ctx context.Context, id uint64, tokenHash string, expiresAt *time.Time) error
	UpdateClusterController(ctx context.Context, id uint64, in ClusterControllerRegistration) error
	UpdateClusterInventorySync(ctx context.Context, id uint64, in ClusterInventorySync) error
	UpdateClusterTopologyNode(ctx context.Context, id, nodeID uint64) error
	ListClusterEdgeIDs(ctx context.Context, clusterID uint64) ([]uint64, error)
	DeleteCluster(ctx context.Context, id uint64) error

	GetNodeByClusterUID(ctx context.Context, clusterID uint64, nodeUID string) (*model.Node, error)
	GetLinkedNodeByClusterName(ctx context.Context, clusterID uint64, nodeName string) (*model.Node, error)
	UpsertNode(ctx context.Context, n *model.Node) error
	DeleteDuplicateNodesByName(ctx context.Context, clusterID uint64, nodeName, keepUID string) error
	UpdateNodeEdge(ctx context.Context, nodeID, edgeID uint64, deviceID *uint64, lastSeen time.Time) error
	UpsertWorkloads(ctx context.Context, items []*model.Workload) error
	UpsertPods(ctx context.Context, items []*model.Pod) error
	UpsertEvents(ctx context.Context, items []*model.Event) error
	DeleteNodes(ctx context.Context, clusterID uint64, refs []NodeRef) error
	DeleteWorkloads(ctx context.Context, clusterID uint64, refs []WorkloadRef) error
	DeletePods(ctx context.Context, clusterID uint64, refs []PodRef) error
	DeleteEvents(ctx context.Context, clusterID uint64, refs []EventRef) error
	DeleteStaleWorkloads(ctx context.Context, clusterID uint64, namespace *string, olderThan time.Time) error
	DeleteStalePods(ctx context.Context, clusterID uint64, namespace *string, olderThan time.Time) error
	DeleteStaleEvents(ctx context.Context, clusterID uint64, namespace *string, olderThan time.Time) error
	ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error)
	ListTopologyNodeLinks(ctx context.Context, clusterID uint64) ([]TopologyNodeLink, error)
	CountNodes(ctx context.Context, clusterID uint64) (int64, error)
	GetNodeCoverage(ctx context.Context, clusterID uint64) (NodeCoverage, error)
	ListWorkloads(ctx context.Context, f ListWorkloadsFilter) ([]*model.Workload, error)
	CountWorkloads(ctx context.Context, f ListWorkloadsFilter) (int64, error)
	ListPods(ctx context.Context, f ListPodsFilter) ([]*model.Pod, error)
	CountPods(ctx context.Context, f ListPodsFilter) (int64, error)
	ListEvents(ctx context.Context, f ListEventsFilter) ([]*model.Event, error)
	CountEvents(ctx context.Context, f ListEventsFilter) (int64, error)
	UpsertInstallation(ctx context.Context, in *model.Installation) error
}

// EdgeIssuer is the narrow bridge to the existing edge identity domain.
type EdgeIssuer interface {
	CreateEdgeIdentity(ctx context.Context, name string, createdBy *uint64) (*EdgeCredential, error)
	RotateEdgeSecret(ctx context.Context, edgeID uint64) (*EdgeCredential, error)
}

// EdgeRemover is implemented by the edge bounded context. K8s owns deciding
// which auto-created edge identities belong to a cluster; edge owns the actual
// edge/device cleanup semantics.
type EdgeRemover interface {
	DeleteEdge(ctx context.Context, edgeID uint64) error
}

type EdgeCredential struct {
	EdgeID    uint64
	AccessKey string
	SecretKey string
}

// TopologyMirror is the optional bridge from Kubernetes inventory into the
// generic topology graph. It is defined in this package so k8s owns the
// reconcile timing without depending on topology concrete types.
type TopologyMirror interface {
	EnsureKubernetesCluster(ctx context.Context, clusterID uint64, currentNodeID *uint64, name, uid, mode, status string) (uint64, error)
	EnsureKubernetesNodeMembership(ctx context.Context, clusterNodeID, deviceNodeID, clusterID, deviceID uint64, nodeName, nodeUID string) error
	PruneKubernetesNodeMemberships(ctx context.Context, clusterNodeID, clusterID uint64, keepDeviceNodeIDs []uint64) error
	DeleteKubernetesCluster(ctx context.Context, clusterID uint64, currentNodeID *uint64) error
	PruneDeletedKubernetesClusters(ctx context.Context, activeClusterIDs []uint64) error
}

type TopologyNodeLink struct {
	NodeName     string
	NodeUID      string
	DeviceID     *uint64
	DeviceNodeID *uint64
}

type NodeCoverage struct {
	ClusterID    uint64
	Total        int64
	EdgeLinked   int64
	DeviceLinked int64
}

type Config struct {
	PublicURL         string
	TunnelAddr        string
	BootstrapTokenTTL time.Duration
	ChartRef          string
	// ChartPath is kept for older callers/tests; prefer ChartRef.
	ChartPath string
}

type Usecase struct {
	repo        Repository
	edgeIssuer  EdgeIssuer
	edgeRemover EdgeRemover
	topology    TopologyMirror
	cfg         Config
}

func NewUsecase(repo Repository, edgeIssuer EdgeIssuer, cfg Config) *Usecase {
	if cfg.BootstrapTokenTTL <= 0 {
		cfg.BootstrapTokenTTL = defaultBootstrapTokenTTL
	}
	u := &Usecase{repo: repo, edgeIssuer: edgeIssuer, cfg: cfg}
	if remover, ok := edgeIssuer.(EdgeRemover); ok {
		u.edgeRemover = remover
	}
	return u
}

func (u *Usecase) SetTopologyMirror(m TopologyMirror) { u.topology = m }
func (u *Usecase) SetEdgeRemover(r EdgeRemover)       { u.edgeRemover = r }

func (u *Usecase) ReconcileTopology(ctx context.Context) error {
	if u.topology == nil {
		return nil
	}
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	const pageSize = 200
	activeClusterIDs := make([]uint64, 0, pageSize)
	for offset := 0; ; offset += pageSize {
		clusters, err := u.repo.ListClusters(ctx, ListClustersFilter{Limit: pageSize, Offset: offset})
		if err != nil {
			return err
		}
		for _, c := range clusters {
			if c == nil {
				continue
			}
			activeClusterIDs = append(activeClusterIDs, c.ID)
			if err := u.reconcileTopology(ctx, c.ID); err != nil {
				return fmt.Errorf("reconcile k8s topology for cluster %d: %w", c.ID, err)
			}
		}
		if len(clusters) < pageSize {
			if err := u.topology.PruneDeletedKubernetesClusters(ctx, activeClusterIDs); err != nil {
				return fmt.Errorf("prune deleted k8s topology clusters: %w", err)
			}
			return nil
		}
	}
}

type ListClustersFilter struct {
	Status string
	Name   string
	Mode   string
	Limit  int
	Offset int
}

type ListWorkloadsFilter struct {
	ClusterID uint64
	Namespace string
	Kind      string
	Query     string
	IssueOnly bool
	Limit     int
	Offset    int
}

type ListPodsFilter struct {
	ClusterID uint64
	Namespace string
	NodeName  string
	Phase     string
	Reason    string
	Query     string
	IssueOnly bool
	Limit     int
	Offset    int
}

type ListEventsFilter struct {
	ClusterID      uint64
	Namespace      string
	Type           string
	Reason         string
	InvolvedKind   string
	InvolvedName   string
	InvolvedPodUID string
	Query          string
	IssueOnly      bool
	Limit          int
	Offset         int
}

type ClusterInventorySync struct {
	ControllerEdgeID     uint64
	SyncedAt             time.Time
	ResourceVersion      string
	ResourceVersionsJSON string
	Scope                string
	Namespace            string
	SyncDurationMS       int64
	WatchLagSeconds      int64
}

type ClusterControllerRegistration struct {
	EdgeID    uint64
	LastSeen  time.Time
	NodeName  string
	Namespace string
	PodName   string
}

type NodeRef struct {
	Name string
	UID  string
}

type WorkloadRef struct {
	Kind      string
	Namespace string
	Name      string
	UID       string
}

type PodRef struct {
	Namespace string
	Name      string
	UID       string
}

type EventRef struct {
	Namespace string
	Name      string
	UID       string
}

type CreateClusterInput struct {
	Name      string
	UID       string
	Mode      string
	CreatedBy *uint64
}

type ClusterRegistration struct {
	Cluster        *model.Cluster
	BootstrapToken string
	InstallCommand string
}

func (u *Usecase) CreateCluster(ctx context.Context, in CreateClusterInput) (*ClusterRegistration, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster name is required"))
	}
	mode, err := normalizeMode(in.Mode)
	if err != nil {
		return nil, err
	}
	token, hash, expiresAt, err := newBootstrapToken(u.cfg.BootstrapTokenTTL)
	if err != nil {
		return nil, err
	}
	var uid *string
	if s := strings.TrimSpace(in.UID); s != "" {
		uid = &s
	}
	c := &model.Cluster{
		Name:                    name,
		UID:                     uid,
		Mode:                    mode,
		Status:                  model.ClusterStatusOffline,
		BootstrapTokenHash:      hash,
		BootstrapTokenExpiresAt: expiresAt,
		CreatedBy:               in.CreatedBy,
	}
	if err := u.repo.CreateCluster(ctx, c); err != nil {
		return nil, fmt.Errorf("create k8s cluster: %w", err)
	}
	if err := u.reconcileTopology(ctx, c.ID); err != nil {
		return nil, fmt.Errorf("reconcile k8s topology: %w", err)
	}
	return &ClusterRegistration{
		Cluster:        c,
		BootstrapToken: token,
		InstallCommand: u.installCommand(c.ID, mode, token),
	}, nil
}

func (u *Usecase) ListClusters(ctx context.Context, f ListClustersFilter) ([]*model.Cluster, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return u.repo.ListClusters(ctx, f)
}

func (u *Usecase) GetCluster(ctx context.Context, id uint64) (*model.Cluster, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.GetCluster(ctx, id)
}

func (u *Usecase) ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if clusterID == 0 {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if _, err := u.repo.GetCluster(ctx, clusterID); err != nil {
		return nil, err
	}
	return u.repo.ListNodes(ctx, clusterID)
}

func (u *Usecase) CountNodes(ctx context.Context, clusterID uint64) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if clusterID == 0 {
		return 0, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if _, err := u.repo.GetCluster(ctx, clusterID); err != nil {
		return 0, err
	}
	return u.repo.CountNodes(ctx, clusterID)
}

func (u *Usecase) GetNodeCoverage(ctx context.Context, clusterID uint64) (NodeCoverage, error) {
	if u.repo == nil {
		return NodeCoverage{}, errs.ErrNotWiredYet
	}
	if clusterID == 0 {
		return NodeCoverage{}, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if _, err := u.repo.GetCluster(ctx, clusterID); err != nil {
		return NodeCoverage{}, err
	}
	return u.repo.GetNodeCoverage(ctx, clusterID)
}

func (u *Usecase) ListWorkloads(ctx context.Context, f ListWorkloadsFilter) ([]*model.Workload, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if f.ClusterID == 0 {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if _, err := u.repo.GetCluster(ctx, f.ClusterID); err != nil {
		return nil, err
	}
	return u.repo.ListWorkloads(ctx, f)
}

func (u *Usecase) CountWorkloads(ctx context.Context, f ListWorkloadsFilter) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if f.ClusterID == 0 {
		return 0, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if _, err := u.repo.GetCluster(ctx, f.ClusterID); err != nil {
		return 0, err
	}
	return u.repo.CountWorkloads(ctx, f)
}

func (u *Usecase) ListPods(ctx context.Context, f ListPodsFilter) ([]*model.Pod, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if f.ClusterID == 0 {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if _, err := u.repo.GetCluster(ctx, f.ClusterID); err != nil {
		return nil, err
	}
	return u.repo.ListPods(ctx, f)
}

func (u *Usecase) CountPods(ctx context.Context, f ListPodsFilter) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if f.ClusterID == 0 {
		return 0, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if _, err := u.repo.GetCluster(ctx, f.ClusterID); err != nil {
		return 0, err
	}
	return u.repo.CountPods(ctx, f)
}

func (u *Usecase) ListEvents(ctx context.Context, f ListEventsFilter) ([]*model.Event, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if f.ClusterID == 0 {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if _, err := u.repo.GetCluster(ctx, f.ClusterID); err != nil {
		return nil, err
	}
	return u.repo.ListEvents(ctx, f)
}

func (u *Usecase) CountEvents(ctx context.Context, f ListEventsFilter) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if f.ClusterID == 0 {
		return 0, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	if _, err := u.repo.GetCluster(ctx, f.ClusterID); err != nil {
		return 0, err
	}
	return u.repo.CountEvents(ctx, f)
}

func (u *Usecase) RotateBootstrapToken(ctx context.Context, id uint64) (*ClusterRegistration, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	c, err := u.repo.GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	token, hash, expiresAt, err := newBootstrapToken(u.cfg.BootstrapTokenTTL)
	if err != nil {
		return nil, err
	}
	if err := u.repo.UpdateClusterToken(ctx, id, hash, expiresAt); err != nil {
		return nil, fmt.Errorf("rotate k8s bootstrap token: %w", err)
	}
	c.BootstrapTokenHash = hash
	c.BootstrapTokenExpiresAt = expiresAt
	return &ClusterRegistration{
		Cluster:        c,
		BootstrapToken: token,
		InstallCommand: u.installCommand(c.ID, c.Mode, token),
	}, nil
}

func (u *Usecase) DeleteCluster(ctx context.Context, id uint64) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	c, err := u.repo.GetCluster(ctx, id)
	if err != nil {
		return err
	}
	edgeIDs, err := u.repo.ListClusterEdgeIDs(ctx, id)
	if err != nil {
		return fmt.Errorf("list k8s cluster edges: %w", err)
	}
	if u.topology != nil {
		if err := u.topology.DeleteKubernetesCluster(ctx, c.ID, c.NodeID); err != nil {
			return fmt.Errorf("delete k8s topology for cluster %d: %w", c.ID, err)
		}
	}
	if err := u.deleteClusterEdges(ctx, edgeIDs); err != nil {
		return err
	}
	if err := u.repo.DeleteCluster(ctx, id); err != nil {
		return fmt.Errorf("delete k8s cluster: %w", err)
	}
	return nil
}

func (u *Usecase) deleteClusterEdges(ctx context.Context, edgeIDs []uint64) error {
	if u.edgeRemover == nil || len(edgeIDs) == 0 {
		return nil
	}
	for _, edgeID := range uniqueNonZeroUint64(edgeIDs) {
		if err := u.edgeRemover.DeleteEdge(ctx, edgeID); err != nil && !errors.Is(err, errs.ErrNotFound) {
			return fmt.Errorf("delete k8s edge %d: %w", edgeID, err)
		}
	}
	return nil
}

type EnrollInput struct {
	BootstrapToken string
	ClusterID      uint64
	ClusterUID     string
	Role           string
	NodeName       string
	NodeUID        string
	ProviderID     string
	Namespace      string
	AgentVersion   string
	Capabilities   []string
}

type EnrollResult struct {
	ClusterID        uint64
	Role             string
	Mode             string
	EdgeID           uint64
	AccessKey        string
	SecretKey        string
	CloudAddr        string
	ManagerPublicURL string
}

func (u *Usecase) Enroll(ctx context.Context, in EnrollInput) (*EnrollResult, error) {
	if u.repo == nil || u.edgeIssuer == nil {
		return nil, errs.ErrNotWiredYet
	}
	c, err := u.repo.GetCluster(ctx, in.ClusterID)
	if err != nil {
		return nil, err
	}
	if !validBootstrapToken(strings.TrimSpace(in.BootstrapToken), c) {
		return nil, errs.ErrUnauthorized
	}
	role := normalizeRole(in.Role)
	now := time.Now()
	switch role {
	case model.RoleNode:
		return u.enrollNode(ctx, c, in, now)
	case model.RoleController, model.RoleServerlessController:
		return u.enrollController(ctx, c, role, in, now)
	default:
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("unsupported k8s enroll role %q", in.Role))
	}
}

// HandleRegister reconciles the optional KubernetesInfo attached to register_edge.
// It is intentionally separate from Enroll: enroll proves bootstrap-token
// possession and issues credentials, while register proves the tunnel identity is
// now online and lets us attach the eventual host device id for node mode.
func (u *Usecase) HandleRegister(ctx context.Context, edgeID uint64, deviceID *uint64, info tunnel.KubernetesInfo) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if info.ClusterID == 0 {
		return errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	now := time.Now()
	switch normalizeRole(info.Role) {
	case model.RoleNode:
		nodeName := strings.TrimSpace(info.NodeName)
		if nodeName == "" {
			nodeName = "edge-" + strconv.FormatUint(edgeID, 10)
		}
		nodeUID := strings.TrimSpace(info.NodeUID)
		if nodeUID == "" {
			if existing, err := u.repo.GetLinkedNodeByClusterName(ctx, info.ClusterID, nodeName); err == nil && strings.TrimSpace(existing.NodeUID) != "" {
				nodeUID = existing.NodeUID
			} else {
				nodeUID = "name:" + nodeName
			}
		}
		ts := now
		if err := u.repo.UpsertNode(ctx, &model.Node{
			ClusterID:  info.ClusterID,
			NodeName:   nodeName,
			NodeUID:    nodeUID,
			EdgeID:     &edgeID,
			DeviceID:   deviceID,
			LastSeenAt: &ts,
		}); err != nil {
			return err
		}
		return u.reconcileTopology(ctx, info.ClusterID)
	case model.RoleController, model.RoleServerlessController:
		if err := u.repo.UpdateClusterController(ctx, info.ClusterID, ClusterControllerRegistration{
			EdgeID:    edgeID,
			LastSeen:  now,
			NodeName:  strings.TrimSpace(info.NodeName),
			Namespace: strings.TrimSpace(info.Namespace),
			PodName:   strings.TrimSpace(info.PodName),
		}); err != nil {
			return err
		}
		mode := strings.TrimSpace(info.Mode)
		if mode == "" {
			mode = model.ModeFullNode
		}
		if normalizeRole(info.Role) == model.RoleServerlessController {
			mode = model.ModeServerless
		}
		scopeType := "cluster"
		if strings.TrimSpace(info.Namespace) != "" {
			scopeType = "namespace"
		}
		ts := now
		if err := u.repo.UpsertInstallation(ctx, &model.Installation{
			ClusterID:        info.ClusterID,
			Mode:             mode,
			ScopeType:        scopeType,
			Namespace:        strings.TrimSpace(info.Namespace),
			ControllerEdgeID: &edgeID,
			LastSeenAt:       &ts,
		}); err != nil {
			return err
		}
		return u.reconcileTopology(ctx, info.ClusterID)
	default:
		return errors.Join(errs.ErrInvalid, fmt.Errorf("unsupported k8s register role %q", info.Role))
	}
}

func (u *Usecase) LookupControllerCluster(ctx context.Context, edgeID uint64) (uint64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if edgeID == 0 {
		return 0, nil
	}
	c, err := u.repo.GetClusterByControllerEdge(ctx, edgeID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return c.ID, nil
}

func (u *Usecase) reconcileTopology(ctx context.Context, clusterID uint64) error {
	if u.topology == nil {
		return nil
	}
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	c, err := u.repo.GetCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	uid := ""
	if c.UID != nil {
		uid = strings.TrimSpace(*c.UID)
	}
	clusterNodeID, err := u.topology.EnsureKubernetesCluster(ctx, c.ID, c.NodeID, c.Name, uid, c.Mode, c.Status)
	if err != nil {
		return err
	}
	if c.NodeID == nil || *c.NodeID != clusterNodeID {
		if err := u.repo.UpdateClusterTopologyNode(ctx, c.ID, clusterNodeID); err != nil {
			return fmt.Errorf("link k8s cluster topology node: %w", err)
		}
	}
	links, err := u.repo.ListTopologyNodeLinks(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("list k8s topology node links: %w", err)
	}
	keep := make([]uint64, 0, len(links))
	for _, link := range links {
		if link.DeviceID == nil || *link.DeviceID == 0 || link.DeviceNodeID == nil || *link.DeviceNodeID == 0 {
			continue
		}
		deviceNodeID := *link.DeviceNodeID
		if err := u.topology.EnsureKubernetesNodeMembership(ctx, clusterNodeID, deviceNodeID, c.ID, *link.DeviceID, link.NodeName, link.NodeUID); err != nil {
			return fmt.Errorf("ensure k8s node topology membership %q: %w", link.NodeName, err)
		}
		keep = append(keep, deviceNodeID)
	}
	if err := u.topology.PruneKubernetesNodeMemberships(ctx, clusterNodeID, c.ID, keep); err != nil {
		return fmt.Errorf("prune k8s node topology memberships: %w", err)
	}
	return nil
}

func (u *Usecase) enrollNode(ctx context.Context, c *model.Cluster, in EnrollInput, now time.Time) (*EnrollResult, error) {
	nodeName := strings.TrimSpace(in.NodeName)
	if nodeName == "" {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("node_name is required"))
	}
	nodeUID := strings.TrimSpace(in.NodeUID)
	if nodeUID == "" {
		if existing, err := u.repo.GetLinkedNodeByClusterName(ctx, c.ID, nodeName); err == nil && strings.TrimSpace(existing.NodeUID) != "" {
			nodeUID = existing.NodeUID
		} else {
			nodeUID = "name:" + nodeName
		}
	}
	ts := now
	if err := u.repo.UpsertNode(ctx, &model.Node{
		ClusterID:  c.ID,
		NodeName:   nodeName,
		NodeUID:    nodeUID,
		ProviderID: strings.TrimSpace(in.ProviderID),
		LastSeenAt: &ts,
	}); err != nil {
		return nil, fmt.Errorf("upsert k8s node: %w", err)
	}
	n, err := u.repo.GetNodeByClusterUID(ctx, c.ID, nodeUID)
	if err != nil {
		return nil, err
	}

	cred, err := u.issueNodeCredential(ctx, c, n)
	if err != nil {
		return nil, err
	}
	if err := u.repo.UpdateNodeEdge(ctx, n.ID, cred.EdgeID, nil, now); err != nil {
		return nil, fmt.Errorf("link k8s node edge: %w", err)
	}
	return u.enrollResult(c.ID, model.RoleNode, c.Mode, cred), nil
}

type InventoryResult struct {
	AcceptedNodes     int
	AcceptedWorkloads int
	AcceptedPods      int
	AcceptedEvents    int
}

func (u *Usecase) IngestInventory(ctx context.Context, edgeID uint64, in tunnel.KubernetesInventoryRequest) (*InventoryResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if edgeID == 0 {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("edge_id is required"))
	}
	if in.ClusterID == 0 {
		return nil, errors.Join(errs.ErrInvalid, fmt.Errorf("cluster_id is required"))
	}
	c, err := u.repo.GetCluster(ctx, in.ClusterID)
	if err != nil {
		return nil, err
	}
	if c.ControllerEdgeID != nil && *c.ControllerEdgeID != 0 && *c.ControllerEdgeID != edgeID {
		return nil, errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not controller for cluster %d", edgeID, in.ClusterID))
	}
	receivedAt := time.Now().UTC()
	now := receivedAt
	if in.Ts > 0 {
		now = time.Unix(in.Ts, 0).UTC()
	}
	syncType := normalizeInventorySyncType(in.SyncType)
	if syncType == inventorySyncDelta {
		if err := u.deleteInventoryDeltas(ctx, in.ClusterID, in); err != nil {
			return nil, err
		}
	}

	nodes := make([]*model.Node, 0, len(in.Nodes))
	for _, item := range in.Nodes {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		uid := strings.TrimSpace(item.UID)
		if uid == "" {
			uid = "name:" + name
		}
		ts := now
		nodes = append(nodes, &model.Node{
			ClusterID:       in.ClusterID,
			NodeName:        name,
			NodeUID:         uid,
			ProviderID:      strings.TrimSpace(item.ProviderID),
			LabelsJSON:      jsonText(item.Labels, "{}"),
			TaintsJSON:      jsonText(item.Taints, "[]"),
			ConditionsJSON:  jsonText(item.Conditions, "[]"),
			CapacityJSON:    jsonText(item.Capacity, "{}"),
			AllocatableJSON: jsonText(item.Allocatable, "{}"),
			KubeletVersion:  strings.TrimSpace(item.KubeletVersion),
			LastSeenAt:      &ts,
		})
	}
	for _, n := range nodes {
		if linked, err := u.repo.GetLinkedNodeByClusterName(ctx, n.ClusterID, n.NodeName); err == nil && linked.NodeUID != n.NodeUID {
			n.EdgeID = linked.EdgeID
			n.DeviceID = linked.DeviceID
		}
		if err := u.repo.UpsertNode(ctx, n); err != nil {
			return nil, fmt.Errorf("upsert k8s inventory node %q: %w", n.NodeName, err)
		}
		if err := u.repo.DeleteDuplicateNodesByName(ctx, n.ClusterID, n.NodeName, n.NodeUID); err != nil {
			return nil, fmt.Errorf("delete duplicate k8s node %q: %w", n.NodeName, err)
		}
	}

	workloads := make([]*model.Workload, 0, len(in.Workloads))
	for _, item := range in.Workloads {
		kind := strings.TrimSpace(item.Kind)
		name := strings.TrimSpace(item.Name)
		if kind == "" || name == "" {
			continue
		}
		ts := now
		workloads = append(workloads, &model.Workload{
			ClusterID:       in.ClusterID,
			Namespace:       strings.TrimSpace(item.Namespace),
			Kind:            kind,
			Name:            name,
			UID:             strings.TrimSpace(item.UID),
			DesiredReplicas: item.DesiredReplicas,
			ReadyReplicas:   item.ReadyReplicas,
			LabelsJSON:      jsonText(item.Labels, "{}"),
			AnnotationsJSON: jsonText(item.Annotations, "{}"),
			ConditionsJSON:  jsonText(item.Conditions, "[]"),
			LastSeenAt:      &ts,
		})
	}
	if err := u.repo.UpsertWorkloads(ctx, workloads); err != nil {
		return nil, fmt.Errorf("upsert k8s workloads: %w", err)
	}

	pods := make([]*model.Pod, 0, len(in.Pods))
	for _, item := range in.Pods {
		name := strings.TrimSpace(item.Name)
		uid := strings.TrimSpace(item.UID)
		if name == "" || uid == "" {
			continue
		}
		ts := now
		pods = append(pods, &model.Pod{
			ClusterID:    in.ClusterID,
			Namespace:    strings.TrimSpace(item.Namespace),
			Name:         name,
			UID:          uid,
			NodeName:     strings.TrimSpace(item.NodeName),
			Phase:        strings.TrimSpace(item.Phase),
			OwnerKind:    strings.TrimSpace(item.OwnerKind),
			OwnerName:    strings.TrimSpace(item.OwnerName),
			RestartCount: item.RestartCount,
			Reason:       strings.TrimSpace(item.Reason),
			LastSeenAt:   &ts,
		})
	}
	if err := u.repo.UpsertPods(ctx, pods); err != nil {
		return nil, fmt.Errorf("upsert k8s pods: %w", err)
	}

	events := make([]*model.Event, 0, len(in.Events))
	for _, item := range in.Events {
		name := strings.TrimSpace(item.Name)
		uid := strings.TrimSpace(item.UID)
		if name == "" || uid == "" {
			continue
		}
		ts := now
		events = append(events, &model.Event{
			ClusterID:           in.ClusterID,
			Namespace:           strings.TrimSpace(item.Namespace),
			Name:                name,
			UID:                 uid,
			Type:                strings.TrimSpace(item.Type),
			Reason:              strings.TrimSpace(item.Reason),
			Message:             strings.TrimSpace(item.Message),
			InvolvedKind:        strings.TrimSpace(item.InvolvedKind),
			InvolvedNamespace:   strings.TrimSpace(item.InvolvedNamespace),
			InvolvedName:        strings.TrimSpace(item.InvolvedName),
			InvolvedUID:         strings.TrimSpace(item.InvolvedUID),
			SourceComponent:     strings.TrimSpace(item.SourceComponent),
			SourceHost:          strings.TrimSpace(item.SourceHost),
			ReportingController: strings.TrimSpace(item.ReportingController),
			ReportingInstance:   strings.TrimSpace(item.ReportingInstance),
			Action:              strings.TrimSpace(item.Action),
			Count:               item.Count,
			FirstTimestamp:      parseK8sTimestamp(item.FirstTimestamp),
			LastTimestamp:       parseK8sTimestamp(item.LastTimestamp),
			EventTime:           parseK8sTimestamp(item.EventTime),
			LastSeenAt:          &ts,
		})
	}
	if err := u.repo.UpsertEvents(ctx, events); err != nil {
		return nil, fmt.Errorf("upsert k8s events: %w", err)
	}
	if syncType == inventorySyncFull {
		if namespace, ok := inventoryPruneNamespace(in); ok {
			olderThan := now.Add(-time.Second)
			if err := u.repo.DeleteStaleWorkloads(ctx, in.ClusterID, namespace, olderThan); err != nil {
				return nil, fmt.Errorf("delete stale k8s workloads: %w", err)
			}
			if err := u.repo.DeleteStalePods(ctx, in.ClusterID, namespace, olderThan); err != nil {
				return nil, fmt.Errorf("delete stale k8s pods: %w", err)
			}
			if err := u.repo.DeleteStaleEvents(ctx, in.ClusterID, namespace, olderThan); err != nil {
				return nil, fmt.Errorf("delete stale k8s events: %w", err)
			}
		}
	}
	if err := u.repo.UpdateClusterInventorySync(ctx, in.ClusterID, ClusterInventorySync{
		ControllerEdgeID:     edgeID,
		SyncedAt:             now,
		ResourceVersion:      strings.TrimSpace(in.ResourceVersion),
		ResourceVersionsJSON: jsonText(in.ResourceVersions, "{}"),
		Scope:                strings.TrimSpace(in.Scope),
		Namespace:            strings.TrimSpace(in.Namespace),
		SyncDurationMS:       nonNegativeInt64(in.CollectDurationMS),
		WatchLagSeconds:      inventoryWatchLagSeconds(receivedAt, in.WatchEventObservedAt),
	}); err != nil {
		return nil, fmt.Errorf("refresh k8s inventory sync: %w", err)
	}
	if err := u.reconcileTopology(ctx, in.ClusterID); err != nil {
		return nil, fmt.Errorf("reconcile k8s topology: %w", err)
	}

	return &InventoryResult{
		AcceptedNodes:     len(nodes),
		AcceptedWorkloads: len(workloads),
		AcceptedPods:      len(pods),
		AcceptedEvents:    len(events),
	}, nil
}

const (
	inventorySyncFull  = "full"
	inventorySyncDelta = "delta"
)

func normalizeInventorySyncType(syncType string) string {
	switch strings.TrimSpace(syncType) {
	case inventorySyncDelta:
		return inventorySyncDelta
	default:
		return inventorySyncFull
	}
}

func (u *Usecase) deleteInventoryDeltas(ctx context.Context, clusterID uint64, in tunnel.KubernetesInventoryRequest) error {
	if len(in.DeletedNodes) > 0 {
		if err := u.repo.DeleteNodes(ctx, clusterID, toNodeRefs(in.DeletedNodes)); err != nil {
			return fmt.Errorf("delete k8s inventory nodes: %w", err)
		}
	}
	if len(in.DeletedWorkloads) > 0 {
		if err := u.repo.DeleteWorkloads(ctx, clusterID, toWorkloadRefs(in.DeletedWorkloads)); err != nil {
			return fmt.Errorf("delete k8s inventory workloads: %w", err)
		}
	}
	if len(in.DeletedPods) > 0 {
		if err := u.repo.DeletePods(ctx, clusterID, toPodRefs(in.DeletedPods)); err != nil {
			return fmt.Errorf("delete k8s inventory pods: %w", err)
		}
	}
	if len(in.DeletedEvents) > 0 {
		if err := u.repo.DeleteEvents(ctx, clusterID, toEventRefs(in.DeletedEvents)); err != nil {
			return fmt.Errorf("delete k8s inventory events: %w", err)
		}
	}
	return nil
}

func toNodeRefs(in []tunnel.KubernetesNodeRef) []NodeRef {
	out := make([]NodeRef, 0, len(in))
	for _, item := range in {
		out = append(out, NodeRef{Name: strings.TrimSpace(item.Name), UID: strings.TrimSpace(item.UID)})
	}
	return out
}

func toWorkloadRefs(in []tunnel.KubernetesWorkloadRef) []WorkloadRef {
	out := make([]WorkloadRef, 0, len(in))
	for _, item := range in {
		out = append(out, WorkloadRef{
			Kind:      strings.TrimSpace(item.Kind),
			Namespace: strings.TrimSpace(item.Namespace),
			Name:      strings.TrimSpace(item.Name),
			UID:       strings.TrimSpace(item.UID),
		})
	}
	return out
}

func toPodRefs(in []tunnel.KubernetesPodRef) []PodRef {
	out := make([]PodRef, 0, len(in))
	for _, item := range in {
		out = append(out, PodRef{
			Namespace: strings.TrimSpace(item.Namespace),
			Name:      strings.TrimSpace(item.Name),
			UID:       strings.TrimSpace(item.UID),
		})
	}
	return out
}

func toEventRefs(in []tunnel.KubernetesEventRef) []EventRef {
	out := make([]EventRef, 0, len(in))
	for _, item := range in {
		out = append(out, EventRef{
			Namespace: strings.TrimSpace(item.Namespace),
			Name:      strings.TrimSpace(item.Name),
			UID:       strings.TrimSpace(item.UID),
		})
	}
	return out
}

func inventoryPruneNamespace(in tunnel.KubernetesInventoryRequest) (*string, bool) {
	switch strings.TrimSpace(in.Scope) {
	case "cluster":
		return nil, true
	case "namespace":
		ns := strings.TrimSpace(in.Namespace)
		if ns == "" {
			return nil, false
		}
		return &ns, true
	default:
		return nil, false
	}
}

func inventoryWatchLagSeconds(receivedAt time.Time, observedUnix int64) int64 {
	if observedUnix <= 0 {
		return 0
	}
	observedAt := time.Unix(observedUnix, 0).UTC()
	lag := receivedAt.Sub(observedAt)
	if lag <= 0 {
		return 0
	}
	return int64(lag.Seconds())
}

func nonNegativeInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func parseK8sTimestamp(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	return &t
}

func uniqueNonZeroUint64(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(values))
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (u *Usecase) enrollController(ctx context.Context, c *model.Cluster, role string, in EnrollInput, now time.Time) (*EnrollResult, error) {
	var cred *EdgeCredential
	var err error
	if c.ControllerEdgeID == nil || *c.ControllerEdgeID == 0 {
		cred, err = u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, "controller"), c.CreatedBy)
	} else {
		cred, err = u.edgeIssuer.RotateEdgeSecret(ctx, *c.ControllerEdgeID)
		if errors.Is(err, errs.ErrNotFound) {
			cred, err = u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, "controller"), c.CreatedBy)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := u.repo.UpdateClusterController(ctx, c.ID, ClusterControllerRegistration{
		EdgeID:    cred.EdgeID,
		LastSeen:  now,
		NodeName:  strings.TrimSpace(in.NodeName),
		Namespace: strings.TrimSpace(in.Namespace),
	}); err != nil {
		return nil, fmt.Errorf("link k8s controller edge: %w", err)
	}
	mode := c.Mode
	scopeType := "cluster"
	if role == model.RoleServerlessController {
		mode = model.ModeServerless
		scopeType = "namespace"
		if strings.TrimSpace(in.Namespace) == "" {
			scopeType = "cluster"
		}
	}
	capabilitiesJSON := mustJSON(in.Capabilities)
	ts := now
	if err := u.repo.UpsertInstallation(ctx, &model.Installation{
		ClusterID:        c.ID,
		Mode:             mode,
		ScopeType:        scopeType,
		Namespace:        strings.TrimSpace(in.Namespace),
		ControllerEdgeID: &cred.EdgeID,
		CapabilitiesJSON: capabilitiesJSON,
		LastSeenAt:       &ts,
	}); err != nil {
		return nil, fmt.Errorf("upsert k8s installation: %w", err)
	}
	return u.enrollResult(c.ID, role, mode, cred), nil
}

func (u *Usecase) issueNodeCredential(ctx context.Context, c *model.Cluster, n *model.Node) (*EdgeCredential, error) {
	if n.EdgeID == nil || *n.EdgeID == 0 {
		return u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, n.NodeName), c.CreatedBy)
	}
	cred, err := u.edgeIssuer.RotateEdgeSecret(ctx, *n.EdgeID)
	if errors.Is(err, errs.ErrNotFound) {
		return u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, n.NodeName), c.CreatedBy)
	}
	return cred, err
}

func (u *Usecase) enrollResult(clusterID uint64, role, mode string, cred *EdgeCredential) *EnrollResult {
	return &EnrollResult{
		ClusterID:        clusterID,
		Role:             role,
		Mode:             mode,
		EdgeID:           cred.EdgeID,
		AccessKey:        cred.AccessKey,
		SecretKey:        cred.SecretKey,
		CloudAddr:        u.cfg.TunnelAddr,
		ManagerPublicURL: u.cfg.PublicURL,
	}
}

func validBootstrapToken(token string, c *model.Cluster) bool {
	if token == "" || c == nil || c.BootstrapTokenHash == "" {
		return false
	}
	if c.BootstrapTokenExpiresAt != nil && time.Now().After(*c.BootstrapTokenExpiresAt) {
		return false
	}
	return passwd.Verify(token, c.BootstrapTokenHash)
}

func newBootstrapToken(ttl time.Duration) (token string, hash string, expiresAt *time.Time, err error) {
	token, err = randomURLSafe(bootstrapTokenBytes)
	if err != nil {
		return "", "", nil, fmt.Errorf("gen k8s bootstrap token: %w", err)
	}
	hash, err = passwd.Hash(token)
	if err != nil {
		return "", "", nil, fmt.Errorf("hash k8s bootstrap token: %w", err)
	}
	exp := time.Now().Add(ttl)
	return token, hash, &exp, nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func normalizeMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "", model.ModeFullNode:
		return model.ModeFullNode, nil
	case model.ModeServerless:
		return model.ModeServerless, nil
	default:
		return "", errors.Join(errs.ErrInvalid, fmt.Errorf("unsupported k8s mode %q", mode))
	}
}

func normalizeRole(role string) string {
	switch strings.TrimSpace(role) {
	case model.RoleServerlessController:
		return model.RoleServerlessController
	case model.RoleController:
		return model.RoleController
	case model.RoleNode:
		return model.RoleNode
	default:
		return strings.TrimSpace(role)
	}
}

func edgeName(clusterName, part string) string {
	name := "k8s:" + strings.TrimSpace(clusterName) + ":" + strings.TrimSpace(part)
	if len(name) <= 128 {
		return name
	}
	return name[:128]
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func jsonText(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return fallback
	}
	return string(b)
}

func (u *Usecase) installCommand(clusterID uint64, mode, token string) string {
	publicURL, tunnelAddr := installEndpoints(u.cfg.PublicURL, u.cfg.TunnelAddr)
	chartRef := installChartRef(u.cfg, publicURL)
	args := []string{
		"helm upgrade --install ongrid-edge",
		shellQuote(chartRef),
	}
	if strings.HasPrefix(strings.ToLower(chartRef), "https://") {
		args = append(args, "--insecure-skip-tls-verify")
	}
	args = append(args,
		"--namespace ongrid-system --create-namespace",
		"--set namespace.create=false",
		"--set-string manager.publicURL="+shellQuote(publicURL),
		"--set-string manager.tunnelAddr="+shellQuote(tunnelAddr),
		"--set-string manager.tlsInsecure=true",
		"--set-string enrollment.clusterID="+strconv.FormatUint(clusterID, 10),
		"--set-string enrollment.bootstrapToken="+shellQuote(token),
		"--set-string mode="+shellQuote(mode),
	)
	return strings.Join(args, " ")
}

func installChartRef(cfg Config, publicURL string) string {
	if chartRef := strings.TrimSpace(cfg.ChartRef); chartRef != "" {
		return chartRef
	}
	if chartPath := strings.TrimSpace(cfg.ChartPath); chartPath != "" {
		return chartPath
	}
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		publicURL = "https://<manager>"
	}
	return publicURL + "/edge/k8s/ongrid-edge.tgz"
}

func installEndpoints(rawPublicURL, rawTunnelAddr string) (string, string) {
	publicURL := strings.TrimSpace(rawPublicURL)
	tunnelAddr := strings.TrimSpace(rawTunnelAddr)
	publicHost := hostFromPublicURL(publicURL)
	tunnelHost, tunnelPort := splitHostPortBestEffort(tunnelAddr)
	if publicURL == "" && tunnelHost != "" {
		publicURL = "https://" + tunnelHost
		publicHost = tunnelHost
	}
	if publicURL == "" {
		publicURL = "https://<manager>"
	}
	if tunnelPort == "" {
		tunnelPort = "40012"
	}
	if tunnelHost == "" && publicHost != "" {
		tunnelHost = publicHost
	}
	if tunnelHost == "" {
		tunnelAddr = "<manager>:40012"
	} else {
		tunnelAddr = net.JoinHostPort(tunnelHost, tunnelPort)
	}
	return publicURL, tunnelAddr
}

func hostFromPublicURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "<manager>") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Hostname())
}

func splitHostPortBestEffort(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "<manager>") {
		return "", ""
	}
	if strings.HasPrefix(raw, ":") {
		return "", strings.TrimPrefix(raw, ":")
	}
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		return strings.Trim(host, "[]"), port
	}
	if strings.Count(raw, ":") == 0 {
		return raw, ""
	}
	if strings.Count(raw, ":") > 1 {
		return strings.Trim(raw, "[]"), ""
	}
	idx := strings.LastIndex(raw, ":")
	if idx <= 0 || idx == len(raw)-1 {
		return "", ""
	}
	return strings.Trim(raw[:idx], "[]"), raw[idx+1:]
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
