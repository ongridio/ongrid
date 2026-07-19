//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&Services{}) }

// Services 查询 Windows 系统服务列表（Get-Service），是 Windows RCA 的第二证据源
// （服务崩溃/启动失败/依赖链）。只读操作，不做 Start-Service / Stop-Service
//。
// 安全保证：
//   - 命令模板是 const string
//   - 字符串类参数（name）经 psQuote 转义后注入
//   - enum 参数（status / start_type）走白名单校验，映射为 PS 枚举名后经 psQuote 转义
//   - enum 校验仅作业务约束，安全不依赖校验正确性（adversarial test 覆盖绕过场景）
type Services struct{}

// 命令模板
// 占位符：
//   %s = filterCondition — 由 buildServicesFilter 构建，所有值经 psQuote 转义
// Select-Object computed properties 将 .NET 属性名映射为 snake_case 输出字段。
// $_.Status.ToString() / $_.StartType.ToString() 确保枚举值序列化为字符串
// （ConvertTo-Json 默认对枚举输出整数）。
// @($_.ServicesDependedOn | ForEach-Object { $_.Name }) 确保依赖服务名是数组（PS 管道展开空集合）。
// ConvertTo-Json -InputObject @() 外壳 + dependencies regex post-process 由 PSQuery.Run 统一处理。
// PowerShell 5.1 ConvertTo-Json 对嵌套空数组输出 {}（已知 bug），PSQuery.fixEmptyArrays 把
// "dependencies":{} 替换为 "dependencies":[]。空依赖服务（多数 Windows 服务）正确显示 []。
//  2026-07-13 B2。
const servicesPipelineTemplate = `Get-Service | Where-Object { %s } | Select-Object @{Name='name';Expression={$_.Name}}, @{Name='display_name';Expression={$_.DisplayName}}, @{Name='status';Expression={$_.Status.ToString()}}, @{Name='start_type';Expression={$_.StartType.ToString()}}, @{Name='dependencies';Expression={@($_.ServicesDependedOn | ForEach-Object { $_.Name })}}`

// servicesParams 是 Services skill 的参数结构。
type servicesParams struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	StartType string `json:"start_type"`
}

// servicesSpec 是 Services skill 的声明式骨架。
// Execute 委托 spec.Run，skeleton 集中 unmarshal + defaults + enum 校验 + PSQuery 构建。
var servicesSpec = SkillSpec[servicesParams]{
	Metadata:  ServicesMetadata(),
	Timeout:   30 * time.Second,
	Template:  servicesPipelineTemplate,
	Depth:     4,
	ArrayKeys: []string{"dependencies"},
	Defaults: func(p *servicesParams) {
		if p.Status == "" {
			p.Status = "all"
		}
		if p.StartType == "" {
			p.StartType = "all"
		}
	},
	BuildFilter: buildServicesFilter,
	ValidateEnums: func(p servicesParams) error {
		// Rule 2：skill 层业务校验（错误提示用，不是安全防线）
		// "all" 是元选项（不在 servicesStatusEnum PS 枚举集中），特判跳过
		if p.Status != "all" && !servicesStatusValid[p.Status] {
			return fmt.Errorf("services: invalid status %q", p.Status)
		}
		if p.StartType != "all" && !servicesStartTypeValid[p.StartType] {
			return fmt.Errorf("services: invalid start_type %q", p.StartType)
		}
		return nil
	},
}

// Metadata 返回 skill 框架可见的规格定义。
// 委托 ServicesMetadata()（metadata.go，跨平台单一真相源）。
func (Services) Metadata() skill.Metadata { return ServicesMetadata() }

// Execute 执行 Get-Service 查询并返回 JSON 结果。
// 委托 servicesSpec.Run（skeleton 集中骨架）。
func (Services) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	return servicesSpec.Run(ctx, params)
}

// buildServicesFilter 构造 Where-Object 过滤条件（作为 SkillSpec.BuildFilter 扩展点）。
// 安全保证：所有用户输入值经 psQuote 转义后注入。
// "all" 或空字符串 → 跳过该过滤条件（$true 语义）。
// 无任何过滤条件时返回 "$true"（全量查询）。
func buildServicesFilter(p servicesParams) string {
	var conditions []string

	if p.Name != "" {
		conditions = append(conditions, `$_.Name -eq `+psQuote(p.Name))
	}
	if p.Status != "all" {
		psStatus := servicesStatusToPS[p.Status]
		conditions = append(conditions, `$_.Status -eq `+psQuote(psStatus))
	}
	if p.StartType != "all" {
		psStartType := servicesStartTypeToPS[p.StartType]
		conditions = append(conditions, `$_.StartType -eq `+psQuote(psStartType))
	}

	return joinFilterConditions(conditions)
}
