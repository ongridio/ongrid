//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	defaultLinuxLastCapability = 63
	procHostRoot               = "/proc/1/root"
	procHostMountNamespace     = "/proc/1/ns/mnt"
)

var k8sHostCapabilities = []int{
	unix.CAP_DAC_READ_SEARCH,
	unix.CAP_NET_ADMIN,
}

func enterK8sHost(ctx context.Context, hostRoot string, uid, gid int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rootFD, err := unix.Open(hostRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open host root: %w", err)
	}
	defer unix.Close(rootFD)

	mountNSFD := -1
	if requiresHostMountNamespace(hostRoot) {
		mountNSFD, err = unix.Open(procHostMountNamespace, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open host mount namespace: %w", err)
		}
		defer unix.Close(mountNSFD)
	}
	lastCapability := linuxLastCapability()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("isolate host launcher filesystem context: %w", err)
	}
	if mountNSFD >= 0 {
		if err := unix.Setns(mountNSFD, unix.CLONE_NEWNS); err != nil {
			return fmt.Errorf("enter host mount namespace: %w", err)
		}
	}
	if err := unix.Fchdir(rootFD); err != nil {
		return fmt.Errorf("select host root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("switch to host root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("change host working directory: %w", err)
	}
	if err := dropToHostEdgeUser(uid, gid, lastCapability); err != nil {
		return err
	}
	if err := unix.Exec(k8sHostEdgeBinary, []string{k8sHostEdgeBinary}, os.Environ()); err != nil {
		return fmt.Errorf("start host edge: %w", err)
	}
	return nil
}

// requiresHostMountNamespace preserves compatibility with older charts that
// reached the host through hostPID's /proc/1/root. New charts pass the explicit
// /host/root hostPath mount, which can be chrooted directly without procfs
// ptrace checks or setns.
func requiresHostMountNamespace(hostRoot string) bool {
	return filepath.Clean(hostRoot) == procHostRoot
}

func linuxLastCapability() int {
	raw, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return defaultLinuxLastCapability
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || value < 0 {
		return defaultLinuxLastCapability
	}
	return value
}

func dropToHostEdgeUser(uid, gid, lastCapability int) error {
	for capability := 0; capability <= lastCapability; capability++ {
		if isK8sHostCapability(capability) {
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("drop capability %d from bounding set: %w", capability, err)
		}
	}
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("preserve capabilities while dropping uid: %w", err)
	}
	if err := unix.Setgroups(nil); err != nil {
		return fmt.Errorf("clear supplementary groups: %w", err)
	}
	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return fmt.Errorf("set host edge gid: %w", err)
	}
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return fmt.Errorf("set host edge uid: %w", err)
	}

	capabilityData := [2]unix.CapUserData{}
	for _, capability := range k8sHostCapabilities {
		mask := uint32(1) << (uint(capability) % 32)
		index := uint(capability) / 32
		capabilityData[index].Effective |= mask
		capabilityData[index].Permitted |= mask
		capabilityData[index].Inheritable |= mask
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if err := unix.Capset(&header, &capabilityData[0]); err != nil {
		return fmt.Errorf("retain host edge capabilities: %w", err)
	}
	for _, capability := range k8sHostCapabilities {
		if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, uintptr(capability), 0, 0); err != nil {
			return fmt.Errorf("raise ambient capability %d: %w", capability, err)
		}
	}
	return nil
}

func isK8sHostCapability(capability int) bool {
	for _, allowed := range k8sHostCapabilities {
		if capability == allowed {
			return true
		}
	}
	return false
}
