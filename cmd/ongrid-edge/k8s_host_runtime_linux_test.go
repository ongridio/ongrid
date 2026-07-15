//go:build linux

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

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
