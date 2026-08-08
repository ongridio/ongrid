//go:build windows

package windows

import (
	"strings"
	"testing"
)

// psQuote 注入测试已删除：
// 原 TestProcesses_Filter_NamePsQuoteInjection_DoesNotEscape 测的是 psQuote 函数
// 本身的安全保证，已被 psquery_test.go:40 集中覆盖。per-skill 重复零价值。
// processes 的 buildProcessesFilter 中 name 走 psQuote 是 Go 类型系统决定
// （调用 `psQuote(p.Name)` 编译时固定），不依赖运行时校验。

// TestProcesses_Filter_TopNGoesThroughPSInt 验证 top_n 通过 PSInt 渲染为
// 裸整数而非引号字符串。Go 类型系统保证 int 参数无法注入 PowerShell 语法。
func TestProcesses_Filter_TopNGoesThroughPSInt(t *testing.T) {
	cases := []int{1, 10, 50, 100}
	for _, topN := range cases {
		p := processesParams{TopN: topN}
		q := processesSpec.buildQuery(p)
		rendered := q.render()

		// top_n 应以裸整数形式出现在 "Select-Object -First N" 中
		if !strings.Contains(rendered, "Select-Object -First ") {
			t.Fatalf("渲染缺少 Select-Object -First\n渲染: %s", rendered)
		}

		// 不应在 -First 后面出现引号（PSInt 不加引号）
		if strings.Contains(rendered, "Select-Object -First '") {
			t.Errorf("top_n 不应被引号包裹（应通过 PSInt 裸注入）\n渲染: %s", rendered)
		}
	}
}

// TestProcesses_Filter_MinMemoryMBGoesThroughPSInt 验证 min_memory_mb
// 通过 %d 格式化注入到 filter，是裸整数而非引号字符串。
func TestProcesses_Filter_MinMemoryMBGoesThroughPSInt(t *testing.T) {
	cases := []int{50, 100, 500, 1024}
	for _, minMem := range cases {
		p := processesParams{MinMemoryMB: minMem}
		processesSpec.Defaults(&p)
		filter := buildProcessesFilter(p)

		// min_memory_mb 应出现在 "$_.WorkingSet64 / 1MB -gt N" 中
		expected := "1MB -gt "
		if !strings.Contains(filter, expected) {
			t.Fatalf("filter 缺少 min_memory_mb 过滤条件\nfilter: %s", filter)
		}

		// 不应在 -gt 后面出现引号
		if strings.Contains(filter, "1MB -gt '") {
			t.Errorf("min_memory_mb 不应被引号包裹（应通过 %%d 裸注入）\nfilter: %s", filter)
		}
	}
}
