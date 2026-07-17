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
	"github.com/ongridio/ongrid/internal/pkg/k8sredact"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const testClusterUID = "cluster-uid-test"

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
	if reg.BootstrapToken == "" || reg.NodeBootstrapToken == "" || reg.BootstrapToken == reg.NodeBootstrapToken {
		t.Fatalf("CreateCluster() did not return distinct controller/node bootstrap tokens")
	}
	if !strings.Contains(reg.InstallCommand, "--set-string enrollment.clusterID=1") {
		t.Fatalf("install command missing cluster id: %s", reg.InstallCommand)
	}

	first, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
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
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "node-uid-a",
	})
	if err != nil {
		t.Fatalf("Enroll(second) error = %v", err)
	}
	if second.EdgeID != first.EdgeID || issuer.rotate != 1 {
		t.Fatalf("retry edge=%d rotates=%d, want edge=%d rotates=1", second.EdgeID, issuer.rotate, first.EdgeID)
	}
}

func TestUsecaseConcurrentNodeEnrollmentUsesSingleIdentity(t *testing.T) {
	ctx := context.Background()
	issuer := newFakeIssuer()
	uc := NewUsecase(newFakeRepo(), issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	in := EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "uid-a",
	}
	type result struct {
		out *EnrollResult
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			out, err := uc.Enroll(ctx, in)
			results <- result{out: out, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	for i, got := range []result{first, second} {
		if got.err != nil {
			t.Fatalf("Enroll(%d) error = %v", i, got.err)
		}
	}
	if first.out.EdgeID != second.out.EdgeID || issuer.nextID != 1 || issuer.rotate != 1 {
		t.Fatalf("concurrent enrollment edges=%d/%d created=%d rotated=%d", first.out.EdgeID, second.out.EdgeID, issuer.nextID, issuer.rotate)
	}
}

func TestUsecaseNodeEnrollmentCannotRotateRegisteredIdentity(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	in := EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "uid-a",
	}
	first, err := uc.Enroll(ctx, in)
	if err != nil {
		t.Fatalf("Enroll(first) error = %v", err)
	}
	deviceID := uint64(42)
	if err := uc.HandleRegister(ctx, first.EdgeID, &deviceID, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: testClusterUID,
		Role:       model.RoleNode,
		NodeName:   "node-a",
		NodeUID:    "uid-a",
	}); err != nil {
		t.Fatalf("HandleRegister() error = %v", err)
	}
	if _, err := uc.Enroll(ctx, in); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Enroll(registered node) error = %v, want conflict", err)
	}
	if issuer.rotate != 0 {
		t.Fatalf("registered node secret rotations = %d, want 0", issuer.rotate)
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
		ImageTag:   "v0.10.0",
	})
	cmd := uc.installCommand(6, model.ModeFullNode, "controller-token", "node-token")
	if !strings.Contains(cmd, "--set-string manager.publicURL='https://manager.example.com'") {
		t.Fatalf("install command missing publicURL: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tunnelAddr='manager.example.com:40012'") {
		t.Fatalf("install command did not derive tunnelAddr from publicURL: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tlsInsecure=true") {
		t.Fatalf("install command missing tlsInsecure for self-signed manager TLS: %s", cmd)
	}
	if !strings.Contains(cmd, "'oci://helm.cnb.cool/ongridio/ongrid-edge'") {
		t.Fatalf("install command should use the CNB OCI chart: %s", cmd)
	}
	if !strings.Contains(cmd, "--version '0.10.0'") {
		t.Fatalf("install command should pin the chart version: %s", cmd)
	}
}

func TestInstallCommandOmitsChartVersionForDevBuild(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{
		PublicURL: "https://manager.example.com",
		ImageTag:  "dev",
	})
	cmd := uc.installCommand(6, model.ModeFullNode, "controller-token", "node-token")
	if !strings.Contains(cmd, "'oci://helm.cnb.cool/ongridio/ongrid-edge'") {
		t.Fatalf("install command should use the CNB OCI chart: %s", cmd)
	}
	if strings.Contains(cmd, "--version") {
		t.Fatalf("development builds should not request a non-SemVer chart version: %s", cmd)
	}
}

func TestUpgradeCommandUsesManagerConfig(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{
		PublicURL:  "https://manager.example.com:8443",
		TunnelAddr: "manager.example.com:40012",
		ImageTag:   "v0.9.1",
	})
	command := uc.UpgradeCommand(&model.Cluster{ControllerNamespace: "ongrid-system"})
	for _, want := range []string{
		"helm upgrade ongrid-edge",
		"'oci://helm.cnb.cool/ongridio/ongrid-edge'",
		"--version '0.9.1'",
		"--namespace 'ongrid-system'",
		"--reuse-values",
		"manager.publicURL='https://manager.example.com:8443'",
		"manager.tunnelAddr='manager.example.com:40012'",
		"image.tag='v0.9.1'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("UpgradeCommand() = %q, missing %q", command, want)
		}
	}
}

func TestInstallCommandKeepsPlaceholdersWhenExternalAddressUnknown(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{
		TunnelAddr: ":40012",
		ImageTag:   "v0.10.0",
	})
	cmd := uc.installCommand(6, model.ModeFullNode, "controller-token", "node-token")
	if !strings.Contains(cmd, "--set-string manager.publicURL='https://<manager>'") {
		t.Fatalf("install command should keep publicURL placeholder: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tunnelAddr='<manager>:40012'") {
		t.Fatalf("install command should keep tunnelAddr placeholder: %s", cmd)
	}
	if !strings.Contains(cmd, "--set-string manager.tlsInsecure=true") {
		t.Fatalf("install command missing tlsInsecure for self-signed manager TLS: %s", cmd)
	}
	if !strings.Contains(cmd, "'oci://helm.cnb.cool/ongridio/ongrid-edge'") {
		t.Fatalf("install command should use the CNB OCI chart: %s", cmd)
	}
	if !strings.Contains(cmd, "--version '0.10.0'") {
		t.Fatalf("install command should pin the chart version: %s", cmd)
	}
}

func TestInstallChartVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "release", input: "v0.10.0", want: "0.10.0"},
		{name: "prerelease", input: "v0.11.0-rc.1", want: "0.11.0-rc.1"},
		{name: "development", input: "dev", want: ""},
		{name: "empty", input: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := installChartVersion(tt.input); got != tt.want {
				t.Fatalf("installChartVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
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

func TestUsecaseEnrollBindsAndRejectsDifferentClusterUID(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	first, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     "physical-a",
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "node-a-uid",
	})
	if err != nil {
		t.Fatalf("Enroll(first cluster UID) error = %v", err)
	}
	cluster, err := repo.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster() error = %v", err)
	}
	if cluster.UID == nil || *cluster.UID != "physical-a" {
		t.Fatalf("bound cluster UID = %v, want physical-a", cluster.UID)
	}
	_, err = uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     "physical-b",
		Role:           model.RoleNode,
		NodeName:       "node-b",
		NodeUID:        "node-b-uid",
	})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Enroll(different cluster UID) error = %v, want conflict", err)
	}
	deviceID := uint64(42)
	err = uc.HandleRegister(ctx, first.EdgeID, &deviceID, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: "physical-b",
		Role:       model.RoleNode,
		NodeName:   "node-a",
		NodeUID:    "node-a-uid",
	})
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("HandleRegister(different cluster UID) error = %v, want forbidden", err)
	}
}

func TestUsecaseNodeTokenCannotEnrollController(t *testing.T) {
	ctx := context.Background()
	uc := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	_, err = uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		Role:           model.RoleController,
		NodeName:       "worker-a",
	})
	if !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("Enroll(controller with node token) error = %v, want unauthorized", err)
	}
}

func TestUsecaseControllerEnrollmentRequiresExplicitRecoveryAfterRegister(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	in := EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleController,
		Namespace:      "ongrid-system",
	}
	first, err := uc.Enroll(ctx, in)
	if err != nil {
		t.Fatalf("Enroll(first) error = %v", err)
	}
	second, err := uc.Enroll(ctx, in)
	if err != nil {
		t.Fatalf("Enroll(retry) error = %v", err)
	}
	if second.EdgeID != first.EdgeID || issuer.rotate != 1 {
		t.Fatalf("retry edge=%d rotates=%d, want edge=%d rotates=1", second.EdgeID, issuer.rotate, first.EdgeID)
	}
	if err := uc.HandleRegister(ctx, second.EdgeID, nil, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: testClusterUID,
		Role:       model.RoleController,
		Mode:       model.ModeFullNode,
		PodName:    "controller-0",
	}); err != nil {
		t.Fatalf("HandleRegister() error = %v", err)
	}
	if _, err := uc.Enroll(ctx, in); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Enroll(after register) error = %v, want conflict", err)
	}
	if issuer.rotate != 1 {
		t.Fatalf("secret rotations after blocked replay = %d, want 1", issuer.rotate)
	}
	rotated, err := uc.RotateBootstrapToken(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("RotateBootstrapToken() error = %v", err)
	}
	in.BootstrapToken = rotated.BootstrapToken
	third, err := uc.Enroll(ctx, in)
	if err != nil {
		t.Fatalf("Enroll(after explicit recovery) error = %v", err)
	}
	if third.EdgeID != first.EdgeID || issuer.rotate != 2 {
		t.Fatalf("recovery edge=%d rotates=%d, want edge=%d rotates=2", third.EdgeID, issuer.rotate, first.EdgeID)
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
	bindFakeController(t, repo, reg.Cluster.ID, 41)

	if err := uc.HandleRegister(ctx, 41, nil, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: testClusterUID,
		Role:       model.RoleController,
		NodeName:   "node-a",
		Namespace:  "ongrid-system",
		PodName:    "ongrid-edge-controller-abc",
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

func TestUsecaseHandleControllerHeartbeatRefreshesClusterLiveness(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", Mode: model.ModeFullNode})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	edgeID := uint64(41)
	old := time.Now().UTC().Add(-time.Hour)
	cluster := repo.clusters[reg.Cluster.ID]
	cluster.ControllerEdgeID = &edgeID
	cluster.LastSeenAt = &old
	cluster.Status = model.ClusterStatusOffline

	startedAt := time.Now().UTC()
	if err := uc.HandleControllerHeartbeat(ctx, edgeID); err != nil {
		t.Fatalf("HandleControllerHeartbeat() error = %v", err)
	}
	if cluster.Status != model.ClusterStatusOnline {
		t.Fatalf("cluster status = %q, want online", cluster.Status)
	}
	if cluster.LastSeenAt == nil || cluster.LastSeenAt.Before(startedAt) {
		t.Fatalf("cluster last_seen_at = %v, want >= %v", cluster.LastSeenAt, startedAt)
	}
	if err := uc.HandleControllerHeartbeat(ctx, 0); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("HandleControllerHeartbeat(0) error = %v, want invalid", err)
	}
}

func TestUsecaseHandleRegisterRejectsUnboundKubernetesIdentity(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if err := repo.BindClusterUID(ctx, reg.Cluster.ID, testClusterUID); err != nil {
		t.Fatalf("BindClusterUID() error = %v", err)
	}

	for _, role := range []string{model.RoleController, model.RoleNode} {
		err := uc.HandleRegister(ctx, 99, nil, tunnel.KubernetesInfo{ClusterID: reg.Cluster.ID, ClusterUID: testClusterUID, Role: role, NodeName: "node-a"})
		if !errors.Is(err, errs.ErrForbidden) {
			t.Fatalf("HandleRegister(%s) error = %v, want forbidden", role, err)
		}
	}
	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{ClusterID: reg.Cluster.ID}); !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("IngestInventory(unbound) error = %v, want forbidden", err)
	}
}

