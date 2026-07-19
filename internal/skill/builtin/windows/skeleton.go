//go:build windows

// skeleton.go 是 Windows skill 的执行骨架（Skill Skeleton 深模块）。
// 问题（重构前）：4 个同构 skill（network/hotfix/services/processes）各自复制了
// ~80 行 Execute 骨架：unmarshal params → applyDefaults → enum 校验 → buildQuery →
// PSQuery.Run。每份拷贝结构相同，差异仅在 params 类型名、错误前缀字符串、enum 校验逻辑。
// 解决方案：提取 SkillSpec[P] 泛型深模块。每个 skill 声明一个 SkillSpec 实例，
// Execute() 缩为 1 行委托。skeleton 集中所有不变量：
//   - timeout 应用
//   - unmarshal 错误格式（含 skill.Key 前缀）
//   - enum 校验顺序（defaults → enum → filter）
//   - PSQuery 构建顺序（filter + 可选 topN）
//   - TopN 钳制（统一 clampInt）
// event_log 是 outlier（FilterHashtable + IncludeMessage 切换 select_fields），
// 不纳入 skeleton，继续直接用 PSQuery.Run。
// enum 单一真相源定义在 enum.go（无 build tag，跨平台可见），此文件只引用。

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

// clampInt 钳制 int 到合法范围。
//   - v <= 0 → def（默认值，表示调用方未指定）
//   - v > max → max
//   - 其他 → v
// 集中化：network/hotfix/processes 3 个 skill 都有 topN 钳制逻辑，
// 原本各自实现，现统一到 skeleton 层。
func clampInt(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// joinFilterConditions 把 filter 条件列表 join 为 PowerShell -and 表达式。
// 空 list → "$true"（Where-Object 全量通过）。
// 单条件 → 原样返回。
// 多条件 → "cond1 -and cond2 -and cond3"。
// 集中化：4 个 skill 都有此模式（conditions + join + $true fallback）。
func joinFilterConditions(conditions []string) string {
	if len(conditions) == 0 {
		return "$true"
	}
	return strings.Join(conditions, " -and ")
}

// SkillSpec 是 Windows skill 的声明式执行骨架。
// 泛型参数 P 是 skill 的 params 类型（如 networkParams / hotfixParams）。
// 每个 skill 声明一个 SkillSpec 实例，Execute() 委托 spec.Run()。
// 字段说明：
//   - Metadata：skill.Metadata（含 Key 用于错误前缀）
//   - Timeout：PSQuery.Run 超时
//   - Template：const PowerShell pipeline 模板，含 %s filter + 可选 %d topN 占位符
//   - Depth：ConvertTo-Json -Depth
//   - ArrayKeys：需 fixEmptyArrays 修复的 JSON key（services 专用）
//   - Defaults：per-skill 默认值函数（不含 TopN 钳制，TopN 由 skeleton 统一钳）
//   - BuildFilter：per-skill filter 构建函数（返回 Where-Object 条件表达式）
//   - ValidateEnums：per-skill enum 校验函数（nil = 无 enum）
//   - ExtractTopN：从 params 提取 TopN 的函数（nil = 该 skill 无 topN 参数）
//   - TopNDefault / TopNMax：TopN 钳制边界
type SkillSpec[P any] struct {
	Metadata      skill.Metadata
	Timeout       time.Duration
	Template      string
	Depth         int
	ArrayKeys     []string
	Defaults      func(p *P)
	BuildFilter   func(p P) string
	ValidateEnums func(p P) error
	ExtractTopN   func(p P) int
	TopNDefault   int
	TopNMax       int
}

// Run 执行 skill：unmarshal → defaults → enum 校验 → buildQuery → PSQuery.Run。
// 不变量（skeleton 集中保障）：
//   - unmarshal 失败时错误含 skill.Key 前缀
//   - enum 校验在 defaults 之后（校验的是钳制后的值）
//   - PSQuery.Params 顺序固定：filter（PSRaw）+ 可选 topN（PSInt）
//   - TopN 经 clampInt 钳制，不再依赖 per-skill defaults
func (s SkillSpec[P]) Run(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p P
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("%s: decode params: %w", s.Metadata.Key, err)
		}
	}
	if s.Defaults != nil {
		s.Defaults(&p)
	}
	if s.ValidateEnums != nil {
		if err := s.ValidateEnums(p); err != nil {
			return nil, err
		}
	}
	q := s.buildQuery(p)
	return q.Run(ctx, s.Timeout)
}

// buildQuery 从 params 构建 PSQuery。
// PSQuery.Params 构建顺序：
//  1. PSRaw(filter) — 总是存在（BuildFilter 返回 "$true" 表示无过滤）
//  2. PSInt(topN) — 仅当 ExtractTopN != nil 时追加
// BuildFilter 是必须字段（无 nil 检查）；调用方必须设置，否则 panic。
// 这是作者时间错误，应在代码审查阶段捕获。
func (s SkillSpec[P]) buildQuery(p P) PSQuery {
	filter := s.BuildFilter(p)
	params := []PSParam{PSRaw(filter)}
	if s.ExtractTopN != nil {
		topN := clampInt(s.ExtractTopN(p), s.TopNDefault, s.TopNMax)
		params = append(params, PSInt(topN))
	}
	return PSQuery{
		Template:  s.Template,
		Params:    params,
		Depth:     s.Depth,
		ArrayKeys: s.ArrayKeys,
	}
}
