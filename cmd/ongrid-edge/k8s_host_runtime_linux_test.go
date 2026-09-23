//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOBIFilesystemRejectsUnMountedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys/fs/bpf"), 0755); err != nil {
		t.Fatal(err)
	}
	err := prepareK8sOBIFilesystem(context.Background(), root, os.Getuid(), os.Getgid())
	if err == nil || !strings.Contains(err.Error(), "bpf filesystem mounted") {
		t.Fatalf("ordinary directory accepted as bpffs: %v", err)
	}
}

func TestRequiresHostMountNamespace(t *testing.T) {
	tests := []struct {
		name     string
		hostRoot string
		want     bool
	}{
		{name: "legacy proc root", hostRoot: "/proc/1/root", want: true},
		{name: "legacy proc root trailing slash", hostRoot: "/proc/1/root/", want: true},
		{name: "explicit host mount", hostRoot: "/host/root", want: false},
		{name: "empty", hostRoot: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresHostMountNamespace(tt.hostRoot); got != tt.want {
				t.Fatalf("requiresHostMountNamespace(%q) = %t, want %t", tt.hostRoot, got, tt.want)
			}
		})
	}
}

func TestK8sHostCapabilities(t *testing.T) {
	t.Setenv("ONGRID_AUTO_APM_ALLOW_BPF", "false")
	for _, capability := range []int{unix.CAP_DAC_READ_SEARCH, unix.CAP_NET_ADMIN} {
		if !isK8sHostCapability(capability) {
			t.Fatalf("capability %d is not retained", capability)
		}
	}
	if isK8sHostCapability(unix.CAP_SYS_ADMIN) {
		t.Fatal("CAP_SYS_ADMIN must be dropped before starting the host edge")
	}
}

func TestAutoAPMCapabilitiesRequireOptIn(t *testing.T) {
	t.Setenv("ONGRID_AUTO_APM_ALLOW_BPF", "false")
	for _, cap := range []int{unix.CAP_BPF, unix.CAP_PERFMON, unix.CAP_SYS_PTRACE, unix.CAP_CHECKPOINT_RESTORE, unix.CAP_NET_RAW, unix.CAP_SYS_ADMIN, unix.CAP_SYS_RESOURCE, unix.CAP_SYS_CHROOT, unix.CAP_SETUID, unix.CAP_SETGID} {
		if isK8sHostCapability(cap) {
			t.Fatalf("default granted capture capability %d", cap)
		}
	}
	t.Setenv("ONGRID_AUTO_APM_ALLOW_BPF", "true")
	for _, cap := range []int{unix.CAP_BPF, unix.CAP_PERFMON, unix.CAP_SYS_PTRACE, unix.CAP_CHECKPOINT_RESTORE, unix.CAP_NET_RAW, unix.CAP_SYS_ADMIN, unix.CAP_SYS_RESOURCE, unix.CAP_SYS_CHROOT, unix.CAP_SETUID, unix.CAP_SETGID} {
		if !isK8sHostCapability(cap) {
			t.Fatalf("missing capability %d", cap)
		}
	}
}
