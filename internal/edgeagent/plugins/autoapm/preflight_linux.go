//go:build linux

package autoapm

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Check the actual uprobe operation under the Edge's identity. A capability
// bitmap or a sysctl value alone cannot establish access under distro/LSM policy.
// The event remains disabled, targets this executable and is closed immediately.
func checkCaptureEnvironment(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := os.ReadFile("/sys/bus/event_source/devices/uprobe/type")
	if err != nil {
		return fmt.Errorf("OBI capture unavailable: kernel uprobe PMU is not accessible: %w", err)
	}
	pmu, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	if err != nil {
		return fmt.Errorf("OBI capture unavailable: invalid kernel uprobe PMU type: %w", err)
	}
	path, err := unix.BytePtrFromString("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("prepare OBI permission check: %w", err)
	}
	attr := unix.PerfEventAttr{
		Type: uint32(pmu), Size: uint32(unsafe.Sizeof(unix.PerfEventAttr{})),
		Bits: unix.PerfBitDisabled, Sample: 1,
		Ext1: uint64(uintptr(unsafe.Pointer(path))),
	}
	fd, err := unix.PerfEventOpen(&attr, -1, 0, -1, unix.PERF_FLAG_FD_CLOEXEC)
	runtime.KeepAlive(path)
	if err != nil {
		paranoid := "unknown"
		if value, readErr := os.ReadFile("/proc/sys/kernel/perf_event_paranoid"); readErr == nil {
			paranoid = strings.TrimSpace(string(value))
		}
		return fmt.Errorf("OBI capture unavailable: perf_event_open(uprobe) failed (kernel.perf_event_paranoid=%s); check CAP_PERFMON/CAP_SYS_ADMIN and container seccomp/AppArmor policy; Kubernetes requires the current chart with node.autoAPM.allowBPF=true: %w", paranoid, err)
	}
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close OBI permission check: %w", err)
	}
	return nil
}
