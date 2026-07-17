package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestK8sCredentialKey(t *testing.T) {
	controller := k8sCredentialKey(&tunnel.KubernetesInfo{Role: "controller"})
	if controller != "controller" {
		t.Fatalf("controller key = %q, want controller", controller)
	}

	nodeA := k8sCredentialKey(&tunnel.KubernetesInfo{Role: "node", NodeName: "worker-a"})
	nodeA2 := k8sCredentialKey(&tunnel.KubernetesInfo{Role: "node", NodeName: "worker-a"})
	nodeB := k8sCredentialKey(&tunnel.KubernetesInfo{Role: "node", NodeName: "worker-b"})
	if nodeA != nodeA2 {
		t.Fatalf("node key should be stable, got %q and %q", nodeA, nodeA2)
	}
	if nodeA == nodeB {
		t.Fatalf("different node names produced same key %q", nodeA)
	}
	if strings.Contains(nodeA, "worker-a") {
		t.Fatalf("node key should not expose node name, got %q", nodeA)
	}
	if !strings.HasPrefix(nodeA, "node-") {
		t.Fatalf("node key = %q, want node-*", nodeA)
	}
}

func TestK8sCredentialFileRoundTrip(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "node-credential.json")
	info := &tunnel.KubernetesInfo{ClusterID: 7, Role: "node", NodeName: "worker-a"}
	cfg := &config.Config{Edge: config.EdgeConfig{CloudAddr: "manager:40012"}}
	out := k8sEnrollResponse{EdgeID: 9, AccessKey: "access", SecretKey: "secret"}

	if err := storeK8sCredentialFile(info, out, cfg, filePath); err != nil {
		t.Fatalf("storeK8sCredentialFile: %v", err)
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := stat.Mode().Perm(); got != 0600 {
		t.Fatalf("credential file mode = %o, want 600", got)
	}

	loadedCfg := &config.Config{}
	loaded, err := loadStoredK8sCredentialFile(loadedCfg, info, filePath, nil)
	if err != nil {
		t.Fatalf("loadStoredK8sCredentialFile: %v", err)
	}
	if !loaded || loadedCfg.Edge.AccessKey != "access" || loadedCfg.Edge.SecretKey != "secret" || loadedCfg.Edge.CloudAddr != "manager:40012" {
		t.Fatalf("loaded=%v cfg=%+v", loaded, loadedCfg.Edge)
	}
}

func TestK8sCredentialFileRejectsDifferentNode(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "node-credential.json")
	info := &tunnel.KubernetesInfo{ClusterID: 7, Role: "node", NodeName: "worker-a"}
	if err := storeK8sCredentialFile(info, k8sEnrollResponse{EdgeID: 9, AccessKey: "access", SecretKey: "secret"}, &config.Config{}, filePath); err != nil {
		t.Fatalf("storeK8sCredentialFile: %v", err)
	}
	loaded, err := loadStoredK8sCredentialFile(&config.Config{}, &tunnel.KubernetesInfo{ClusterID: 7, Role: "node", NodeName: "worker-b"}, filePath, nil)
	if err == nil || loaded {
		t.Fatalf("loaded=%v err=%v, want node mismatch", loaded, err)
	}
}

func TestK8sSecretClientDataKeyRoundTrip(t *testing.T) {
	var patched map[string]map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"controller": base64.StdEncoding.EncodeToString([]byte(`{"access_key":"ak","secret_key":"sk"}`)),
				},
			})
		case http.MethodPatch:
			if got := r.Header.Get("Content-Type"); got != "application/merge-patch+json" {
				t.Fatalf("Content-Type = %q, want merge patch", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Fatalf("decode patch: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client := &k8sSecretClient{
		baseURL:    server.URL,
		namespace:  "ongrid-system",
		secretName: "ongrid-edge-credentials",
		token:      "test-token",
		client:     server.Client(),
	}

	raw, found, err := client.getDataKey(context.Background(), "controller")
	if err != nil {
		t.Fatalf("getDataKey: %v", err)
	}
	if !found || string(raw) != `{"access_key":"ak","secret_key":"sk"}` {
		t.Fatalf("getDataKey found=%v raw=%q", found, string(raw))
	}

	if err := client.patchDataKey(context.Background(), "controller", []byte(`{"access_key":"new"}`)); err != nil {
		t.Fatalf("patchDataKey: %v", err)
	}
	encoded := patched["data"]["controller"]
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode patched data: %v", err)
	}
	if string(decoded) != `{"access_key":"new"}` {
		t.Fatalf("patched credential = %q", string(decoded))
	}
}
