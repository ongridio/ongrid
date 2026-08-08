//go:build windows

// Windows 平台的 detectMachineSpec 实现。GlobalMemoryStatusEx 返回总物理内存，
// 逻辑 CPU 核数复用 runtime.NumCPU()（Go 标准库已包装 GetSystemInfo）。
// golang.org/x/sys/windows 未暴露 GlobalMemoryStatusEx，用 syscall 直接调 kernel32。
// 不 mock 系统 API（dll 加载流程无法在不改 build 流程的情况下注入），依赖
// quota_windows_test.go 的 smoke test 确保运行不 panic。
package skill

import (
	"runtime"
	"syscall"
	"unsafe"
)

// memoryStatusEx 对应 Windows MEMORYSTATUSEX struct（64 bytes）。
// 字段对齐严格按 Win32 头文件顺序，dwLength 必须在调 GlobalMemoryStatusEx 前设置。
type memoryStatusEx struct {
	Length                 uint32
	MemoryLoad             uint32
	TotalPhys              uint64
	AvailPhys              uint64
	TotalPageFile          uint64
	AvailPageFile          uint64
	TotalVirtual           uint64
	AvailVirtual           uint64
	AvailExtendedVirtual   uint64
}

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx       = kernel32.NewProc("GlobalMemoryStatusEx")
)

// detectMachineSpec 返回 Windows 总物理 RAM 和 CPU 逻辑核数。
// 失败时返回 (0, runtime.NumCPU())。computeAdaptive 会走 floor 保护
// （RAM=0 时算出 0 MB，clamp 到 512MB 下限）。
func detectMachineSpec() (totalRAM uint64, cpuCores int) {
	cpuCores = runtime.NumCPU()
	var memInfo memoryStatusEx
	memInfo.Length = uint32(unsafe.Sizeof(memInfo))
	// GlobalMemoryStatusEx 返回非零表示成功（BOOL）。
	r1, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memInfo)))
	if r1 == 0 {
		// 调用失败；返回 0 RAM，让 computeAdaptive 走 floor。
		return 0, cpuCores
	}
	return memInfo.TotalPhys, cpuCores
}
