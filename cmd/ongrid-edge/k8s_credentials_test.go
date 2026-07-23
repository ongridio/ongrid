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

func TestControllerCredentialFileDoesNotContainTelemetryCredential(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "controller-credential.json")
	info := &tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"}
	out := k8sEnrollResponse{
		EdgeID:    9,
		AccessKey: "controller-access",
		SecretKey: "controller-secret",
		Telemetry: &k8sTelemetryConfig{
			ClusterID: 7,
			AccessKey: "kt_access",
			SecretKey: "ks_secret",
		},
	}
	if err := storeK8sCredentialFile(info, out, &config.Config{}, filePath); err != nil {
		t.Fatalf("storeK8sCredentialFile: %v", err)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read controller credential: %v", err)
	}
	if strings.Contains(string(raw), "kt_access") || strings.Contains(string(raw), "ks_secret") || strings.Contains(string(raw), "telemetry") {
		t.Fatalf("controller credential contains telemetry data: %s", raw)
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

func TestTelemetrySecretDataContainsOnlyPublishedDataPlaneFields(t *testing.T) {
	in := k8sTelemetryConfig{
		ClusterID:              7,
		AccessKey:              "kt_access",
		SecretKey:              "ks_secret",
		TracesEndpoint:         "https://manager.example/v1/traces",
		LogsEndpoint:           "https://manager.example/loki/api/v1/push",
		RemoteWriteEndpoint:    "https://manager.example/prometheus/api/v1/write",
		RemoteWriteBasicUser:   "kt_access",
		RemoteWriteBasicPass:   "ks_secret",
		RemoteWriteTLSInsecure: true,
		RemoteWriteTLSCAPEM:    "test-ca",
	}
	got := telemetrySecretData(in)
	wants := map[string]string{
		"telemetry-cluster-id":                "7",
		"telemetry-access-key":                "kt_access",
		"telemetry-secret-key":                "ks_secret",
		"telemetry-traces-endpoint":           "https://manager.example/v1/traces",
		"telemetry-logs-endpoint":             "https://manager.example/loki/api/v1/push",
		"telemetry-remote-write-endpoint":     "https://manager.example/prometheus/api/v1/write",
		"telemetry-remote-write-basic-user":   "kt_access",
		"telemetry-remote-write-basic-pass":   "ks_secret",
		"telemetry-remote-write-tls-insecure": "true",
		"telemetry-remote-write-ca.pem":       "test-ca",
	}
	for key, want := range wants {
		if value := string(got[key]); value != want {
			t.Fatalf("%s = %q, want %q", key, value, want)
		}
	}
	if _, ok := got["controller"]; ok {
		t.Fatal("telemetry Secret must not contain the controller credential document")
	}
}

func TestApplyManagerTelemetryTLSIsOriginScoped(t *testing.T) {
	t.Setenv("ONGRID_K8S_ENROLL_TLS_INSECURE", "true")
	managerTarget := k8sTelemetryConfig{RemoteWriteEndpoint: "https://manager.example/prometheus/api/v1/write"}
	applyManagerTelemetryTLS(&managerTarget, "https://manager.example")
	if !managerTarget.RemoteWriteTLSInsecure {
		t.Fatal("manager-origin remote_write did not inherit the explicit manager TLS setting")
	}

	externalTarget := k8sTelemetryConfig{RemoteWriteEndpoint: "https://metrics.example/api/v1/write"}
	applyManagerTelemetryTLS(&externalTarget, "https://manager.example")
	if externalTarget.RemoteWriteTLSInsecure {
		t.Fatal("external remote_write must not inherit the manager TLS setting")
	}
}

func TestDataContainsValuesRequiresProjectedEmptyKeys(t *testing.T) {
	desired := map[string][]byte{
		"endpoint": []byte("https://metrics.example/write"),
		"bearer":   {},
	}
	if dataContainsValues(map[string][]byte{"endpoint": []byte("https://metrics.example/write")}, desired) {
		t.Fatal("missing empty key must still trigger a Secret patch for volume projection")
	}
	if !dataContainsValues(map[string][]byte{
		"endpoint": []byte("https://metrics.example/write"),
		"bearer":   {},
	}, desired) {
		t.Fatal("matching Secret data was not recognized")
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

func TestRefreshK8sTelemetryConfigPreservesCurrentCredentialProof(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/k8s/telemetry-config" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "controller-access" || pass != "controller-secret" {
			t.Fatalf("controller auth = %q/%q, ok=%v", user, pass, ok)
		}
		var proof map[string]string
		if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
			t.Fatalf("decode credential proof: %v", err)
		}
		if proof["access_key"] != "kt_current" || proof["secret_key"] != "ks_current" {
			t.Fatalf("credential proof = %#v", proof)
		}
		if err := json.NewEncoder(w).Encode(k8sTelemetryConfig{
			ClusterID:           7,
			AccessKey:           proof["access_key"],
			SecretKey:           proof["secret_key"],
			TracesEndpoint:      "https://manager.example/v1/traces",
			LogsEndpoint:        "https://manager.example/loki/api/v1/push",
			RemoteWriteEndpoint: "https://manager.example/prometheus/api/v1/write",
		}); err != nil {
			t.Fatalf("encode telemetry config: %v", err)
		}
	}))
	defer server.Close()

	out, err := refreshK8sTelemetryConfig(context.Background(), &config.Config{Edge: config.EdgeConfig{
		AccessKey: "controller-access",
		SecretKey: "controller-secret",
	}}, server.URL, "kt_current", "ks_current")
	if err != nil {
		t.Fatalf("refreshK8sTelemetryConfig: %v", err)
	}
	if out.AccessKey != "kt_current" || out.SecretKey != "ks_current" {
		t.Fatalf("refreshed credential = %#v", out)
	}
}
