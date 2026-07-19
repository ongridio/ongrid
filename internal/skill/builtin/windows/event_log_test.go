//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

// boolPtr 返回指向 b 的指针，用于 *bool 类型字段赋值。
func boolPtr(b bool) *bool { return &b }

// TestEventLog_Metadata_Valid 验证 Metadata 通过框架校验且字段正确。
func TestEventLog_Metadata_Valid(t *testing.T) {
	m := (EventLog{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.Key != "host_windows_event_log" {
		t.Fatalf("unexpected key %q, want %q", m.Key, "host_windows_event_log")
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
	if m.Scope != skill.ScopeHost {
		t.Fatalf("want ScopeHost, got %v", m.Scope)
	}
}

// TestEventLog_Metadata_ParamsSchema 验证参数 schema 完整性。
func TestEventLog_Metadata_ParamsSchema(t *testing.T) {
	m := (EventLog{}).Metadata()
	params := m.Params
	if len(params) != 5 {
		t.Fatalf("expected 5 params, got %d", len(params))
	}

	// log_name: required enum
	if params[0].Name != "log_name" || !params[0].Required || params[0].Type != "enum" {
		t.Fatalf("log_name param incorrect: %+v", params[0])
	}
	if len(params[0].Enum) != 5 {
		t.Fatalf("log_name enum should have 5 values, got %d", len(params[0].Enum))
	}

	// max_events: int with default 100
	if params[1].Name != "max_events" || params[1].Type != "int" {
		t.Fatalf("max_events param incorrect: %+v", params[1])
	}

	// level: enum with default "Error"
	if params[2].Name != "level" || params[2].Type != "enum" {
		t.Fatalf("level param incorrect: %+v", params[2])
	}
	if len(params[2].Enum) != 5 {
		t.Fatalf("level enum should have 5 values, got %d", len(params[2].Enum))
	}

	// since: duration
	if params[3].Name != "since" || params[3].Type != "duration" {
		t.Fatalf("since param incorrect: %+v", params[3])
	}

	// include_message: bool default true
	if params[4].Name != "include_message" || params[4].Type != "bool" {
		t.Fatalf("include_message param incorrect: %+v", params[4])
	}
}

// TestEventLog_Execute_InvalidLogName_ReturnsError 验证非法 log_name 被拒绝。
func TestEventLog_Execute_InvalidLogName_ReturnsError(t *testing.T) {
	cases := []string{
		"",                  // 空字符串
		"InvalidLog",        // 不在枚举中
		"system",            // 大小写不匹配
		"Application'; rm",  // 注入 payload
		"System\x00null",      // null byte
	}
	for _, logName := range cases {
		params, _ := json.Marshal(map[string]any{
			"log_name":   logName,
			"max_events":  10,
			"level":       "Error",
		})
		_, err := (EventLog{}).Execute(context.Background(), params)
		if err == nil {
			t.Errorf("expected error for log_name %q, got nil", logName)
		}
	}
}

// TestEventLog_Execute_InvalidLevel_ReturnsError 验证非法 level 被拒绝。
// 注：空字符串 "" 不在此列 — applyDefaults 将 "" 替换为 defaultLevel("Error")，是合法行为。
func TestEventLog_Execute_InvalidLevel_ReturnsError(t *testing.T) {
	cases := []string{
		"debug",        // 不在枚举中
		"error",        // 大小写不匹配（应为 Error）
		"Error'; rm",   // 注入 payload
	}
	for _, level := range cases {
		params, _ := json.Marshal(map[string]any{
			"log_name":   "System",
			"max_events":  10,
			"level":       level,
		})
		_, err := (EventLog{}).Execute(context.Background(), params)
		if err == nil {
			t.Errorf("expected error for level %q, got nil", level)
		}
	}
}

// TestEventLog_Execute_MaxEventsBoundary 验证 max_events 边界值被正确钳制。
func TestEventLog_Execute_MaxEventsBoundary(t *testing.T) {
	cases := []struct {
		name       string
		maxEvents  int
		expectClamp int
	}{
		{"zero_clamps_to_default", 0, 100},
		{"negative_clamps_to_default", -1, 100},
		{"one_passes", 1, 1},
		{"max_boundary_passes", 1000, 1000},
		{"over_max_clamps_to_max", 1001, 1000},
		{"huge_number_clamps", 999999, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 通过 buildEventLogQuery 验证钳制后的值出现在渲染中
			p := eventLogParams{
				LogName:   "System",
				MaxEvents: tc.maxEvents,
				Level:     "Error",
				Since:     "1h",
				IncludeMessage: boolPtr(true),
			}
			q := buildEventLogQuery(p)
			rendered := q.render()
			// 渲染中应包含 -MaxEvents <expectClamp>
			needle := "-MaxEvents " + strconv.Itoa(tc.expectClamp)
			if !strings.Contains(rendered, needle) {
				t.Errorf("max_events %d → 渲染中期望包含 %q, 渲染: %s",
					tc.maxEvents, needle, rendered)
			}
		})
	}
}

// TestEventLog_Execute_DefaultParams 验证默认参数正确应用。
func TestEventLog_Execute_DefaultParams(t *testing.T) {
	// 空参数 → 应用所有默认值
	p := eventLogParams{}
	p = applyDefaults(p)

	if p.MaxEvents != 100 {
		t.Errorf("default MaxEvents = %d, want 100", p.MaxEvents)
	}
	if p.Level != "Error" {
		t.Errorf("default Level = %q, want %q", p.Level, "Error")
	}
	if p.Since != "1h" {
		t.Errorf("default Since = %q, want %q", p.Since, "1h")
	}
	if p.IncludeMessage == nil || !*p.IncludeMessage {
		t.Error("default IncludeMessage should be true")
	}
}

// TestEventLog_Execute_InvalidSince_UsesDefault 验证非法 since 字符串回退到默认值。
func TestEventLog_Execute_InvalidSince_UsesDefault(t *testing.T) {
	cases := []string{
		"",           // 空字符串
		"invalid",    // 非法格式
		"abc123",     // 非法格式
	}
	for _, since := range cases {
		d := parseSinceOrDefault(since)
		if d != time.Hour {
			t.Errorf("parseSinceOrDefault(%q) = %v, want %v", since, d, time.Hour)
		}
	}
}

// TestEventLog_Execute_ValidSince 验证合法 since 字符串被正确解析。
func TestEventLog_Execute_ValidSince(t *testing.T) {
	cases := map[string]time.Duration{
		"30m":  30 * time.Minute,
		"1h":   time.Hour,
		"2h":   2 * time.Hour,
		"3600s": time.Hour,
	}
	for since, want := range cases {
		got := parseSinceOrDefault(since)
		if got != want {
			t.Errorf("parseSinceOrDefault(%q) = %v, want %v", since, got, want)
		}
	}
}

// TestEventLog_Execute_InvalidJSON_ReturnsError 验证非法 JSON 返回错误。
func TestEventLog_Execute_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := (EventLog{}).Execute(context.Background(),
		json.RawMessage(`{"log_name": invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestEventLog_CommandTemplate_HasNoEventsFallback 验证命令模板含 try/catch
// 包裹 Get-WinEvent，确保无匹配事件时返回 [] 而非 exit 1。
func TestEventLog_CommandTemplate_HasNoEventsFallback(t *testing.T) {
	p := eventLogParams{
		LogName:   "Application",
		MaxEvents: 5,
		Level:     "Error",
		Since:     "1h",
	}
	q := buildEventLogQuery(p)
	rendered := q.render()
	// 必须含 try { ... } catch { @() }
	if !strings.Contains(rendered, "try {") || !strings.Contains(rendered, "} catch { @() }") {
		t.Errorf("渲染缺少 try/catch 无事件兜底: %s", rendered)
	}
	// PSQuery.Run 统一提供 ConvertTo-Json -InputObject @() 外壳
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json -InputObject @(") {
		t.Errorf("完整命令缺少 ConvertTo-Json -InputObject @(): %s", full)
	}
}
