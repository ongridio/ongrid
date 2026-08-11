//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// PSParam 是 PowerShell 命令参数的密封接口（sealed interface）。
// unexported 方法 psParam() 保证仅包内三种类型可实现：
// PSString / PSInt / PSRaw。调用方无法自定义新类型绕过转义。
// 设计理由：
// 原始 []any 无法区分 user string（需 psQuote）和 derived string
// （已 psQuote'd 或 const 派生）。[]PSParam 在编译时拒绝裸 string。
type PSParam interface {
	psParam()
}

// PSString 是需 psQuote 转义的用户字符串参数。
// 渲染时经 psQuote 包装后注入模板 %s 占位符。
type PSString string

// PSInt 是数字类参数，渲染为裸整数（%d 语义）。
// Go 类型系统保证 int 不可能包含注入字符。
type PSInt int

// PSRaw 是原样注入的字符串（const 派生或已 psQuote'd 组合表达式）。
// 不做任何转义，调用方自行保证安全性。
type PSRaw string

func (PSString) psParam() {}
func (PSInt) psParam()   {}
func (PSRaw) psParam()   {}

// PSQuery 集中管理 PowerShell 命令构建、执行、JSON 序列化修复。
// 集中化解决：
//   - PS 5.1 ConvertTo-Json 空集合输出 null（B1）
//   - PS 5.1 ConvertTo-Json 嵌套空数组输出 {}（B2）
//   - prefix（UTF-8 + en-US culture + ErrorActionPreference）散落 3 处（Rule of Three）
// 使用方式：
//	q := PSQuery{
//	    Template:  `Get-Service | Where-Object { %s } | Select-Object ...`,
//	    Params:    []PSParam{PSRaw(filter)},
//	    Depth:     4,
//	    ArrayKeys: []string{"dependencies"},
//	}
//	result, err := q.Run(ctx, 30*time.Second)
type PSQuery struct {
	Template  string    // PowerShell pipeline 表达式，含 %s 占位符（不含 ConvertTo-Json + @() 外壳）
	Params    []PSParam // 按 Template 中 %s 顺序排列的参数
	Depth     int       // ConvertTo-Json -Depth 值（event_log=3, services/processes=4）
	ArrayKeys []string  // 需 regex 修复空数组 {} → [] 的 JSON key（nil → 不修复）
}

// Run 执行 PowerShell 命令，返回修复后的 JSON。
// 内部流程：render() → ConvertTo-Json wrap → prefix → exec → fixEmptyArrays → return
func (q PSQuery) Run(ctx context.Context, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fullCmd := q.buildFullCmd()

	out, err := exec.CommandContext(ctx,
		"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fullCmd,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("powershell: %w (stdout: %s)", err, out)
	}

	out = fixEmptyArrays(out, q.ArrayKeys)
	return json.RawMessage(out), nil
}

// buildFullCmd 构建完整的 PowerShell 命令字符串（prefix + ConvertTo-Json wrap + rendered template）。
func (q PSQuery) buildFullCmd() string {
	rendered := q.render()
	jsonCmd := "ConvertTo-Json -InputObject @(\n" + rendered + "\n) -Depth " + strconv.Itoa(q.Depth) + " -Compress"
	return utf8Prefix + culturePrefix + errorActionPrefix + jsonCmd
}

// render 将 Template 的 %s 占位符替换为参数渲染结果。
// PSString → psQuote，PSInt → 裸整数，PSRaw → 原样注入。
func (q PSQuery) render() string {
	args := make([]any, len(q.Params))
	for i, p := range q.Params {
		switch v := p.(type) {
		case PSString:
			args[i] = psQuote(string(v))
		case PSInt:
			args[i] = int(v)
		case PSRaw:
			args[i] = string(v)
		default:
			panic(fmt.Sprintf("PSQuery: unknown PSParam type %T", p))
		}
	}
	return fmt.Sprintf(q.Template, args...)
}

// fixEmptyArrays 修复 PowerShell 5.1 ConvertTo-Json 嵌套空数组 bug（B2）：
// `"key":{}` → `"key":[]`。仅对声明的 arrayKeys 做 regex，避免误伤合法空对象。
func fixEmptyArrays(out []byte, arrayKeys []string) []byte {
	for _, key := range arrayKeys {
		// `"key"\s*:\s*\{\}` 匹配 ConvertTo-Json 空嵌套数组的输出
		pattern := `"` + regexp.QuoteMeta(key) + `"\s*:\s*\{\}`
		re := regexp.MustCompile(pattern)
		replacement := `"` + key + `":[]`
		out = re.ReplaceAll(out, []byte(replacement))
	}
	return out
}
