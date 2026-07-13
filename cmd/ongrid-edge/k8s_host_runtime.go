package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	installK8sHostRuntimeCommand = "install-k8s-host-runtime"
	enterK8sHostCommand          = "enter-k8s-host"

	k8sHostRuntimeDir        = "/var/lib/ongrid-edge/k8s-runtime"
	k8sHostEdgeBinary        = k8sHostRuntimeDir + "/ongrid-edge"
	k8sHostPluginDir         = k8sHostRuntimeDir + "/plugins"
	k8sHostServiceAccountDir = k8sHostRuntimeDir + "/serviceaccount"
	k8sHostStateDir          = "/var/lib/ongrid-edge/k8s-state"

	containerPluginDir         = "/usr/local/lib/ongrid-edge"
	containerServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

type k8sHostInstallPaths struct {
	hostRoot             string
	edgeSource           string
	pluginSourceDir      string
	serviceAccountSource string
	uid                  int
	gid                  int
}

func runK8sHostCommand(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case installK8sHostRuntimeCommand:
		if len(args) != 4 {
			return true, fmt.Errorf("usage: %s <host-root> <uid> <gid>", installK8sHostRuntimeCommand)
		}
		uid, gid, err := parseK8sHostIDs(args[2], args[3])
		if err != nil {
			return true, err
		}
		executable, err := os.Executable()
		if err != nil {
			return true, fmt.Errorf("resolve edge executable: %w", err)
		}
		return true, installK8sHostRuntime(ctx, k8sHostInstallPaths{
			hostRoot:             args[1],
			edgeSource:           executable,
			pluginSourceDir:      containerPluginDir,
			serviceAccountSource: containerServiceAccountDir,
			uid:                  uid,
			gid:                  gid,
		})
	case enterK8sHostCommand:
		if len(args) != 4 {
			return true, fmt.Errorf("usage: %s <host-root> <uid> <gid>", enterK8sHostCommand)
		}
		uid, gid, err := parseK8sHostIDs(args[2], args[3])
		if err != nil {
			return true, err
		}
		return true, enterK8sHost(ctx, args[1], uid, gid)
	default:
		return false, nil
	}
}

func parseK8sHostIDs(uidRaw, gidRaw string) (int, int, error) {
	uid, err := parseNonNegativeID("uid", uidRaw)
	if err != nil {
		return 0, 0, err
	}
	gid, err := parseNonNegativeID("gid", gidRaw)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func parseNonNegativeID(name, value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("invalid %s %q", name, value)
	}
	return id, nil
}

func installK8sHostRuntime(ctx context.Context, paths k8sHostInstallPaths) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(paths.hostRoot) == "" {
		return fmt.Errorf("host root is required")
	}
	info, err := os.Stat(paths.hostRoot)
	if err != nil {
		return fmt.Errorf("stat host root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("host root %q is not a directory", paths.hostRoot)
	}

	runtimeDir := hostPath(paths.hostRoot, k8sHostRuntimeDir)
	pluginDir := hostPath(paths.hostRoot, k8sHostPluginDir)
	serviceAccountDir := hostPath(paths.hostRoot, k8sHostServiceAccountDir)
	stateDir := hostPath(paths.hostRoot, k8sHostStateDir)
	for _, dir := range []string{runtimeDir, pluginDir, serviceAccountDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create host runtime directory %q: %w", dir, err)
		}
	}
	for _, dir := range []string{
		stateDir,
		filepath.Join(stateDir, "credentials"),
		filepath.Join(stateDir, "plugins"),
		filepath.Join(stateDir, ".upgrade"),
	} {
		if err := ensureOwnedDirectory(dir, paths.uid, paths.gid, 0750); err != nil {
			return err
		}
	}

	if err := copyFileAtomic(ctx, paths.edgeSource, hostPath(paths.hostRoot, k8sHostEdgeBinary), 0755); err != nil {
		return fmt.Errorf("install host edge binary: %w", err)
	}
	if err := copyRegularFiles(ctx, paths.pluginSourceDir, pluginDir, 0755); err != nil {
		return fmt.Errorf("install host edge plugins: %w", err)
	}
	for _, name := range []string{"token", "ca.crt", "namespace"} {
		dst := filepath.Join(serviceAccountDir, name)
		if err := copyFileAtomic(ctx, filepath.Join(paths.serviceAccountSource, name), dst, 0400); err != nil {
			return fmt.Errorf("install service account %s: %w", name, err)
		}
		if err := os.Chown(dst, paths.uid, paths.gid); err != nil {
			return fmt.Errorf("set service account %s owner: %w", name, err)
		}
	}
	return nil
}

func hostPath(root, absolutePath string) string {
	clean := filepath.Clean(absolutePath)
	return filepath.Join(root, strings.TrimPrefix(clean, string(filepath.Separator)))
}

func ensureOwnedDirectory(path string, uid, gid int, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create state directory %q: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set state directory %q permissions: %w", path, err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("set state directory %q owner: %w", path, err)
	}
	return nil
}

func copyRegularFiles(ctx context.Context, sourceDir, destinationDir string, mode os.FileMode) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			continue
		}
		if err := copyFileAtomic(ctx, filepath.Join(sourceDir, entry.Name()), filepath.Join(destinationDir, entry.Name()), mode); err != nil {
			return err
		}
	}
	return nil
}

func copyFileAtomic(ctx context.Context, source, destination string, mode os.FileMode) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if err := src.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()

	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".ongrid-edge-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmpClosed := false
	defer func() {
		if !tmpClosed {
			if err := tmp.Close(); retErr == nil && err != nil {
				retErr = err
			}
		}
		if err := os.Remove(tmpName); retErr == nil && err != nil && !os.IsNotExist(err) {
			retErr = err
		}
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	tmpClosed = true
	if err := os.Rename(tmpName, destination); err != nil {
		return err
	}
	return nil
}
