package integration

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	edgek8s "github.com/ongridio/ongrid/internal/edgeagent/k8s"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	k8sbiz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	devicestore "github.com/ongridio/ongrid/internal/manager/data/device/store"
	edgestore "github.com/ongridio/ongrid/internal/manager/data/edge/store"
	k8sstore "github.com/ongridio/ongrid/internal/manager/data/k8s/store"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	k8shttp "github.com/ongridio/ongrid/internal/manager/server/k8s"
	edgesvc "github.com/ongridio/ongrid/internal/manager/service/edge"
	k8ssvc "github.com/ongridio/ongrid/internal/manager/service/k8s"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestKubernetesNodeCredentialRecoveryChain(t *testing.T) {
	for _, startup := range []string{"ready", "manager-unavailable", "inventory-delayed"} {
		t.Run(startup, func(t *testing.T) {
			ctx := t.Context()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovery.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			for _, migrate := range []func(*gorm.DB) error{edgestore.Migrate, devicestore.Migrate, k8sstore.Migrate} {
				if err := migrate(db); err != nil {
					t.Fatal(err)
				}
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			edgeRepo := edgestore.NewRepo(db)
			edges := edgebiz.NewUsecase(edgeRepo, devicestore.NewRepo(db), devicestore.NewEdgeDeviceRepo(db), log)
			nodeRepo := k8sstore.NewRepo(db)
			nodes := k8sbiz.NewUsecase(nodeRepo, k8ssvc.NewEdgeIdentityIssuer(edgesvc.New(edges, nil, log)), k8sbiz.Config{})
			reg, err := nodes.CreateCluster(ctx, k8sbiz.CreateClusterInput{Name: "recovery-test"})
			if err != nil {
				t.Fatal(err)
			}
			if err := nodeRepo.UpdateClusterController(ctx, reg.Cluster.ID, k8sbiz.ClusterControllerRegistration{EdgeID: 999, LastSeen: time.Now()}); err != nil {
				t.Fatal(err)
			}
			clusterUID := "physical-cluster"
			register := func(cred *k8sbiz.EnrollResult, name, uid string) {
				t.Helper()
				if err := edges.HandleRegister(ctx, cred.EdgeID, tunnel.HostInfo{Hostname: name, Fingerprint: name}, "test"); err != nil {
					t.Fatal(err)
				}
				edge, err := edgeRepo.GetByID(ctx, cred.EdgeID)
				if err != nil {
					t.Fatal(err)
				}
				if err := nodes.HandleRegister(ctx, cred.EdgeID, edge.DeviceID, tunnel.KubernetesInfo{ClusterID: reg.Cluster.ID, ClusterUID: clusterUID, Role: "node", NodeName: name, NodeUID: uid}); err != nil {
					t.Fatal(err)
				}
			}
			enroll := func(name, uid string) *k8sbiz.EnrollResult {
				t.Helper()
				out, err := nodes.Enroll(ctx, k8sbiz.EnrollInput{BootstrapToken: reg.NodeBootstrapToken, ClusterID: reg.Cluster.ID, ClusterUID: clusterUID, Role: "node", NodeName: name, NodeUID: uid})
				if err != nil {
					t.Fatal(err)
				}
				register(out, name, uid)
				return out
			}
			old := enroll("node-a", "uid-old")
			healthy := enroll("node-b", "uid-healthy")
			healthyBefore, err := edgeRepo.GetByID(ctx, healthy.EdgeID)
			if err != nil {
				t.Fatal(err)
			}
			replaceNode := func() {
				if _, err := nodes.IngestInventory(ctx, 999, tunnel.KubernetesInventoryRequest{
					ClusterID: reg.Cluster.ID, Scope: "cluster", Nodes: []tunnel.KubernetesNodeSnapshot{
						{Name: "node-a", UID: "uid-new"}, {Name: "node-b", UID: "uid-healthy"},
					},
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := edgeRepo.GetByID(ctx, old.EdgeID); !errors.Is(err, errs.ErrNotFound) {
					t.Fatalf("old Edge was not deleted: %v", err)
				}
				if _, err := edgebiz.NewAccessKeyAuthenticator(edgeRepo, log).Authenticate(ctx, old.AccessKey, old.SecretKey); !errors.Is(err, errs.ErrUnauthorized) {
					t.Fatalf("old credential should reproduce unauthorized: %v", err)
				}
			}
			if startup != "inventory-delayed" {
				replaceNode()
			}
			router := chi.NewRouter()
			k8shttp.NewHandler(k8ssvc.New(nodes)).RegisterInternal(router)
			var failedOnce atomic.Bool
			manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if startup == "manager-unavailable" && r.URL.Path == "/internal/k8s/reconcile-node" && failedOnce.CompareAndSwap(false, true) {
					http.Error(w, "temporary Manager outage", http.StatusServiceUnavailable)
					return
				}
				router.ServeHTTP(w, r)
			}))
			defer manager.Close()
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uid := map[string]string{
					"/api/v1/namespaces/kube-system": clusterUID,
					"/api/v1/nodes/node-a":           "uid-new", "/api/v1/nodes/node-b": "uid-healthy",
				}[r.URL.Path]
				if uid == "" {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"uid": uid}})
			}))
			defer api.Close()
			host, port, err := net.SplitHostPort(strings.TrimPrefix(api.URL, "https://"))
			if err != nil {
				t.Fatal(err)
			}
			accountDir := t.TempDir()
			for name, value := range map[string][]byte{
				"token": []byte("test-token"), "namespace": []byte("ongrid"),
				"ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}),
			} {
				if err := os.WriteFile(filepath.Join(accountDir, name), value, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range map[string]string{
				"ONGRID_EDGE_MODE": "k8s-node", "ONGRID_K8S_ROLE": "node", "ONGRID_K8S_MODE": "full-node", "ONGRID_K8S_NODE_UID": "",
				"ONGRID_K8S_CLUSTER_ID": strconv.FormatUint(reg.Cluster.ID, 10), "ONGRID_K8S_BOOTSTRAP_TOKEN": reg.NodeBootstrapToken,
				"ONGRID_MANAGER_PUBLIC_URL": manager.URL, "ONGRID_K8S_SERVICE_ACCOUNT_DIR": accountDir, "ONGRID_K8S_CREDENTIAL_SECRET": "",
				"KUBERNETES_SERVICE_HOST": host, "KUBERNETES_SERVICE_PORT": port,
			} {
				t.Setenv(key, value)
			}
			for _, tc := range []struct {
				name, uid string
				cached    *k8sbiz.EnrollResult
				replaced  bool
			}{
				{name: "node-a", uid: "uid-new", cached: old, replaced: true},
				{name: "node-b", uid: "uid-healthy", cached: healthy},
			} {
				t.Run(tc.name, func(t *testing.T) {
					file := filepath.Join(t.TempDir(), "credential.json")
					legacy, err := json.Marshal(map[string]any{"cluster_id": reg.Cluster.ID, "role": "node", "node_name": tc.name, "edge_id": tc.cached.EdgeID, "access_key": tc.cached.AccessKey, "secret_key": tc.cached.SecretKey})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, legacy, 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("ONGRID_K8S_NODE_NAME", tc.name)
					t.Setenv("ONGRID_K8S_CREDENTIAL_FILE", file)
					cfg := &config.Config{}
					info, pending, err := edgek8s.EnsureEnrollment(ctx, cfg, "test", log)
					if err != nil {
						t.Fatal(err)
					}
					wantPending := tc.replaced && startup != "ready"
					if pending != wantPending {
						t.Fatalf("pending=%v, want %v", pending, wantPending)
					}
					if pending {
						if startup == "inventory-delayed" {
							replaceNode()
						}
						oldKey, oldSecret := cfg.Edge.AccessKey, cfg.Edge.SecretKey
						if err := edgek8s.RetryNodeEnrollment(ctx, *cfg, "test", log); !errors.Is(err, edgek8s.ErrNodeCredentialsRecovered) {
							t.Fatalf("recovery did not request a restart after persisting credentials: %v", err)
						}
						if cfg.Edge.AccessKey != oldKey || cfg.Edge.SecretKey != oldSecret {
							t.Fatal("recovery mutated the running Agent config")
						}
						// The supervised restart must load the persisted replacement identity.
						info, pending, err = edgek8s.EnsureEnrollment(ctx, cfg, "test", log)
						if err != nil || pending {
							t.Fatalf("restart: pending=%v error=%v", pending, err)
						}
					}
					session, err := edgebiz.NewAccessKeyAuthenticator(edgeRepo, log).Authenticate(ctx, cfg.Edge.AccessKey, cfg.Edge.SecretKey)
					if err != nil {
						t.Fatalf("reconciled credential cannot authenticate: %v", err)
					}
					if (session.EdgeID != tc.cached.EdgeID) != tc.replaced || info.NodeUID != tc.uid {
						t.Fatalf("unexpected recovered identity: edge=%d nodeUID=%q", session.EdgeID, info.NodeUID)
					}
					register(&k8sbiz.EnrollResult{EdgeID: session.EdgeID}, tc.name, tc.uid)
					bound, err := nodeRepo.GetNodeByClusterUID(ctx, reg.Cluster.ID, tc.uid)
					if err != nil || bound.EdgeID == nil || *bound.EdgeID != session.EdgeID || bound.DeviceID == nil {
						t.Fatalf("recovered node is not registered: %v", err)
					}
					restarted := &config.Config{}
					if _, _, err := edgek8s.EnsureEnrollment(ctx, restarted, "test", log); err != nil || restarted.Edge.AccessKey != cfg.Edge.AccessKey || restarted.Edge.SecretKey != cfg.Edge.SecretKey {
						t.Fatalf("restart did not retain recovered credential: %v", err)
					}
					if !tc.replaced && (cfg.Edge.AccessKey != tc.cached.AccessKey || cfg.Edge.SecretKey != tc.cached.SecretKey) {
						t.Fatal("healthy legacy node credential changed")
					}
				})
			}
			healthyAfter, err := edgeRepo.GetByID(ctx, healthy.EdgeID)
			if err != nil || healthyAfter.SecretKeyHash != healthyBefore.SecretKeyHash || *healthyAfter.DeviceID != *healthyBefore.DeviceID {
				t.Fatalf("recovery affected the healthy node: %v", err)
			}
			if _, err := edgebiz.NewAccessKeyAuthenticator(edgeRepo, log).Authenticate(ctx, old.AccessKey, old.SecretKey); !errors.Is(err, errs.ErrUnauthorized) {
				t.Fatalf("recovery reactivated deleted credentials: %v", err)
			}
			var count int64
			if err := db.Model(&k8smodel.Node{}).Where("cluster_id = ?", reg.Cluster.ID).Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("recovery duplicated nodes: count=%d error=%v", count, err)
			}
		})
	}
}
