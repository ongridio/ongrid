//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&EventLog{}) }

// EventLog 查询 Windows EventLog（System/Application/Security/Setup/ForwardedEvents）。
// 返回最近 N 条匹配级别的事件，是 Windows RCA 的第一证据源。
// 安全保证：
//   - 命令模板是 const string
//   - 字符串类参数（log_name / level）经 psQuote 转义后注入
//   - 数字类参数（max_events / since）走 %d 格式化动词
//   - enum 校验仅作业务约束，安全不依赖校验正确性
type EventLog struct{}

// 命令模板
// 占位符：
//   %s = psQuote(log_name)   — 字符串类，经 psQuote 转义
//   %d = level_int             — 数字类（1=Critical/2=Error/3=Warning/4=Information/5=Verbose），Go 类型系统保证
//   %d = since_seconds         — 数字类，Go 类型系统保证
//   %d = max_events            — 数字类，Go 类型系统保证
//   %s = select_fields         — const 派生字符串（非 LLM 输入），直接注入
// 注：LogName 放入 FilterHashtable（不放 -LogName 参数），因为 -LogName 和
// -FilterHashtable 属于不同 parameter set，同时使用会触发 AmbiguousParameterSet。
// Level 用 int（不用 LevelDisplayName），因为 FilterHashtable 只接受 Level(int)。
// 无匹配事件处理：Get-WinEvent 抛 NoMatchingEventsFound（在 ErrorActionPreference=Stop
// 下成为终止错误），用 try/catch 转为 @()。
// ConvertTo-Json -InputObject @() 外壳 + try/catch 空事件兜底由 PSQuery.Run 统一处理。
//  2026-07-13 B1。
const eventLogPipelineTemplate = `$events = try { Get-WinEvent -FilterHashtable @{LogName=%s; Level=%d; StartTime=[DateTime]::Now.Subtract([TimeSpan]::FromSeconds(%d))} -MaxEvents %d } catch { @() }; $events | Select-Object %s`

// levelDisplayExpr 用 Level(int) → 英文名称映射，绕过 .NET 本地化。
// 不依赖 culture prefix 是否生效，确定性强。
// 对称 levelToInt map 的反向映射，hardcode 在 const string 中。
const levelDisplayExpr = `@{Name='LevelDisplayName';Expression={switch($_.Level){1{'Critical'}2{'Error'}3{'Warning'}4{'Information'}5{'Verbose'}default{'Unknown'}}}}`

// timeCreatedExpr 将 DateTime 格式化为 ISO 8601 round-trip（'o' 格式），
// 替代 ConvertTo-Json 默认的 /Date(epoch_ms)/ 序列化。
const timeCreatedExpr = `@{Name='TimeCreated';Expression={$_.TimeCreated.ToString('o')}}`

// 合法枚举值
var (
	validLogNames = map[string]bool{
		"System":           true,
		"Application":      true,
		"Security":         true,
		"Setup":            true,
		"ForwardedEvents":  true,
	}
	validLevels = map[string]bool{
		"Critical":    true,
		"Error":       true,
		"Warning":     true,
		"Information": true,
		"Verbose":     true,
	}
)

// levelToInt 将级别字符串映射为 Windows EventLog Level 整数。
// FilterHashtable 只接受 Level(int)，不接受 LevelDisplayName(string)。
var levelToInt = map[string]int{
	"Critical":    1,
	"Error":       2,
	"Warning":     3,
	"Information": 4,
	"Verbose":     5,
}

// 钳制边界
// defaultMaxEvents / maxMaxEvents / defaultSince / defaultLevel 定义在 metadata.go
// （无 build tag，跨平台单一真相源）。
const (
	eventLogTimeout = 30 * time.Second
)

