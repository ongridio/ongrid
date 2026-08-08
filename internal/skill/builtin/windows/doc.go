//go:build windows

// Package windows 包含 Windows edge agent 的内置 skill 实现。
// 所有文件带 //go:build windows build tag，Linux 编译时不包含。
// 对称 Linux skill 在 internal/skill/builtin/（flat namespace）。
// # 安全分析
// LLM 能影响的字段及安全保证来源：
//   - LogName (string)：LLM 选择 5 个枚举值之一
//     注入方式：作为 -LogName 参数值进入 PowerShell 命令
//     安全保证：psQuote() 转义（不依赖 enum 校验，数学可证明不逃逸）
//   - Level (string)：LLM 选择 5 个枚举值之一
//     注入方式：作为 FilterHashtable LevelDisplayName 值进入命令
//     安全保证：psQuote() 转义（不依赖 enum 校验）
//   - MaxEvents (int)：LLM 选择 1-1000 范围
//     注入方式：作为 -MaxEvents 参数值进入命令
//     安全保证：Go int 类型通过 %d 格式化动词注入（类型系统保证）
//   - Since (string→Duration)：LLM 传入 Go duration 字符串
//     注入方式：解析为秒数后通过 %d 注入命令
//     安全保证：time.ParseDuration + int() 转换 + %d 格式化动词
//   - IncludeMessage (bool)：LLM 选择 true/false
//     注入方式：Go if 分支选择 Select-Object 字段列表
//     安全保证：不进入 PowerShell 字符串（Go 代码控制分支）
// # ard Rules 遵守声明
//   - Rule 1：命令模板 const string + psQuote 受控注入（升级版）
//   - Rule 2：参数白名单 + skill 层 enum/int 校验（业务约束）
//   - Rule 3：不用 Invoke-Expression / -ExecutionPolicy Bypass / Add-Type / COM / Win32 API
//   - Rule 4：强制 UTF-8 输出（[Console]::OutputEncoding）
//   - Rule 5：目标 PowerShell 5.1（powershell.exe -NoProfile -NonInteractive）
//   - Rule 6：审计友好（Ongrid audit + Windows EventLog 4104 双重留痕）
// #  Layer 1 culture 强制
// PSQuery.buildFullCmd 前缀强制 en-US culture，解决：
//   - LevelDisplayName / Status 等 .NET 枚举字段英文渲染
//   - Get-WinEvent Message 字段英文渲染（message table DLL 按 thread culture 选语言包）
// []: ../../../docs/adr/034-windows-skill-set.md
// []: ../../../docs/adr/036-windows-edge-i18n-permission-privacy.md
package windows
