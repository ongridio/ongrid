package k8s

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func TestClusterCaptureFollowsNodesAndEnvironment(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	nodeID := uint64(95)
	cluster := &model.Cluster{Name: "test", NodeID: &nodeID}
	if err := repo.CreateCluster(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	bindFakeController(t, repo, cluster.ID, 63)
	bindFakeNode(t, repo, cluster.ID, 64, "node-a", "node-a-uid")
	uc := NewUsecase(repo, nil, Config{})
	environment := "test"
	notified := map[uint64]bool{}
	uc.SetAutoAPMProviders(func(context.Context, uint64) (string, error) { return environment, nil }, func(_ context.Context, id uint64) { notified[id] = true })
	spec := autoapm.Spec{Kubernetes: &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "orders"}}}}
	saved, err := uc.SetAutoAPM(ctx, cluster.ID, spec.Map())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Spec.Environment != "" || saved.Defaults.Environment != "test" || !notified[64] {
		t.Fatalf("save/default/notification mismatch: %+v", saved)
	}
	bindFakeNode(t, repo, cluster.ID, 65, "node-b", "node-b-uid")
	environment = "production"
	for _, id := range []uint64{64, 65} {
		runtime, managed, err := uc.AutoAPMForEdge(ctx, id)
		if err != nil || !managed || runtime == nil || runtime.Environment != environment || len(runtime.Kubernetes.Rules) != 1 {
			t.Fatalf("node %d did not inherit: %+v %v %v", id, runtime, managed, err)
		}
	}
	controller, managed, err := uc.AutoAPMForEdge(ctx, 63)
	if err != nil || !managed || controller != nil {
		t.Fatalf("controller must not capture: %+v %v %v", controller, managed, err)
	}
	_, managed, err = uc.AutoAPMForEdge(ctx, 1000)
	if err != nil || managed {
		t.Fatal("ordinary host classified as Kubernetes")
	}
	spec.Environment = "override"
	if _, err := uc.SetAutoAPM(ctx, cluster.ID, spec.Map()); err == nil {
		t.Fatal("environment override accepted")
	}
	spec.Environment = ""
	spec.Kubernetes.Rules = nil
	if _, err := uc.SetAutoAPM(ctx, cluster.ID, spec.Map()); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := uc.AutoAPMForEdge(ctx, 64)
	if err != nil || runtime.Selected() {
		t.Fatal("empty rules must stop capture")
	}
}

func TestTelemetryClusterMappingAndOwnership(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	nodeID := uint64(132)
	cluster := &model.Cluster{Name: "mapped", NodeID: &nodeID}
	if err := repo.CreateCluster(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	bindFakeController(t, repo, cluster.ID, 63)
	bindFakeNode(t, repo, cluster.ID, 64, "node-a", "uid-a")
	uc := NewUsecase(repo, nil, Config{})
	for _, id := range []uint64{63, 64} {
		unified, internal, err := uc.TelemetryClusterForEdge(ctx, id)
		if err != nil || unified != 132 || internal != cluster.ID {
			t.Fatalf("edge %d: %d %d %v", id, unified, internal, err)
		}
	}
	unified, internal, err := uc.TelemetryClusterForEdge(ctx, 1000)
	if err != nil || unified != 0 || internal != 0 {
		t.Fatal("ordinary host classified as Kubernetes")
	}
	wire, err := uc.resolveTelemetryConfig(ctx, cluster, "test", "test")
	if err != nil || wire.ClusterNodeID != 132 || wire.ClusterID != cluster.ID {
		t.Fatalf("gateway mapping: %+v %v", wire, err)
	}
	spec := autoapm.Spec{ClusterID: 999, K8sClusterID: cluster.ID, Kubernetes: &autoapm.Kubernetes{}}
	if _, err := uc.SetAutoAPM(ctx, cluster.ID, spec.Map()); err == nil {
		t.Fatal("accepted user supplied mapping")
	}
	repo.clusters[cluster.ID].NodeID = nil
	if _, _, err := uc.TelemetryClusterForEdge(ctx, 64); err == nil {
		t.Fatal("missing mapping fell back to internal ID")
	}
}
