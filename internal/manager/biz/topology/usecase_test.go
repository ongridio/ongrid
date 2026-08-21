package topology_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	store "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func newUC(t *testing.T) *biz.Usecase {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return biz.NewUsecase(store.NewNodeRepo(db), store.NewRelationRepo(db), store.NewRelationTypeRepo(db), store.NewNodeTypeRepo(db), nil)
}

func TestCreateNodeValidation(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	if _, err := uc.CreateNode(ctx, "", "x", ""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("missing type: want ErrInvalid, got %v", err)
	}
	if _, err := uc.CreateNode(ctx, "service", "", ""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("missing name: want ErrInvalid, got %v", err)
	}
	if _, err := uc.CreateNode(ctx, "service", "x", "not-json"); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid json: want ErrInvalid, got %v", err)
	}
	n, err := uc.CreateNode(ctx, "service", "order-api", `{"team":"pay"}`)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if n.ID == 0 {
		t.Fatalf("expected non-zero ID")
	}
}

func TestCreateRelationValidates(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	a, _ := uc.CreateNode(ctx, "service", "a", "")
	b, _ := uc.CreateNode(ctx, "service", "b", "")

	// Endpoints must exist.
	if _, err := uc.CreateRelation(ctx, a.ID, 9999, model.RelDependsOn, ""); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing dst: want ErrNotFound, got %v", err)
	}

	// Self-edge rejected.
	if _, err := uc.CreateRelation(ctx, a.ID, a.ID, model.RelDependsOn, ""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("self-edge: want ErrInvalid, got %v", err)
	}

	// Unknown relation type rejected.
	if _, err := uc.CreateRelation(ctx, a.ID, b.ID, "made_up_thing", ""); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("unknown type: want ErrNotFound (via RT lookup), got %v", err)
	}

	// Happy path.
	r, err := uc.CreateRelation(ctx, a.ID, b.ID, model.RelDependsOn, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if r.ID == 0 || r.Type != model.RelDependsOn {
		t.Fatalf("bad relation: %+v", r)
	}
}

func TestAssignEnrollmentDeviceIsIdempotentAndRejectsAutomaticMove(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	clusterA, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "bare-metal-a", "")
	if err != nil {
		t.Fatalf("CreateNode(clusterA): %v", err)
	}
	clusterB, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "bare-metal-b", "")
	if err != nil {
		t.Fatalf("CreateNode(clusterB): %v", err)
	}
	device, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "host-a", "")
	if err != nil {
		t.Fatalf("CreateNode(device): %v", err)
	}
	if err := uc.AssignEnrollmentDevice(ctx, clusterA.ID, device.ID, 11, 21); err != nil {
		t.Fatalf("AssignEnrollmentDevice(first): %v", err)
	}
	if err := uc.AssignEnrollmentDevice(ctx, clusterA.ID, device.ID, 11, 21); err != nil {
		t.Fatalf("AssignEnrollmentDevice(retry): %v", err)
	}
	relations, total, err := uc.ListRelations(ctx, biz.RelationListFilter{SrcID: device.ID, Type: model.RelMemberOf})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if total != 1 || len(relations) != 1 || relations[0].DstID != clusterA.ID || !strings.Contains(relations[0].PropsJSON, `"source":"edge_enrollment"`) {
		t.Fatalf("relations = %+v total=%d", relations, total)
	}
	if err := uc.AssignEnrollmentDevice(ctx, clusterB.ID, device.ID, 12, 21); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("move error = %v, want ErrConflict", err)
	}
}

