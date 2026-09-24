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

func TestK8sHostCapabilitiesDoNotDependOnLegacySwitch(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		t.Setenv("ONGRID_AUTO_APM_ALLOW_BPF", value)
		for _, capability := range []int{unix.CAP_DAC_READ_SEARCH, unix.CAP_NET_ADMIN, unix.CAP_BPF, unix.CAP_PERFMON, unix.CAP_SYS_PTRACE, unix.CAP_CHECKPOINT_RESTORE, unix.CAP_NET_RAW, unix.CAP_SYS_ADMIN, unix.CAP_SYS_RESOURCE, unix.CAP_SYS_CHROOT, unix.CAP_SETUID, unix.CAP_SETGID} {
			if !isK8sHostCapability(capability) {
				t.Fatalf("legacy switch %q: capability %d is not retained", value, capability)
			}
		}
		if isK8sHostCapability(unix.CAP_SYS_BOOT) {
			t.Fatal("unrelated CAP_SYS_BOOT must be dropped")
		}
	}
}
