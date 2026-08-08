//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&Network{}) }

// Network 查询 Windows TCP 连接列表（Get-NetTCPConnection），是网络层故障的
// 硬证据源（端口占用 / 异常连接 / 可疑外联）。只读操作，不做
// Stop-NetTCPConnection / Set-NetTCPConnection
//。
// 安全保证：
//   - 命令模板是 const string
//   - 字符串类参数（remote_address）经 psQuote 转义后注入
//   - int 参数（local_port / remote_port / top_n）走 %d 格式化动词，Go 类型系统保证
//   - enum 参数（state）走白名单校验，映射为 PS 枚举名后经 psQuote 转义
//   - enum 校验仅作业务约束，安全不依赖校验正确性（adversarial test 覆盖绕过场景）
type Network struct{}

// 命令模板
// 占位符：
//   %s = filterCondition — 由 buildNetworkFilter 构建，remote_address 经 psQuote 转义
//   %d = topN             — int 类型，%d 格式化注入
// Select-Object computed properties 将 .NET 属性名映射为 snake_case 输出字段：
//   - local_address: $_.LocalAddress（本地 IP 地址）
//   - local_port: $_.LocalPort（本地端口）
//   - remote_address: $_.RemoteAddress（远程 IP 地址）
//   - remote_port: $_.RemotePort（远程端口）
//   - state: $_.State.ToString()（TCP 状态枚举转字符串，避免 ConvertTo-Json 输出整数）
//   - owning_process: $_.OwningProcess（归属进程 PID）
// ConvertTo-Json -InputObject @() 外壳由 PSQuery.Run 统一处理。
const networkPipelineTemplate = `Get-NetTCPConnection | Where-Object { %s } | Select-Object -First %d | Select-Object @{Name='local_address';Expression={$_.LocalAddress}}, @{Name='local_port';Expression={$_.LocalPort}}, @{Name='remote_address';Expression={$_.RemoteAddress}}, @{Name='remote_port';Expression={$_.RemotePort}}, @{Name='state';Expression={$_.State.ToString()}}, @{Name='owning_process';Expression={$_.OwningProcess}}`

// networkParams 是 Network skill 的参数结构。
type networkParams struct {
	State         string `json:"state"`
	LocalPort     int    `json:"local_port"`
	RemoteAddress string `json:"remote_address"`
	RemotePort    int    `json:"remote_port"`
	TopN          int    `json:"top_n"`
}

// networkSpec 是 Network skill 的声明式骨架。
// Execute 委托 spec.Run，skeleton 集中 unmarshal + defaults + enum 校验 + PSQuery 构建。
// TopN 钳制由 skeleton 通过 clampInt 统一处理（networkTopNDefault/Max 仅作边界声明）。
var networkSpec = SkillSpec[networkParams]{
	Metadata:    NetworkMetadata(),
	Timeout:     30 * time.Second,
	Template:    networkPipelineTemplate,
	Depth:       4,
	Defaults: func(p *networkParams) {
		if p.State == "" {
			p.State = "all"
		}
	},
	BuildFilter: buildNetworkFilter,
	ValidateEnums: func(p networkParams) error {
		// Rule 2：skill 层业务校验（错误提示用，不是安全防线）
		// "all" 是元选项（不在 networkStateEnum PS 枚举集中），特判跳过
		if p.State != "all" && !networkStateValid[p.State] {
			return fmt.Errorf("network: invalid state %q", p.State)
		}
		return nil
	},
	ExtractTopN: func(p networkParams) int { return p.TopN },
	TopNDefault: 50,
	TopNMax:     500,
}

// Metadata 返回 skill 框架可见的规格定义。
// 委托 NetworkMetadata()（metadata.go，跨平台单一真相源）。
func (Network) Metadata() skill.Metadata { return NetworkMetadata() }

// Execute 执行 Get-NetTCPConnection 查询并返回 JSON 结果。
// 委托 networkSpec.Run（skeleton 集中骨架）。
func (Network) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	return networkSpec.Run(ctx, params)
}

// buildNetworkFilter 构造 Where-Object 过滤条件（作为 SkillSpec.BuildFilter 扩展点）。
// 安全保证：所有用户输入值经 psQuote 转义后注入。
// port 参数走 %d 格式化（Go int 类型保证）。
// "all" 或空值 → 跳过该过滤条件（$true 语义）。
// 无任何过滤条件时返回 "$true"（全量查询）。
func buildNetworkFilter(p networkParams) string {
	var conditions []string

	if p.State != "" && p.State != "all" {
		psState := networkStateToPS[p.State]
		conditions = append(conditions, `$_.State -eq `+psQuote(psState))
	}
	if p.LocalPort > 0 {
		conditions = append(conditions, fmt.Sprintf(`$_.LocalPort -eq %d`, p.LocalPort))
	}
	if p.RemoteAddress != "" {
		conditions = append(conditions, `$_.RemoteAddress -eq `+psQuote(p.RemoteAddress))
	}
	if p.RemotePort > 0 {
		conditions = append(conditions, fmt.Sprintf(`$_.RemotePort -eq %d`, p.RemotePort))
	}

	return joinFilterConditions(conditions)
}
