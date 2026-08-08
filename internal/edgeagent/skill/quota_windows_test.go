//go:build windows

// Windows 平台的 Quota detectMachineSpec 测试。Linux 等平台跳过
// （quota_test.go 的 computeAdaptive 是纯函数，已在跨平台覆盖）。
package skill

import (
	"testing"
)

// TestDetectMachineSpec_Windows_ReturnsPositive 验证 Windows 上
// detectMachineSpec 返回合理值（RAM > 0、CPU 核数 >= 1）。CI 跑在
// windows-latest runner，至少 2 核 / 7GB RAM。
// 不 mock 系统 API（GlobalMemoryStatusEx / GetSystemInfo）— Go syscall
// 包装层无法在不修改 dll 加载流程的情况下注入 mock。通过 smoke test
// 确保函数运行不 panic 且返回 sane defaults；具体 RAM/核数断言留给
// qa/ 在真实生产机器上验证。
func TestDetectMachineSpec_Windows_ReturnsPositive(t *testing.T) {
	ram, cores := detectMachineSpec()
	if ram == 0 {
		t.Error("detectMachineSpec returned RAM = 0; GlobalMemoryStatusEx likely failed")
	}
	if cores < 1 {
		t.Errorf("detectMachineSpec returned cores = %d; want >= 1", cores)
	}
	// 下限保护（CI windows-latest 最小 7GB；本地 dev 机 >= 4GB）。
	const minExpectedRAM uint64 = 2 * 1024 * 1024 * 1024 // 2GB
	if ram < minExpectedRAM {
		t.Errorf("detectMachineSpec RAM = %d bytes (%.2f GB), want >= %d bytes (2GB)",
			ram, float64(ram)/(1024*1024*1024), minExpectedRAM)
	}
}