func TestUsecaseNodeEnrollmentCompensatesUnboundEdge(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.failUpdateNodeEdge = true
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	_, err = uc.Enroll(ctx, EnrollInput{BootstrapToken: reg.NodeBootstrapToken, ClusterID: reg.Cluster.ID, ClusterUID: testClusterUID, Role: model.RoleNode, NodeName: "node-a", NodeUID: "uid-a"})
	if err == nil {
		t.Fatal("Enroll() error = nil, want link failure")
	}
	if len(issuer.deleted) != 1 || issuer.deleted[0] != 1 {
		t.Fatalf("compensated edges = %v, want [1]", issuer.deleted)
	}
}

func TestUsecaseNodeEnrollmentReusesInventoryNodeUID(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if err := repo.UpsertNode(ctx, &model.Node{
		ClusterID:       reg.Cluster.ID,
		NodeName:        "node-a",
		NodeUID:         "inventory-node-uid",
		LabelsJSON:      `{"topology.kubernetes.io/zone":"test-a"}`,
		ConditionsJSON:  `[{"type":"Ready","status":"True"}]`,
		CapacityJSON:    `{"cpu":"8"}`,
		AllocatableJSON: `{"cpu":"7"}`,
		KubeletVersion:  "v1.34.9",
	}); err != nil {
		t.Fatalf("seed inventory node: %v", err)
	}

	result, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
	})
	if err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	node, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "inventory-node-uid")
	if err != nil {
		t.Fatalf("GetNodeByClusterUID() error = %v", err)
	}
	if node.EdgeID == nil || *node.EdgeID != result.EdgeID {
		t.Fatalf("inventory node edge = %v, want %d", node.EdgeID, result.EdgeID)
	}
	if node.LabelsJSON != `{"topology.kubernetes.io/zone":"test-a"}` ||
		node.ConditionsJSON != `[{"type":"Ready","status":"True"}]` ||
		node.CapacityJSON != `{"cpu":"8"}` ||
		node.AllocatableJSON != `{"cpu":"7"}` ||
		node.KubeletVersion != "v1.34.9" {
		t.Fatalf("inventory metadata was overwritten during enrollment: %#v", node)
	}
	if _, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "name:node-a"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("placeholder node error = %v, want not found", err)
	}
}

func TestUsecaseControllerEnrollmentCompensatesUnboundEdge(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.failBindController = true
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	_, err = uc.Enroll(ctx, EnrollInput{BootstrapToken: reg.BootstrapToken, ClusterID: reg.Cluster.ID, ClusterUID: testClusterUID, Role: model.RoleController})
	if err == nil {
		t.Fatal("Enroll() error = nil, want bind failure")
	}
	if len(issuer.deleted) != 1 || issuer.deleted[0] != 1 {
		t.Fatalf("compensated edges = %v, want [1]", issuer.deleted)
	}
}

func TestUsecaseBootstrapTokensRemainValidUntilRotation(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	cluster := repo.clusters[reg.Cluster.ID]
	if !validBootstrapToken(reg.BootstrapToken, cluster, model.RoleController) {
		t.Fatal("controller token should remain valid until manual rotation")
	}
	if !validBootstrapToken(reg.NodeBootstrapToken, cluster, model.RoleNode) {
		t.Fatal("node token should remain valid until manual rotation")
	}

	rotated, err := uc.RotateBootstrapToken(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("RotateBootstrapToken() error = %v", err)
	}
	cluster = repo.clusters[reg.Cluster.ID]
	if validBootstrapToken(reg.BootstrapToken, cluster, model.RoleController) {
		t.Fatal("old controller token should be rejected after manual rotation")
	}
	if validBootstrapToken(reg.NodeBootstrapToken, cluster, model.RoleNode) {
		t.Fatal("old node token should be rejected after manual rotation")
	}
	if !validBootstrapToken(rotated.BootstrapToken, cluster, model.RoleController) {
		t.Fatal("rotated controller token should be accepted")
	}
	if !validBootstrapToken(rotated.NodeBootstrapToken, cluster, model.RoleNode) {
		t.Fatal("rotated node token should be accepted")
	}
}

