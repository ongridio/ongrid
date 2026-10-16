package k8s

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestK8sRolePredicates(t *testing.T) {
	for _, tc := range []struct {
		info             *tunnel.KubernetesInfo
		node, controller bool
	}{
		{nil, false, false},
		{&tunnel.KubernetesInfo{Role: "node"}, true, false},
		{&tunnel.KubernetesInfo{Role: " controller "}, false, true},
		{&tunnel.KubernetesInfo{Role: "unknown"}, false, false},
	} {
		if IsNode(tc.info) != tc.node || IsController(tc.info) != tc.controller {
			t.Fatalf("unexpected role predicates for %+v", tc.info)
		}
	}
}

func TestEnsureEnrollmentReusesStoredIdentity(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, role := range []string{"node", "controller"} {
		t.Run(role, func(t *testing.T) {
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/nodes/node-a" {
					_, _ = io.WriteString(w, `{"metadata":{"uid":"node-uid"}}`)
					return
				}
				if r.URL.Path != "/api/v1/namespaces/kube-system" || r.Header.Get("Authorization") != "Bearer kube-token" {
					t.Errorf("unexpected identity request: %s", r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, `{"metadata":{"uid":"cluster-uid"}}`)
			}))
			defer api.Close()
			accountDir := t.TempDir()
			for name, value := range map[string][]byte{
				"token":     []byte("kube-token"),
				"namespace": []byte("ongrid"),
				"ca.crt":    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}),
			} {
				if err := os.WriteFile(filepath.Join(accountDir, name), value, 0600); err != nil {
					t.Fatal(err)
				}
			}
			host, port, err := net.SplitHostPort(strings.TrimPrefix(api.URL, "https://"))
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/internal/k8s/enroll" || r.Header.Get("Authorization") != "Bearer bootstrap" {
					t.Errorf("unexpected enrollment request: %s", r.URL.Path)
				}
				var request k8sEnrollRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				wantCapabilities := []string{"node_edge", "otel_gateway"}
				if role == "controller" {
					wantCapabilities[0] = "k8s_inventory"
				}
				if request.AgentVersion != "test-version" || request.ClusterUID != "cluster-uid" || request.Role != role || request.NodeUID != "node-uid" || !reflect.DeepEqual(request.Capabilities, wantCapabilities) {
					t.Errorf("enrollment request = %+v", request)
				}
				_ = json.NewEncoder(w).Encode(k8sEnrollResponse{EdgeID: 9, Role: role, Mode: "full-node", AccessKey: "access", SecretKey: "secret", CloudAddr: "new-manager:40012", ManagerPublicURL: "https://canonical.example/"})
			}))
			defer manager.Close()
			for key, value := range map[string]string{
				"ONGRID_EDGE_MODE": "k8s-" + role, "ONGRID_K8S_ROLE": "", "ONGRID_K8S_MODE": "full-node",
				"ONGRID_K8S_CLUSTER_ID": "7", "ONGRID_K8S_NODE_NAME": "node-a", "ONGRID_K8S_NODE_UID": "node-uid",
				"ONGRID_K8S_BOOTSTRAP_TOKEN": "bootstrap", "ONGRID_MANAGER_PUBLIC_URL": manager.URL,
				"ONGRID_K8S_CREDENTIAL_FILE":   filepath.Join(t.TempDir(), "credential.json"),
				"ONGRID_K8S_CREDENTIAL_SECRET": "", "ONGRID_K8S_TELEMETRY_SECRET": "",
				"ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED": "true", "ONGRID_K8S_SERVICE_ACCOUNT_DIR": accountDir,
				"KUBERNETES_SERVICE_HOST": host, "KUBERNETES_SERVICE_PORT": port,
			} {
				t.Setenv(key, value)
			}
			cfg := &config.Config{Edge: config.EdgeConfig{CloudAddr: "configured-manager:40012"}}
			info, _, err := EnsureEnrollment(t.Context(), cfg, "test-version", log)
			if err != nil {
				t.Fatal(err)
			}
			if info.ClusterID != 7 || info.ClusterUID != "cluster-uid" || info.Role != role || info.Mode != "full-node" || cfg.Edge.AccessKey != "access" || cfg.Edge.SecretKey != "secret" || cfg.Edge.CloudAddr != "configured-manager:40012" {
				t.Fatalf("unexpected enrollment: %+v %+v", info, cfg.Edge)
			}
			t.Setenv("ONGRID_K8S_BOOTSTRAP_TOKEN", "")
			restored := &config.Config{}
			if _, _, err := EnsureEnrollment(t.Context(), restored, "test-version", log); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || restored.Edge.AccessKey != "access" || restored.Edge.CloudAddr != "new-manager:40012" || restored.Edge.ManagerPublicURL != "https://canonical.example" {
				t.Fatalf("stored credentials were not reused: calls=%d config=%+v", calls.Load(), restored.Edge)
			}
			if role == "controller" {
				t.Setenv("ONGRID_K8S_TELEMETRY_SECRET", "telemetry")
				t.Setenv("ONGRID_K8S_TELEMETRY_REQUIRED", "false")
				if _, _, err := EnsureEnrollment(t.Context(), restored, "test-version", log); err != nil {
					t.Fatalf("optional telemetry blocked control plane: %v", err)
				}
				t.Setenv("ONGRID_K8S_TELEMETRY_REQUIRED", "true")
				if _, _, err := EnsureEnrollment(t.Context(), restored, "test-version", log); err == nil || !strings.Contains(err.Error(), "refresh required kubernetes telemetry config") {
					t.Fatalf("required telemetry error = %v", err)
				}
			}
		})
	}
}

func TestEnsureEnrollmentModeValidationAndCancellation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for key, value := range map[string]string{"ONGRID_EDGE_MODE": "", "ONGRID_K8S_CLUSTER_ID": "", "ONGRID_K8S_BOOTSTRAP_TOKEN": ""} {
		t.Setenv(key, value)
	}
	if info, _, err := EnsureEnrollment(t.Context(), &config.Config{}, "test", log); info != nil || err != nil {
		t.Fatalf("host mode: info=%+v err=%v", info, err)
	}
	t.Setenv("ONGRID_EDGE_MODE", "k8s-node")
	for _, id := range []string{"", "0", "bad"} {
		t.Setenv("ONGRID_K8S_CLUSTER_ID", id)
		if _, _, err := EnsureEnrollment(t.Context(), &config.Config{}, "test", log); err == nil {
			t.Fatalf("accepted cluster id %q", id)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RunTelemetryConfigSync(ctx, &config.Config{}, nil, log); err != nil {
		t.Fatal(err)
	}
}
