package k8s

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestUsecaseCreateClusterAndEnrollNode(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{PublicURL: "https://manager.example", TunnelAddr: "manager.example:40012"})

	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", Mode: model.ModeFullNode})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if reg.Cluster.ID == 0 {
		t.Fatalf("CreateCluster() did not assign cluster id")
	}
	if reg.BootstrapToken == "" {
		t.Fatalf("CreateCluster() did not return bootstrap token")
	}
	if !strings.Contains(reg.InstallCommand, "--set-string enrollment.clusterID=1") {
		t.Fatalf("install command missing cluster id: %s", reg.InstallCommand)
	}

	first, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "node-uid-a",
	})
	if err != nil {
		t.Fatalf("Enroll(first) error = %v", err)
	}
	if first.EdgeID == 0 || first.AccessKey == "" || first.SecretKey == "" {
		t.Fatalf("Enroll(first) returned incomplete edge credentials: %#v", first)
	}

	second, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "node-uid-a",
	})
	if err != nil {
		t.Fatalf("Enroll(second) error = %v", err)
	}
	if second.EdgeID != first.EdgeID {
		t.Fatalf("Enroll(second) edge id = %d, want reused %d", second.EdgeID, first.EdgeID)
	}
	if second.SecretKey == first.SecretKey {
		t.Fatalf("Enroll(second) should rotate plaintext secret for the reused edge identity")
	}
}

func TestUsecaseCreateClusterRejectsUnsupportedMode(t *testing.T) {
	ctx := context.Background()
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{PublicURL: "https://manager.example", TunnelAddr: "manager.example:40012"})

	_, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", Mode: "unsupported"})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("CreateCluster(unsupported) error = %v, want invalid input", err)
	}
}

func TestInstallCommandDerivesExternalTunnelAddr(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{
		PublicURL:  "https://manager.example.com",
		TunnelAddr: ":40012",
	})
	cmd := uc.installCommand(6, model.ModeFullNode, "token")
	if !strings.Contains(cmd, "--set-string manager.publicURL='https://manager.example.com'") {
		t.Fatalf("install command missing publicURL: %s", cmd)
	}
	if !strings.Contains(cmd, "--set namespace.create=false") {
		t.Fatalf("install command should disable chart-managed Namespace when using --create-namespace: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tunnelAddr='manager.example.com:40012'") {
		t.Fatalf("install command did not derive tunnelAddr from publicURL: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tlsInsecure=true") {
		t.Fatalf("install command missing tlsInsecure for self-signed manager TLS: %s", cmd)
	}
	if !strings.Contains(cmd, "--insecure-skip-tls-verify") {
		t.Fatalf("install command missing Helm chart TLS skip for manager-hosted HTTPS chart: %s", cmd)
	}
	if !strings.Contains(cmd, "'https://manager.example.com/edge/k8s/ongrid-edge.tgz'") {
		t.Fatalf("install command should use manager-hosted chart URL: %s", cmd)
	}
}

func TestInstallCommandUsesConfiguredChartRef(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{
		PublicURL: "https://manager.example.com",
		ChartRef:  "oci://ghcr.io/ongridio/charts/ongrid-edge",
	})
	cmd := uc.installCommand(6, model.ModeFullNode, "token")
	if !strings.Contains(cmd, "'oci://ghcr.io/ongridio/charts/ongrid-edge'") {
		t.Fatalf("install command should use configured chart ref: %s", cmd)
	}
	if strings.Contains(cmd, "--insecure-skip-tls-verify") {
		t.Fatalf("install command should not add HTTPS chart TLS flag for OCI chart refs: %s", cmd)
	}
}

func TestInstallCommandKeepsPlaceholdersWhenExternalAddressUnknown(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{
		TunnelAddr: ":40012",
	})
	cmd := uc.installCommand(6, model.ModeFullNode, "token")
	if !strings.Contains(cmd, "--set-string manager.publicURL='https://<manager>'") {
		t.Fatalf("install command should keep publicURL placeholder: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tunnelAddr='<manager>:40012'") {
		t.Fatalf("install command should keep tunnelAddr placeholder: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tlsInsecure=true") {
		t.Fatalf("install command missing tlsInsecure for self-signed manager TLS: %s", cmd)
	}
	if !strings.Contains(cmd, "--insecure-skip-tls-verify") {
		t.Fatalf("install command missing Helm chart TLS skip for placeholder HTTPS chart: %s", cmd)
	}
	if !strings.Contains(cmd, "'https://<manager>/edge/k8s/ongrid-edge.tgz'") {
		t.Fatalf("install command should use placeholder chart URL: %s", cmd)
	}
}

func TestUsecaseEnrollRejectsInvalidToken(t *testing.T) {
	ctx := context.Background()
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	_, err = uc.Enroll(ctx, EnrollInput{
		BootstrapToken: "wrong",
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "node-uid-a",
	})
	if !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("Enroll() error = %v, want unauthorized", err)
	}
}

