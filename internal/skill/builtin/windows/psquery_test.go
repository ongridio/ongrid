//go:build windows

package windows

import (
	"strconv"
	"strings"
	"testing"
)

// adversarialPayloads 是包级单一权威注入 payload 列表。
// 从 powershell_test.go TestPsQuote_AdversarialInjection 迁移，
// 供 PSQuery 级注入测试 + per-skill PSRaw 构造安全测试引用。
// 所有 payload 经 psQuote 后必须满足不变量：
//  1. 输出始终以 ' 开头、' 结尾
//  2. 输入中每个 ' 在输出中成对出现（转义为 ''）
//  3. 总 ' 数 = 原始 ' 数 * 2 + 2（首尾包裹）
var adversarialPayloads = []string{
	`'; Remove-Item C:\ -Force; '`,           // 典型注入
	`a$(whoami)`,                              // 子表达式插值
	`"; net user /add h; "`,                   // 双引号注入
	`'; Invoke-Expression 'bar'; '`,           // IEX 注入
	`O'Brien`,                                 // 合法但有单引号
	`正常中文`,                                  // UTF-8 多字节
	"`command`",                               // 反引号
	"line1\nline2",                            // 换行符
	"\u201cquote\u201d",                       // Unicode 引号
	"$var",                                    // 变量插值
	"';Start-Process calc;'#",                 // 注释符注入
	strings.Repeat("';", 100),                 // 超长重复注入
	strings.Repeat("A", 10000),                // 超长字符串
	"\x00\x01\x02",                            // 控制字符
	"'; whoami; '",                            // 命令分隔
	"${{ malicious }}",                        // 大括号插值
}

// TestPSQuery_PSStringInjection_CannotEscape 验证 PSString 类型参数经 psQuote
// 转义后注入模板，注入 payload 无法逃逸单引号上下文。
func TestPSQuery_PSStringInjection_CannotEscape(t *testing.T) {
	for _, payload := range adversarialPayloads {
		q := PSQuery{
			Template: `$x = %s; $x`,
			Params:   []PSParam{PSString(payload)},
		}
		rendered := q.render()

		// 不变量 1：渲染结果必须包含 psQuote(payload)
		expected := psQuote(payload)
		if !strings.Contains(rendered, expected) {
			t.Errorf("payload %q 未以 psQuote 形式出现在渲染结果中\n渲染: %s\n期望包含: %s",
				payload, rendered, expected)
			continue
		}

		// 不变量 2：payload 裸形式不应出现在渲染结果中（除非无注入特征）
		if containsInjectionPattern(rendered, payload) {
			t.Errorf("payload %q 可能以未转义形式出现在渲染结果中\n渲染: %s",
				payload, rendered)
		}
	}
}

// TestPSQuery_PSIntRendersAsNumber 验证 PSInt 类型参数渲染为裸整数（非引号字符串）。
func TestPSQuery_PSIntRendersAsNumber(t *testing.T) {
	cases := []int{0, 1, 42, -1, 1000, 999999}
	for _, n := range cases {
		q := PSQuery{
			Template: `Select-Object -First %s`,
			Params:   []PSParam{PSInt(n)},
		}
		rendered := q.render()

		// PSInt 应渲染为裸数字，不应有引号
		if strings.Contains(rendered, "'") {
			t.Errorf("PSInt(%d) 渲染结果不应包含引号\n渲染: %s", n, rendered)
		}

		// 渲染结果应包含该数字
		expected := strconv.Itoa(n)
		if !strings.Contains(rendered, expected) {
			t.Errorf("PSInt(%d) 渲染结果应包含 %q\n渲染: %s", n, expected, rendered)
		}
	}
}

// TestPSQuery_PSRawPassThrough 验证 PSRaw 类型参数原样注入（不做转义）。
// PSRaw 用于 const 派生字符串或已 psQuote'd 的组合表达式。
func TestPSQuery_PSRawPassThrough(t *testing.T) {
	cases := []string{
		`$_.Name -eq 'Spooler'`,                 // 已 psQuote'd 的 Where-Object 条件
		`$true`,                                  // const 派生
		`@{Name='level';Expression={switch($_.Level){1{'Critical'}}}`, // computed property
		"Id, ProviderName, Message",              // Select-Object 字段列表
	}
	for _, raw := range cases {
		q := PSQuery{
			Template: `Get-Service | Where-Object { %s }`,
			Params:   []PSParam{PSRaw(raw)},
		}
		rendered := q.render()

		// PSRaw 应原样出现在渲染结果中
		if !strings.Contains(rendered, raw) {
			t.Errorf("PSRaw(%q) 未原样出现在渲染结果中\n渲染: %s", raw, rendered)
		}
	}
}

// TestPSQuery_ArrayKeysRegex 验证 fixEmptyArrays 对声明的 key 做
// `"key":{}` → `"key":[]` 替换，且不误伤非声明 key。
func TestPSQuery_ArrayKeysRegex(t *testing.T) {
	// 模拟 PowerShell 5.1 ConvertTo-Json 的空嵌套数组 bug 输出
	input := []byte(`[{"name":"svc1","dependencies":{}},{"name":"svc2","dependencies":{},"metadata":{"key":"value"}}]`)

	got := fixEmptyArrays(input, []string{"dependencies"})

	// dependencies":{} 应被替换为 dependencies":[]
	if strings.Contains(string(got), `"dependencies":{}`) {
		t.Errorf("fixEmptyArrays 未替换 dependencies:{}\n输出: %s", got)
	}
	// 替换后应有 dependencies":[]
	if !strings.Contains(string(got), `"dependencies":[]`) {
		t.Errorf("fixEmptyArrays 应输出 dependencies:[]\n输出: %s", got)
	}
	// 非声明的 metadata 对象不应被误伤
	if !strings.Contains(string(got), `"metadata":{"key":"value"}`) {
		t.Errorf("fixEmptyArrays 不应误伤非声明 key metadata\n输出: %s", got)
	}
}

