//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&Hotfix{}) }

// Hotfix 查询 Windows 已安装补丁列表（Get-HotFix），是 RCA "近期变更"
// 证据源（客户报障问的第一句："最近装了什么 patch？"）。只读操作。
// 安全保证：
//   - 命令模板是 const string
//   - 无字符串类参数 — top_n 和 since_days 均为 int，走 PSInt / %d 格式化
//   - Go 类型系统保证 int 参数无法注入 PowerShell 语法
type Hotfix struct{}

// 命令模板
// 占位符：
//	%s = filterCondition — 由 buildHotfixFilter 构建（const 派生，无用户字符串）
//	%d = topN            — int 类型，%d 格式化注入
// Sort-Object InstalledOn -Descending：按安装时间降序（最近优先）。
// InstalledOn computed property 含 null 保护（$null -ne $_.InstalledOn）：
//	部分 hotfix（尤其旧 WSUS 补丁）InstalledOn 为 null，ErrorActionPreference=Stop 下
//	null.ToString() 会致命错误。
// .ToString('o') 转为 ISO 8601 字符串（避免 ConvertTo-Json 序列化 DateTime 问题，S 5.1 经验）。
// ConvertTo-Json -InputObject @() 外壳由 PSQuery.Run 统一处理。
const hotfixPipelineTemplate = `Get-HotFix | Where-Object { %s } | Sort-Object InstalledOn -Descending | Select-Object -First %d | Select-Object @{Name='hotfix_id';Expression={$_.HotFixID}}, @{Name='description';Expression={$_.Description}}, @{Name='installed_on';Expression={ if ($null -ne $_.InstalledOn) { $_.InstalledOn.ToString('o') } }}, @{Name='source';Expression={$_.Source}}, @{Name='installed_by';Expression={$_.InstalledBy}}`

// hotfixSinceDaysMax 是 since_days 的钳制上界（与 metadata.go 共用语义）。
const hotfixSinceDaysMax = 365

// hotfixParams 是 Hotfix skill 的参数结构。
type hotfixParams struct {
	TopN      int `json:"top_n"`
	SinceDays int `json:"since_days"`
}

// hotfixSpec 是 Hotfix skill 的声明式骨架。
// Execute 委托 spec.Run，skeleton 集中 unmarshal + defaults + PSQuery 构建。
// TopN 钳制由 skeleton 通过 clampInt 统一处理。
// SinceDays 钳制在 Defaults 内（因 SinceDays=0 是合法值"不过滤"，不走 clampInt 的 <=0 规则）。
var hotfixSpec = SkillSpec[hotfixParams]{
	Metadata:    HotfixMetadata(),
	Timeout:     30 * time.Second,
	Template:    hotfixPipelineTemplate,
	Depth:       4,
	Defaults: func(p *hotfixParams) {
		if p.SinceDays < 0 {
			p.SinceDays = 0
		}
		if p.SinceDays > hotfixSinceDaysMax {
			p.SinceDays = hotfixSinceDaysMax
		}
	},
	BuildFilter: buildHotfixFilter,
	ExtractTopN: func(p hotfixParams) int { return p.TopN },
	TopNDefault: 20,
	TopNMax:     200,
}

// Metadata 返回 skill 框架可见的规格定义。
// 委托 HotfixMetadata()（metadata.go，跨平台单一真相源）。
func (Hotfix) Metadata() skill.Metadata { return HotfixMetadata() }

// Execute 执行 Get-HotFix 查询并返回 JSON 结果。
// 委托 hotfixSpec.Run（skeleton 集中骨架）。
func (Hotfix) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	return hotfixSpec.Run(ctx, params)
}

// buildHotfixFilter 构造 Where-Object 过滤条件（作为 SkillSpec.BuildFilter 扩展点）。
// since_days > 0 时，过滤 InstalledOn > (当前时间 - N 天) 的 hotfix。
// 使用 (Get-Date).AddDays(-N) 动态计算截止日期，N 通过 %d 注入（Go int 安全）。
// 无过滤条件时返回 "$true"（全量查询）。
func buildHotfixFilter(p hotfixParams) string {
	if p.SinceDays > 0 {
		return fmt.Sprintf(`$_.InstalledOn -gt (Get-Date).AddDays(-%d)`, p.SinceDays)
	}
	return "$true"
}