func TestResolveDeviceCluster(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	device, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "host-a", "")
	if err != nil {
		t.Fatalf("CreateNode(device): %v", err)
	}
	service, err := uc.CreateNode(ctx, string(model.NodeTypeService), "orders", "")
	if err != nil {
		t.Fatalf("CreateNode(service): %v", err)
	}
	cluster, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "edge-fleet-a", "")
	if err != nil {
		t.Fatalf("CreateNode(cluster): %v", err)
	}

	id, name, err := uc.ResolveDeviceCluster(ctx, device.ID)
	if err != nil || id != 0 || name != "" {
		t.Fatalf("unbound ResolveDeviceCluster = (%d, %q, %v), want empty", id, name, err)
	}
	if _, err := uc.CreateRelation(ctx, device.ID, service.ID, model.RelMemberOf, ""); err != nil {
		t.Fatalf("CreateRelation(service membership): %v", err)
	}
	id, name, err = uc.ResolveDeviceCluster(ctx, device.ID)
	if err != nil || id != 0 || name != "" {
		t.Fatalf("non-cluster ResolveDeviceCluster = (%d, %q, %v), want empty", id, name, err)
	}
	if _, err := uc.CreateRelation(ctx, device.ID, cluster.ID, model.RelMemberOf, ""); err != nil {
		t.Fatalf("CreateRelation(cluster membership): %v", err)
	}
	id, name, err = uc.ResolveDeviceCluster(ctx, device.ID)
	if err != nil {
		t.Fatalf("ResolveDeviceCluster: %v", err)
	}
	if id != cluster.ID || name != cluster.Name {
		t.Fatalf("ResolveDeviceCluster = (%d, %q), want (%d, %q)", id, name, cluster.ID, cluster.Name)
	}
}

func TestDeviceClusterMembershipIsExclusiveAndKubernetesOwned(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	clusterA, _ := uc.CreateNode(ctx, string(model.NodeTypeCluster), "bare-metal-a", `{"source":"manual"}`)
	clusterB, _ := uc.CreateNode(ctx, string(model.NodeTypeCluster), "bare-metal-b", `{"source":"manual"}`)
	k8sCluster, _ := uc.CreateNode(ctx, string(model.NodeTypeCluster), "k8s", `{"source":"kubernetes"}`)
	device, _ := uc.CreateNode(ctx, string(model.NodeTypeDevice), "host-a", "")

	first, err := uc.CreateRelation(ctx, device.ID, clusterA.ID, model.RelMemberOf, `{"source":"manual"}`)
	if err != nil {
		t.Fatalf("CreateRelation(first): %v", err)
	}
	repeated, err := uc.CreateRelation(ctx, device.ID, clusterA.ID, model.RelMemberOf, `{"source":"manual"}`)
	if err != nil || repeated.ID != first.ID {
		t.Fatalf("CreateRelation(idempotent) = %+v, %v; want relation %d", repeated, err, first.ID)
	}
	if _, err := uc.CreateRelation(ctx, device.ID, clusterB.ID, model.RelMemberOf, `{"source":"manual"}`); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("CreateRelation(second cluster) error = %v, want ErrConflict", err)
	}

	k8sDevice, _ := uc.CreateNode(ctx, string(model.NodeTypeDevice), "host-k8s", "")
	if _, err := uc.CreateRelation(ctx, k8sDevice.ID, k8sCluster.ID, model.RelMemberOf, `{"source":"manual"}`); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("CreateRelation(manual to k8s) error = %v, want ErrInvalid", err)
	}
	if _, err := uc.CreateRelation(ctx, k8sDevice.ID, clusterA.ID, model.RelMemberOf, `{"source":"kubernetes"}`); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("CreateRelation(k8s to manual) error = %v, want ErrInvalid", err)
	}
	if _, err := uc.CreateRelation(ctx, k8sDevice.ID, k8sCluster.ID, model.RelMemberOf, `{"source":"kubernetes"}`); err != nil {
		t.Fatalf("CreateRelation(k8s membership): %v", err)
	}
	if _, err := uc.CreateRelation(ctx, k8sDevice.ID, clusterA.ID, model.RelMemberOf, `{"source":"manual"}`); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("CreateRelation(k8s device to manual cluster) error = %v, want ErrConflict", err)
	}
}

type clusterDeleteGuardStub struct {
	blockedID uint64
}

func (g clusterDeleteGuardStub) ValidateClusterDelete(_ context.Context, clusterNodeID uint64) error {
	if clusterNodeID == g.blockedID {
		return errs.ErrConflict
	}
	return nil
}

