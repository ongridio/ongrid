package k8s

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewInClusterAPIClientUsesConfiguredServiceAccountDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"token":     "node-token\n",
		"namespace": "ongrid-system\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("ONGRID_K8S_SERVICE_ACCOUNT_DIR", dir)
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "6443")

	client, err := newInClusterAPIClient()
	if err != nil {
		t.Fatalf("newInClusterAPIClient() error = %v", err)
	}
	if client.token != "node-token" {
		t.Fatalf("token = %q, want node-token", client.token)
	}
	if client.namespace != "ongrid-system" {
		t.Fatalf("namespace = %q, want ongrid-system", client.namespace)
	}
	if client.baseURL != "https://10.0.0.1:6443" {
		t.Fatalf("baseURL = %q, want https://10.0.0.1:6443", client.baseURL)
	}
}
