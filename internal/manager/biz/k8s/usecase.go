package k8s

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/ongridio/ongrid/internal/pkg/k8sredact"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	defaultBootstrapTokenTTL    = 90 * 24 * time.Hour
	defaultEventRetention       = 24 * time.Hour
	defaultEventMaxPerCluster   = 5000
	defaultEventCleanupInterval = time.Hour
	eventRetentionBatchLimit    = 1000
	bootstrapTokenBytes         = 32
)

// Repository is the k8s bounded context persistence contract.
type Repository interface {
	CreateCluster(ctx context.Context, c *model.Cluster) error
	GetCluster(ctx context.Context, id uint64) (*model.Cluster, error)
	GetClusterByControllerEdge(ctx context.Context, edgeID uint64) (*model.Cluster, error)
	ListClusters(ctx context.Context, f ListClustersFilter) ([]*model.Cluster, error)
	CountClusters(ctx context.Context, f ListClustersFilter) (int64, error)
	UpdateClusterTokens(ctx context.Context, id uint64, controllerTokenHash, nodeTokenHash string, expiresAt *time.Time) error
	ClaimControllerBootstrapToken(ctx context.Context, id uint64, tokenHash string) (bool, error)
	RestoreControllerBootstrapToken(ctx context.Context, id uint64, tokenHash string) error
	UpdateClusterController(ctx context.Context, id uint64, in ClusterControllerRegistration) error
	BindControllerEnrollment(ctx context.Context, id uint64, registration ClusterControllerRegistration, installation *model.Installation) error
	UpdateClusterInventorySync(ctx context.Context, id uint64, in ClusterInventorySync) error
	UpdateClusterTopologyNode(ctx context.Context, id, nodeID uint64) error
	UpdateDeviceTopologyNode(ctx context.Context, id, nodeID uint64) error
	ListClusterEdgeIDs(ctx context.Context, clusterID uint64) ([]uint64, error)
	GetClusterIDByEdgeID(ctx context.Context, edgeID uint64) (uint64, error)
	DeleteCluster(ctx context.Context, id uint64) error

	GetNodeByClusterUID(ctx context.Context, clusterID uint64, nodeUID string) (*model.Node, error)
	GetNodeByEdgeID(ctx context.Context, edgeID uint64) (*model.Node, error)
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
	DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error)
	DeleteOldestEvents(ctx context.Context, clusterID uint64, keep, limit int) (int64, error)
	ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error)
	ListTopologyNodeLinks(ctx context.Context, clusterID uint64) ([]TopologyNodeLink, error)
	CountNodes(ctx context.Context, clusterID uint64) (int64, error)
	GetNodeCoverage(ctx context.Context, clusterID uint64) (NodeCoverage, error)
	GetNodeCoverageByClusterIDs(ctx context.Context, clusterIDs []uint64) (map[uint64]NodeCoverage, error)
	ListEdgeAttachments(ctx context.Context, limit, offset int) ([]EdgeAttachment, int64, error)
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
	EnsureNodeForDevice(ctx context.Context, deviceID uint64, deviceName string) (uint64, error)
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
	DeviceName   string
	DeviceNodeID *uint64
}

type NodeCoverage struct {
	ClusterID    uint64
	Total        int64
	EdgeLinked   int64
	DeviceLinked int64
}

type EdgeAttachment struct {
	EdgeID      uint64
	ClusterID   uint64
	ClusterName string
	ClusterMode string
	NodeName    string
	Kind        string
}

type ClusterHealthSummary struct {
	DegradedWorkloads    int64
	PendingPods          int64
	CrashLoopBackOffPods int64
	OOMKilledPods        int64
	ImagePullBackOffPods int64
	NotReadyNodes        int64
}

type Config struct {
	PublicURL            string
	TunnelAddr           string
	BootstrapTokenTTL    time.Duration
	ChartRef             string
	EventRetention       time.Duration
	EventMaxPerCluster   int
	EventCleanupInterval time.Duration
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
	if cfg.EventRetention <= 0 {
		cfg.EventRetention = defaultEventRetention
	}
	if cfg.EventMaxPerCluster <= 0 {
		cfg.EventMaxPerCluster = defaultEventMaxPerCluster
	}
	if cfg.EventCleanupInterval <= 0 {
		cfg.EventCleanupInterval = defaultEventCleanupInterval
	}
	u := &Usecase{repo: repo, edgeIssuer: edgeIssuer, cfg: cfg}
	if remover, ok := edgeIssuer.(EdgeRemover); ok {
		u.edgeRemover = remover
	}
	return u
}

