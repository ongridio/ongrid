package k8s

import (
	"context"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type CreateClusterInput = biz.CreateClusterInput
type ClusterRegistration = biz.ClusterRegistration
type ListClustersFilter = biz.ListClustersFilter
type DeleteClusterInput = biz.DeleteClusterInput
type ListNodesFilter = biz.ListNodesFilter
type ListPodsFilter = biz.ListPodsFilter
type ListWorkloadsFilter = biz.ListWorkloadsFilter
type ListEventsFilter = biz.ListEventsFilter
type EnrollInput = biz.EnrollInput
type EnrollResult = biz.EnrollResult
type InventoryResult = biz.InventoryResult
type NodeCoverage = biz.NodeCoverage
type ClusterHealthSummary = biz.ClusterHealthSummary
type EdgeAttachment = biz.EdgeAttachment

// Service is the manager/k8s service-layer shim over biz.Usecase.
type Service struct {
	uc *biz.Usecase
}

func New(uc *biz.Usecase) *Service { return &Service{uc: uc} }

func (s *Service) CreateCluster(ctx context.Context, in CreateClusterInput) (*ClusterRegistration, error) {
	return s.uc.CreateCluster(ctx, in)
}

func (s *Service) ListClusters(ctx context.Context, f ListClustersFilter) ([]*model.Cluster, error) {
	return s.uc.ListClusters(ctx, f)
}

func (s *Service) CountClusters(ctx context.Context, f ListClustersFilter) (int64, error) {
	return s.uc.CountClusters(ctx, f)
}

func (s *Service) GetCluster(ctx context.Context, id uint64) (*model.Cluster, error) {
	return s.uc.GetCluster(ctx, id)
}

func (s *Service) ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error) {
	return s.uc.ListNodes(ctx, clusterID)
}

func (s *Service) ListNodesPage(ctx context.Context, f ListNodesFilter) ([]*model.Node, int64, error) {
	return s.uc.ListNodesPage(ctx, f)
}

func (s *Service) CountNodes(ctx context.Context, clusterID uint64) (int64, error) {
	return s.uc.CountNodes(ctx, clusterID)
}

func (s *Service) GetNodeCoverage(ctx context.Context, clusterID uint64) (NodeCoverage, error) {
	return s.uc.GetNodeCoverage(ctx, clusterID)
}

func (s *Service) GetNodeCoverageByClusterIDs(ctx context.Context, clusterIDs []uint64) (map[uint64]NodeCoverage, error) {
	return s.uc.GetNodeCoverageByClusterIDs(ctx, clusterIDs)
}

func (s *Service) UpgradeCommand(cluster *model.Cluster) string {
	return s.uc.UpgradeCommand(cluster)
}

func (s *Service) ListEdgeAttachments(ctx context.Context, limit, offset int) ([]EdgeAttachment, int64, error) {
	return s.uc.ListEdgeAttachments(ctx, limit, offset)
}

func (s *Service) GetClusterHealth(ctx context.Context, clusterID uint64) (ClusterHealthSummary, error) {
	return s.uc.GetClusterHealth(ctx, clusterID)
}

func (s *Service) ListWorkloads(ctx context.Context, f ListWorkloadsFilter) ([]*model.Workload, error) {
	return s.uc.ListWorkloads(ctx, f)
}

func (s *Service) CountWorkloads(ctx context.Context, f ListWorkloadsFilter) (int64, error) {
	return s.uc.CountWorkloads(ctx, f)
}

func (s *Service) ListPods(ctx context.Context, f ListPodsFilter) ([]*model.Pod, error) {
	return s.uc.ListPods(ctx, f)
}

func (s *Service) CountPods(ctx context.Context, f ListPodsFilter) (int64, error) {
	return s.uc.CountPods(ctx, f)
}

func (s *Service) ListEvents(ctx context.Context, f ListEventsFilter) ([]*model.Event, error) {
	return s.uc.ListEvents(ctx, f)
}

func (s *Service) CountEvents(ctx context.Context, f ListEventsFilter) (int64, error) {
	return s.uc.CountEvents(ctx, f)
}

func (s *Service) RotateBootstrapToken(ctx context.Context, id uint64) (*ClusterRegistration, error) {
	return s.uc.RotateBootstrapToken(ctx, id)
}

func (s *Service) DeleteCluster(ctx context.Context, in DeleteClusterInput) error {
	return s.uc.DeleteCluster(ctx, in)
}

func (s *Service) Enroll(ctx context.Context, in EnrollInput) (*EnrollResult, error) {
	return s.uc.Enroll(ctx, in)
}

func (s *Service) HandleRegister(ctx context.Context, edgeID uint64, deviceID *uint64, info tunnel.KubernetesInfo) error {
	return s.uc.HandleRegister(ctx, edgeID, deviceID, info)
}

func (s *Service) LookupControllerCluster(ctx context.Context, edgeID uint64) (uint64, error) {
	return s.uc.LookupControllerCluster(ctx, edgeID)
}

func (s *Service) HandleControllerHeartbeat(ctx context.Context, edgeID uint64) error {
	return s.uc.HandleControllerHeartbeat(ctx, edgeID)
}

func (s *Service) ManagedClusterIDForEdge(ctx context.Context, edgeID uint64) (uint64, bool, error) {
	return s.uc.ManagedClusterIDForEdge(ctx, edgeID)
}

func (s *Service) IngestInventory(ctx context.Context, edgeID uint64, in tunnel.KubernetesInventoryRequest) (int, int, int, int, error) {
	out, err := s.uc.IngestInventory(ctx, edgeID, in)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return out.AcceptedNodes, out.AcceptedWorkloads, out.AcceptedPods, out.AcceptedEvents, nil
}
