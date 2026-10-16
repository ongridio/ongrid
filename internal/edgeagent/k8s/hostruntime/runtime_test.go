package hostruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallK8sHostRuntime(t *testing.T) {
	sourceRoot := t.TempDir()
	edgeSource := writeTestFile(t, filepath.Join(sourceRoot, "ongrid-edge"), "edge")
	pluginDir := filepath.Join(sourceRoot, "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	writeTestFile(t, filepath.Join(pluginDir, "node_exporter"), "plugin")
	serviceAccountDir := filepath.Join(sourceRoot, "serviceaccount")
	if err := os.MkdirAll(serviceAccountDir, 0755); err != nil {
		t.Fatalf("mkdir service account: %v", err)
	}
	for _, name := range []string{"token", "ca.crt", "namespace"} {
		writeTestFile(t, filepath.Join(serviceAccountDir, name), name)
	}

	hostRoot := t.TempDir()
	legacyLink := hostPath(hostRoot, legacyK8sHostServiceAccountLink)
	if err := os.MkdirAll(filepath.Dir(legacyLink), 0755); err != nil {
		t.Fatalf("mkdir legacy service account parent: %v", err)
	}
	if err := os.Symlink(k8sHostServiceAccountDir, legacyLink); err != nil {
		t.Fatalf("create legacy service account link: %v", err)
	}
	err := Install(context.Background(), InstallPaths{
		HostRoot:             hostRoot,
		EdgeSource:           edgeSource,
		PluginSourceDir:      pluginDir,
		ServiceAccountSource: serviceAccountDir,
		UID:                  os.Getuid(),
		GID:                  os.Getgid(),
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	assertFileContents(t, hostPath(hostRoot, k8sHostEdgeBinary), "edge")
	assertFileContents(t, filepath.Join(hostPath(hostRoot, k8sHostPluginDir), "node_exporter"), "plugin")
	assertFileContents(t, filepath.Join(hostPath(hostRoot, k8sHostServiceAccountDir), "token"), "token")
	if _, err := os.Lstat(legacyLink); !os.IsNotExist(err) {
		t.Fatalf("legacy service account link still exists: %v", err)
	}
	for _, path := range []string{
		hostPath(hostRoot, k8sHostStateDir),
		filepath.Join(hostPath(hostRoot, k8sHostStateDir), "credentials"),
		filepath.Join(hostPath(hostRoot, k8sHostStateDir), "plugins"),
		hostPath(hostRoot, k8sHostSecretDir),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0750 {
			t.Fatalf("%s mode = %o, want 750", path, got)
		}
	}
}

func TestRemoveLegacyK8sHostServiceAccountLinkPreservesExistingPath(t *testing.T) {
	hostRoot := t.TempDir()
	linkPath := hostPath(hostRoot, legacyK8sHostServiceAccountLink)
	if err := os.MkdirAll(linkPath, 0755); err != nil {
		t.Fatalf("mkdir existing service account path: %v", err)
	}
	if err := removeLegacyK8sHostServiceAccountLink(hostRoot); err != nil {
		t.Fatalf("removeLegacyK8sHostServiceAccountLink() error = %v", err)
	}
	if info, err := os.Stat(linkPath); err != nil || !info.IsDir() {
		t.Fatalf("existing service account path was changed: info=%v err=%v", info, err)
	}
}

func TestInstallK8sHostRuntimeHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Install(ctx, InstallPaths{HostRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Install() error = nil")
	}
}

func writeTestFile(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
}
