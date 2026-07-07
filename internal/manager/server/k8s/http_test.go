package k8s

import (
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
)

func TestClusterCapabilitiesFromModel(t *testing.T) {
	edgeID := uint64(42)

	fullNode := clusterCapabilitiesFromModel(&model.Cluster{
		Mode:                     model.ModeFullNode,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
	})
	assertCapabilityStatus(t, fullNode, "inventory", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "node-metrics", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "events", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "telemetry", capabilityStatusQueryReady)
	assertCapabilityStatus(t, fullNode, "host-access", capabilityStatusDegraded)

	serverless := clusterCapabilitiesFromModel(&model.Cluster{
		Mode:             model.ModeServerless,
		ControllerEdgeID: &edgeID,
	})
	assertCapabilityStatus(t, serverless, "inventory", capabilityStatusDegraded)
	assertCapabilityStatus(t, serverless, "node-metrics", capabilityStatusNotApplicable)
	assertCapabilityStatus(t, serverless, "events", capabilityStatusReady)
	assertCapabilityStatus(t, serverless, "telemetry", capabilityStatusQueryReady)
	assertCapabilityStatus(t, serverless, "host-access", capabilityStatusNotApplicable)

	offline := clusterCapabilitiesFromModel(&model.Cluster{Mode: model.ModeFullNode})
	assertCapabilityStatus(t, offline, "inventory", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "node-metrics", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "events", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "telemetry", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "host-access", capabilityStatusUnavailable)
}

func TestClusterCapabilitiesUseNodeCoverage(t *testing.T) {
	edgeID := uint64(42)
	cluster := &model.Cluster{
		Mode:                     model.ModeFullNode,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
	}

	partial := biz.NodeCoverage{ClusterID: 1, Total: 5, EdgeLinked: 3, DeviceLinked: 3}
	caps := clusterCapabilitiesFromModelWithCoverage(cluster, &partial)
	assertCapabilityStatus(t, caps, "node-metrics", capabilityStatusDegraded)
	assertCapabilityStatus(t, caps, "host-access", capabilityStatusDegraded)

	complete := biz.NodeCoverage{ClusterID: 1, Total: 5, EdgeLinked: 5, DeviceLinked: 5}
	caps = clusterCapabilitiesFromModelWithCoverage(cluster, &complete)
	assertCapabilityStatus(t, caps, "node-metrics", capabilityStatusReady)
	assertCapabilityStatus(t, caps, "host-access", capabilityStatusReady)

	dto := clusterDTOFromModelWithCoverage(cluster, &partial)
	if dto.NodeEdgeCoverage == nil {
		t.Fatal("node edge coverage is nil")
	}
	if dto.NodeEdgeCoverage.Missing != 2 || dto.NodeEdgeCoverage.Percent != 60 {
		t.Fatalf("node edge coverage = %+v, want missing=2 percent=60", dto.NodeEdgeCoverage)
	}
}

func assertCapabilityStatus(t *testing.T, items []clusterCapabilityDTO, key, want string) {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			if item.Status != want {
				t.Fatalf("capability %q status = %q, want %q", key, item.Status, want)
			}
			return
		}
	}
	t.Fatalf("capability %q not found", key)
}