func TestUsecaseListNodesPagePaginatesAndFiltersIssues(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	edgeID := uint64(10)
	repo.nodes[nodeKey(reg.Cluster.ID, "ready")] = &model.Node{
		ClusterID: reg.Cluster.ID, NodeName: "ready", NodeUID: "ready", EdgeID: &edgeID,
		ConditionsJSON: `[{"type":"Ready","status":"True"}]`,
	}
	repo.nodes[nodeKey(reg.Cluster.ID, "not-ready")] = &model.Node{
		ClusterID: reg.Cluster.ID, NodeName: "not-ready", NodeUID: "not-ready", EdgeID: &edgeID,
		ConditionsJSON: `[{"type":"Ready","status":"False"}]`,
	}
	repo.nodes[nodeKey(reg.Cluster.ID, "missing-edge")] = &model.Node{
		ClusterID: reg.Cluster.ID, NodeName: "missing-edge", NodeUID: "missing-edge",
		ConditionsJSON: `[{"type":"Ready","status":"True"}]`,
	}

	items, total, err := uc.ListNodesPage(ctx, ListNodesFilter{ClusterID: reg.Cluster.ID, Limit: 2})
	if err != nil {
		t.Fatalf("ListNodesPage() error = %v", err)
	}
	if len(items) != 2 || total != 3 {
		t.Fatalf("ListNodesPage() = %d/%d, want 2/3", len(items), total)
	}
	issues, issueTotal, err := uc.ListNodesPage(ctx, ListNodesFilter{ClusterID: reg.Cluster.ID, IssueOnly: true, Limit: 100})
	if err != nil {
		t.Fatalf("ListNodesPage(issue) error = %v", err)
	}
	if len(issues) != 2 || issueTotal != 2 {
		t.Fatalf("ListNodesPage(issue) = %d/%d, want 2/2", len(issues), issueTotal)
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
	bindFakeNode(t, repo, reg.Cluster.ID, 4, "node-a", "name:node-a")
	bindFakeController(t, repo, reg.Cluster.ID, 99)

	deviceID := uint64(17)
	if err := uc.HandleRegister(ctx, 4, &deviceID, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: testClusterUID,
		Role:       model.RoleNode,
		NodeName:   "node-a",
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

func TestUsecaseInventoryReplacesSameNameNodeWithDifferentRealUID(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)
	old, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "uid-old",
	})
	if err != nil {
		t.Fatalf("Enroll(old node) error = %v", err)
	}

	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Scope:     "cluster",
		Nodes: []tunnel.KubernetesNodeSnapshot{{
			Name: "node-a",
			UID:  "uid-new",
		}},
	}); err != nil {
		t.Fatalf("IngestInventory() error = %v", err)
	}
	if _, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "uid-old"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("old node error = %v, want not found", err)
	}
	replacement, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "uid-new")
	if err != nil {
		t.Fatalf("replacement node error = %v", err)
	}
	if replacement.EdgeID != nil || replacement.DeviceID != nil {
		t.Fatalf("replacement inherited identity: edge=%v device=%v", replacement.EdgeID, replacement.DeviceID)
	}
	if len(issuer.deleted) != 1 || issuer.deleted[0] != old.EdgeID {
		t.Fatalf("deleted edges = %v, want [%d]", issuer.deleted, old.EdgeID)
	}
}

func TestUsecaseTopologyReconcilesKubernetesNodesIntoCluster(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	uc.SetTopologyMirror(mirror)
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", UID: testClusterUID})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if got := len(mirror.clusters); got != 1 {
		t.Fatalf("clusters mirrored after create = %d, want 1", got)
	}

	deviceID := uint64(17)
	deviceNodeID := uint64(701)
	repo.deviceNodeIDs[deviceID] = deviceNodeID
	bindFakeNode(t, repo, reg.Cluster.ID, 4, "node-a", "node-uid-a")
	if err := uc.HandleRegister(ctx, 4, &deviceID, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: testClusterUID,
		Role:       model.RoleNode,
		NodeName:   "node-a",
		NodeUID:    "node-uid-a",
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
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod", UID: testClusterUID})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	deviceID := uint64(17)
	bindFakeNode(t, repo, reg.Cluster.ID, 4, "node-a", "node-uid-a")
	if err := uc.HandleRegister(ctx, 4, &deviceID, tunnel.KubernetesInfo{
		ClusterID:  reg.Cluster.ID,
		ClusterUID: testClusterUID,
		Role:       model.RoleNode,
		NodeName:   "node-a",
		NodeUID:    "node-uid-a",
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
		ClusterUID:     testClusterUID,
		Role:           model.RoleController,
		NodeName:       "control-plane",
		Namespace:      "ongrid-system",
	})
	if err != nil {
		t.Fatalf("Enroll(controller) error = %v", err)
	}
	node, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
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
		ClusterUID:     testClusterUID,
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
		ClusterUID:     testClusterUID,
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

func TestUsecaseDeleteClusterKeepsClusterAndTopologyWhenEdgeDeleteFails(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, issuer, Config{})
	uc.SetTopologyMirror(mirror)
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	controller, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.BootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleController,
		NodeName:       "control-plane",
	})
	if err != nil {
		t.Fatalf("Enroll(controller) error = %v", err)
	}
	issuer.failDeleteEdgeID = controller.EdgeID

	if err := uc.DeleteCluster(ctx, DeleteClusterInput{ID: reg.Cluster.ID, Force: true}); err == nil {
		t.Fatal("DeleteCluster() error = nil, want edge delete failure")
	}
	if _, err := repo.GetCluster(ctx, reg.Cluster.ID); err != nil {
		t.Fatalf("cluster should remain for retry: %v", err)
	}
	if got := len(mirror.deletions); got != 0 {
		t.Fatalf("topology deletions = %d, want 0", got)
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
	bindFakeController(t, repo, reg.Cluster.ID, 99)

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
	want = []string{"nodes:cluster", "workloads:cluster", "pods:cluster", "events:cluster"}
	if strings.Join(repo.pruned, ",") != strings.Join(want, ",") {
		t.Fatalf("cluster prune = %v, want %v", repo.pruned, want)
	}
}