// eventLogParams 是 EventLog skill 的参数结构。
// IncludeMessage 用 *bool 以区分"未设置"（nil → 默认 true）和"显式 false"。
type eventLogParams struct {
	LogName        string `json:"log_name"`
	MaxEvents      int    `json:"max_events"`
	Level          string `json:"level"`
	Since          string `json:"since"`
	IncludeMessage *bool  `json:"include_message"`
}

// Metadata 返回 skill 框架可见的规格定义。
// 委托 EventLogMetadata()（metadata.go，跨平台单一真相源）。
func (EventLog) Metadata() skill.Metadata { return EventLogMetadata() }

// Execute 执行 Get-WinEvent 查询并返回 JSON 结果。
func (EventLog) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p eventLogParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("event_log: decode params: %w", err)
		}
	}

	// Rule 2：skill 层业务校验（错误提示用，不是安全防线）
	p = applyDefaults(p)
	if !validLogNames[p.LogName] {
		return nil, fmt.Errorf("event_log: invalid log_name %q", p.LogName)
	}
	if !validLevels[p.Level] {
		return nil, fmt.Errorf("event_log: invalid level %q", p.Level)
	}

	// TODO():  — Security 日志权限检测
	// 当 LogName == "Security" 时，尝试轻量查询检测当前服务账户是否有权访问。
	// NetworkService 默认无权读 Security EventLog（需 SeSecurityPrivilege）。
	// 失败时返回友好错误："Security EventLog 需 LocalSystem 权限，请用 supervisor.exe --install --elevated 重装"

	// Rule 1（升级版）：字符串走 psQuote，数字走 %d
	q := buildEventLogQuery(p)
	return q.Run(ctx, eventLogTimeout)
}

// applyDefaults 应用默认值并钳制参数到合法范围。
func applyDefaults(p eventLogParams) eventLogParams {
	// R1：钳制逻辑统一走 clampMaxEvents，避免两处不同行为
	p.MaxEvents = clampMaxEvents(p.MaxEvents)
	if p.Level == "" {
		p.Level = defaultLevel
	}
	if p.Since == "" {
		p.Since = defaultSince
	}
	// IncludeMessage 用 *bool：nil → 默认 true
	if p.IncludeMessage == nil {
		t := true
		p.IncludeMessage = &t
	}
	return p
}

// parseSinceOrDefault 解析 since 字符串为 time.Duration，失败时返回默认 1h。
func parseSinceOrDefault(since string) time.Duration {
	if since == "" {
		return time.Hour
	}
	d, err := time.ParseDuration(since)
	if err != nil {
		return time.Hour
	}
	return d
}

// buildEventLogQuery 从校验后的参数构建 PSQuery。
// 安全保证：
//   - LogName：PSString → psQuote 转义后注入
//   - Level / MaxEvents / SinceSeconds：PSInt → 裸整数注入（Go int，类型系统保证）
//   - selectFields：PSRaw → const 派生字符串（非 LLM 输入），原样注入
func buildEventLogQuery(p eventLogParams) PSQuery {
	selectFields := "Id, ProviderName, " + levelDisplayExpr + ", " + timeCreatedExpr
	if p.IncludeMessage != nil && *p.IncludeMessage {
		selectFields += ", Message"
	}

	sinceSeconds := int(parseSinceOrDefault(p.Since).Seconds())
	maxEvents := clampMaxEvents(p.MaxEvents)
	lvl := levelToInt[p.Level]

	return PSQuery{
		Template: eventLogPipelineTemplate,
		Params: []PSParam{
			PSString(p.LogName),
			PSInt(lvl),
			PSInt(sinceSeconds),
			PSInt(maxEvents),
			PSRaw(selectFields),
		},
		Depth: 3,
	}
}

// clampMaxEvents 钳制 max_events 到合法范围。
// n < 1 → 默认值 100；n > 1000 → 上限 1000；否则原样返回。
func clampMaxEvents(n int) int {
	if n < 1 {
		return defaultMaxEvents
	}
	if n > maxMaxEvents {
		return maxMaxEvents
	}
	return n
}