func TestUsecaseLookupControllerCluster(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	if err := uc.HandleRegister(ctx, 41, nil, tunnel.KubernetesInfo{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		NodeName:  "node-a",
		Namespace: "ongrid-system",
		PodName:   "ongrid-edge-controller-abc",
	}); err != nil {
		t.Fatalf("HandleRegister(controller) error = %v", err)
	}
	registered, err := uc.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster() error = %v", err)
	}
	if registered.ControllerNodeName != "node-a" || registered.ControllerNamespace != "ongrid-system" || registered.ControllerPodName != "ongrid-edge-controller-abc" {
		t.Fatalf("controller runtime location = node:%q namespace:%q pod:%q", registered.ControllerNodeName, registered.ControllerNamespace, registered.ControllerPodName)
	}
	if repo.lastInstallation == nil || repo.lastInstallation.ScopeType != "cluster" {
		t.Fatalf("installation scope = %+v, want cluster scope for full-node", repo.lastInstallation)
	}

	clusterID, err := uc.LookupControllerCluster(ctx, 41)
	if err != nil {
		t.Fatalf("LookupControllerCluster() error = %v", err)
	}
	if clusterID != reg.Cluster.ID {
		t.Fatalf("LookupControllerCluster() = %d, want %d", clusterID, reg.Cluster.ID)
	}
	clusterID, err = uc.LookupControllerCluster(ctx, 99)
	if err != nil {
		t.Fatalf("LookupControllerCluster(unbound) error = %v", err)
	}
	if clusterID != 0 {
		t.Fatalf("LookupControllerCluster(unbound) = %d, want 0", clusterID)
	}
}

func TestUsecaseInventoryMergesNodeUIDWithExistingNodeEdge(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	deviceID := uint64(17)
	if err := uc.HandleRegister(ctx, 4, &deviceID, tunnel.KubernetesInfo{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleNode,
		NodeName:  "node-a",
	}); err != nil {
		t.Fatalf("HandleRegister(node) error = %v", err)
	}

	out, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Nodes: []tunnel.KubernetesNodeSnapshot{{
			Name:           "node-a",
			UID:            "real-node-uid",
			KubeletVersion: "v1.34.0",
		}},
	})
	if err != nil {
		t.Fatalf("IngestInventory() error = %v", err)
	}
	if out.AcceptedNodes != 1 {
		t.Fatalf("AcceptedNodes = %d, want 1", out.AcceptedNodes)
	}
	got, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "real-node-uid")
	if err != nil {
		t.Fatalf("GetNodeByClusterUID(real): %v", err)
	}
	if got.EdgeID == nil || *got.EdgeID != 4 || got.DeviceID == nil || *got.DeviceID != deviceID {
		t.Fatalf("real node link = edge:%v device:%v, want edge 4 device %d", got.EdgeID, got.DeviceID, deviceID)
	}
	if _, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "name:node-a"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("fallback node err = %v, want not found", err)
	}
}

func TestUsecaseTopologyReconcilesKubernetesNodesIntoCluster(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	uc.SetTopologyMirror(mirror)
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", UID: "cluster-uid"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if got := len(mirror.clusters); got != 1 {
		t.Fatalf("clusters mirrored after create = %d, want 1", got)
	}

	deviceID := uint64(17)
	deviceNodeID := uint64(701)
	repo.deviceNodeIDs[deviceID] = deviceNodeID
	if err := uc.HandleRegister(ctx, 4, &deviceID, tunnel.KubernetesInfo{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleNode,
		NodeName:  "node-a",
		NodeUID:   "node-uid-a",
	}); err != nil {
		t.Fatalf("HandleRegister(node) error = %v", err)
	}
	cluster, err := repo.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster() error = %v", err)
	}
	if cluster.NodeID == nil || *cluster.NodeID != fakeTopologyClusterNodeID(reg.Cluster.ID) {
		t.Fatalf("cluster node_id = %v, want %d", cluster.NodeID, fakeTopologyClusterNodeID(reg.Cluster.ID))
	}
	if got := len(mirror.memberships); got != 1 {
		t.Fatalf("memberships = %d, want 1", got)
	}
	got := mirror.memberships[0]
	if got.clusterNodeID != fakeTopologyClusterNodeID(reg.Cluster.ID) || got.deviceNodeID != deviceNodeID || got.deviceID != deviceID || got.nodeName != "node-a" {
		t.Fatalf("membership = %#v", got)
	}
	if got := len(mirror.prunes); got != 2 {
		t.Fatalf("prunes = %d, want 2 create+register reconciles", got)
	}
	if keep := mirror.prunes[len(mirror.prunes)-1].keep; len(keep) != 1 || keep[0] != deviceNodeID {
		t.Fatalf("last prune keep = %v, want [%d]", keep, deviceNodeID)
	}
}

func TestUsecaseTopologyBackfillsMissingDeviceNode(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	uc.SetTopologyMirror(mirror)
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", UID: "cluster-uid"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	deviceID := uint64(17)
	if err := uc.HandleRegister(ctx, 4, &deviceID, tunnel.KubernetesInfo{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleNode,
		NodeName:  "node-a",
		NodeUID:   "node-uid-a",
	}); err != nil {
		t.Fatalf("HandleRegister(node) error = %v", err)
	}

	deviceNodeID := fakeTopologyDeviceNodeID(deviceID)
	if got := len(mirror.deviceNodes); got != 1 {
		t.Fatalf("device nodes mirrored = %d, want 1", got)
	}
	if got := mirror.deviceNodes[0]; got.deviceID != deviceID || got.deviceName != "node-a" {
		t.Fatalf("device node mirror = %#v", got)
	}
	if got := repo.deviceNodeIDs[deviceID]; got != deviceNodeID {
		t.Fatalf("repo device node id = %d, want %d", got, deviceNodeID)
	}
	if got := len(mirror.memberships); got != 1 {
		t.Fatalf("memberships = %d, want 1", got)
	}
	got := mirror.memberships[0]
	if got.clusterNodeID != fakeTopologyClusterNodeID(reg.Cluster.ID) || got.deviceNodeID != deviceNodeID || got.deviceID != deviceID || got.nodeName != "node-a" {
		t.Fatalf("membership = %#v", got)
	}
}