// TestPSQuery_ArrayKeysNil_NoOp 验证 ArrayKeys 为 nil 时不做 regex。
func TestPSQuery_ArrayKeysNil_NoOp(t *testing.T) {
	input := []byte(`{"dependencies":{},"metadata":{}}`)
	got := fixEmptyArrays(input, nil)
	if string(got) != string(input) {
		t.Errorf("ArrayKeys=nil 时不应修改输出\n输入: %s\n输出: %s", input, got)
	}
}

// TestPSQuery_ConvertToJsonWrap 验证 PSQuery.Run 构建的完整命令
// 包含 ConvertTo-Json -InputObject @(...) 外壳。
// 注意：此测试验证 render() 输出（不含 prefix），因为 Run() 需要
// 真实执行 PowerShell。render() 是 Run 的第一步，也是集中化的核心。
func TestPSQuery_ConvertToJsonWrap(t *testing.T) {
	q := PSQuery{
		Template: `Get-Process | Select-Object name, id`,
		Params:   nil,
		Depth:    4,
	}
	// buildFullCmd 是 Run 内部的完整命令构建（prefix + ConvertTo-Json wrap）
	full := q.buildFullCmd()

	// 必须包含 ConvertTo-Json -InputObject @(...) 外壳
	if !strings.Contains(full, "ConvertTo-Json -InputObject @(") {
		t.Errorf("命令缺少 ConvertTo-Json -InputObject @() 外壳\n命令: %s", full)
	}
	// 必须包含 -Depth 4
	if !strings.Contains(full, "-Depth 4") {
		t.Errorf("命令缺少 -Depth 4\n命令: %s", full)
	}
	// 必须包含 -Compress
	if !strings.Contains(full, "-Compress") {
		t.Errorf("命令缺少 -Compress\n命令: %s", full)
	}
}

// TestPSQuery_PrefixConcatenation 验证 buildFullCmd 输出包含
// 所有前缀（utf8 + culture + errorAction）。
func TestPSQuery_PrefixConcatenation(t *testing.T) {
	q := PSQuery{
		Template: `Get-Process`,
		Depth:    2,
	}
	full := q.buildFullCmd()

	mustContain := []string{
		"[Console]::OutputEncoding",
		"UTF8",
		"DefaultThreadCurrentUICulture",
		"en-US",
		`$ErrorActionPreference = "Stop"`,
	}
	for _, s := range mustContain {
		if !strings.Contains(full, s) {
			t.Errorf("命令缺少前缀元素 %q\n命令: %s", s, full)
		}
	}
}

// TestPSQuery_RenderMultipleParams 验证多参数模板渲染。
func TestPSQuery_RenderMultipleParams(t *testing.T) {
	q := PSQuery{
		Template: `Get-WinEvent -FilterHashtable @{LogName=%s; Level=%d} -MaxEvents %d | Select-Object %s`,
		Params: []PSParam{
			PSString("System"),
			PSInt(2),
			PSInt(100),
			PSRaw("Id, ProviderName, Message"),
		},
	}
	rendered := q.render()

	// PSString("System") → 'System'
	if !strings.Contains(rendered, "LogName='System'") {
		t.Errorf("PSString 渲染错误\n渲染: %s", rendered)
	}
	// PSInt(2) → 裸 2
	if !strings.Contains(rendered, "Level=2") {
		t.Errorf("PSInt 渲染错误\n渲染: %s", rendered)
	}
	// PSInt(100) → 裸 100
	if !strings.Contains(rendered, "-MaxEvents 100") {
		t.Errorf("PSInt 渲染错误\n渲染: %s", rendered)
	}
	// PSRaw → 原样
	if !strings.Contains(rendered, "Select-Object Id, ProviderName, Message") {
		t.Errorf("PSRaw 渲染错误\n渲染: %s", rendered)
	}
}

// containsInjectionPattern 检查渲染/命令中是否包含 payload 的裸注入形式。
// 如果 payload 包含注入特征字符且其原文（非 psQuote 转义后）出现在 cmd 中，
// 则认为存在注入风险。供 PSQuery 级 + per-skill PSRaw 构造安全测试引用。
func containsInjectionPattern(cmd, payload string) bool {
	// 如果 payload 包含注入特征字符
	if !strings.ContainsAny(payload, "'\"$`;") {
		return false // 无注入特征，不需要检查
	}

	// psQuote 转义后的形式是安全的
	quoted := psQuote(payload)

	// 检查命令中是否包含去掉外层引号后的 payload 裸形式
	cmdWithoutQuoted := strings.ReplaceAll(cmd, quoted, "")
	if strings.Contains(cmdWithoutQuoted, payload) {
		return true // payload 裸形式出现在命令中
	}
	return false
}