func TestDeleteClusterEnforcesRelationsAndDependentGuards(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	cluster, _ := uc.CreateNode(ctx, string(model.NodeTypeCluster), "bare-metal", `{"source":"manual"}`)
	device, _ := uc.CreateNode(ctx, string(model.NodeTypeDevice), "host", "")
	relation, err := uc.CreateRelation(ctx, device.ID, cluster.ID, model.RelMemberOf, `{"source":"manual"}`)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	if err := uc.DeleteNode(ctx, cluster.ID); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("DeleteNode(with relation) error = %v, want ErrConflict", err)
	}
	if err := uc.DeleteRelation(ctx, relation.ID); err != nil {
		t.Fatalf("DeleteRelation: %v", err)
	}
	uc.AddClusterDeleteGuard(clusterDeleteGuardStub{blockedID: cluster.ID})
	if err := uc.DeleteNode(ctx, cluster.ID); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("DeleteNode(blocked dependency) error = %v, want ErrConflict", err)
	}
}

func TestValidateEnrollmentClusterRejectsKubernetesOwnedNode(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	cluster, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "k8s-prod", `{"source":"kubernetes"}`)
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := uc.ValidateEnrollmentCluster(ctx, cluster.ID); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("ValidateEnrollmentCluster error = %v, want ErrInvalid", err)
	}
}

func TestValidateUpgradeClusterRequiresEveryRequestedMember(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	cluster, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "bare-metal-prod", `{"source":"manual"}`)
	if err != nil {
		t.Fatalf("CreateNode(cluster): %v", err)
	}
	member, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "member", "")
	if err != nil {
		t.Fatalf("CreateNode(member): %v", err)
	}
	outsider, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "outsider", "")
	if err != nil {
		t.Fatalf("CreateNode(outsider): %v", err)
	}
	if _, err := uc.CreateRelation(ctx, member.ID, cluster.ID, model.RelMemberOf, `{"source":"manual"}`); err != nil {
		t.Fatalf("CreateRelation(member): %v", err)
	}

	if err := uc.ValidateUpgradeCluster(ctx, cluster.ID, []uint64{member.ID}); err != nil {
		t.Fatalf("ValidateUpgradeCluster(member) error = %v", err)
	}
	if err := uc.ValidateUpgradeCluster(ctx, cluster.ID, []uint64{member.ID, outsider.ID}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("ValidateUpgradeCluster(outsider) error = %v, want ErrConflict", err)
	}
}

func TestEnsureDeviceConnectionIsCanonicalAndIdempotent(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	a, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "host-a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "switch-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := uc.EnsureDeviceConnection(ctx, b.ID, a.ID, `{"source":"network_discovery"}`); err != nil {
		t.Fatal(err)
	}
	if err := uc.EnsureDeviceConnection(ctx, a.ID, b.ID, `{"source":"network_discovery"}`); err != nil {
		t.Fatal(err)
	}
	rels, _, err := uc.ListRelations(ctx, biz.RelationListFilter{Type: model.RelConnectedTo})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].SrcID != minID(a.ID, b.ID) || rels[0].DstID != maxID(a.ID, b.ID) {
		t.Fatalf("relations=%+v", rels)
	}
}

func minID(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func maxID(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func TestEnsureKubernetesClusterUpsertsTopologyNode(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	id, err := uc.EnsureKubernetesCluster(ctx, 42, nil, "prod", "uid-prod", "full-node", "online")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster(create) error = %v", err)
	}
	n, err := uc.GetNode(ctx, id)
	if err != nil {
		t.Fatalf("GetNode() error = %v", err)
	}
	if n.Type != string(model.NodeTypeCluster) || n.Name != "prod" {
		t.Fatalf("node = %#v", n)
	}
	if !strings.Contains(n.PropsJSON, `"source":"kubernetes"`) || !strings.Contains(n.PropsJSON, `"k8s_cluster_id":42`) {
		t.Fatalf("props_jsonb = %s", n.PropsJSON)
	}

	id2, err := uc.EnsureKubernetesCluster(ctx, 42, &id, "prod-renamed", "uid-prod", "full-node", "online")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster(update) error = %v", err)
	}
	if id2 != id {
		t.Fatalf("node id after update = %d, want %d", id2, id)
	}
	n, err = uc.GetNode(ctx, id)
	if err != nil {
		t.Fatalf("GetNode(updated) error = %v", err)
	}
	if n.Name != "prod-renamed" {
		t.Fatalf("updated name = %q, want prod-renamed", n.Name)
	}
}

