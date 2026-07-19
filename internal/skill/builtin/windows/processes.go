//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&Processes{}) }

// Processes 查询 Windows 进程列表（Get-Process），是 CPU/RAM 飙高类故障的
// 硬证据源（哪个进程在吃资源）。只读操作，不做 Stop-Process
//。
// 安全保证：
//   - 命令模板是 const string
//   - 字符串类参数（name）经 psQuote 转义后注入
//   - int 参数（top_n / min_memory_mb）走 %d 格式化动词，Go 类型系统保证
//   - enum 校验仅作业务约束，安全不依赖校验正确性（adversarial test 覆盖绕过场景）
type Processes struct{}

// 命令模板
// 占位符：
//   %s = filterCondition — 由 buildProcessesFilter 构建，name 经 psQuote 转义
//   %d = topN             — int 类型，%d 格式化注入
// Sort-Object CPU -Descending 按 CPU 时间降序排列。
// Select-Object -First %d 取前 N 个进程。
// Select-Object computed properties 将 .NET 属性名映射为 snake_case 输出字段：
//   - name: $_.Name（进程名）
//   - id: $_.Id（PID）
//   - cpu_seconds: $_.CPU（总 CPU 秒数，round 2）
//   - working_set_mb: $_.WorkingSet64 / 1MB（工作集 MB，round 2）
//   - path: $_.Path（可执行路径，系统进程可能为 null）
// ConvertTo-Json -InputObject @() 外壳由 PSQuery.Run 统一处理。
const processesPipelineTemplate = `Get-Process | Where-Object { %s } | Sort-Object CPU -Descending | Select-Object -First %d | Select-Object @{Name='name';Expression={$_.Name}}, @{Name='id';Expression={$_.Id}}, @{Name='cpu_seconds';Expression={[math]::Round($_.CPU, 2)}}, @{Name='working_set_mb';Expression={[math]::Round($_.WorkingSet64 / 1MB, 2)}}, @{Name='path';Expression={$_.Path}}`

// processesParams 是 Processes skill 的参数结构。
type processesParams struct {
	Name        string `json:"name"`
	TopN        int    `json:"top_n"`
	MinMemoryMB int    `json:"min_memory_mb"`
}

// processesSpec 是 Processes skill 的声明式骨架。
// Execute 委托 spec.Run，skeleton 集中 unmarshal + defaults + PSQuery 构建。
// TopN 钳制由 skeleton 通过 clampInt 统一处理。
// MinMemoryMB 负值钳制在 Defaults 内（因 MinMemoryMB=0 是合法值"不过滤"）。
var processesSpec = SkillSpec[processesParams]{
	Metadata:    ProcessesMetadata(),
	Timeout:     30 * time.Second,
	Template:    processesPipelineTemplate,
	Depth:       4,
	Defaults: func(p *processesParams) {
		if p.MinMemoryMB < 0 {
			p.MinMemoryMB = 0
		}
	},
	BuildFilter: buildProcessesFilter,
	ExtractTopN: func(p processesParams) int { return p.TopN },
	TopNDefault: 10,
	TopNMax:     100,
}

// Metadata 返回 skill 框架可见的规格定义。
// 委托 ProcessesMetadata()（metadata.go，跨平台单一真相源）。
func (Processes) Metadata() skill.Metadata { return ProcessesMetadata() }

// Execute 执行 Get-Process 查询并返回 JSON 结果。
// 委托 processesSpec.Run（skeleton 集中骨架）。
func (Processes) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	return processesSpec.Run(ctx, params)
}

// buildProcessesFilter 构造 Where-Object 过滤条件（作为 SkillSpec.BuildFilter 扩展点）。
// 安全保证：name 经 psQuote 转义，min_memory_mb 走 %d。
// 无任何过滤条件时返回 "$true"（全量查询）。
func buildProcessesFilter(p processesParams) string {
	var conditions []string

	if p.Name != "" {
		conditions = append(conditions, `$_.Name -eq `+psQuote(p.Name))
	}
	if p.MinMemoryMB > 0 {
		conditions = append(conditions, fmt.Sprintf(`$_.WorkingSet64 / 1MB -gt %d`, p.MinMemoryMB))
	}

	return joinFilterConditions(conditions)
}
