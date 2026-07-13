package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestRunK8sHostCommandIgnoresUnrelatedCommand(t *testing.T) {
	handled, err := runK8sHostCommand(context.Background(), []string{"--version"})
	if err != nil {
		t.Fatalf("runK8sHostCommand() error = %v", err)
	}
	if handled {
		t.Fatal("runK8sHostCommand() handled unrelated command")
	}
}

func TestRunK8sHostCommandValidatesArguments(t *testing.T) {
	handled, err := runK8sHostCommand(context.Background(), []string{installK8sHostRuntimeCommand})
	if !handled {
		t.Fatal("runK8sHostCommand() did not handle install command")
	}
	if err == nil {
		t.Fatal("runK8sHostCommand() error = nil")
	}

	_, _, err = parseK8sHostIDs("-1", "0")
	if err == nil {
		t.Fatal("parseK8sHostIDs() accepted a negative uid")
	}
}

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
	err := installK8sHostRuntime(context.Background(), k8sHostInstallPaths{
		hostRoot:             hostRoot,
		edgeSource:           edgeSource,
		pluginSourceDir:      pluginDir,
		serviceAccountSource: serviceAccountDir,
		uid:                  os.Getuid(),
		gid:                  os.Getgid(),
	})
	if err != nil {
		t.Fatalf("installK8sHostRuntime() error = %v", err)
	}

	assertFileContents(t, hostPath(hostRoot, k8sHostEdgeBinary), "edge")
	assertFileContents(t, filepath.Join(hostPath(hostRoot, k8sHostPluginDir), "node_exporter"), "plugin")
	assertFileContents(t, filepath.Join(hostPath(hostRoot, k8sHostServiceAccountDir), "token"), "token")
	for _, path := range []string{
		hostPath(hostRoot, k8sHostStateDir),
		filepath.Join(hostPath(hostRoot, k8sHostStateDir), "credentials"),
		filepath.Join(hostPath(hostRoot, k8sHostStateDir), "plugins"),
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

func TestInstallK8sHostRuntimeHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := installK8sHostRuntime(ctx, k8sHostInstallPaths{hostRoot: t.TempDir()})
	if err == nil {
		t.Fatal("installK8sHostRuntime() error = nil")
	}
}

func TestK8sRolePredicates(t *testing.T) {
	if !isK8sNode(&tunnel.KubernetesInfo{Role: "node"}) {
		t.Fatal("isK8sNode() = false for node role")
	}
	if isK8sNode(&tunnel.KubernetesInfo{Role: "controller"}) {
		t.Fatal("isK8sNode() = true for controller role")
	}
	if !isK8sController(&tunnel.KubernetesInfo{Role: "controller"}) {
		t.Fatal("isK8sController() = false for controller role")
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
