package topology

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type ClusterDeviceResolver interface {
	DeviceIDsForNodes(context.Context, []uint64) ([]uint64, error)
}

func (u *Usecase) WithClusterDevices(devices ClusterDeviceResolver) *Usecase {
	u.clusterDevices = devices
	return u
}

// ResolveClusterTelemetryScope uses the existing unified topology identity.
// Kubernetes owns its telemetry ID; device clusters follow current membership.
func (u *Usecase) ResolveClusterTelemetryScope(ctx context.Context, nodeID uint64) (string, []string, error) {
	if u.nodes == nil {
		return "", nil, errs.ErrNotWiredYet
	}
	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return "", nil, err
	}
	if node.Type != string(model.NodeTypeCluster) {
		return "", nil, fmt.Errorf("%w: node must be a cluster", errs.ErrInvalid)
	}
	if id, owned, valid := topologyKubernetesClusterProps(node.PropsJSON); owned {
		if !valid || id == 0 {
			return "", nil, fmt.Errorf("%w: Kubernetes cluster identity is missing", errs.ErrInvalid)
		}
		return strconv.FormatUint(id, 10), nil, nil
	}
	if u.relations == nil || u.clusterDevices == nil {
		return "", nil, errs.ErrNotWiredYet
	}
	relations, err := u.relations.List(ctx, RelationListFilter{DstID: nodeID, Type: model.RelMemberOf, Limit: 5001})
	if err != nil {
		return "", nil, err
	}
	if len(relations) > 5000 {
		return "", nil, fmt.Errorf("%w: cluster exceeds 5000 members", errs.ErrBudgetExceeded)
	}
	ids := make([]uint64, 0, len(relations))
	for _, relation := range relations {
		ids = append(ids, relation.SrcID)
	}
	deviceIDs, err := u.clusterDevices.DeviceIDsForNodes(ctx, ids)
	if err != nil {
		return "", nil, err
	}
	if len(deviceIDs) > 5000 {
		return "", nil, fmt.Errorf("%w: cluster exceeds 5000 devices", errs.ErrBudgetExceeded)
	}
	devices := make([]string, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		devices = append(devices, strconv.FormatUint(id, 10))
	}
	slices.Sort(devices)
	return "", slices.Compact(devices), nil
}
