//go:build windows

package windows

import (
	"strings"
)

// psQuote 将 Go 字符串转为 PowerShell 单引号字符串字面量。
// PowerShell 单引号字符串语义：所有字符字面量，唯一转义 ' → ''。
// 数学保证：任意输入 s 经 psQuote 后，PowerShell tokenizer 解析还原为 s，
// 无法逃逸到命令语法（不依赖任何调用方校验逻辑）。
// 用途：所有字符串类参数注入 PowerShell 命令前必须经过 psQuote。
// 数字类参数通过 %d / %f 格式化动词注入，Go 类型系统保证。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// 命令前缀常量
// 由 PSQuery.buildFullCmd 引用，集中拼接为 prefix + ConvertTo-Json wrap + template。
const (
	// utf8Prefix 强制 PowerShell 输出 UTF-8
	utf8Prefix = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; `

	// culturePrefix 强制 en-US culture
	// 解决 LevelDisplayName / Status / Message 等 .NET 枚举和 message table DLL 的本地化
		// 双层防线：
	//   - DefaultThreadCurrent*Culture：AppDomain 级别，影响所有新线程（包括 PowerShell runspace 内部线程）
	//   - Thread.CurrentThread.*Culture：当前线程级别，兜底
		// 用 GetCultureInfo('en-US') 而非字符串赋值，避免 PowerShell 隐式类型转换的不确定性。
	// 注：LevelDisplayName 已额外用 computed property（levelDisplayExpr）做确定性映射，
	// 此 culture prefix 主要解决 Message 字段的本地化。
	culturePrefix = `[System.Globalization.CultureInfo]::DefaultThreadCurrentUICulture = [System.Globalization.CultureInfo]::GetCultureInfo('en-US'); ` +
		`[System.Globalization.CultureInfo]::DefaultThreadCurrentCulture = [System.Globalization.CultureInfo]::GetCultureInfo('en-US'); ` +
		`[System.Threading.Thread]::CurrentThread.CurrentUICulture = [System.Globalization.CultureInfo]::GetCultureInfo('en-US'); ` +
		`[System.Threading.Thread]::CurrentThread.CurrentCulture = [System.Globalization.CultureInfo]::GetCultureInfo('en-US'); `

	// errorActionPrefix 使错误立即抛出，不会静默继续
	errorActionPrefix = `$ErrorActionPreference = "Stop"; `
)
