//go:build windows

package windows

import (
	"strings"
	"testing"
)

// TestEventLog_LevelInjection_MappedToInt 验证 level 参数的注入防护。
// 安全模型：level 经 levelToInt 映射为 int，通过 PSInt 注入命令。
// 即使注入 payload 绕过 enum 校验，levelToInt[payload] 返回 0（Go zero value），
// payload 字符串本身永远不会出现在渲染结果中。
func TestEventLog_LevelInjection_MappedToInt(t *testing.T) {
	for _, payload := range adversarialPayloads {
		p := eventLogParams{
			LogName:        "System",
			MaxEvents:      50,
			Level:          payload,
			Since:          "1h",
			IncludeMessage: boolPtr(false),
		}
		q := buildEventLogQuery(p)
		rendered := q.render()

		// level payload 字符串本身不应出现在渲染中（被 levelToInt 转为 int 0）
		if strings.Contains(rendered, payload) {
			t.Errorf("level payload %q 不应出现在渲染中（应被 levelToInt 转为 int）\n渲染: %s",
				payload, rendered)
		}

		// 渲染中应包含 Level=0（无效 payload 的 int 映射）
		if !strings.Contains(rendered, "Level=0") {
			t.Errorf("无效 level payload 应映射为 Level=0\n渲染: %s", rendered)
		}
	}
}

// TestEventLog_TemplateContainsFixedElements 验证渲染结果包含固定元素。
func TestEventLog_TemplateContainsFixedElements(t *testing.T) {
	p := eventLogParams{
		LogName:        "System",
		MaxEvents:      100,
		Level:          "Error",
		Since:          "1h",
		IncludeMessage: boolPtr(true),
	}
	q := buildEventLogQuery(p)
	rendered := q.render()

	// 渲染必须包含这些固定元素（来自 const 模板）
	mustContain := []string{
		"Get-WinEvent",
		"LogName=",
		"-MaxEvents",
		"-FilterHashtable",
		"LevelDisplayName",
		"StartTime",
		"[DateTime]::Now.Subtract",
		"[TimeSpan]::FromSeconds",
		"Select-Object",
	}
	for _, s := range mustContain {
		if !strings.Contains(rendered, s) {
			t.Errorf("渲染中缺少固定元素 %q\n渲染: %s", s, rendered)
		}
	}

	// ConvertTo-Json 外壳在 buildFullCmd 中，不在 render 中
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json") {
		t.Errorf("完整命令应包含 ConvertTo-Json\n完整命令: %s", full)
	}
	if !strings.Contains(full, "-Depth 3") {
		t.Errorf("完整命令应包含 -Depth 3\n完整命令: %s", full)
	}
	if !strings.Contains(full, "-Compress") {
		t.Errorf("完整命令应包含 -Compress\n完整命令: %s", full)
	}
}

// TestEventLog_IncludeMessage_TogglesSelectFields 验证
// IncludeMessage 控制是否包含 Message 字段。
func TestEventLog_IncludeMessage_TogglesSelectFields(t *testing.T) {
	p := eventLogParams{
		LogName:   "System",
		MaxEvents: 10,
		Level:     "Error",
		Since:     "1h",
	}

	// IncludeMessage=true → 渲染包含 Message
	p.IncludeMessage = boolPtr(true)
	qWith := buildEventLogQuery(p)
	if !strings.Contains(qWith.render(), "Message") {
		t.Errorf("IncludeMessage=true 时渲染应包含 Message 字段\n渲染: %s", qWith.render())
	}

	// IncludeMessage=false → 渲染不包含 Message
	p.IncludeMessage = boolPtr(false)
	qWithout := buildEventLogQuery(p)
	if strings.Contains(qWithout.render(), "Message") {
		t.Errorf("IncludeMessage=false 时渲染不应包含 Message 字段\n渲染: %s", qWithout.render())
	}
}

// TestEventLog_NumericParamsArePSInt 验证数字类参数
// 通过 PSInt 渲染为裸整数（不经过 psQuote，依赖 Go 类型系统安全）。
func TestEventLog_NumericParamsArePSInt(t *testing.T) {
	p := eventLogParams{
		LogName:        "Application",
		MaxEvents:      42,
		Level:          "Warning",
		Since:          "30m",
		IncludeMessage: boolPtr(true),
	}
	q := buildEventLogQuery(p)
	rendered := q.render()

	// MaxEvents=42 应直接出现在渲染中
	if !strings.Contains(rendered, "-MaxEvents 42") {
		t.Errorf("MaxEvents=42 未正确出现在渲染中\n渲染: %s", rendered)
	}

	// since=30m → 1800 秒应出现在渲染中
	if !strings.Contains(rendered, "FromSeconds(1800)") {
		t.Errorf("since=30m (1800s) 未正确出现在渲染中\n渲染: %s", rendered)
	}
}
