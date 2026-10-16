package k8s

import (
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestNodeCredentialRecoveryAfterInventoryReplacement(t *testing.T) {
	repo, issuer := newFakeRepo(), newFakeIssuer()
	uc := NewUsecase(repo, issuer, Config{})
	ctx := t.Context()
	reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	bindFakeController(t, repo, reg.Cluster.ID, 99)
	in := EnrollInput{BootstrapToken: reg.NodeBootstrapToken, ClusterID: reg.Cluster.ID, ClusterUID: testClusterUID, Role: model.RoleNode, NodeName: "node-a", NodeUID: "uid-old"}
	old, err := uc.Enroll(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := uint64(42)
	if err := uc.HandleRegister(ctx, old.EdgeID, &deviceID, tunnel.KubernetesInfo{ClusterID: in.ClusterID, ClusterUID: in.ClusterUID, Role: in.Role, NodeName: in.NodeName, NodeUID: in.NodeUID}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.IngestInventory(ctx, 99, tunnel.KubernetesInventoryRequest{
		ClusterID: in.ClusterID, Scope: "cluster", Nodes: []tunnel.KubernetesNodeSnapshot{{Name: "node-a", UID: "uid-new"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, exists := issuer.edges[old.EdgeID]; exists {
		t.Fatal("inventory did not delete the old Edge")
	}
	in.NodeUID, in.CachedEdgeID = "uid-new", old.EdgeID
	recovered, err := uc.Enroll(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.EdgeID == old.EdgeID || recovered.AccessKey == old.AccessKey || recovered.ReuseCredentials || issuer.rotate != 0 {
		t.Fatal("replacement did not receive a new identity")
	}
	// If the Agent could not persist the response, retry the pending enrollment.
	retried, err := uc.Enroll(ctx, in)
	if err != nil || retried.EdgeID != recovered.EdgeID || issuer.rotate != 1 {
		t.Fatalf("pending recovery retry: error=%v rotations=%d", err, issuer.rotate)
	}
	if err := uc.HandleRegister(ctx, retried.EdgeID, &deviceID, tunnel.KubernetesInfo{ClusterID: in.ClusterID, ClusterUID: in.ClusterUID, Role: in.Role, NodeName: in.NodeName, NodeUID: in.NodeUID}); err != nil {
		t.Fatalf("recovered node cannot register: %v", err)
	}
	if _, err := uc.Enroll(ctx, in); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("stale identity took over registered replacement: %v", err)
	}
	in.CachedEdgeID = retried.EdgeID
	reused, err := uc.Enroll(ctx, in)
	if err != nil || !reused.ReuseCredentials || reused.EdgeID != retried.EdgeID || reused.AccessKey != "" || reused.SecretKey != "" || issuer.rotate != 1 {
		t.Fatalf("healthy identity was not preserved: error=%v rotations=%d", err, issuer.rotate)
	}
}

func TestNodeCredentialRecoveryPreservesBindingsAndAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*EnrollInput)
		registered bool
		revoke     bool
		wantError  error
	}{
		{name: "healthy pending"},
		{name: "healthy registered", registered: true},
		{name: "revoked identity remains revoked", registered: true, revoke: true},
		{name: "invalid bootstrap", change: func(in *EnrollInput) { in.BootstrapToken = "invalid" }, wantError: errs.ErrUnauthorized},
		{name: "other cluster", change: func(in *EnrollInput) { in.ClusterUID = "other-cluster" }, wantError: errs.ErrConflict},
		{name: "other node name", change: func(in *EnrollInput) { in.NodeName = "other-node" }, wantError: errs.ErrConflict},
		{name: "unknown UID", change: func(in *EnrollInput) { in.NodeUID = "unknown" }, wantError: errs.ErrNotFound},
		{name: "registered replacement", registered: true, change: func(in *EnrollInput) { in.CachedEdgeID = 999 }, wantError: errs.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, issuer := newFakeRepo(), newFakeIssuer()
			uc := NewUsecase(repo, issuer, Config{})
			ctx := t.Context()
			reg, err := uc.CreateCluster(ctx, CreateClusterInput{Name: "prod"})
			if err != nil {
				t.Fatal(err)
			}
			in := EnrollInput{BootstrapToken: reg.NodeBootstrapToken, ClusterID: reg.Cluster.ID, ClusterUID: testClusterUID, Role: model.RoleNode, NodeName: "node-a", NodeUID: "uid-a"}
			old, err := uc.Enroll(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if tc.registered {
				deviceID := uint64(42)
				if err := uc.HandleRegister(ctx, old.EdgeID, &deviceID, tunnel.KubernetesInfo{ClusterID: in.ClusterID, ClusterUID: in.ClusterUID, Role: in.Role, NodeName: in.NodeName, NodeUID: in.NodeUID}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.revoke {
				if err := issuer.DeleteEdge(ctx, old.EdgeID); err != nil {
					t.Fatal(err)
				}
			}
			in.CachedEdgeID = old.EdgeID
			if tc.change != nil {
				tc.change(&in)
			}
			out, err := uc.Enroll(ctx, in)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("recovery error=%v, want %v", err, tc.wantError)
			}
			if err == nil && (!out.ReuseCredentials || out.EdgeID != old.EdgeID || out.AccessKey != "" || out.SecretKey != "") {
				t.Fatal("recovery changed or disclosed a bound credential")
			}
			if issuer.nextID != 1 || issuer.rotate != 0 {
				t.Fatalf("unexpected credential mutation: created=%d rotated=%d", issuer.nextID, issuer.rotate)
			}
		})
	}
}