func (u *Usecase) SetTopologyMirror(m TopologyMirror) { u.topology = m }
func (u *Usecase) SetEdgeRemover(r EdgeRemover)       { u.edgeRemover = r }

func (u *Usecase) EventCleanupInterval() time.Duration {
	if u == nil || u.cfg.EventCleanupInterval <= 0 {
		return defaultEventCleanupInterval
	}
	return u.cfg.EventCleanupInterval
}

func (u *Usecase) CleanupEvents(ctx context.Context, now time.Time) (EventRetentionStats, error) {
	var stats EventRetentionStats
	if u.repo == nil {
		return stats, errs.ErrNotWiredYet
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if u.cfg.EventRetention > 0 {
		cutoff := now.Add(-u.cfg.EventRetention)
		for {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			n, err := u.repo.DeleteEventsBefore(ctx, cutoff, eventRetentionBatchLimit)
			if err != nil {
				return stats, fmt.Errorf("delete aged k8s events: %w", err)
			}
			stats.DeletedByTTL += n
			if n == 0 {
				break
			}
		}
	}
	if u.cfg.EventMaxPerCluster > 0 {
		const pageSize = 200
		for offset := 0; ; offset += pageSize {
			clusters, err := u.repo.ListClusters(ctx, ListClustersFilter{Limit: pageSize, Offset: offset})
			if err != nil {
				return stats, fmt.Errorf("list k8s clusters for event retention: %w", err)
			}
			for _, c := range clusters {
				if c == nil {
					continue
				}
				for {
					if err := ctx.Err(); err != nil {
						return stats, err
					}
					n, err := u.repo.DeleteOldestEvents(ctx, c.ID, u.cfg.EventMaxPerCluster, eventRetentionBatchLimit)
					if err != nil {
						return stats, fmt.Errorf("delete old k8s events for cluster %d: %w", c.ID, err)
					}
					stats.DeletedByClusterLimit += n
					if n == 0 {
						break
					}
				}
			}
			if len(clusters) < pageSize {
				break
			}
		}
	}
	return stats, nil
}

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

type DeleteClusterInput struct {
	ID    uint64
	Force bool
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

type EventRetentionStats struct {
	DeletedByTTL          int64
	DeletedByClusterLimit int64
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
	Cluster            *model.Cluster
	BootstrapToken     string
	NodeBootstrapToken string
	InstallCommand     string
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
	controllerToken, controllerHash, expiresAt, err := newBootstrapToken(u.cfg.BootstrapTokenTTL)
	if err != nil {
		return nil, err
	}
	nodeToken, nodeHash, _, err := newBootstrapToken(u.cfg.BootstrapTokenTTL)
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
		BootstrapTokenHash:      controllerHash,
		NodeBootstrapTokenHash:  nodeHash,
		BootstrapTokenExpiresAt: expiresAt,
		CreatedBy:               in.CreatedBy,
	}
	if err := u.repo.CreateCluster(ctx, c); err != nil {
		return nil, fmt.Errorf("create k8s cluster: %w", err)
	}
	if err := u.reconcileTopology(ctx, c.ID); err != nil {
		if cleanupErr := u.repo.DeleteCluster(ctx, c.ID); cleanupErr != nil {
			return nil, errors.Join(fmt.Errorf("reconcile k8s topology: %w", err), fmt.Errorf("rollback k8s cluster: %w", cleanupErr))
		}
		return nil, fmt.Errorf("reconcile k8s topology: %w", err)
	}
	return &ClusterRegistration{
		Cluster:            c,
		BootstrapToken:     controllerToken,
		NodeBootstrapToken: nodeToken,
		InstallCommand:     u.installCommand(c.ID, mode, controllerToken, nodeToken),
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

func (u *Usecase) CountClusters(ctx context.Context, f ListClustersFilter) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	return u.repo.CountClusters(ctx, f)
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

func (u *Usecase) GetNodeCoverageByClusterIDs(ctx context.Context, clusterIDs []uint64) (map[uint64]NodeCoverage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	clusterIDs = uniqueNonZeroUint64(clusterIDs)
	if len(clusterIDs) == 0 {
		return map[uint64]NodeCoverage{}, nil
	}
	return u.repo.GetNodeCoverageByClusterIDs(ctx, clusterIDs)
}

func (u *Usecase) ListEdgeAttachments(ctx context.Context, limit, offset int) ([]EdgeAttachment, int64, error) {
	if u.repo == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	return u.repo.ListEdgeAttachments(ctx, limit, offset)
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

func (u *Usecase) GetClusterHealth(ctx context.Context, clusterID uint64) (ClusterHealthSummary, error) {
	var out ClusterHealthSummary
	if u.repo == nil {
		return out, errs.ErrNotWiredYet
	}
	if _, err := u.repo.GetCluster(ctx, clusterID); err != nil {
		return out, err
	}
	var err error
	if out.DegradedWorkloads, err = u.repo.CountWorkloads(ctx, ListWorkloadsFilter{ClusterID: clusterID, IssueOnly: true}); err != nil {
		return out, err
	}
	if out.PendingPods, err = u.repo.CountPods(ctx, ListPodsFilter{ClusterID: clusterID, Phase: "Pending"}); err != nil {
		return out, err
	}
	if out.CrashLoopBackOffPods, err = u.repo.CountPods(ctx, ListPodsFilter{ClusterID: clusterID, Reason: "CrashLoopBackOff"}); err != nil {
		return out, err
	}
	if out.OOMKilledPods, err = u.repo.CountPods(ctx, ListPodsFilter{ClusterID: clusterID, Reason: "OOMKilled"}); err != nil {
		return out, err
	}
	imagePullBackOff, err := u.repo.CountPods(ctx, ListPodsFilter{ClusterID: clusterID, Reason: "ImagePullBackOff"})
	if err != nil {
		return out, err
	}
	errImagePull, err := u.repo.CountPods(ctx, ListPodsFilter{ClusterID: clusterID, Reason: "ErrImagePull"})
	if err != nil {
		return out, err
	}
	out.ImagePullBackOffPods = imagePullBackOff + errImagePull
	nodes, err := u.repo.ListNodes(ctx, clusterID)
	if err != nil {
		return out, err
	}
	for _, node := range nodes {
		if node != nil && nodeIsNotReady(node.ConditionsJSON) {
			out.NotReadyNodes++
		}
	}
	return out, nil
}

func nodeIsNotReady(raw string) bool {
	var conditions []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &conditions); err != nil {
		return false
	}
	for _, condition := range conditions {
		if condition.Type == "Ready" {
			return condition.Status == "False" || condition.Status == "Unknown"
		}
	}
	return false
}

func (u *Usecase) RotateBootstrapToken(ctx context.Context, id uint64) (*ClusterRegistration, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	c, err := u.repo.GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	controllerToken, controllerHash, expiresAt, err := newBootstrapToken(u.cfg.BootstrapTokenTTL)
	if err != nil {
		return nil, err
	}
	nodeToken, nodeHash, _, err := newBootstrapToken(u.cfg.BootstrapTokenTTL)
	if err != nil {
		return nil, err
	}
	if err := u.repo.UpdateClusterTokens(ctx, id, controllerHash, nodeHash, expiresAt); err != nil {
		return nil, fmt.Errorf("rotate k8s bootstrap token: %w", err)
	}
	c.BootstrapTokenHash = controllerHash
	c.NodeBootstrapTokenHash = nodeHash
	c.BootstrapTokenExpiresAt = expiresAt
	return &ClusterRegistration{
		Cluster:            c,
		BootstrapToken:     controllerToken,
		NodeBootstrapToken: nodeToken,
		InstallCommand:     u.installCommand(c.ID, c.Mode, controllerToken, nodeToken),
	}, nil
}

func (u *Usecase) DeleteCluster(ctx context.Context, in DeleteClusterInput) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if in.ID == 0 {
		return errs.ErrInvalid
	}
	c, err := u.repo.GetCluster(ctx, in.ID)
	if err != nil {
		return err
	}
	if !in.Force && EffectiveClusterStatus(c, time.Now().UTC()) == model.ClusterStatusOnline {
		return fmt.Errorf("%w: k8s cluster %d is still reporting; uninstall the Helm release first or retry with force", errs.ErrConflict, c.ID)
	}
	edgeIDs, err := u.repo.ListClusterEdgeIDs(ctx, in.ID)
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
	if err := u.repo.DeleteCluster(ctx, in.ID); err != nil {
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
	role := normalizeRole(in.Role)
	if !validBootstrapToken(strings.TrimSpace(in.BootstrapToken), c, role) {
		return nil, errs.ErrUnauthorized
	}
	now := time.Now()
	switch role {
	case model.RoleNode:
		return u.enrollNode(ctx, c, in, now)
	case model.RoleController:
		tokenHash := tokenDigest(strings.TrimSpace(in.BootstrapToken))
		claimed, err := u.repo.ClaimControllerBootstrapToken(ctx, c.ID, tokenHash)
		if err != nil {
			return nil, fmt.Errorf("claim k8s controller bootstrap token: %w", err)
		}
		if !claimed {
			return nil, errs.ErrUnauthorized
		}
		out, err := u.enrollController(ctx, c, in, now)
		if err == nil {
			return out, nil
		}
		if restoreErr := u.repo.RestoreControllerBootstrapToken(ctx, c.ID, tokenHash); restoreErr != nil {
			return nil, errors.Join(err, fmt.Errorf("restore k8s controller bootstrap token: %w", restoreErr))
		}
		return nil, err
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
		node, err := u.repo.GetNodeByEdgeID(ctx, edgeID)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not enrolled as a k8s node", edgeID))
			}
			return err
		}
		if node.ClusterID != info.ClusterID {
			return errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not enrolled for cluster %d", edgeID, info.ClusterID))
		}
		if name := strings.TrimSpace(info.NodeName); name != "" && name != node.NodeName {
			return errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not enrolled for node %q", edgeID, name))
		}
		if uid := strings.TrimSpace(info.NodeUID); uid != "" && uid != node.NodeUID {
			return errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not enrolled for node uid %q", edgeID, uid))
		}
		if err := u.repo.UpdateNodeEdge(ctx, node.ID, edgeID, deviceID, now); err != nil {
			return fmt.Errorf("refresh k8s node edge: %w", err)
		}
		return u.reconcileTopology(ctx, info.ClusterID)
	case model.RoleController:
		cluster, err := u.repo.GetClusterByControllerEdge(ctx, edgeID)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not enrolled as a k8s controller", edgeID))
			}
			return err
		}
		if cluster.ID != info.ClusterID {
			return errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not controller for cluster %d", edgeID, info.ClusterID))
		}
		if err := u.repo.UpdateClusterController(ctx, info.ClusterID, ClusterControllerRegistration{
			EdgeID:    edgeID,
			LastSeen:  now,
			NodeName:  strings.TrimSpace(info.NodeName),
			Namespace: strings.TrimSpace(info.Namespace),
			PodName:   strings.TrimSpace(info.PodName),
		}); err != nil {
			return err
		}
		mode, err := normalizeMode(info.Mode)
		if err != nil {
			return err
		}
		ts := now
		if err := u.repo.UpsertInstallation(ctx, &model.Installation{
			ClusterID:        info.ClusterID,
			Mode:             mode,
			ScopeType:        "cluster",
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

func (u *Usecase) ManagedClusterIDForEdge(ctx context.Context, edgeID uint64) (uint64, bool, error) {
	if u.repo == nil {
		return 0, false, errs.ErrNotWiredYet
	}
	if edgeID == 0 {
		return 0, false, errors.Join(errs.ErrInvalid, fmt.Errorf("edge_id is required"))
	}
	clusterID, err := u.repo.GetClusterIDByEdgeID(ctx, edgeID)
	if errors.Is(err, errs.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return clusterID, true, nil
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
	clusterNodeID, err := u.topology.EnsureKubernetesCluster(ctx, c.ID, c.NodeID, c.Name, uid, c.Mode, EffectiveClusterStatus(c, time.Now().UTC()))
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
		if link.DeviceID == nil || *link.DeviceID == 0 {
			continue
		}
		deviceID := *link.DeviceID
		deviceNodeID := uint64(0)
		if link.DeviceNodeID != nil {
			deviceNodeID = *link.DeviceNodeID
		}
		if deviceNodeID == 0 {
			deviceName := strings.TrimSpace(link.DeviceName)
			if deviceName == "" {
				deviceName = strings.TrimSpace(link.NodeName)
			}
			nodeID, err := u.topology.EnsureNodeForDevice(ctx, deviceID, deviceName)
			if err != nil {
				return fmt.Errorf("ensure topology node for k8s device %d: %w", deviceID, err)
			}
			if nodeID == 0 {
				continue
			}
			if err := u.repo.UpdateDeviceTopologyNode(ctx, deviceID, nodeID); err != nil {
				return fmt.Errorf("link k8s device topology node: %w", err)
			}
			deviceNodeID = nodeID
		}
		if err := u.topology.EnsureKubernetesNodeMembership(ctx, clusterNodeID, deviceNodeID, c.ID, deviceID, link.NodeName, link.NodeUID); err != nil {
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
	if n.EdgeID != nil && *n.EdgeID != 0 {
		return nil, errors.Join(errs.ErrConflict, fmt.Errorf("k8s node %q is already enrolled; restore its local credential file before restarting", nodeName))
	}

	cred, created, err := u.issueNodeCredential(ctx, c, n)
	if err != nil {
		return nil, err
	}
	if err := u.repo.UpdateNodeEdge(ctx, n.ID, cred.EdgeID, nil, now); err != nil {
		if created {
			return nil, u.compensateCreatedEdge(ctx, cred.EdgeID, fmt.Errorf("link k8s node edge: %w", err))
		}
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
	if c.ControllerEdgeID == nil || *c.ControllerEdgeID == 0 || *c.ControllerEdgeID != edgeID {
		return nil, errors.Join(errs.ErrForbidden, fmt.Errorf("edge %d is not controller for cluster %d", edgeID, in.ClusterID))
	}
	receivedAt := time.Now().UTC()
	now := receivedAt
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
			LabelsJSON:      jsonText(k8sredact.StringMap(item.Labels), "{}"),
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
			LabelsJSON:      jsonText(k8sredact.StringMap(item.Labels), "{}"),
			AnnotationsJSON: jsonText(k8sredact.StringMap(item.Annotations), "{}"),
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
			Message:             k8sredact.Text(strings.TrimSpace(item.Message)),
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
	if syncType == inventorySyncFull || len(in.Nodes) > 0 || len(in.DeletedNodes) > 0 {
		if err := u.reconcileTopology(ctx, in.ClusterID); err != nil {
			return nil, fmt.Errorf("reconcile k8s topology: %w", err)
		}
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

func (u *Usecase) enrollController(ctx context.Context, c *model.Cluster, in EnrollInput, now time.Time) (*EnrollResult, error) {
	var cred *EdgeCredential
	created := false
	var err error
	if c.ControllerEdgeID == nil || *c.ControllerEdgeID == 0 {
		cred, err = u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, "controller"), c.CreatedBy)
		created = err == nil
	} else {
		cred, err = u.edgeIssuer.RotateEdgeSecret(ctx, *c.ControllerEdgeID)
		if errors.Is(err, errs.ErrNotFound) {
			cred, err = u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, "controller"), c.CreatedBy)
			created = err == nil
		}
	}
	if err != nil {
		return nil, err
	}
	registration := ClusterControllerRegistration{
		EdgeID:    cred.EdgeID,
		LastSeen:  now,
		NodeName:  strings.TrimSpace(in.NodeName),
		Namespace: strings.TrimSpace(in.Namespace),
	}
	mode := c.Mode
	scopeType := "cluster"
	capabilitiesJSON := mustJSON(in.Capabilities)
	ts := now
	installation := &model.Installation{
		ClusterID:        c.ID,
		Mode:             mode,
		ScopeType:        scopeType,
		Namespace:        strings.TrimSpace(in.Namespace),
		ControllerEdgeID: &cred.EdgeID,
		CapabilitiesJSON: capabilitiesJSON,
		LastSeenAt:       &ts,
	}
	if err := u.repo.BindControllerEnrollment(ctx, c.ID, registration, installation); err != nil {
		bindErr := fmt.Errorf("bind k8s controller enrollment: %w", err)
		if created {
			return nil, u.compensateCreatedEdge(ctx, cred.EdgeID, bindErr)
		}
		return nil, bindErr
	}
	return u.enrollResult(c.ID, model.RoleController, mode, cred), nil
}

func (u *Usecase) issueNodeCredential(ctx context.Context, c *model.Cluster, n *model.Node) (*EdgeCredential, bool, error) {
	cred, err := u.edgeIssuer.CreateEdgeIdentity(ctx, edgeName(c.Name, n.NodeName), c.CreatedBy)
	return cred, err == nil, err
}

func (u *Usecase) compensateCreatedEdge(ctx context.Context, edgeID uint64, cause error) error {
	if u.edgeRemover == nil || edgeID == 0 {
		return cause
	}
	if err := u.edgeRemover.DeleteEdge(ctx, edgeID); err != nil {
		return errors.Join(cause, fmt.Errorf("remove unbound k8s edge %d: %w", edgeID, err))
	}
	return cause
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

func validBootstrapToken(token string, c *model.Cluster, role string) bool {
	if token == "" || c == nil {
		return false
	}
	if role == model.RoleController && c.BootstrapTokenExpiresAt != nil && time.Now().After(*c.BootstrapTokenExpiresAt) {
		return false
	}
	want := c.BootstrapTokenHash
	if role == model.RoleNode {
		want = c.NodeBootstrapTokenHash
	}
	if role != model.RoleNode && role != model.RoleController {
		return false
	}
	got := tokenDigest(token)
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func newBootstrapToken(ttl time.Duration) (token string, hash string, expiresAt *time.Time, err error) {
	token, err = randomURLSafe(bootstrapTokenBytes)
	if err != nil {
		return "", "", nil, fmt.Errorf("gen k8s bootstrap token: %w", err)
	}
	hash = tokenDigest(token)
	exp := time.Now().Add(ttl)
	return token, hash, &exp, nil
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
	default:
		return "", errors.Join(errs.ErrInvalid, fmt.Errorf("unsupported k8s mode %q", mode))
	}
}

func normalizeRole(role string) string {
	switch strings.TrimSpace(role) {
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

func (u *Usecase) installCommand(clusterID uint64, mode, controllerToken, nodeToken string) string {
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
		"--set-string enrollment.controllerBootstrapToken="+shellQuote(controllerToken),
		"--set-string enrollment.nodeBootstrapToken="+shellQuote(nodeToken),
		"--set-string mode="+shellQuote(mode),
	)
	return strings.Join(args, " ")
}

func (u *Usecase) UpgradeCommand(cluster *model.Cluster) string {
	if cluster == nil {
		return ""
	}
	publicURL, tunnelAddr := installEndpoints(u.cfg.PublicURL, u.cfg.TunnelAddr)
	chartRef := installChartRef(u.cfg, publicURL)
	namespace := strings.TrimSpace(cluster.ControllerNamespace)
	if namespace == "" {
		namespace = "ongrid-system"
	}
	args := []string{
		"helm upgrade ongrid-edge",
		shellQuote(chartRef),
	}
	if strings.HasPrefix(strings.ToLower(chartRef), "https://") {
		args = append(args, "--insecure-skip-tls-verify")
	}
	args = append(args,
		"--namespace "+shellQuote(namespace),
		"--reuse-values",
		"--set-string manager.publicURL="+shellQuote(publicURL),
		"--set-string manager.tunnelAddr="+shellQuote(tunnelAddr),
		"--set-string manager.tlsInsecure=true",
	)
	return strings.Join(args, " ")
}

func installChartRef(cfg Config, publicURL string) string {
	if chartRef := strings.TrimSpace(cfg.ChartRef); chartRef != "" {
		return chartRef
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
