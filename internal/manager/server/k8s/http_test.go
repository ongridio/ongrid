package k8s

import (
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
)

func TestClusterCapabilitiesFromModel(t *testing.T) {
	edgeID := uint64(42)
	now := time.Now().UTC()

	fullNode := clusterCapabilitiesFromModel(&model.Cluster{
		Status:                   model.ClusterStatusOnline,
		Mode:                     model.ModeFullNode,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
		LastSeenAt:               &now,
		InventorySyncedAt:        &now,
	})
	assertCapabilityStatus(t, fullNode, "inventory", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "events", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "telemetry", capabilityStatusQueryReady)
	assertCapabilityMissing(t, fullNode, "node-metrics")
	assertCapabilityMissing(t, fullNode, "host-access")

	offline := clusterCapabilitiesFromModel(&model.Cluster{Mode: model.ModeFullNode})
	assertCapabilityStatus(t, offline, "inventory", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "events", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "telemetry", capabilityStatusUnavailable)
	assertCapabilityMissing(t, offline, "node-metrics")
	assertCapabilityMissing(t, offline, "host-access")
}

func TestClusterCapabilitiesUseNodeCoverage(t *testing.T) {
	edgeID := uint64(42)
	now := time.Now().UTC()
	cluster := &model.Cluster{
		Status:                   model.ClusterStatusOnline,
		Mode:                     model.ModeFullNode,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
		LastSeenAt:               &now,
		InventorySyncedAt:        &now,
	}

	partial := biz.NodeCoverage{ClusterID: 1, Total: 5, EdgeLinked: 3, DeviceLinked: 3}
	caps := clusterCapabilitiesFromModelWithCoverage(cluster, &partial)
	assertCapabilityMissing(t, caps, "node-metrics")
	assertCapabilityMissing(t, caps, "host-access")

	complete := biz.NodeCoverage{ClusterID: 1, Total: 5, EdgeLinked: 5, DeviceLinked: 5}
	caps = clusterCapabilitiesFromModelWithCoverage(cluster, &complete)
	assertCapabilityMissing(t, caps, "node-metrics")
	assertCapabilityMissing(t, caps, "host-access")

	dto := clusterDTOFromModelWithCoverage(cluster, &partial)
	if dto.NodeEdgeCoverage == nil {
		t.Fatal("node edge coverage is nil")
	}
	if dto.NodeEdgeCoverage.Missing != 2 || dto.NodeEdgeCoverage.Percent != 60 {
		t.Fatalf("node edge coverage = %+v, want missing=2 percent=60", dto.NodeEdgeCoverage)
	}
}

func TestClusterDTOUsesEffectiveOfflineStatusForStaleOnlineCluster(t *testing.T) {
	edgeID := uint64(42)
	old := time.Now().UTC().Add(-(biz.ClusterOnlineTTL + time.Minute))
	cluster := &model.Cluster{
		Mode:                     model.ModeFullNode,
		Status:                   model.ClusterStatusOnline,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
		LastSeenAt:               &old,
		InventorySyncedAt:        &old,
	}

	dto := clusterDTOFromModel(cluster)
	if dto.Status != model.ClusterStatusOffline {
		t.Fatalf("dto status = %q, want %q", dto.Status, model.ClusterStatusOffline)
	}
	assertCapabilityStatus(t, dto.Capabilities, "inventory", capabilityStatusUnavailable)
	assertCapabilityStatus(t, dto.Capabilities, "events", capabilityStatusUnavailable)
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

func assertCapabilityMissing(t *testing.T, items []clusterCapabilityDTO, key string) {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			t.Fatalf("capability %q should not be exposed", key)
		}
	}
}