func TestUsecaseDeleteClusterDeletesTopologyMirror(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	uc.SetTopologyMirror(mirror)
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	clusterNodeID := fakeTopologyClusterNodeID(reg.Cluster.ID)

	if err := uc.DeleteCluster(ctx, DeleteClusterInput{ID: reg.Cluster.ID}); err != nil {
		t.Fatalf("DeleteCluster() error = %v", err)
	}
	if _, err := repo.GetCluster(ctx, reg.Cluster.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("GetCluster(after delete) error = %v, want ErrNotFound", err)
	}
	if got := len(mirror.deletions); got != 1 {
		t.Fatalf("topology deletions = %d, want 1", got)
	}
	if got := mirror.deletions[0]; got.clusterID != reg.Cluster.ID || got.currentNodeID == nil || *got.currentNodeID != clusterNodeID {
		t.Fatalf("topology deletion = %#v, want cluster %d node %d", got, reg.Cluster.ID, clusterNodeID)
	}
}

func TestUsecaseDeleteClusterDeletesAssociatedEdges(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	controller, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleController,
		NodeName:       "control-plane",
		Namespace:      "ongrid-system",
	})
	if err != nil {
		t.Fatalf("Enroll(controller) error = %v", err)
	}
	node, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "node-uid-a",
	})
	if err != nil {
		t.Fatalf("Enroll(node) error = %v", err)
	}

	if err := uc.DeleteCluster(ctx, DeleteClusterInput{ID: reg.Cluster.ID, Force: true}); err != nil {
		t.Fatalf("DeleteCluster() error = %v", err)
	}
	want := map[uint64]bool{controller.EdgeID: true, node.EdgeID: true}
	if len(issuer.deleted) != len(want) {
		t.Fatalf("deleted edges = %v, want %v", issuer.deleted, want)
	}
	for _, got := range issuer.deleted {
		if !want[got] {
			t.Fatalf("unexpected deleted edge %d, want %v", got, want)
		}
	}
}

func TestUsecaseDeleteClusterRejectsActiveClusterWithoutForce(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if _, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleController,
		NodeName:       "control-plane",
	}); err != nil {
		t.Fatalf("Enroll(controller) error = %v", err)
	}

	if err := uc.DeleteCluster(ctx, DeleteClusterInput{ID: reg.Cluster.ID}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("DeleteCluster(active) error = %v, want ErrConflict", err)
	}
	if _, err := repo.GetCluster(ctx, reg.Cluster.ID); err != nil {
		t.Fatalf("active cluster should remain after rejected delete: %v", err)
	}
}

func TestUsecaseDeleteClusterIgnoresMissingAssociatedEdge(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	controller, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleController,
		NodeName:       "control-plane",
	})
	if err != nil {
		t.Fatalf("Enroll(controller) error = %v", err)
	}
	delete(issuer.edges, controller.EdgeID)

	if err := uc.DeleteCluster(ctx, DeleteClusterInput{ID: reg.Cluster.ID, Force: true}); err != nil {
		t.Fatalf("DeleteCluster() error = %v", err)
	}
	if _, err := repo.GetCluster(ctx, reg.Cluster.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("GetCluster(after delete) error = %v, want ErrNotFound", err)
	}
}

func TestUsecaseReconcileTopologyPrunesDeletedClusters(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	uc.SetTopologyMirror(mirror)
	active, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "active"})
	if err != nil {
		t.Fatalf("CreateCluster(active) error = %v", err)
	}
	deleted, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "deleted"})
	if err != nil {
		t.Fatalf("CreateCluster(deleted) error = %v", err)
	}
	if err := repo.DeleteCluster(ctx, deleted.Cluster.ID); err != nil {
		t.Fatalf("repo.DeleteCluster(deleted) error = %v", err)
	}

	if err := uc.ReconcileTopology(ctx); err != nil {
		t.Fatalf("ReconcileTopology() error = %v", err)
	}
	if got := len(mirror.deletedClusterPrunes); got != 1 {
		t.Fatalf("deleted cluster prunes = %d, want 1", got)
	}
	keep := mirror.deletedClusterPrunes[0]
	if len(keep) != 1 || keep[0] != active.Cluster.ID {
		t.Fatalf("deleted cluster prune keep = %v, want [%d]", keep, active.Cluster.ID)
	}
}

func TestUsecaseInventoryPrunesOnlyDeclaredScope(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Scope:     "namespace",
		Namespace: "ongrid-system",
	}); err != nil {
		t.Fatalf("IngestInventory(namespace) error = %v", err)
	}
	want := []string{"workloads:ongrid-system", "pods:ongrid-system", "events:ongrid-system"}
	if strings.Join(repo.pruned, ",") != strings.Join(want, ",") {
		t.Fatalf("namespace prune = %v, want %v", repo.pruned, want)
	}

	repo.pruned = nil
	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Scope:     "cluster",
	}); err != nil {
		t.Fatalf("IngestInventory(cluster) error = %v", err)
	}
	want = []string{"workloads:cluster", "pods:cluster", "events:cluster"}
	if strings.Join(repo.pruned, ",") != strings.Join(want, ",") {
		t.Fatalf("cluster prune = %v, want %v", repo.pruned, want)
	}
}

