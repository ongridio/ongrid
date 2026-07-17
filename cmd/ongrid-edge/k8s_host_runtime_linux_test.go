//go:build linux

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

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
	for _, capability := range []int{unix.CAP_DAC_READ_SEARCH, unix.CAP_NET_ADMIN} {
		if !isK8sHostCapability(capability) {
			t.Fatalf("capability %d is not retained", capability)
		}
	}
	if isK8sHostCapability(unix.CAP_SYS_ADMIN) {
		t.Fatal("CAP_SYS_ADMIN must be dropped before starting the host edge")
	}
}