func TestEnsureKubernetesClusterDoesNotAdoptManualNodeWithSameName(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	manual, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "prod", `{"owner":"operator"}`)
	if err != nil {
		t.Fatalf("CreateNode(manual) error = %v", err)
	}

	k8sNodeID, err := uc.EnsureKubernetesCluster(ctx, 42, &manual.ID, "prod", "uid-prod", "full-node", "online")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster() error = %v", err)
	}
	if k8sNodeID == manual.ID {
		t.Fatalf("k8s cluster adopted manual topology node %d", manual.ID)
	}
	gotManual, err := uc.GetNode(ctx, manual.ID)
	if err != nil {
		t.Fatalf("GetNode(manual) error = %v", err)
	}
	if gotManual.PropsJSON != `{"owner":"operator"}` {
		t.Fatalf("manual node props changed to %s", gotManual.PropsJSON)
	}
	gotK8s, err := uc.GetNode(ctx, k8sNodeID)
	if err != nil {
		t.Fatalf("GetNode(k8s) error = %v", err)
	}
	if !strings.Contains(gotK8s.PropsJSON, `"k8s_cluster_id":42`) {
		t.Fatalf("k8s node props = %s", gotK8s.PropsJSON)
	}
}

func TestEnsureKubernetesNodeMembershipCreatesAndPrunes(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	clusterID, err := uc.EnsureKubernetesCluster(ctx, 7, nil, "prod", "", "full-node", "online")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster() error = %v", err)
	}
	devA, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "node-a", "")
	if err != nil {
		t.Fatalf("CreateNode(devA) error = %v", err)
	}
	devB, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "node-b", "")
	if err != nil {
		t.Fatalf("CreateNode(devB) error = %v", err)
	}
	if err := uc.EnsureKubernetesNodeMembership(ctx, clusterID, devA.ID, 7, 101, "node-a", "uid-a"); err != nil {
		t.Fatalf("EnsureKubernetesNodeMembership(devA) error = %v", err)
	}
	if err := uc.EnsureKubernetesNodeMembership(ctx, clusterID, devB.ID, 7, 102, "node-b", "uid-b"); err != nil {
		t.Fatalf("EnsureKubernetesNodeMembership(devB) error = %v", err)
	}
	rels, total, err := uc.ListRelations(ctx, biz.RelationListFilter{DstID: clusterID, Type: model.RelMemberOf})
	if err != nil {
		t.Fatalf("ListRelations() error = %v", err)
	}
	if total != 2 || len(rels) != 2 {
		t.Fatalf("relations total=%d len=%d, want 2", total, len(rels))
	}

	if err := uc.PruneKubernetesNodeMemberships(ctx, clusterID, 7, []uint64{devA.ID}); err != nil {
		t.Fatalf("PruneKubernetesNodeMemberships() error = %v", err)
	}
	rels, total, err = uc.ListRelations(ctx, biz.RelationListFilter{DstID: clusterID, Type: model.RelMemberOf})
	if err != nil {
		t.Fatalf("ListRelations(after prune) error = %v", err)
	}
	if total != 1 || len(rels) != 1 || rels[0].SrcID != devA.ID {
		t.Fatalf("relations after prune total=%d rels=%#v, want only devA", total, rels)
	}
}

