package k8s

import (
	"context"
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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func setNodeEnrollmentEnvironment(t *testing.T, managerURL string) string {
	t.Helper()
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/namespaces/kube-system":
			_, _ = io.WriteString(w, `{"metadata":{"uid":"cluster-uid"}}`)
		case "/api/v1/nodes/node-a":
			_, _ = io.WriteString(w, `{"metadata":{"name":"node-a","uid":"uid-new"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	accountDir := t.TempDir()
	for name, value := range map[string][]byte{
		"token": []byte("token"), "namespace": []byte("ongrid"),
		"ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}),
	} {
		if err := os.WriteFile(filepath.Join(accountDir, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	host, port, err := net.SplitHostPort(strings.TrimPrefix(api.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "credential.json")
	for key, value := range map[string]string{
		"ONGRID_EDGE_MODE": "k8s-node", "ONGRID_K8S_ROLE": "node", "ONGRID_K8S_MODE": "full-node",
		"ONGRID_K8S_CLUSTER_ID": "7", "ONGRID_K8S_NODE_NAME": "node-a", "ONGRID_K8S_NODE_UID": "",
		"ONGRID_K8S_BOOTSTRAP_TOKEN": "bootstrap", "ONGRID_MANAGER_PUBLIC_URL": managerURL,
		"ONGRID_K8S_CREDENTIAL_FILE": file, "ONGRID_K8S_CREDENTIAL_SECRET": "",
		"ONGRID_K8S_SERVICE_ACCOUNT_DIR": accountDir, "KUBERNETES_SERVICE_HOST": host, "KUBERNETES_SERVICE_PORT": port,
	} {
		t.Setenv(key, value)
	}
	return file
}

func TestEnsureEnrollmentReconcilesNodeCredential(t *testing.T) {
	for _, tc := range []struct {
		name, storedUID string
		reuse           bool
		status          int
		wantError       bool
		noBootstrap     bool
		persistFailure  bool
	}{
		{name: "issue505 legacy deleted identity"},
		{name: "recreated node", storedUID: "uid-old"},
		{name: "healthy legacy node", reuse: true},
		{name: "healthy current node", storedUID: "uid-new", reuse: true},
		{name: "same UID with deleted Edge", storedUID: "uid-new"},
		{name: "current node manager unavailable", storedUID: "uid-new", status: 503, reuse: true},
		{name: "legacy manager unavailable", status: 503, reuse: true},
		{name: "legacy old manager", status: 404, reuse: true},
		{name: "manager rate limited", status: 429, reuse: true},
		{name: "legacy invalid bootstrap", status: 401, reuse: true},
		{name: "legacy forbidden bootstrap", status: 403, reuse: true},
		{name: "legacy registered replacement", status: 409, reuse: true},
		{name: "replacement unavailable", storedUID: "uid-old", status: 503, wantError: true},
		{name: "replacement already bound", storedUID: "uid-old", status: 409, wantError: true},
		{name: "legacy without bootstrap", noBootstrap: true, reuse: true},
		{name: "replacement without bootstrap", storedUID: "uid-old", noBootstrap: true, wantError: true},
		{name: "atomic persistence failure", persistFailure: true, wantError: true},
		{name: "healthy legacy metadata write failure", reuse: true, persistFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.persistFailure && (os.Geteuid() == 0 || os.Geteuid() == -1) {
				t.Skip("requires POSIX permission enforcement for a non-root user")
			}
			var file string
			var calls atomic.Int32
			manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/internal/k8s/reconcile-node" || r.Header.Get("Authorization") != "Bearer bootstrap" {
					t.Errorf("unexpected recovery request: %s", r.URL.Path)
				}
				var req struct {
					NodeUID      string `json:"node_uid"`
					CachedEdgeID uint64 `json:"cached_edge_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				wantCachedID := uint64(260)
				if calls.Load() > 1 && !tc.reuse {
					wantCachedID = 261
				}
				if req.NodeUID != "uid-new" || req.CachedEdgeID != wantCachedID {
					t.Errorf("recovery request = %+v", req)
				}
				if calls.Load() > 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{"cluster_id": 7, "role": "node", "edge_id": wantCachedID, "reuse_credentials": true})
					return
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				if tc.persistFailure {
					if err := os.Chmod(filepath.Dir(file), 0500); err != nil {
						t.Error(err)
					}
				}
				if tc.reuse {
					_, _ = io.WriteString(w, `{"cluster_id":7,"role":"node","edge_id":260,"reuse_credentials":true}`)
				} else {
					_, _ = io.WriteString(w, `{"cluster_id":7,"role":"node","edge_id":261,"access_key":"new-ak","secret_key":"new-sk"}`)
				}
			}))
			defer manager.Close()
			file = setNodeEnrollmentEnvironment(t, manager.URL)
			if tc.noBootstrap {
				t.Setenv("ONGRID_K8S_BOOTSTRAP_TOKEN", "")
			}
			if tc.persistFailure {
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(file), 0700) })
			}
			old := k8sEnrollResponse{EdgeID: 260, AccessKey: "old-ak", SecretKey: "old-sk"}
			if err := storeK8sCredentialFile(&tunnel.KubernetesInfo{ClusterID: 7, Role: "node", NodeName: "node-a", NodeUID: tc.storedUID}, old, &config.Config{}, file); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{}
			info, pending, err := EnsureEnrollment(t.Context(), cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
			if (err != nil) != tc.wantError {
				t.Fatalf("EnsureEnrollment error = %v", err)
			}
			wantPending := !tc.wantError && (tc.status == 404 || tc.status == 429 || tc.status >= 500)
			if pending != wantPending {
				t.Fatalf("pending recovery = %v, want %v", pending, wantPending)
			}
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status != 0 || tc.noBootstrap || tc.persistFailure {
				if string(before) != string(after) {
					t.Fatal("failed reconciliation changed persisted credentials")
				}
				if !tc.wantError && (cfg.Edge.AccessKey != "old-ak" || (!tc.persistFailure && info.NodeUID != tc.storedUID)) {
					t.Fatal("fallback must retain legacy credentials and registration metadata")
				}
				if tc.noBootstrap && calls.Load() != 0 {
					t.Fatal("recovery attempted without bootstrap")
				}
				return
			}
			wantKey := "new-ak"
			if tc.reuse {
				wantKey = "old-ak"
			}
			var stored k8sStoredCredential
			if err := json.Unmarshal(after, &stored); err != nil {
				t.Fatal(err)
			}
			if info.NodeUID != "uid-new" || cfg.Edge.AccessKey != wantKey || stored.NodeUID != "uid-new" || stored.AccessKey != wantKey {
				t.Fatalf("identity was not reconciled: uid=%q storedUID=%q edge=%d", info.NodeUID, stored.NodeUID, stored.EdgeID)
			}
			wantCalls := int32(1)
			if tc.storedUID == "uid-new" && tc.reuse && string(before) != string(after) {
				t.Fatal("healthy current credential file was rewritten")
			}
			if calls.Load() != wantCalls {
				t.Fatalf("enrollment calls=%d, want %d", calls.Load(), wantCalls)
			}
			if _, _, err := EnsureEnrollment(t.Context(), &config.Config{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil || calls.Load() != wantCalls+1 {
				t.Fatalf("restart did not reuse recovered identity: calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func TestRetryNodeEnrollmentKeepsHealthyCredentials(t *testing.T) {
	var calls atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"cluster_id":7,"role":"node","edge_id":260,"reuse_credentials":true}`)
	}))
	defer manager.Close()
	file := setNodeEnrollmentEnvironment(t, manager.URL)
	if err := storeK8sCredentialFile(&tunnel.KubernetesInfo{ClusterID: 7, Role: "node", NodeName: "node-a"}, k8sEnrollResponse{EdgeID: 260, AccessKey: "old-ak", SecretKey: "old-sk"}, &config.Config{}, file); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	info, pending, err := EnsureEnrollment(t.Context(), cfg, "test", log)
	if err != nil || !pending {
		t.Fatalf("initial outage: pending=%v error=%v", pending, err)
	}
	if err := RetryNodeEnrollment(t.Context(), *cfg, "test", log); err != nil {
		t.Fatalf("healthy node should not restart: %v", err)
	}
	if calls.Load() != 3 || cfg.Edge.AccessKey != "old-ak" || cfg.Edge.SecretKey != "old-sk" || info.NodeUID != "" {
		t.Fatal("recovery changed the running Agent or stopped before reconciliation")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var stored k8sStoredCredential
	if err := json.Unmarshal(raw, &stored); err != nil || stored.NodeUID != "uid-new" || stored.EdgeID != 260 {
		t.Fatalf("healthy metadata was not reconciled: %v", err)
	}
}

func TestRetryNodeEnrollmentStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := RetryNodeEnrollment(ctx, config.Config{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("retry stopped before cancellation")
	}
}

func TestRetryNodeEnrollmentWaitsForCredentialPersistence(t *testing.T) {
	if os.Geteuid() == 0 || os.Geteuid() == -1 {
		t.Skip("requires POSIX permission enforcement for a non-root user")
	}
	var calls atomic.Int32
	var file string
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case 2:
			if err := os.Chmod(filepath.Dir(file), 0500); err != nil {
				t.Error(err)
			}
		default:
			if err := os.Chmod(filepath.Dir(file), 0700); err != nil {
				t.Error(err)
			}
		}
		_, _ = io.WriteString(w, `{"cluster_id":7,"role":"node","edge_id":261,"access_key":"new-ak","secret_key":"new-sk"}`)
	}))
	defer manager.Close()
	file = setNodeEnrollmentEnvironment(t, manager.URL)
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(file), 0700) })
	if err := storeK8sCredentialFile(&tunnel.KubernetesInfo{ClusterID: 7, Role: "node", NodeName: "node-a"}, k8sEnrollResponse{EdgeID: 260, AccessKey: "old-ak", SecretKey: "old-sk"}, &config.Config{}, file); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	_, pending, err := EnsureEnrollment(t.Context(), cfg, "test", log)
	if err != nil || !pending {
		t.Fatalf("initial outage: pending=%v error=%v", pending, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := RetryNodeEnrollment(ctx, *cfg, "test", log); !errors.Is(err, ErrNodeCredentialsRecovered) {
		t.Fatalf("expected restart only after persistence recovered: %v", err)
	}
	if calls.Load() != 3 || cfg.Edge.AccessKey != "old-ak" || cfg.Edge.SecretKey != "old-sk" {
		t.Fatal("recovery restarted too early or changed the running Agent config")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var stored k8sStoredCredential
	if err := json.Unmarshal(raw, &stored); err != nil || stored.EdgeID != 261 || stored.AccessKey != "new-ak" || stored.SecretKey != "new-sk" {
		t.Fatalf("replacement credential was not persisted: %v", err)
	}
}
