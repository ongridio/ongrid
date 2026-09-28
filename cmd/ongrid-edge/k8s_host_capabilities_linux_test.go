//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Run explicitly in an isolated Linux environment with the node launcher's
// capabilities. UID changes and bounding-set drops must never affect go test's
// main process, so both the launcher and simulated OBI run in child processes.
func TestK8sHostCapabilitiesSurviveJVMAttach(t *testing.T) {
	if os.Getenv("ONGRID_TEST_K8S_CAPABILITIES") != "1" {
		t.Skip("requires Linux node capabilities; set ONGRID_TEST_K8S_CAPABILITIES=1")
	}
	const phaseEnv = "ONGRID_TEST_K8S_CAPABILITIES_PHASE"
	const uidEnv = "ONGRID_TEST_K8S_CAPABILITIES_UID"
	const testArg = "-test.run=^TestK8sHostCapabilitiesSurviveJVMAttach$"
	switch os.Getenv(phaseEnv) {
	case "":
		for _, uid := range []int{65532, 0} {
			t.Run(strconv.Itoa(uid), func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], testArg)
				cmd.Env = append(os.Environ(), phaseEnv+"=launch", uidEnv+"="+strconv.Itoa(uid))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("launcher/OBI check: %v\n%s", err, out)
				}
			})
		}
		return
	case "launch":
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		uid, err := strconv.Atoi(os.Getenv(uidEnv))
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := dropToHostEdgeUser(uid, uid, linuxLastCapability()); err != nil {
			t.Fatal(err)
		}
		if err := os.Setenv(phaseEnv, "attach"); err != nil {
			t.Fatal(err)
		}
		// A real exec is essential: PR_SET_KEEPCAPS alone is cleared by exec.
		if err := unix.Exec(os.Args[0], []string{os.Args[0], testArg}, os.Environ()); err != nil {
			t.Fatal(err)
		}
	case "attach":
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		uid, gid := syscall.Geteuid(), syscall.Getegid()
		assertK8sAttachCapabilities(t, "after exec")
		for _, target := range []int{0, 1000, 65532, 0} {
			if err := syscall.Setegid(target); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Seteuid(target); err != nil {
				t.Fatal(err)
			}
			assertK8sAttachCapabilities(t, fmt.Sprintf("during attach to uid %d", target))
			// JVM attach uses Go's process-wide Seteuid. Other collection threads
			// must also retain their capabilities while attach is in progress.
			t.Run("concurrent collector", func(t *testing.T) {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				assertK8sAttachCapabilities(t, "concurrent collection thread")
			})
			if err := syscall.Seteuid(uid); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Setegid(gid); err != nil {
				t.Fatal(err)
			}
			assertK8sAttachCapabilities(t, "after restoring identity")
		}
	default:
		t.Fatal("unknown helper phase")
	}
}

func assertK8sAttachCapabilities(t *testing.T, phase string) {
	t.Helper()
	var data [2]unix.CapUserData
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if err := unix.Capget(&header, &data[0]); err != nil {
		t.Fatal(err)
	}
	var want [2]uint32
	for _, capability := range k8sHostCapabilities {
		want[capability/32] |= uint32(1) << uint(capability%32)
		if value, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(capability), 0, 0); err != nil || value != 1 {
			t.Fatalf("%s: ambient capability %d lost: %d, %v", phase, capability, value, err)
		}
	}
	for i := range data {
		if data[i].Effective != want[i] || data[i].Permitted != want[i] || data[i].Inheritable != want[i] {
			t.Fatalf("%s: capabilities[%d] = %+v, want only %#x", phase, i, data[i], want[i])
		}
	}
	for _, capability := range []int{unix.CAP_SETPCAP, unix.CAP_SYS_BOOT} {
		if value, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0); err != nil || value != 0 {
			t.Fatalf("%s: unrelated capability %d remains available: %d, %v", phase, capability, value, err)
		}
	}
	if value, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err != nil || value != 1 {
		t.Fatalf("%s: no_new_privs changed: %d, %v", phase, value, err)
	}
	fd, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Setns(fd, unix.CLONE_NEWNET); err != nil {
		t.Fatalf("%s: network namespace operation failed: %v", phase, err)
	}
}