func TestUsecaseChunkedInventoryPrunesOnlyAfterFinalChunk(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)

	first := tunnel.KubernetesInventoryRequest{
		ClusterID:  reg.Cluster.ID,
		Scope:      "cluster",
		SyncType:   inventorySyncFull,
		SnapshotID: "snapshot-1",
		ChunkIndex: 0,
		ChunkCount: 2,
		Nodes:      []tunnel.KubernetesNodeSnapshot{{Name: "node-a", UID: "uid-a"}},
	}
	if _, err := uc.IngestInventory(ctx, 99, first); err != nil {
		t.Fatalf("IngestInventory(first chunk) error = %v", err)
	}
	if len(repo.pruned) != 0 {
		t.Fatalf("pruned after first chunk = %v, want none", repo.pruned)
	}
	cluster, err := repo.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster(after first chunk) error = %v", err)
	}
	if cluster.InventorySyncedAt != nil {
		t.Fatalf("InventorySyncedAt after first chunk = %v, want nil", cluster.InventorySyncedAt)
	}

	final := first
	final.ChunkIndex = 1
	final.Nodes = nil
	final.Pods = []tunnel.KubernetesPodSnapshot{{Namespace: "default", Name: "api", UID: "pod-uid"}}
	if _, err := uc.IngestInventory(ctx, 99, final); err != nil {
		t.Fatalf("IngestInventory(final chunk) error = %v", err)
	}
	wantPruned := []string{"nodes:cluster", "workloads:cluster", "pods:cluster", "events:cluster"}
	if strings.Join(repo.pruned, ",") != strings.Join(wantPruned, ",") {
		t.Fatalf("pruned after final chunk = %v, want %v", repo.pruned, wantPruned)
	}
	cluster, err = repo.GetCluster(ctx, reg.Cluster.ID)
	if err != nil {
		t.Fatalf("GetCluster(after final chunk) error = %v", err)
	}
	if cluster.InventorySyncedAt == nil {
		t.Fatal("InventorySyncedAt after final chunk = nil")
	}
}

func TestUsecaseChunkedInventoryRejectsOutOfOrderChunk(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)
	_, err = uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID:  reg.Cluster.ID,
		SyncType:   inventorySyncFull,
		SnapshotID: "snapshot-1",
		ChunkIndex: 1,
		ChunkCount: 2,
	})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("IngestInventory(out of order) error = %v, want conflict", err)
	}
}

func TestPrepareInventoryChunkUsesDatabaseSafeTimestampPrecision(t *testing.T) {
	u := NewUsecase(newFakeRepo(), newFakeIssuer(), Config{})
	receivedAt := time.Date(2026, time.July, 10, 10, 20, 30, 123456789, time.FixedZone("UTC+8", 8*60*60))

	got, final, err := u.prepareInventoryChunk(tunnel.KubernetesInventoryRequest{
		ClusterID:  1,
		SyncType:   inventorySyncFull,
		SnapshotID: "snapshot-precision",
		ChunkIndex: 0,
		ChunkCount: 2,
	}, receivedAt)
	if err != nil {
		t.Fatalf("prepareInventoryChunk() error = %v", err)
	}
	if final {
		t.Fatal("prepareInventoryChunk() final = true, want false")
	}
	want := receivedAt.UTC().Truncate(time.Millisecond)
	if !got.Equal(want) || got.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("prepareInventoryChunk() timestamp = %s, want %s", got, want)
	}
}

func TestUsecaseInventoryDeltaDeletesNodeIdentity(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)
	node, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "uid-a",
	})
	if err != nil {
		t.Fatalf("Enroll(node) error = %v", err)
	}

	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		SyncType:  "delta",
		DeletedNodes: []tunnel.KubernetesNodeRef{{
			Name: "node-a",
			UID:  "uid-a",
		}},
	}); err != nil {
		t.Fatalf("IngestInventory(delta) error = %v", err)
	}
	if _, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "uid-a"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("deleted node error = %v, want not found", err)
	}
	if len(issuer.deleted) != 1 || issuer.deleted[0] != node.EdgeID {
		t.Fatalf("deleted edges = %v, want [%d]", issuer.deleted, node.EdgeID)
	}
}

