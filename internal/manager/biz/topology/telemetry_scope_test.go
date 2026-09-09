package topology_test

import (
	"context"
	"slices"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/topology"
)

type clusterDevices struct{ nodeID uint64 }

func (d clusterDevices) DeviceIDsForNodes(_ context.Context, ids []uint64) ([]uint64, error) {
	if slices.Contains(ids, d.nodeID) {
		return []uint64{650}, nil
	}
	return []uint64{}, nil
}

func TestClusterTelemetryScopeFollowsMembershipAndKubernetesIdentity(t *testing.T) {
	uc, ctx := newUC(t), context.Background()
	cluster, err := uc.CreateNode(ctx, "cluster", "hosts", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	device, err := uc.CreateNode(ctx, "device", "host", "")
	if err != nil {
		t.Fatal(err)
	}
	uc.WithClusterDevices(clusterDevices{nodeID: device.ID})
	relation, err := uc.CreateRelation(ctx, device.ID, cluster.ID, model.RelMemberOf, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	id, devices, err := uc.ResolveClusterTelemetryScope(ctx, cluster.ID)
	if err != nil || id != "" || len(devices) != 1 || devices[0] != "650" {
		t.Fatalf("device scope: %q %v %v", id, devices, err)
	}
	if err := uc.DeleteRelation(ctx, relation.ID); err != nil {
		t.Fatal(err)
	}
	id, devices, err = uc.ResolveClusterTelemetryScope(ctx, cluster.ID)
	if err != nil || id != "" || devices == nil || len(devices) != 0 {
		t.Fatalf("empty scope: %q %v %v", id, devices, err)
	}
	k8sID, err := uc.EnsureKubernetesCluster(ctx, 48, nil, "kubernetes", "uid", "full-node", "online")
	if err != nil {
		t.Fatal(err)
	}
	id, devices, err = uc.ResolveClusterTelemetryScope(ctx, k8sID)
	if err != nil || id != "48" || devices != nil {
		t.Fatalf("Kubernetes scope: %q %v %v", id, devices, err)
	}
}