func TestUsecaseInventoryDeltaDeletesPodWithoutPrune(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Scope:     "cluster",
		Pods: []tunnel.KubernetesPodSnapshot{{
			Namespace: "default",
			Name:      "api-1",
			UID:       "pod-uid-1",
			Phase:     "Running",
		}},
	}); err != nil {
		t.Fatalf("IngestInventory(full) error = %v", err)
	}
	if len(repo.pods) != 1 {
		t.Fatalf("pods after full sync = %d, want 1", len(repo.pods))
	}

	repo.pruned = nil
	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Scope:     "cluster",
		SyncType:  "delta",
		DeletedPods: []tunnel.KubernetesPodRef{{
			Namespace: "default",
			Name:      "api-1",
			UID:       "pod-uid-1",
		}},
	}); err != nil {
		t.Fatalf("IngestInventory(delta) error = %v", err)
	}
	if len(repo.pods) != 0 {
		t.Fatalf("pods after delta delete = %d, want 0", len(repo.pods))
	}
	if len(repo.pruned) != 0 {
		t.Fatalf("delta sync should not prune by scope, got %v", repo.pruned)
	}
}

func TestUsecaseInventoryUpdatesSyncMetadata(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	collectedAt := time.Now().Add(-10 * time.Second).Unix()
	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID:         reg.Cluster.ID,
		Role:              model.RoleController,
		Scope:             "namespace",
		Namespace:         "ongrid-system",
		Ts:                collectedAt,
		ResourceVersion:   "42",
		ResourceVersions:  map[string]string{"pods:ongrid-system": "42", "events:ongrid-system": "41"},
		CollectDurationMS: 123,
	}); err != nil {
		t.Fatalf("IngestInventory() error = %v", err)
	}
	got, err := repo.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster() error = %v", err)
	}
	if got.ControllerEdgeID == nil || *got.ControllerEdgeID != 99 {
		t.Fatalf("ControllerEdgeID = %v, want 99", got.ControllerEdgeID)
	}
	if got.InventoryResourceVersion != "42" || got.InventoryScope != "namespace" || got.InventoryNamespace != "ongrid-system" {
		t.Fatalf("inventory metadata = rv:%q scope:%q ns:%q", got.InventoryResourceVersion, got.InventoryScope, got.InventoryNamespace)
	}
	if got.InventorySyncDurationMS != 123 || got.InventoryWatchLagSeconds != 0 || got.InventorySyncedAt == nil {
		t.Fatalf("sync health = duration:%d lag:%d synced:%v", got.InventorySyncDurationMS, got.InventoryWatchLagSeconds, got.InventorySyncedAt)
	}
	if !strings.Contains(got.InventoryResourceVersionsJSON, `"pods:ongrid-system":"42"`) {
		t.Fatalf("resource_versions_json = %s", got.InventoryResourceVersionsJSON)
	}
}

func TestUsecaseInventoryUsesWatchObservedAtForLag(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID:            reg.Cluster.ID,
		Role:                 model.RoleController,
		Ts:                   time.Now().Add(-5 * time.Minute).Unix(),
		WatchEventObservedAt: time.Now().Add(-4 * time.Second).Unix(),
		WatchTriggerReason:   "pods:MODIFIED",
	}); err != nil {
		t.Fatalf("IngestInventory() error = %v", err)
	}
	got, err := repo.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster() error = %v", err)
	}
	if got.InventoryWatchLagSeconds <= 0 || got.InventoryWatchLagSeconds > 20 {
		t.Fatalf("InventoryWatchLagSeconds = %d, want recent watch lag", got.InventoryWatchLagSeconds)
	}
}

func TestUsecaseInventoryAcceptsEvents(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	out, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Events: []tunnel.KubernetesEventSnapshot{{
			Namespace:         "default",
			Name:              "pod-a.123",
			UID:               "event-uid-a",
			Type:              "Warning",
			Reason:            "FailedScheduling",
			Message:           "0/1 nodes are available",
			InvolvedKind:      "Pod",
			InvolvedNamespace: "default",
			InvolvedName:      "pod-a",
			InvolvedUID:       "pod-uid-a",
			Count:             2,
			LastTimestamp:     "2026-06-29T11:00:00Z",
		}},
	})
	if err != nil {
		t.Fatalf("IngestInventory() error = %v", err)
	}
	if out.AcceptedEvents != 1 {
		t.Fatalf("AcceptedEvents = %d, want 1", out.AcceptedEvents)
	}
	if len(repo.events) != 1 {
		t.Fatalf("events len = %d, want 1", len(repo.events))
	}
	got := repo.events[0]
	if got.Reason != "FailedScheduling" || got.InvolvedName != "pod-a" || got.LastTimestamp == nil {
		t.Fatalf("event not normalized correctly: %#v", got)
	}
}