func TestUsecaseFullInventoryPrunesStaleNodeIdentity(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	issuer := newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)
	node, err := uc.Enroll(ctx, EnrollInput{
		BootstrapToken: reg.NodeBootstrapToken,
		ClusterID:      reg.Cluster.ID,
		ClusterUID:     testClusterUID,
		Role:           model.RoleNode,
		NodeName:       "node-a",
		NodeUID:        "uid-a",
	})
	if err != nil {
		t.Fatalf("Enroll(node) error = %v", err)
	}
	stored, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "uid-a")
	if err != nil {
		t.Fatalf("GetNodeByClusterUID() error = %v", err)
	}
	stale := time.Now().Add(-time.Hour)
	repo.nodes[nodeKey(reg.Cluster.ID, stored.NodeUID)].LastSeenAt = &stale

	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Scope:     "cluster",
	}); err != nil {
		t.Fatalf("IngestInventory(full) error = %v", err)
	}
	if _, err := repo.GetNodeByClusterUID(ctx, reg.Cluster.ID, "uid-a"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("stale node error = %v, want not found", err)
	}
	if len(issuer.deleted) != 1 || issuer.deleted[0] != node.EdgeID {
		t.Fatalf("deleted edges = %v, want [%d]", issuer.deleted, node.EdgeID)
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
	bindFakeController(t, repo, reg.Cluster.ID, 99)

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

func TestUsecasePodDeltaDoesNotReconcileTopology(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	uc.SetTopologyMirror(mirror)
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)
	clusterReconciles := len(mirror.clusters)

	_, err = uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		SyncType:  "delta",
		Pods:      []tunnel.KubernetesPodSnapshot{{Namespace: "default", Name: "api-1", UID: "pod-1"}},
	})
	if err != nil {
		t.Fatalf("IngestInventory(delta) error = %v", err)
	}
	if got := len(mirror.clusters); got != clusterReconciles {
		t.Fatalf("topology reconciles = %d, want unchanged %d", got, clusterReconciles)
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
	bindFakeController(t, repo, reg.Cluster.ID, 99)

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
	if got.InventorySyncedAt.Before(time.Unix(collectedAt, 0).Add(5 * time.Second)) {
		t.Fatalf("InventorySyncedAt = %v, must use manager receive time instead of edge timestamp", got.InventorySyncedAt)
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
	bindFakeController(t, repo, reg.Cluster.ID, 99)

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

func TestUsecaseClusterHealthUsesExactRepositoryCounts(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	clusterID := reg.Cluster.ID
	repo.pods[podKey(clusterID, "default", "pending", "pending-uid")] = &model.Pod{ClusterID: clusterID, Namespace: "default", Name: "pending", UID: "pending-uid", Phase: "Pending"}
	repo.pods[podKey(clusterID, "default", "crash", "crash-uid")] = &model.Pod{ClusterID: clusterID, Namespace: "default", Name: "crash", UID: "crash-uid", Phase: "Running", Reason: "CrashLoopBackOff"}
	repo.pods[podKey(clusterID, "default", "pull", "pull-uid")] = &model.Pod{ClusterID: clusterID, Namespace: "default", Name: "pull", UID: "pull-uid", Phase: "Pending", Reason: "ErrImagePull"}
	repo.nodes[nodeKey(clusterID, "node-uid")] = &model.Node{ClusterID: clusterID, NodeName: "node-a", NodeUID: "node-uid", ConditionsJSON: `[{"type":"Ready","status":"False"}]`}

	health, err := uc.GetClusterHealth(ctx, clusterID)
	if err != nil {
		t.Fatalf("GetClusterHealth() error = %v", err)
	}
	if health.PendingPods != 2 || health.CrashLoopBackOffPods != 1 || health.ImagePullBackOffPods != 1 || health.NotReadyNodes != 1 {
		t.Fatalf("health = %+v", health)
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
	bindFakeController(t, repo, reg.Cluster.ID, 99)

	out, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: reg.Cluster.ID,
		Role:      model.RoleController,
		Events: []tunnel.KubernetesEventSnapshot{{
			Namespace:         "default",
			Name:              "pod-a.123",
			UID:               "event-uid-a",
			Type:              "Warning",
			Reason:            "FailedScheduling",
			Message:           "0/1 nodes are available; token=top-secret",
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
	if strings.Contains(got.Message, "top-secret") || !strings.Contains(got.Message, "[REDACTED]") {
		t.Fatalf("event message not redacted: %q", got.Message)
	}
}

func TestRedactInventoryMap(t *testing.T) {
	in := map[string]string{
		"app":                   "api",
		"example.com/api-token": "top-secret",
		"endpoint":              "https://user:password@example.com/api",
	}
	out := k8sredact.StringMap(in)
	if out["app"] != "api" || out["example.com/api-token"] != "[REDACTED]" {
		t.Fatalf("redacted inventory map = %#v", out)
	}
	if strings.Contains(out["endpoint"], "password") {
		t.Fatalf("credential URL not redacted: %q", out["endpoint"])
	}
	if in["example.com/api-token"] != "top-secret" {
		t.Fatal("redaction must not mutate the inventory request map")
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

func TestUsecaseListEventsIssueOnlyExcludesRecoveredWarnings(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	uc := NewUsecase(repo, newFakeIssuer(), Config{})
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	repo.pods[podKey(reg.Cluster.ID, "default", "broken", "pod-broken")] = &model.Pod{
		ClusterID: reg.Cluster.ID,
		Namespace: "default",
		Name:      "broken",
		UID:       "pod-broken",
		Phase:     "Failed",
	}
	repo.nodes[nodeKey(reg.Cluster.ID, "node-bad")] = &model.Node{
		ClusterID:      reg.Cluster.ID,
		NodeName:       "node-bad",
		NodeUID:        "node-bad",
		ConditionsJSON: `[{"type":"DiskPressure","status":"True"}]`,
	}
	repo.events = []*model.Event{
		{ClusterID: reg.Cluster.ID, UID: "active-pod", Type: "Warning", InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "broken", InvolvedUID: "pod-broken"},
		{ClusterID: reg.Cluster.ID, UID: "recovered-pod", Type: "Warning", InvolvedKind: "Pod", InvolvedNamespace: "default", InvolvedName: "recovered", InvolvedUID: "pod-recovered"},
		{ClusterID: reg.Cluster.ID, UID: "active-node", Type: "Warning", InvolvedKind: "Node", InvolvedName: "node-bad"},
		{ClusterID: reg.Cluster.ID, UID: "unknown", Type: "Warning", InvolvedKind: "PersistentVolumeClaim", InvolvedNamespace: "default", InvolvedName: "data"},
	}

	items, err := uc.ListEvents(ctx, ListEventsFilter{ClusterID: reg.Cluster.ID, IssueOnly: true, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents(issue) error = %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("ListEvents(issue) count = %d, want 3", len(items))
	}
	total, err := uc.CountEvents(ctx, ListEventsFilter{ClusterID: reg.Cluster.ID, IssueOnly: true})
	if err != nil {
		t.Fatalf("CountEvents(issue) error = %v", err)
	}
	if total != 3 {
		t.Fatalf("CountEvents(issue) = %d, want 3", total)
	}
}

type fakeRepo struct {
	nextClusterID      uint64
	nextNodeID         uint64
	clusters           map[uint64]*model.Cluster
	nodes              map[string]*model.Node
	deviceNodeIDs      map[uint64]uint64
	pods               map[string]*model.Pod
	events             []*model.Event
	pruned             []string
	lastInstallation   *model.Installation
	failUpdateNodeEdge bool
	failBindController bool
}

func bindFakeController(t *testing.T, repo *fakeRepo, clusterID, edgeID uint64) {
	t.Helper()
	c, ok := repo.clusters[clusterID]
	if !ok {
		t.Fatalf("cluster %d not found", clusterID)
	}
	uid := testClusterUID
	c.UID = &uid
	c.ControllerEdgeID = &edgeID
}

func bindFakeNode(t *testing.T, repo *fakeRepo, clusterID, edgeID uint64, nodeName, nodeUID string) {
	t.Helper()
	c, ok := repo.clusters[clusterID]
	if !ok {
		t.Fatalf("cluster %d not found", clusterID)
	}
	uid := testClusterUID
	c.UID = &uid
	repo.nextNodeID++
	repo.nodes[nodeKey(clusterID, nodeUID)] = &model.Node{
		ID:        repo.nextNodeID,
		ClusterID: clusterID,
		NodeName:  nodeName,
		NodeUID:   nodeUID,
		EdgeID:    &edgeID,
	}
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

func (r *fakeRepo) BindClusterUID(_ context.Context, id uint64, uid string) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return errs.ErrInvalid
	}
	if c.UID == nil || strings.TrimSpace(*c.UID) == "" {
		for clusterID, existing := range r.clusters {
			if clusterID != id && existing.UID != nil && strings.TrimSpace(*existing.UID) == uid {
				return errs.ErrConflict
			}
		}
		c.UID = &uid
		return nil
	}
	if strings.TrimSpace(*c.UID) != uid {
		return errs.ErrConflict
	}
	return nil
}

func (r *fakeRepo) UpdateClusterTokens(_ context.Context, id uint64, controllerTokenHash, nodeTokenHash string) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
	c.BootstrapTokenHash = controllerTokenHash
	c.NodeBootstrapTokenHash = nodeTokenHash
	c.ControllerPodName = ""
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

func (r *fakeRepo) TouchClusterControllerHeartbeat(_ context.Context, edgeID uint64, at time.Time) error {
	for _, c := range r.clusters {
		if c.ControllerEdgeID != nil && *c.ControllerEdgeID == edgeID {
			c.LastSeenAt = &at
			c.Status = model.ClusterStatusOnline
		}
	}
	return nil
}

func (r *fakeRepo) BindControllerEnrollment(ctx context.Context, id uint64, registration ClusterControllerRegistration, installation *model.Installation) error {
	if r.failBindController {
		return errors.New("bind controller failed")
	}
	if err := r.UpdateClusterController(ctx, id, registration); err != nil {
		return err
	}
	if installation == nil {
		return errs.ErrInvalid
	}
	cp := *installation
	r.lastInstallation = &cp
	return nil
}

func (r *fakeRepo) UpdateClusterInventorySync(_ context.Context, id uint64, in ClusterInventorySync) error {
	c, ok := r.clusters[id]
	if !ok {
		return errs.ErrNotFound
	}
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

func (r *fakeRepo) GetClusterIDByEdgeID(_ context.Context, edgeID uint64) (uint64, error) {
	for clusterID, c := range r.clusters {
		if c.ControllerEdgeID != nil && *c.ControllerEdgeID == edgeID {
			return clusterID, nil
		}
		for _, node := range r.nodes {
			if node.ClusterID == clusterID && node.EdgeID != nil && *node.EdgeID == edgeID {
				return clusterID, nil
			}
		}
	}
	return 0, errs.ErrNotFound
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

func (r *fakeRepo) GetNodeByEdgeID(_ context.Context, edgeID uint64) (*model.Node, error) {
	for _, n := range r.nodes {
		if n.EdgeID != nil && *n.EdgeID == edgeID {
			cp := *n
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) GetNodeByClusterName(_ context.Context, clusterID uint64, nodeName string) (*model.Node, error) {
	for _, n := range r.nodes {
		if n.ClusterID == clusterID && n.NodeName == nodeName {
			cp := *n
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
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

func (r *fakeRepo) ListNodesByRefs(_ context.Context, clusterID uint64, refs []NodeRef) ([]*model.Node, error) {
	out := make([]*model.Node, 0, len(refs))
	for _, n := range r.nodes {
		if n.ClusterID != clusterID {
			continue
		}
		for _, ref := range refs {
			if (ref.UID != "" && n.NodeUID == ref.UID) || (ref.UID == "" && ref.Name != "" && n.NodeName == ref.Name) {
				cp := *n
				out = append(out, &cp)
				break
			}
		}
	}
	return out, nil
}

func (r *fakeRepo) ListStaleNodes(_ context.Context, clusterID uint64, olderThan time.Time) ([]*model.Node, error) {
	r.pruned = append(r.pruned, "nodes:cluster")
	var out []*model.Node
	for _, n := range r.nodes {
		if n.ClusterID != clusterID || (n.LastSeenAt != nil && !n.LastSeenAt.Before(olderThan)) {
			continue
		}
		cp := *n
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) UpsertNode(_ context.Context, n *model.Node) error {
	key := nodeKey(n.ClusterID, n.NodeUID)
	if existing, ok := r.nodes[key]; ok {
		existing.NodeName = n.NodeName
		existing.ProviderID = n.ProviderID
		existing.LabelsJSON = n.LabelsJSON
		existing.TaintsJSON = n.TaintsJSON
		existing.ConditionsJSON = n.ConditionsJSON
		existing.CapacityJSON = n.CapacityJSON
		existing.AllocatableJSON = n.AllocatableJSON
		existing.KubeletVersion = n.KubeletVersion
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
	if r.failUpdateNodeEdge {
		return errors.New("update node edge failed")
	}
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

func (r *fakeRepo) ListNodesPage(ctx context.Context, f ListNodesFilter) ([]*model.Node, error) {
	items, err := r.ListNodes(ctx, f.ClusterID)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(f.Query))
	filtered := make([]*model.Node, 0, len(items))
	for _, item := range items {
		text := strings.ToLower(strings.Join([]string{item.NodeName, item.NodeUID, item.ProviderID, item.KubeletVersion}, " "))
		if query == "" || strings.Contains(text, query) {
			filtered = append(filtered, item)
		}
	}
	if f.Offset >= len(filtered) {
		return []*model.Node{}, nil
	}
	end := len(filtered)
	if f.Limit > 0 && f.Offset+f.Limit < end {
		end = f.Offset + f.Limit
	}
	return filtered[f.Offset:end], nil
}

func (r *fakeRepo) CountNodesPage(ctx context.Context, f ListNodesFilter) (int64, error) {
	f.Limit = 0
	f.Offset = 0
	items, err := r.ListNodesPage(ctx, f)
	return int64(len(items)), err
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

func (r *fakeRepo) GetNodeCoverageByClusterIDs(ctx context.Context, clusterIDs []uint64) (map[uint64]NodeCoverage, error) {
	out := make(map[uint64]NodeCoverage, len(clusterIDs))
	for _, clusterID := range clusterIDs {
		coverage, err := r.GetNodeCoverage(ctx, clusterID)
		if err != nil {
			return nil, err
		}
		out[clusterID] = coverage
	}
	return out, nil
}

func (r *fakeRepo) ListEdgeAttachments(_ context.Context, limit, offset int) ([]EdgeAttachment, int64, error) {
	out := make([]EdgeAttachment, 0)
	for _, cluster := range r.clusters {
		if cluster.ControllerEdgeID != nil {
			out = append(out, EdgeAttachment{
				EdgeID:      *cluster.ControllerEdgeID,
				ClusterID:   cluster.ID,
				ClusterName: cluster.Name,
				ClusterMode: cluster.Mode,
				NodeName:    cluster.ControllerNodeName,
				Kind:        "k8s-controller",
			})
		}
		for _, node := range r.nodes {
			if node.ClusterID != cluster.ID || node.EdgeID == nil {
				continue
			}
			out = append(out, EdgeAttachment{
				EdgeID:      *node.EdgeID,
				ClusterID:   cluster.ID,
				ClusterName: cluster.Name,
				ClusterMode: cluster.Mode,
				NodeName:    node.NodeName,
				Kind:        "k8s-node",
			})
			if cluster.ControllerNodeName != "" && node.NodeName == cluster.ControllerNodeName {
				out = append(out, EdgeAttachment{
					EdgeID:      *node.EdgeID,
					ClusterID:   cluster.ID,
					ClusterName: cluster.Name,
					ClusterMode: cluster.Mode,
					NodeName:    node.NodeName,
					Kind:        "k8s-controller-runtime",
				})
			}
		}
	}
	total := int64(len(out))
	if offset >= len(out) {
		return []EdgeAttachment{}, total, nil
	}
	end := len(out)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return out[offset:end], total, nil
}

func (r *fakeRepo) ListWorkloads(_ context.Context, _ ListWorkloadsFilter) ([]*model.Workload, error) {
	return nil, nil
}

func (r *fakeRepo) CountWorkloads(ctx context.Context, f ListWorkloadsFilter) (int64, error) {
	items, err := r.ListWorkloads(ctx, f)
	return int64(len(items)), err
}

func (r *fakeRepo) ListPods(_ context.Context, f ListPodsFilter) ([]*model.Pod, error) {
	out := make([]*model.Pod, 0, len(r.pods))
	for _, item := range r.pods {
		if item.ClusterID != f.ClusterID || (f.Phase != "" && item.Phase != f.Phase) || (f.Reason != "" && item.Reason != f.Reason) {
			continue
		}
		if f.IssueOnly && item.Phase != "Pending" && item.Phase != "Failed" && item.Reason != "CrashLoopBackOff" && item.Reason != "OOMKilled" && item.Reason != "ImagePullBackOff" && item.Reason != "ErrImagePull" {
			continue
		}
		cp := *item
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) CountPods(ctx context.Context, f ListPodsFilter) (int64, error) {
	items, err := r.ListPods(ctx, f)
	return int64(len(items)), err
}

func (r *fakeRepo) ListEvents(_ context.Context, f ListEventsFilter) ([]*model.Event, error) {
	out := make([]*model.Event, 0, len(r.events))
	for _, item := range r.events {
		if item.ClusterID != f.ClusterID || (f.Type != "" && item.Type != f.Type) || (f.Reason != "" && item.Reason != f.Reason) {
			continue
		}
		if f.InvolvedKind != "" && item.InvolvedKind != f.InvolvedKind {
			continue
		}
		if f.InvolvedName != "" && item.InvolvedName != f.InvolvedName {
			continue
		}
		cp := *item
		out = append(out, &cp)
	}
	if f.Offset >= len(out) {
		return []*model.Event{}, nil
	}
	end := len(out)
	if f.Limit > 0 && f.Offset+f.Limit < end {
		end = f.Offset + f.Limit
	}
	return out[f.Offset:end], nil
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
	nextID           uint64
	rotate           int
	edges            map[uint64]string
	deleted          []uint64
	failDeleteEdgeID uint64
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
	if edgeID == i.failDeleteEdgeID {
		return errors.New("delete edge failed")
	}
	if _, ok := i.edges[edgeID]; !ok {
		return errs.ErrNotFound
	}
	delete(i.edges, edgeID)
	i.deleted = append(i.deleted, edgeID)
	return nil
}