func TestDeleteKubernetesClusterRemovesOwnedNodeAndRelations(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	clusterID, err := uc.EnsureKubernetesCluster(ctx, 7, nil, "prod", "", "full-node", "online")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster() error = %v", err)
	}
	dev, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "node-a", "")
	if err != nil {
		t.Fatalf("CreateNode(device) error = %v", err)
	}
	app, err := uc.CreateNode(ctx, string(model.NodeTypeApp), "checkout", "")
	if err != nil {
		t.Fatalf("CreateNode(app) error = %v", err)
	}
	manualCluster, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "manual-prod", "")
	if err != nil {
		t.Fatalf("CreateNode(manual cluster) error = %v", err)
	}
	if err := uc.EnsureKubernetesNodeMembership(ctx, clusterID, dev.ID, 7, 101, "node-a", "uid-a"); err != nil {
		t.Fatalf("EnsureKubernetesNodeMembership() error = %v", err)
	}
	if _, err := uc.CreateRelation(ctx, app.ID, clusterID, model.RelDependsOn, ""); err != nil {
		t.Fatalf("CreateRelation(app->cluster) error = %v", err)
	}

	if err := uc.DeleteKubernetesCluster(ctx, 7, &clusterID); err != nil {
		t.Fatalf("DeleteKubernetesCluster() error = %v", err)
	}
	if _, err := uc.GetNode(ctx, clusterID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("deleted k8s cluster node lookup error = %v, want ErrNotFound", err)
	}
	if _, err := uc.GetNode(ctx, dev.ID); err != nil {
		t.Fatalf("device node should remain, got %v", err)
	}
	if _, err := uc.GetNode(ctx, app.ID); err != nil {
		t.Fatalf("app node should remain, got %v", err)
	}
	if _, err := uc.GetNode(ctx, manualCluster.ID); err != nil {
		t.Fatalf("manual cluster node should remain, got %v", err)
	}
	rels, total, err := uc.ListRelations(ctx, biz.RelationListFilter{SrcOrDstID: clusterID})
	if err != nil {
		t.Fatalf("ListRelations() error = %v", err)
	}
	if total != 0 || len(rels) != 0 {
		t.Fatalf("relations after delete total=%d len=%d, want 0", total, len(rels))
	}
}

func TestPruneDeletedKubernetesClustersRemovesOnlyStaleOwnedClusters(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	activeCluster, err := uc.EnsureKubernetesCluster(ctx, 7, nil, "active", "", "full-node", "online")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster(active) error = %v", err)
	}
	staleCluster, err := uc.EnsureKubernetesCluster(ctx, 8, nil, "stale", "", "full-node", "offline")
	if err != nil {
		t.Fatalf("EnsureKubernetesCluster(stale) error = %v", err)
	}
	manualCluster, err := uc.CreateNode(ctx, string(model.NodeTypeCluster), "manual", "")
	if err != nil {
		t.Fatalf("CreateNode(manual cluster) error = %v", err)
	}
	dev, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "node-a", "")
	if err != nil {
		t.Fatalf("CreateNode(device) error = %v", err)
	}
	app, err := uc.CreateNode(ctx, string(model.NodeTypeApp), "checkout", "")
	if err != nil {
		t.Fatalf("CreateNode(app) error = %v", err)
	}
	if err := uc.EnsureKubernetesNodeMembership(ctx, staleCluster, dev.ID, 8, 101, "node-a", "uid-a"); err != nil {
		t.Fatalf("EnsureKubernetesNodeMembership(stale) error = %v", err)
	}
	if _, err := uc.CreateRelation(ctx, app.ID, staleCluster, model.RelDependsOn, ""); err != nil {
		t.Fatalf("CreateRelation(app->stale) error = %v", err)
	}

	if err := uc.PruneDeletedKubernetesClusters(ctx, []uint64{7}); err != nil {
		t.Fatalf("PruneDeletedKubernetesClusters() error = %v", err)
	}
	if _, err := uc.GetNode(ctx, staleCluster); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("stale k8s cluster node lookup error = %v, want ErrNotFound", err)
	}
	for name, id := range map[string]uint64{
		"active k8s cluster": activeCluster,
		"manual cluster":     manualCluster.ID,
		"device":             dev.ID,
		"app":                app.ID,
	} {
		if _, err := uc.GetNode(ctx, id); err != nil {
			t.Fatalf("%s should remain, got %v", name, err)
		}
	}
	rels, total, err := uc.ListRelations(ctx, biz.RelationListFilter{SrcOrDstID: staleCluster})
	if err != nil {
		t.Fatalf("ListRelations(stale) error = %v", err)
	}
	if total != 0 || len(rels) != 0 {
		t.Fatalf("stale relations after prune total=%d len=%d, want 0", total, len(rels))
	}
}

