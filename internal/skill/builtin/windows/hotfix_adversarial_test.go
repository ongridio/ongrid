//go:build windows

package windows

import (
	"strconv"
	"strings"
	"testing"
)

// TestHotfix_Filter_TopNGoesThroughPSInt 验证 top_n 通过 PSInt 渲染为
// 裸整数而非引号字符串。Go 类型系统保证 int 参数无法注入 PowerShell 语法。
func TestHotfix_Filter_TopNGoesThroughPSInt(t *testing.T) {
	cases := []int{1, 10, 20, 50, 200}
	for _, topN := range cases {
		p := hotfixParams{TopN: topN}
		q := hotfixSpec.buildQuery(p)
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

// TestHotfix_Filter_SinceDaysGoesThroughPSInt 验证 since_days 通过 %d 格式化
// 注入到 filter，是裸整数而非引号字符串。
func TestHotfix_Filter_SinceDaysGoesThroughPSInt(t *testing.T) {
	cases := []int{1, 7, 30, 90, 365}
	for _, sinceDays := range cases {
		p := hotfixParams{SinceDays: sinceDays}
		filter := buildHotfixFilter(p)

		// since_days 应出现在 "AddDays(-N)" 中
		if !strings.Contains(filter, "AddDays(-") {
			t.Fatalf("filter 缺少 since_days 过滤条件\nfilter: %s", filter)
		}

		// 应包含正确的天数数字
		expectedNum := "-" + strconv.Itoa(sinceDays)
		if !strings.Contains(filter, expectedNum) {
			t.Errorf("since_days=%d: filter 应包含 %q\nfilter: %s", sinceDays, expectedNum, filter)
		}

		// 不应在 AddDays 后面出现引号包裹天数
		if strings.Contains(filter, "AddDays('") {
			t.Errorf("since_days 不应被引号包裹（应通过 %%d 裸注入）\nfilter: %s", filter)
		}
	}
}