func TestUsecaseCleanupEventsAppliesRetentionAndClusterCap(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := newFakeRepo()
	repo.clusters[1] = &model.Cluster{ID: 1, Name: "prod-a", Mode: model.ModeFullNode, Status: model.ClusterStatusOnline}
	repo.clusters[2] = &model.Cluster{ID: 2, Name: "prod-b", Mode: model.ModeFullNode, Status: model.ClusterStatusOnline}
	event := func(clusterID uint64, uid string, ts time.Time) *model.Event {
		return &model.Event{
			ClusterID:     clusterID,
			Namespace:     "default",
			Name:          uid,
			UID:           uid,
			Type:          "Warning",
			Reason:        "Unhealthy",
			Message:       uid,
			LastTimestamp: &ts,
			LastSeenAt:    &now,
		}
	}
	repo.events = []*model.Event{
		event(1, "expired", now.Add(-48*time.Hour)),
		event(1, "newest", now.Add(-1*time.Hour)),
		event(1, "second-newest", now.Add(-2*time.Hour)),
		event(1, "over-cap", now.Add(-3*time.Hour)),
		event(2, "other-cluster", now.Add(-6*time.Hour)),
	}
	uc := NewUsecase(repo, newFakeIssuer(), Config{
		EventRetention:       24 * time.Hour,
		EventMaxPerCluster:   2,
		EventCleanupInterval: time.Hour,
	})

	stats, err := uc.CleanupEvents(ctx, now)
	if err != nil {
		t.Fatalf("CleanupEvents() error = %v", err)
	}
	if stats.DeletedByTTL != 1 || stats.DeletedByClusterLimit != 1 {
		t.Fatalf("CleanupEvents() stats = %+v, want ttl=1 cap=1", stats)
	}
	remaining := map[string]bool{}
	for _, item := range repo.events {
		remaining[item.UID] = true
	}
	for _, uid := range []string{"newest", "second-newest", "other-cluster"} {
		if !remaining[uid] {
			t.Fatalf("remaining events missing %s: %+v", uid, remaining)
		}
	}
	for _, uid := range []string{"expired", "over-cap"} {
		if remaining[uid] {
			t.Fatalf("event %s should have been deleted: %+v", uid, remaining)
		}
	}
}

type fakeRepo struct {
	nextClusterID    uint64
	nextNodeID       uint64
	clusters         map[uint64]*model.Cluster
	nodes            map[string]*model.Node
	deviceNodeIDs    map[uint64]uint64
	pods             map[string]*model.Pod
	events           []*model.Event
	pruned           []string
	lastInstallation *model.Installation
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		clusters:      map[uint64]*model.Cluster{},
		nodes:         map[string]*model.Node{},
		deviceNodeIDs: map[uint64]uint64{},
		pods:          map[string]*model.Pod{},
	}
}

func (r *fakeRepo) CreateCluster(_ context.Context, c *model.Cluster) error {
	r.nextClusterID++
	cp := *c
	cp.ID = r.nextClusterID
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.clusters[cp.ID] = &cp
	*c = cp
	return nil
}

func (r *fakeRepo) GetCluster(_ context.Context, id uint64) (*model.Cluster, error) {
	c, ok := r.clusters[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *fakeRepo) GetClusterByControllerEdge(_ context.Context, edgeID uint64) (*model.Cluster, error) {
	for _, c := range r.clusters {
		if c.ControllerEdgeID != nil && *c.ControllerEdgeID == edgeID {
			cp := *c
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) ListClusters(_ context.Context, _ ListClustersFilter) ([]*model.Cluster, error) {
	out := make([]*model.Cluster, 0, len(r.clusters))
	for _, c := range r.clusters {
		cp := *c
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) CountClusters(context.Context, ListClustersFilter) (int64, error) {
	return int64(len(r.clusters)), nil
}

func (r *fakeRepo) UpdateClusterToken(_ context.Context, id uint64, tokenHash string, expiresAt *time.Time) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
	c.BootstrapTokenHash = tokenHash
	c.BootstrapTokenExpiresAt = expiresAt
	return nil
}

func (r *fakeRepo) UpdateClusterController(_ context.Context, id uint64, in ClusterControllerRegistration) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
	c.ControllerEdgeID = &in.EdgeID
	c.ControllerNodeName = in.NodeName
	c.ControllerNamespace = in.Namespace
	c.ControllerPodName = in.PodName
	c.LastSeenAt = &in.LastSeen
	c.Status = model.ClusterStatusOnline
	return nil
}

func (r *fakeRepo) UpdateClusterInventorySync(_ context.Context, id uint64, in ClusterInventorySync) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
	c.ControllerEdgeID = &in.ControllerEdgeID
	c.LastSeenAt = &in.SyncedAt
	c.Status = model.ClusterStatusOnline
	c.InventoryResourceVersion = in.ResourceVersion
	c.InventoryResourceVersionsJSON = in.ResourceVersionsJSON
	c.InventoryScope = in.Scope
	c.InventoryNamespace = in.Namespace
	c.InventorySyncDurationMS = in.SyncDurationMS
	c.InventoryWatchLagSeconds = in.WatchLagSeconds
	c.InventorySyncedAt = &in.SyncedAt
	return nil
}

func (r *fakeRepo) UpdateClusterTopologyNode(_ context.Context, id, nodeID uint64) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
	c.NodeID = &nodeID
	return nil
}

func (r *fakeRepo) UpdateDeviceTopologyNode(_ context.Context, id, nodeID uint64) error {
	r.deviceNodeIDs[id] = nodeID
	return nil
}

func (r *fakeRepo) ListClusterEdgeIDs(_ context.Context, clusterID uint64) ([]uint64, error) {
	c, ok := r.clusters[clusterID]
	if !ok {
		return nil, errs.ErrNotFound
	}
	var out []uint64
	if c.ControllerEdgeID != nil {
		out = append(out, *c.ControllerEdgeID)
	}
	for _, n := range r.nodes {
		if n.ClusterID == clusterID && n.EdgeID != nil {
			out = append(out, *n.EdgeID)
		}
	}
	return uniqueNonZeroUint64(out), nil
}

