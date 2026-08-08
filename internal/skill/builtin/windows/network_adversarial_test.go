//go:build windows

package windows

import (
	"strings"
	"testing"
)

// psQuote 注入测试已删除：
// 原 TestNetwork_Filter_RemoteAddressPsQuoteInjection_DoesNotEscape 测的是 psQuote 函数
// 本身的安全保证，已被 psquery_test.go:40 集中覆盖。per-skill 重复零价值。
// network 的 buildNetworkFilter 中 remote_address 走 psQuote 是 Go 类型系统决定
// （调用 `psQuote(p.RemoteAddress)` 编译时固定），不依赖运行时校验。

// TestNetwork_Filter_StateInjection_MappedToEmpty 验证 state 参数的注入防护。
// 安全模型：state 经 networkStateToPS map 映射为 PowerShell 枚举名称字符串，
// 再经 psQuote 转义后注入。即使注入 payload 绕过 enum 校验，
// networkStateToPS[payload] 返回 ""（Go zero value），payload 字符串本身
// 永远不会出现在 filter 中。
// 此测试保留（per-skill 业务语义：每个 skill 的 toPSMap 独立声明，类型系统不保证）。
func TestNetwork_Filter_StateInjection_MappedToEmpty(t *testing.T) {
	for _, payload := range adversarialPayloads {
		p := networkParams{
			State: payload,
			TopN:  50,
		}
		filter := buildNetworkFilter(p)

		// payload 字符串本身不应出现在 filter 中（被 networkStateToPS 转为 ""）
		if strings.Contains(filter, payload) {
			t.Errorf("state payload %q 不应出现在 filter 中（应被 networkStateToPS 转为空字符串）\nfilter: %s",
				payload, filter)
		}
	}
}

// TestNetwork_Filter_PortsGoThroughPSInt 验证 local_port / remote_port
// 通过 PSInt 渲染为裸整数而非引号字符串。Go 类型系统保证 int 参数无法注入 PowerShell 语法。
func TestNetwork_Filter_PortsGoThroughPSInt(t *testing.T) {
	portCases := []struct {
		name string
		port int
	}{
		{"local_port_80", 80},
		{"local_port_443", 443},
		{"local_port_8080", 8080},
		{"local_port_65535", 65535},
	}
	for _, tc := range portCases {
		t.Run(tc.name, func(t *testing.T) {
			p := networkParams{LocalPort: tc.port, TopN: 50}
			networkSpec.Defaults(&p)
			filter := buildNetworkFilter(p)

			// port 应以裸整数形式出现在 "$_.LocalPort -eq N" 中
			if !strings.Contains(filter, "$_.LocalPort -eq ") {
				t.Fatalf("filter 缺少 local_port 过滤条件\nfilter: %s", filter)
			}

			// 不应在 -eq 后面出现引号（PSInt 不加引号）
			if strings.Contains(filter, "$_.LocalPort -eq '") {
				t.Errorf("local_port 不应被引号包裹（应通过 PSInt 裸注入）\nfilter: %s", filter)
			}
		})
	}

	remotePortCases := []struct {
		name string
		port int
	}{
		{"remote_port_22", 22},
		{"remote_port_443", 443},
		{"remote_port_3306", 3306},
	}
	for _, tc := range remotePortCases {
		t.Run(tc.name, func(t *testing.T) {
			p := networkParams{RemotePort: tc.port, TopN: 50}
			networkSpec.Defaults(&p)
			filter := buildNetworkFilter(p)

			if !strings.Contains(filter, "$_.RemotePort -eq ") {
				t.Fatalf("filter 缺少 remote_port 过滤条件\nfilter: %s", filter)
			}

			if strings.Contains(filter, "$_.RemotePort -eq '") {
				t.Errorf("remote_port 不应被引号包裹（应通过 PSInt 裸注入）\nfilter: %s", filter)
			}
		})
	}
}

