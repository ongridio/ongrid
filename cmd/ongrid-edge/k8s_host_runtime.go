package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/ongridio/ongrid/internal/edgeagent/k8s/hostruntime"
)

const (
	installK8sHostRuntimeCommand = "install-k8s-host-runtime"
	enterK8sHostCommand          = "enter-k8s-host"
	containerPluginDir           = "/usr/local/lib/ongrid-edge"
	containerServiceAccountDir   = "/var/run/secrets/kubernetes.io/serviceaccount"
)

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
		if err := hostruntime.Install(ctx, hostruntime.InstallPaths{
			HostRoot:             args[1],
			EdgeSource:           executable,
			PluginSourceDir:      containerPluginDir,
			ServiceAccountSource: containerServiceAccountDir,
			UID:                  uid,
			GID:                  gid,
		}); err != nil {
			return true, err
		}
		return true, hostruntime.PrepareOBIFilesystem(ctx, args[1], uid, gid)
	case enterK8sHostCommand:
		if len(args) != 4 {
			return true, fmt.Errorf("usage: %s <host-root> <uid> <gid>", enterK8sHostCommand)
		}
		uid, gid, err := parseK8sHostIDs(args[2], args[3])
		if err != nil {
			return true, err
		}
		return true, hostruntime.Enter(ctx, args[1], uid, gid)
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
