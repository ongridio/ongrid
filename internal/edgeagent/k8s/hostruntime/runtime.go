package hostruntime

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	k8sHostRuntimeDir               = "/var/lib/ongrid-edge/k8s-runtime"
	k8sHostEdgeBinary               = k8sHostRuntimeDir + "/ongrid-edge"
	k8sHostPluginDir                = k8sHostRuntimeDir + "/plugins"
	k8sHostServiceAccountDir        = k8sHostRuntimeDir + "/serviceaccount"
	legacyK8sHostServiceAccountLink = "/run/secrets/kubernetes.io/serviceaccount"
	k8sHostStateDir                 = "/var/lib/ongrid-edge/k8s-state"
	k8sHostSecretDir                = k8sHostStateDir + "/secrets"
)

// InstallPaths identifies the runtime sources, host root and Edge user.
type InstallPaths struct {
	HostRoot             string
	EdgeSource           string
	PluginSourceDir      string
	ServiceAccountSource string
	UID                  int
	GID                  int
}

// Install copies the Edge runtime and service account into the host root.
func Install(ctx context.Context, paths InstallPaths) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(paths.HostRoot) == "" {
		return fmt.Errorf("host root is required")
	}
	info, err := os.Stat(paths.HostRoot)
	if err != nil {
		return fmt.Errorf("stat host root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("host root %q is not a directory", paths.HostRoot)
	}
	runtimeDir := hostPath(paths.HostRoot, k8sHostRuntimeDir)
	pluginDir := hostPath(paths.HostRoot, k8sHostPluginDir)
	serviceAccountDir := hostPath(paths.HostRoot, k8sHostServiceAccountDir)
	stateDir := hostPath(paths.HostRoot, k8sHostStateDir)
	for _, dir := range []string{runtimeDir, pluginDir, serviceAccountDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create host runtime directory %q: %w", dir, err)
		}
	}
	for _, dir := range []string{
		stateDir,
		filepath.Join(stateDir, "credentials"),
		filepath.Join(stateDir, "plugins"),
		hostPath(paths.HostRoot, k8sHostSecretDir),
		filepath.Join(stateDir, ".upgrade"),
	} {
		if err := ensureOwnedDirectory(dir, paths.UID, paths.GID, 0750); err != nil {
			return err
		}
	}

	if err := copyFileAtomic(ctx, paths.EdgeSource, hostPath(paths.HostRoot, k8sHostEdgeBinary), 0755); err != nil {
		return fmt.Errorf("install host edge binary: %w", err)
	}
	if err := copyRegularFiles(ctx, paths.PluginSourceDir, pluginDir, 0755); err != nil {
		return fmt.Errorf("install host edge plugins: %w", err)
	}
	for _, name := range []string{"token", "ca.crt", "namespace"} {
		dst := filepath.Join(serviceAccountDir, name)
		if err := copyFileAtomic(ctx, filepath.Join(paths.ServiceAccountSource, name), dst, 0400); err != nil {
			return fmt.Errorf("install service account %s: %w", name, err)
		}
		if err := os.Chown(dst, paths.UID, paths.GID); err != nil {
			return fmt.Errorf("set service account %s owner: %w", name, err)
		}
	}
	if err := removeLegacyK8sHostServiceAccountLink(paths.HostRoot); err != nil {
		return err
	}
	return nil
}

func removeLegacyK8sHostServiceAccountLink(hostRoot string) error {
	linkPath := hostPath(hostRoot, legacyK8sHostServiceAccountLink)
	info, err := os.Lstat(linkPath)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			return fmt.Errorf("read legacy service account link: %w", err)
		}
		if target != k8sHostServiceAccountDir {
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove legacy service account link: %w", err)
		}
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("inspect legacy service account link: %w", err)
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
