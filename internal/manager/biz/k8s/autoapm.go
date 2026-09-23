package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type AutoAPMConfig struct {
	Spec     autoapm.Spec    `json:"spec"`
	Defaults AutoAPMDefaults `json:"defaults"`
}
type AutoAPMDefaults struct {
	Environment string `json:"environment"`
	ClusterName string `json:"cluster_name"`
}

func (u *Usecase) SetAutoAPMProviders(environment func(context.Context, uint64) (string, error), notify func(context.Context, uint64)) {
	u.autoAPMEnvironment, u.autoAPMNotify = environment, notify
}

func (u *Usecase) GetAutoAPM(ctx context.Context, clusterID uint64) (*AutoAPMConfig, error) {
	cluster, err := u.repo.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	raw := map[string]interface{}{}
	if cluster.AutoAPMConfigJSON != "" {
		if err := json.Unmarshal([]byte(cluster.AutoAPMConfigJSON), &raw); err != nil {
			return nil, fmt.Errorf("read cluster capture settings: %w", err)
		}
	}
	spec, err := autoapm.Parse(raw)
	if err != nil {
		return nil, err
	}
	if spec.Kubernetes == nil {
		spec.Kubernetes = &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{}}
	}
	if spec.Kubernetes.Rules == nil {
		spec.Kubernetes.Rules = []autoapm.KubernetesRule{}
	}
	out := &AutoAPMConfig{Spec: spec, Defaults: AutoAPMDefaults{ClusterName: cluster.Name}}
	if cluster.NodeID != nil && u.autoAPMEnvironment != nil {
		out.Defaults.Environment, err = u.autoAPMEnvironment(ctx, *cluster.NodeID)
		if err != nil {
			return nil, fmt.Errorf("read cluster environment: %w", err)
		}
	}
	return out, nil
}

func (u *Usecase) SetAutoAPM(ctx context.Context, clusterID uint64, raw map[string]interface{}) (*AutoAPMConfig, error) {
	spec, err := autoapm.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	if spec.ClusterID != 0 || spec.K8sClusterID != 0 {
		return nil, fmt.Errorf("%w: cluster identities are manager-owned", errs.ErrInvalid)
	}
	if spec.Kubernetes == nil || len(spec.Targets) > 0 || spec.Environment != "" {
		return nil, fmt.Errorf("%w: Kubernetes rules required; environment is inherited from the cluster", errs.ErrInvalid)
	}
	cluster, err := u.repo.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for _, rule := range spec.Kubernetes.Rules {
		if cluster.InventoryNamespace != "" && rule.Namespace != cluster.InventoryNamespace {
			return nil, fmt.Errorf("%w: namespace is outside the cluster inventory scope", errs.ErrInvalid)
		}
	}
	edgeIDs, err := u.repo.ListClusterEdgeIDs(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode cluster capture settings: %w", err)
	}
	if err := u.repo.UpdateAutoAPM(ctx, clusterID, string(body)); err != nil {
		return nil, err
	}
	if u.autoAPMNotify != nil {
		for _, edgeID := range edgeIDs {
			u.autoAPMNotify(ctx, edgeID)
		}
	}
	return u.GetAutoAPM(ctx, clusterID)
}

// AutoAPMForEdge supplies cluster-owned rules to every current and future node.
// managed=true also covers controllers, which must never scan host processes.
func (u *Usecase) AutoAPMForEdge(ctx context.Context, edgeID uint64) (spec *autoapm.Spec, managed bool, err error) {
	clusterID, err := u.repo.GetClusterIDByEdgeID(ctx, edgeID)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	node, err := u.repo.GetNodeByEdgeID(ctx, edgeID)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	if node.ClusterID != clusterID {
		return nil, true, fmt.Errorf("Kubernetes node cluster mismatch")
	}
	config, err := u.GetAutoAPM(ctx, clusterID)
	if err != nil {
		return nil, true, err
	}
	if config.Spec.Environment == "" {
		config.Spec.Environment = config.Defaults.Environment
	}
	return &config.Spec, true, nil
}

func (u *Usecase) AutoAPMSpecs(ctx context.Context) ([]string, error) {
	return u.repo.ListAutoAPMSpecs(ctx)
}

// TelemetryClusterForEdge translates registration identity into the shared
// topology identity. Controllers and gateways belong to the same cluster.
func (u *Usecase) TelemetryClusterForEdge(ctx context.Context, edgeID uint64) (uint64, uint64, error) {
	clusterID, err := u.repo.GetClusterIDByEdgeID(ctx, edgeID)
	if errors.Is(err, errs.ErrNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	cluster, err := u.repo.GetCluster(ctx, clusterID)
	if err != nil {
		return 0, 0, err
	}
	if cluster.NodeID == nil || *cluster.NodeID == 0 {
		return 0, 0, fmt.Errorf("Kubernetes cluster %d has no topology mapping", clusterID)
	}
	return *cluster.NodeID, clusterID, nil
}