func TestDeleteNodeForDeviceRemovesRelationsAndNode(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	dev, err := uc.CreateNode(ctx, string(model.NodeTypeDevice), "node-a", "")
	if err != nil {
		t.Fatalf("CreateNode(device) error = %v", err)
	}
	svc, err := uc.CreateNode(ctx, string(model.NodeTypeService), "svc-a", "")
	if err != nil {
		t.Fatalf("CreateNode(service) error = %v", err)
	}
	if _, err := uc.CreateRelation(ctx, svc.ID, dev.ID, model.RelDeployedOn, ""); err != nil {
		t.Fatalf("CreateRelation(deployed_on) error = %v", err)
	}
	if _, err := uc.CreateRelation(ctx, dev.ID, svc.ID, model.RelMonitors, ""); err != nil {
		t.Fatalf("CreateRelation(monitors) error = %v", err)
	}

	if err := uc.DeleteNodeForDevice(ctx, 99, dev.ID); err != nil {
		t.Fatalf("DeleteNodeForDevice() error = %v", err)
	}
	if _, err := uc.GetNode(ctx, dev.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("deleted device node lookup error = %v, want ErrNotFound", err)
	}
	if _, err := uc.GetNode(ctx, svc.ID); err != nil {
		t.Fatalf("service node should remain, got %v", err)
	}
	rels, total, err := uc.ListRelations(ctx, biz.RelationListFilter{SrcOrDstID: dev.ID})
	if err != nil {
		t.Fatalf("ListRelations() error = %v", err)
	}
	if total != 0 || len(rels) != 0 {
		t.Fatalf("relations after delete total=%d len=%d, want 0", total, len(rels))
	}
}

func TestRegisterRelationType(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	// Built-in collision.
	_, err := uc.RegisterRelationType(ctx, model.RelationType{
		Name:         model.RelDependsOn,
		Direction:    string(model.DirectionDstToSrc),
		SemanticsTag: string(model.SemanticsHardDep),
	})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("builtin collision: want ErrConflict, got %v", err)
	}

	// Bad direction.
	_, err = uc.RegisterRelationType(ctx, model.RelationType{
		Name:         "shares_storage_with",
		Direction:    "no_such_direction",
		SemanticsTag: string(model.SemanticsRedundancy),
	})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("bad direction: want ErrInvalid, got %v", err)
	}

	// Bad semantics tag.
	_, err = uc.RegisterRelationType(ctx, model.RelationType{
		Name:         "shares_storage_with",
		Direction:    string(model.DirectionBidirectional),
		SemanticsTag: "weird_bucket",
	})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("bad tag: want ErrInvalid, got %v", err)
	}

	// Happy path — custom type registered.
	rt, err := uc.RegisterRelationType(ctx, model.RelationType{
		Name:              "shares_storage_with",
		DisplayName:       "共享存储",
		PropagatesFailure: true,
		Direction:         string(model.DirectionBidirectional),
		SemanticsTag:      string(model.SemanticsRedundancy),
		Description:       "两节点挂同一块 NAS / SAN; 一方掉盘另一方也会受影响.",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if rt.Builtin {
		t.Fatalf("expected Builtin=false for operator-registered")
	}
}

func TestDeleteRelationTypeGuards(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()

	// Built-in rejected.
	if err := uc.DeleteRelationType(ctx, model.RelMemberOf); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("delete builtin: want ErrConflict, got %v", err)
	}

	// Register + use + try delete (should refuse because referenced).
	if _, err := uc.RegisterRelationType(ctx, model.RelationType{
		Name: "owns", Direction: string(model.DirectionSrcToDst),
		SemanticsTag: string(model.SemanticsAnnotation),
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	a, _ := uc.CreateNode(ctx, "service", "a", "")
	b, _ := uc.CreateNode(ctx, "service", "b", "")
	if _, err := uc.CreateRelation(ctx, a.ID, b.ID, "owns", ""); err != nil {
		t.Fatalf("create relation: %v", err)
	}
	if err := uc.DeleteRelationType(ctx, "owns"); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("delete with refs: want ErrConflict, got %v", err)
	}
}
