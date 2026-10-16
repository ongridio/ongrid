//go:build linux

package hostruntime

import (
	"errors"
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
	const lastCapabilityEnv = "ONGRID_TEST_K8S_LAST_CAPABILITY"
	const deniedCapabilitiesEnv = "ONGRID_TEST_K8S_DENIED_CAPABILITIES"
	const deniedSetEnv = "ONGRID_TEST_K8S_DENIED_SET"
	const permissionErrorEnv = "ONGRID_TEST_K8S_PERMISSION_ERROR"
	const testArg = "-test.run=^TestK8sHostCapabilitiesSurviveJVMAttach$"
	switch os.Getenv(phaseEnv) {
	case "":
		const optional = uint64(1)<<unix.CAP_BPF | uint64(1)<<unix.CAP_PERFMON | uint64(1)<<unix.CAP_CHECKPOINT_RESTORE
		for _, tc := range []struct {
			name                string
			lastCapability      int
			deniedCapabilities  uint64
			deniedSet           string
			wantPermissionError bool
		}{
			{name: "pre_5_8", lastCapability: 37},
			{name: "linux_5_8", lastCapability: 39},
			{name: "current", lastCapability: linuxLastCapability()},
			{name: "chart_defaults", lastCapability: linuxLastCapability(), deniedCapabilities: optional},
			{name: "missing_bpf", lastCapability: linuxLastCapability(), deniedCapabilities: 1 << unix.CAP_BPF},
			{name: "missing_perfmon", lastCapability: linuxLastCapability(), deniedCapabilities: 1 << unix.CAP_PERFMON},
			{name: "missing_checkpoint_restore", lastCapability: linuxLastCapability(), deniedCapabilities: 1 << unix.CAP_CHECKPOINT_RESTORE},
			{name: "missing_optional_bounding", lastCapability: linuxLastCapability(), deniedCapabilities: optional, deniedSet: "bounding"},
			{name: "missing_optional_permitted", lastCapability: linuxLastCapability(), deniedCapabilities: optional, deniedSet: "permitted"},
			{name: "missing_net_admin", lastCapability: linuxLastCapability(), deniedCapabilities: 1 << unix.CAP_NET_ADMIN, wantPermissionError: true},
			{name: "missing_sys_admin", lastCapability: linuxLastCapability(), deniedCapabilities: 1 << unix.CAP_SYS_ADMIN, wantPermissionError: true},
			{name: "missing_fallback", lastCapability: linuxLastCapability(), deniedCapabilities: optional | 1<<unix.CAP_SYS_ADMIN, wantPermissionError: true},
		} {
			if tc.lastCapability > linuxLastCapability() {
				continue
			}
			for _, uid := range []int{65532, 0} {
				t.Run(fmt.Sprintf("%s/uid_%d", tc.name, uid), func(t *testing.T) {
					cmd := exec.CommandContext(t.Context(), os.Args[0], testArg)
					cmd.Env = append(os.Environ(), phaseEnv+"=launch", uidEnv+"="+strconv.Itoa(uid),
						lastCapabilityEnv+"="+strconv.Itoa(tc.lastCapability),
						deniedCapabilitiesEnv+"="+strconv.FormatUint(tc.deniedCapabilities, 10),
						deniedSetEnv+"="+tc.deniedSet, permissionErrorEnv+"="+strconv.FormatBool(tc.wantPermissionError))
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("launcher/OBI check: %v\n%s", err, out)
					}
				})
			}
		}
		return
	case "launch":
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		lastCapability, err := strconv.Atoi(os.Getenv(lastCapabilityEnv))
		if err != nil {
			t.Fatal(err)
		}
		deniedCapabilities, err := strconv.ParseUint(os.Getenv(deniedCapabilitiesEnv), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		var granted [2]unix.CapUserData
		header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		if err := unix.Capget(&header, &granted[0]); err != nil {
			t.Fatal(err)
		}
		// Simulate both old kernels and runtimes that omit capabilities on new kernels.
		for capability := 0; capability <= linuxLastCapability(); capability++ {
			unsupported := capability > lastCapability
			if unsupported || deniedCapabilities&(uint64(1)<<uint(capability)) != 0 {
				if unsupported || os.Getenv(deniedSetEnv) != "permitted" {
					if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
						t.Fatal(err)
					}
				}
				if unsupported || os.Getenv(deniedSetEnv) != "bounding" {
					mask := uint32(1) << uint(capability%32)
					granted[capability/32].Effective &^= mask
					granted[capability/32].Permitted &^= mask
					granted[capability/32].Inheritable &^= mask
				}
			}
		}
		if err := unix.Capset(&header, &granted[0]); err != nil {
			t.Fatal(err)
		}
		uid, err := strconv.Atoi(os.Getenv(uidEnv))
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		err = dropToHostEdgeUser(uid, uid, lastCapability)
		if os.Getenv(permissionErrorEnv) == "true" {
			if !errors.Is(err, unix.EPERM) {
				t.Fatalf("missing supported capability: got %v, want EPERM", err)
			}
			return
		}
		if err != nil {
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
		lastCapability, err := strconv.Atoi(os.Getenv(lastCapabilityEnv))
		if err != nil {
			t.Fatal(err)
		}
		deniedCapabilities, err := strconv.ParseUint(os.Getenv(deniedCapabilitiesEnv), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		uid, gid := syscall.Geteuid(), syscall.Getegid()
		assertK8sAttachCapabilities(t, "after exec", lastCapability, deniedCapabilities)
		for _, target := range []int{0, 1000, 65532, 0} {
			if err := syscall.Setegid(target); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Seteuid(target); err != nil {
				t.Fatal(err)
			}
			assertK8sAttachCapabilities(t, fmt.Sprintf("during attach to uid %d", target), lastCapability, deniedCapabilities)
			// JVM attach uses Go's process-wide Seteuid. Other collection threads
			// must also retain their capabilities while attach is in progress.
			t.Run("concurrent collector", func(t *testing.T) {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				assertK8sAttachCapabilities(t, "concurrent collection thread", lastCapability, deniedCapabilities)
			})
			if err := syscall.Seteuid(uid); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Setegid(gid); err != nil {
				t.Fatal(err)
			}
			assertK8sAttachCapabilities(t, "after restoring identity", lastCapability, deniedCapabilities)
		}
	default:
		t.Fatal("unknown helper phase")
	}
}

func assertK8sAttachCapabilities(t *testing.T, phase string, lastCapability int, deniedCapabilities uint64) {
	t.Helper()
	var data [2]unix.CapUserData
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if err := unix.Capget(&header, &data[0]); err != nil {
		t.Fatal(err)
	}
	var want [2]uint32
	for _, capability := range k8sHostCapabilities {
		if capability > lastCapability || deniedCapabilities&(uint64(1)<<uint(capability)) != 0 {
			continue
		}
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

// Execute the existing real OBI suite after the launcher's non-root transition.
// Run in an isolated Linux container with the rendered Chart's capabilities.
func TestK8sHostAutoAPMIntegration(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_K8S_AUTOAPM_BINARY")
	if binary == "" {
		t.Skip("requires compiled Auto APM integration tests and pinned OBI/Collector binaries")
	}
	if os.Getenv("ONGRID_TEST_AUTOAPM_BIN_DIR") == "" {
		t.Fatal("ONGRID_TEST_AUTOAPM_BIN_DIR is required for real OBI collection")
	}
	const phaseEnv = "ONGRID_TEST_K8S_AUTOAPM_LAUNCH"
	if os.Getenv(phaseEnv) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestK8sHostAutoAPMIntegration$")
		cmd.Env = append(os.Environ(), phaseEnv+"=1")
		out, err := cmd.CombinedOutput()
		t.Logf("%s", out)
		if err != nil {
			t.Fatalf("non-root host Auto APM: %v", err)
		}
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := dropToHostEdgeUser(65532, 65532, linuxLastCapability()); err != nil {
		t.Fatal(err)
	}
	if err := unix.Exec(binary, []string{binary, "-test.v", "-test.timeout=6m", "-test.run=^TestOBISelectiveIntegration$"}, os.Environ()); err != nil {
		t.Fatal(err)
	}
}
