//go:build windows

package windows

import (
	"strings"
	"testing"
)

// psQuote 注入测试已删除：
// 原 TestServices_Filter_NamePsQuoteInjection_DoesNotEscape 测的是 psQuote 函数
// 本身的安全保证，已被 psquery_test.go:40 集中覆盖。per-skill 重复零价值。
// services 的 buildServicesFilter 中 name 走 psQuote 是 Go 类型系统决定
// （调用 `psQuote(p.Name)` 编译时固定），不依赖运行时校验。

// TestServices_Filter_StatusInjection_MappedToEmpty 验证 status 参数的注入防护。
// 安全模型：status 经 servicesStatusToPS map 映射为 PowerShell 枚举名称字符串，
// 再经 psQuote 转义后注入。即使注入 payload 绕过 enum 校验，
// servicesStatusToPS[payload] 返回 ""（Go zero value），payload 字符串本身
// 永远不会出现在 filter 中。
// 此测试保留（per-skill 业务语义：每个 skill 的 toPSMap 独立声明，类型系统不保证）。
func TestServices_Filter_StatusInjection_MappedToEmpty(t *testing.T) {
	for _, payload := range adversarialPayloads {
		p := servicesParams{
			Name:      "",
			Status:    payload,
			StartType: "all",
		}
		filter := buildServicesFilter(p)

		// payload 字符串本身不应出现在 filter 中（被 servicesStatusToPS 转为 ""）
		if strings.Contains(filter, payload) {
			t.Errorf("status payload %q 不应出现在 filter 中（应被 servicesStatusToPS 转为空字符串）\nfilter: %s",
				payload, filter)
		}
	}
}

// TestServices_Filter_StartTypeInjection_MappedToEmpty 验证 start_type 参数的注入防护。
func TestServices_Filter_StartTypeInjection_MappedToEmpty(t *testing.T) {
	for _, payload := range adversarialPayloads {
		p := servicesParams{
			Name:      "",
			Status:    "all",
			StartType: payload,
		}
		filter := buildServicesFilter(p)

		// payload 字符串本身不应出现在 filter 中
		if strings.Contains(filter, payload) {
			t.Errorf("start_type payload %q 不应出现在 filter 中\nfilter: %s", payload, filter)
		}
	}
}