func (r *fakeRepo) DeleteCluster(_ context.Context, id uint64) error {
	if _, ok := r.clusters[id]; !ok {
		return errs.ErrNotFound
	}
	delete(r.clusters, id)
	return nil
}

func (r *fakeRepo) GetNodeByClusterUID(_ context.Context, clusterID uint64, nodeUID string) (*model.Node, error) {
	n, ok := r.nodes[nodeKey(clusterID, nodeUID)]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *n
	return &cp, nil
}

func (r *fakeRepo) GetLinkedNodeByClusterName(_ context.Context, clusterID uint64, nodeName string) (*model.Node, error) {
	for _, n := range r.nodes {
		if n.ClusterID == clusterID && n.NodeName == nodeName && (n.EdgeID != nil || n.DeviceID != nil) {
			cp := *n
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) UpsertNode(_ context.Context, n *model.Node) error {
	key := nodeKey(n.ClusterID, n.NodeUID)
	if existing, ok := r.nodes[key]; ok {
		existing.NodeName = n.NodeName
		existing.ProviderID = n.ProviderID
		if n.EdgeID != nil {
			existing.EdgeID = n.EdgeID
		}
		if n.DeviceID != nil {
			existing.DeviceID = n.DeviceID
		}
		existing.LastSeenAt = n.LastSeenAt
		return nil
	}
	r.nextNodeID++
	cp := *n
	cp.ID = r.nextNodeID
	r.nodes[key] = &cp
	return nil
}

func (r *fakeRepo) DeleteDuplicateNodesByName(_ context.Context, clusterID uint64, nodeName, keepUID string) error {
	for key, n := range r.nodes {
		if n.ClusterID == clusterID && n.NodeName == nodeName && n.NodeUID != keepUID {
			delete(r.nodes, key)
		}
	}
	return nil
}

func (r *fakeRepo) UpdateNodeEdge(_ context.Context, nodeID, edgeID uint64, deviceID *uint64, lastSeen time.Time) error {
	for _, n := range r.nodes {
		if n.ID == nodeID {
			n.EdgeID = &edgeID
			if deviceID != nil {
				n.DeviceID = deviceID
			}
			n.LastSeenAt = &lastSeen
			return nil
		}
	}
	return errs.ErrNotFound
}

func (r *fakeRepo) UpsertWorkloads(_ context.Context, _ []*model.Workload) error {
	return nil
}

func (r *fakeRepo) UpsertPods(_ context.Context, items []*model.Pod) error {
	for _, item := range items {
		cp := *item
		r.pods[podKey(cp.ClusterID, cp.Namespace, cp.Name, cp.UID)] = &cp
	}
	return nil
}

func (r *fakeRepo) UpsertEvents(_ context.Context, items []*model.Event) error {
	r.events = append(r.events, items...)
	return nil
}

func (r *fakeRepo) DeleteNodes(_ context.Context, clusterID uint64, refs []NodeRef) error {
	for _, ref := range refs {
		for key, n := range r.nodes {
			if n.ClusterID != clusterID {
				continue
			}
			if ref.UID != "" && n.NodeUID == ref.UID {
				delete(r.nodes, key)
				continue
			}
			if ref.UID == "" && ref.Name != "" && n.NodeName == ref.Name {
				delete(r.nodes, key)
			}
		}
	}
	return nil
}

func (r *fakeRepo) DeleteWorkloads(context.Context, uint64, []WorkloadRef) error {
	return nil
}

func (r *fakeRepo) DeletePods(_ context.Context, clusterID uint64, refs []PodRef) error {
	for _, ref := range refs {
		for key, p := range r.pods {
			if p.ClusterID != clusterID {
				continue
			}
			if ref.UID != "" && p.UID == ref.UID {
				delete(r.pods, key)
				continue
			}
			if ref.UID == "" && ref.Name != "" && p.Namespace == ref.Namespace && p.Name == ref.Name {
				delete(r.pods, key)
			}
		}
	}
	return nil
}

func (r *fakeRepo) DeleteEvents(_ context.Context, clusterID uint64, refs []EventRef) error {
	filtered := r.events[:0]
	for _, event := range r.events {
		deleted := false
		for _, ref := range refs {
			if event.ClusterID != clusterID {
				continue
			}
			if ref.UID != "" && event.UID == ref.UID {
				deleted = true
				break
			}
			if ref.UID == "" && ref.Name != "" && event.Namespace == ref.Namespace && event.Name == ref.Name {
				deleted = true
				break
			}
		}
		if !deleted {
			filtered = append(filtered, event)
		}
	}
	r.events = filtered
	return nil
}

func (r *fakeRepo) DeleteStaleWorkloads(_ context.Context, _ uint64, namespace *string, _ time.Time) error {
	r.pruned = append(r.pruned, "workloads:"+namespaceLabel(namespace))
	return nil
}

func (r *fakeRepo) DeleteStalePods(_ context.Context, _ uint64, namespace *string, _ time.Time) error {
	r.pruned = append(r.pruned, "pods:"+namespaceLabel(namespace))
	return nil
}

func (r *fakeRepo) DeleteStaleEvents(_ context.Context, _ uint64, namespace *string, _ time.Time) error {
	r.pruned = append(r.pruned, "events:"+namespaceLabel(namespace))
	return nil
}

func (r *fakeRepo) DeleteEventsBefore(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	filtered := r.events[:0]
	var deleted int64
	for _, event := range r.events {
		if deleted < int64(limit) && fakeEventTimestamp(event).Before(cutoff) {
			deleted++
			continue
		}
		filtered = append(filtered, event)
	}
	r.events = filtered
	return deleted, nil
}

func (r *fakeRepo) DeleteOldestEvents(_ context.Context, clusterID uint64, keep, limit int) (int64, error) {
	if keep < 0 || limit <= 0 {
		return 0, nil
	}
	type eventRef struct {
		index int
		event *model.Event
	}
	clusterEvents := make([]eventRef, 0)
	for i, event := range r.events {
		if event.ClusterID == clusterID {
			clusterEvents = append(clusterEvents, eventRef{index: i, event: event})
		}
	}
	if len(clusterEvents) <= keep {
		return 0, nil
	}
	sort.SliceStable(clusterEvents, func(i, j int) bool {
		left := fakeEventTimestamp(clusterEvents[i].event)
		right := fakeEventTimestamp(clusterEvents[j].event)
		if !left.Equal(right) {
			return left.After(right)
		}
		return clusterEvents[i].index < clusterEvents[j].index
	})
	deleteIndexes := map[int]struct{}{}
	for i := keep; i < len(clusterEvents) && len(deleteIndexes) < limit; i++ {
		deleteIndexes[clusterEvents[i].index] = struct{}{}
	}
	if len(deleteIndexes) == 0 {
		return 0, nil
	}
	filtered := r.events[:0]
	for i, event := range r.events {
		if _, ok := deleteIndexes[i]; ok {
			continue
		}
		filtered = append(filtered, event)
	}
	r.events = filtered
	return int64(len(deleteIndexes)), nil
}

func (r *fakeRepo) ListNodes(_ context.Context, clusterID uint64) ([]*model.Node, error) {
	out := make([]*model.Node, 0)
	for _, n := range r.nodes {
		if n.ClusterID == clusterID {
			cp := *n
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *fakeRepo) ListTopologyNodeLinks(_ context.Context, clusterID uint64) ([]TopologyNodeLink, error) {
	out := make([]TopologyNodeLink, 0)
	for _, n := range r.nodes {
		if n.ClusterID != clusterID {
			continue
		}
		link := TopologyNodeLink{
			NodeName: n.NodeName,
			NodeUID:  n.NodeUID,
			DeviceID: n.DeviceID,
		}
		if n.DeviceID != nil {
			link.DeviceName = n.NodeName
			if nodeID := r.deviceNodeIDs[*n.DeviceID]; nodeID != 0 {
				link.DeviceNodeID = &nodeID
			}
		}
		out = append(out, link)
	}
	return out, nil
}

func (r *fakeRepo) CountNodes(ctx context.Context, clusterID uint64) (int64, error) {
	items, err := r.ListNodes(ctx, clusterID)
	return int64(len(items)), err
}

func (r *fakeRepo) GetNodeCoverage(ctx context.Context, clusterID uint64) (NodeCoverage, error) {
	items, err := r.ListNodes(ctx, clusterID)
	if err != nil {
		return NodeCoverage{}, err
	}
	out := NodeCoverage{ClusterID: clusterID, Total: int64(len(items))}
	for _, item := range items {
		if item.EdgeID != nil {
			out.EdgeLinked++
		}
		if item.DeviceID != nil {
			out.DeviceLinked++
		}
	}
	return out, nil
}

func (r *fakeRepo) ListWorkloads(_ context.Context, _ ListWorkloadsFilter) ([]*model.Workload, error) {
	return nil, nil
}

func (r *fakeRepo) CountWorkloads(ctx context.Context, f ListWorkloadsFilter) (int64, error) {
	items, err := r.ListWorkloads(ctx, f)
	return int64(len(items)), err
}

func (r *fakeRepo) ListPods(_ context.Context, _ ListPodsFilter) ([]*model.Pod, error) {
	out := make([]*model.Pod, 0, len(r.pods))
	for _, item := range r.pods {
		cp := *item
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) CountPods(ctx context.Context, f ListPodsFilter) (int64, error) {
	items, err := r.ListPods(ctx, f)
	return int64(len(items)), err
}

func (r *fakeRepo) ListEvents(_ context.Context, _ ListEventsFilter) ([]*model.Event, error) {
	return nil, nil
}

func (r *fakeRepo) CountEvents(ctx context.Context, f ListEventsFilter) (int64, error) {
	items, err := r.ListEvents(ctx, f)
	return int64(len(items)), err
}

func (r *fakeRepo) UpsertInstallation(_ context.Context, item *model.Installation) error {
	if item != nil {
		cp := *item
		r.lastInstallation = &cp
	}
	return nil
}

func nodeKey(clusterID uint64, nodeUID string) string {
	return strings.Join([]string{strconv.FormatUint(clusterID, 10), strings.TrimSpace(nodeUID)}, ":")
}

func podKey(clusterID uint64, namespace, name, uid string) string {
	return strings.Join([]string{
		strconv.FormatUint(clusterID, 10),
		strings.TrimSpace(namespace),
		strings.TrimSpace(name),
		strings.TrimSpace(uid),
	}, ":")
}

func namespaceLabel(namespace *string) string {
	if namespace == nil {
		return "cluster"
	}
	return *namespace
}

func fakeEventTimestamp(event *model.Event) time.Time {
	if event == nil {
		return time.Time{}
	}
	for _, ts := range []*time.Time{event.LastTimestamp, event.EventTime, event.FirstTimestamp, event.LastSeenAt} {
		if ts != nil && !ts.IsZero() {
			return *ts
		}
	}
	return event.CreatedAt
}

type fakeTopologyMirror struct {
	clusters             []fakeTopologyCluster
	deviceNodes          []fakeTopologyDeviceNode
	memberships          []fakeTopologyMembership
	prunes               []fakeTopologyPrune
	deletions            []fakeTopologyDeletion
	deletedClusterPrunes [][]uint64
}

type fakeTopologyCluster struct {
	clusterID     uint64
	currentNodeID *uint64
	name          string
	uid           string
	mode          string
	status        string
}

type fakeTopologyDeviceNode struct {
	deviceID   uint64
	deviceName string
}

type fakeTopologyMembership struct {
	clusterNodeID uint64
	deviceNodeID  uint64
	clusterID     uint64
	deviceID      uint64
	nodeName      string
	nodeUID       string
}

type fakeTopologyPrune struct {
	clusterNodeID uint64
	clusterID     uint64
	keep          []uint64
}

type fakeTopologyDeletion struct {
	clusterID     uint64
	currentNodeID *uint64
}

func (m *fakeTopologyMirror) EnsureNodeForDevice(_ context.Context, deviceID uint64, deviceName string) (uint64, error) {
	m.deviceNodes = append(m.deviceNodes, fakeTopologyDeviceNode{
		deviceID:   deviceID,
		deviceName: deviceName,
	})
	return fakeTopologyDeviceNodeID(deviceID), nil
}

func (m *fakeTopologyMirror) EnsureKubernetesCluster(_ context.Context, clusterID uint64, currentNodeID *uint64, name, uid, mode, status string) (uint64, error) {
	m.clusters = append(m.clusters, fakeTopologyCluster{
		clusterID:     clusterID,
		currentNodeID: currentNodeID,
		name:          name,
		uid:           uid,
		mode:          mode,
		status:        status,
	})
	return fakeTopologyClusterNodeID(clusterID), nil
}

func (m *fakeTopologyMirror) EnsureKubernetesNodeMembership(_ context.Context, clusterNodeID, deviceNodeID, clusterID, deviceID uint64, nodeName, nodeUID string) error {
	m.memberships = append(m.memberships, fakeTopologyMembership{
		clusterNodeID: clusterNodeID,
		deviceNodeID:  deviceNodeID,
		clusterID:     clusterID,
		deviceID:      deviceID,
		nodeName:      nodeName,
		nodeUID:       nodeUID,
	})
	return nil
}

func (m *fakeTopologyMirror) PruneKubernetesNodeMemberships(_ context.Context, clusterNodeID, clusterID uint64, keepDeviceNodeIDs []uint64) error {
	keep := append([]uint64(nil), keepDeviceNodeIDs...)
	m.prunes = append(m.prunes, fakeTopologyPrune{clusterNodeID: clusterNodeID, clusterID: clusterID, keep: keep})
	return nil
}

func (m *fakeTopologyMirror) DeleteKubernetesCluster(_ context.Context, clusterID uint64, currentNodeID *uint64) error {
	var nodeID *uint64
	if currentNodeID != nil {
		cp := *currentNodeID
		nodeID = &cp
	}
	m.deletions = append(m.deletions, fakeTopologyDeletion{clusterID: clusterID, currentNodeID: nodeID})
	return nil
}

func (m *fakeTopologyMirror) PruneDeletedKubernetesClusters(_ context.Context, activeClusterIDs []uint64) error {
	keep := append([]uint64(nil), activeClusterIDs...)
	m.deletedClusterPrunes = append(m.deletedClusterPrunes, keep)
	return nil
}

func fakeTopologyClusterNodeID(clusterID uint64) uint64 {
	return 900 + clusterID
}

func fakeTopologyDeviceNodeID(deviceID uint64) uint64 {
	return 1900 + deviceID
}

type fakeIssuer struct {
	nextID  uint64
	rotate  int
	edges   map[uint64]string
	deleted []uint64
}

func newFakeIssuer() *fakeIssuer {
	return &fakeIssuer{edges: map[uint64]string{}}
}

func (i *fakeIssuer) CreateEdgeIdentity(_ context.Context, _ string, _ *uint64) (*EdgeCredential, error) {
	i.nextID++
	accessKey := "ak-" + strconv.FormatUint(i.nextID, 10)
	i.edges[i.nextID] = accessKey
	return &EdgeCredential{EdgeID: i.nextID, AccessKey: accessKey, SecretKey: "sk-create"}, nil
}

func (i *fakeIssuer) RotateEdgeSecret(_ context.Context, edgeID uint64) (*EdgeCredential, error) {
	accessKey, ok := i.edges[edgeID]
	if !ok {
		return nil, errs.ErrNotFound
	}
	i.rotate++
	return &EdgeCredential{EdgeID: edgeID, AccessKey: accessKey, SecretKey: "sk-rotate-" + strconv.Itoa(i.rotate)}, nil
}

func (i *fakeIssuer) DeleteEdge(_ context.Context, edgeID uint64) error {
	if _, ok := i.edges[edgeID]; !ok {
		return errs.ErrNotFound
	}
	delete(i.edges, edgeID)
	i.deleted = append(i.deleted, edgeID)
	return nil
}
