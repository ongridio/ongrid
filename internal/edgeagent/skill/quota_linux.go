//go:build !windows

// 非 Windows 平台（Linux 等）的 detectMachineSpec 对称实现。
// /proc/meminfo 读取 MemTotal 字段（KB），CPU 复用 runtime.NumCPU。
// Linux edge 是  的"推荐做但可选"目标（issue body）；此文件保证
// `go build ./...` 在 Linux runner 上不缺符号， 也能用同一套配额逻辑。
package skill

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// detectMachineSpec 在 Linux 上读 /proc/meminfo 的 MemTotal 行（单位 KB）
// 并返回总 RAM 字节数。CPU 用 runtime.NumCPU（Go 在 Linux 上调
// sched_getaffinity，返回可用核数，符合"配额应基于实际可调度核数"语义）。
// /proc/meminfo 不存在或解析失败时返回 (0, runtime.NumCPU())。
func detectMachineSpec() (totalRAM uint64, cpuCores int) {
	cpuCores = runtime.NumCPU()
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, cpuCores
	}
	for _, line := range strings.Split(string(data), "\n") {
		// 行形如 "MemTotal:       16384560 kB"
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, cpuCores
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, cpuCores
		}
		return kb * 1024, cpuCores
	}
	return 0, cpuCores
}
