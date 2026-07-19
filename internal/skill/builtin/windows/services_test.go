//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/skill"
)

// TestServices_Metadata_Valid 验证 Metadata 通过框架校验且字段正确。
func TestServices_Metadata_Valid(t *testing.T) {
	m := (Services{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.Key != "host_windows_services" {
		t.Fatalf("unexpected key %q, want %q", m.Key, "host_windows_services")
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
	if m.Scope != skill.ScopeHost {
		t.Fatalf("want ScopeHost, got %v", m.Scope)
	}
}

// TestServices_Metadata_ParamsSchema 验证参数 schema 完整性。
func TestServices_Metadata_ParamsSchema(t *testing.T) {
	m := (Services{}).Metadata()
	params := m.Params
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}

	// name: optional string
	if params[0].Name != "name" || params[0].Required || params[0].Type != "string" {
		t.Fatalf("name param incorrect: %+v", params[0])
	}

	// status: enum with default "all"
	if params[1].Name != "status" || params[1].Type != "enum" {
		t.Fatalf("status param incorrect: %+v", params[1])
	}
	if len(params[1].Enum) != 3 {
		t.Fatalf("status enum should have 3 values, got %d", len(params[1].Enum))
	}

	// start_type: enum with default "all"
	if params[2].Name != "start_type" || params[2].Type != "enum" {
		t.Fatalf("start_type param incorrect: %+v", params[2])
	}
	if len(params[2].Enum) != 4 {
		t.Fatalf("start_type enum should have 4 values, got %d", len(params[2].Enum))
	}
}

// TestServices_Execute_InvalidStatus_ReturnsError 验证非法 status 被拒绝。
// 注：空字符串 "" 被 applyDefaults 替换为 "all"，是合法行为。
func TestServices_Execute_InvalidStatus_ReturnsError(t *testing.T) {
	cases := []string{
		"Running",   // 大小写不匹配（应为全小写 running）
		"paused",    // 不在枚举中
		"all'; rm",  // 注入 payload
		"RUNNING",   // 大小写不匹配
	}
	for _, status := range cases {
		params, _ := json.Marshal(map[string]any{
			"status": status,
		})
		_, err := (Services{}).Execute(context.Background(), params)
		if err == nil {
			t.Errorf("expected error for status %q, got nil", status)
		}
	}
}

// TestServices_Execute_InvalidStartType_ReturnsError 验证非法 start_type 被拒绝。
func TestServices_Execute_InvalidStartType_ReturnsError(t *testing.T) {
	cases := []string{
		"automatic",  // 大小写不匹配（应为 auto）
		"boot",       // 不在枚举中
		"all'; rm",   // 注入 payload
		"Auto",       // 大小写不匹配
	}
	for _, startType := range cases {
		params, _ := json.Marshal(map[string]any{
			"start_type": startType,
		})
		_, err := (Services{}).Execute(context.Background(), params)
		if err == nil {
			t.Errorf("expected error for start_type %q, got nil", startType)
		}
	}
}

// TestServices_Execute_DefaultParams 验证默认参数正确应用。
func TestServices_Execute_DefaultParams(t *testing.T) {
	p := servicesParams{}
	servicesSpec.Defaults(&p)

	if p.Name != "" {
		t.Errorf("default Name = %q, want empty", p.Name)
	}
	if p.Status != "all" {
		t.Errorf("default Status = %q, want %q", p.Status, "all")
	}
	if p.StartType != "all" {
		t.Errorf("default StartType = %q, want %q", p.StartType, "all")
	}
}

// TestServices_Execute_AllEnumValuesPassValidation 验证 "all" 元选项通过 enum 校验。
// Regression：CRITICAL bug — servicesStatusValid["all"] 派生不含 "all"，
// 若不在 ValidateEnums 特判会导致默认参数执行失败。
func TestServices_Execute_AllEnumValuesPassValidation(t *testing.T) {
	cases := []servicesParams{
		{Status: "all", StartType: "all"},
		{Status: "running", StartType: "all"},
		{Status: "stopped", StartType: "all"},
		{Status: "all", StartType: "auto"},
		{Status: "all", StartType: "manual"},
		{Status: "all", StartType: "disabled"},
	}
	for _, p := range cases {
		if err := servicesSpec.ValidateEnums(p); err != nil {
			t.Errorf("ValidateEnums(%+v) 不应报错，实际: %v", p, err)
		}
	}
}

// TestServices_Execute_InvalidJSON_ReturnsError 验证非法 JSON 返回错误。
func TestServices_Execute_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := (Services{}).Execute(context.Background(),
		json.RawMessage(`{"name": invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestServices_Command_FilterConditions 验证不同参数组合生成正确的过滤条件。
func TestServices_Command_FilterConditions(t *testing.T) {
	cases := []struct {
		name       string
		params     servicesParams
		conditions []string // 命令中应包含的条件片段
		exclusions []string // 命令中不应包含的片段
	}{
		{
			name:       "all_defaults_no_filter",
			params:     servicesParams{Name: "", Status: "all", StartType: "all"},
			conditions: []string{"$true"},
			exclusions: []string{"$_.Name -eq", "$_.Status -eq", "$_.StartType -eq"},
		},
		{
			name:       "name_only_filter",
			params:     servicesParams{Name: "Spooler", Status: "all", StartType: "all"},
			conditions: []string{"$_.Name -eq 'Spooler'"},
			exclusions: []string{"$_.Status -eq", "$_.StartType -eq"},
		},
		{
			name:       "status_running_filter",
			params:     servicesParams{Name: "", Status: "running", StartType: "all"},
			conditions: []string{"$_.Status -eq 'Running'"},
			exclusions: []string{"$_.Name -eq", "$_.StartType -eq"},
		},
		{
			name:       "status_stopped_filter",
			params:     servicesParams{Name: "", Status: "stopped", StartType: "all"},
			conditions: []string{"$_.Status -eq 'Stopped'"},
			exclusions: []string{"$_.Name -eq", "$_.StartType -eq"},
		},
		{
			name:       "start_type_auto_filter",
			params:     servicesParams{Name: "", Status: "all", StartType: "auto"},
			conditions: []string{"$_.StartType -eq 'Automatic'"},
			exclusions: []string{"$_.Name -eq", "$_.Status -eq"},
		},
		{
			name:       "start_type_disabled_filter",
			params:     servicesParams{Name: "", Status: "all", StartType: "disabled"},
			conditions: []string{"$_.StartType -eq 'Disabled'"},
			exclusions: []string{"$_.Name -eq", "$_.Status -eq"},
		},
		{
			name:       "all_three_filters",
			params:     servicesParams{Name: "Spooler", Status: "running", StartType: "auto"},
			conditions: []string{"$_.Name -eq 'Spooler'", "$_.Status -eq 'Running'", "$_.StartType -eq 'Automatic'", "-and"},
			exclusions: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := servicesSpec.buildQuery(tc.params)
			rendered := q.render()
			for _, cond := range tc.conditions {
				if !strings.Contains(rendered, cond) {
					t.Errorf("expected rendered to contain %q\n渲染: %s", cond, rendered)
				}
			}
			for _, excl := range tc.exclusions {
				if strings.Contains(rendered, excl) {
					t.Errorf("rendered should not contain %q\n渲染: %s", excl, rendered)
				}
			}
		})
	}
}

// TestServices_Command_TemplateIsConst 验证渲染结果包含固定元素。
func TestServices_Command_TemplateIsConst(t *testing.T) {
	p := servicesParams{Name: "Spooler", Status: "running", StartType: "auto"}
	q := servicesSpec.buildQuery(p)
	rendered := q.render()

	mustContain := []string{
		"Get-Service",
		"Where-Object",
		"Select-Object",
		"name",         // computed property name
		"display_name",
		"status",
		"start_type",
		"dependencies",
	}
	for _, s := range mustContain {
		if !strings.Contains(rendered, s) {
			t.Errorf("渲染中缺少固定元素 %q\n渲染: %s", s, rendered)
		}
	}
	// 依赖字段必须用 ForEach-Object 管道确保空集合序列化为 []
	if !strings.Contains(rendered, "@($_.ServicesDependedOn | ForEach-Object { $_.Name })") {
		t.Errorf("dependencies 表达式应为 ForEach-Object 管道形式\n渲染: %s", rendered)
	}

	// ConvertTo-Json 外壳 + dependencies regex 由 PSQuery.Run 统一处理
	// ArrayKeys 必须包含 dependencies
	if len(q.ArrayKeys) != 1 || q.ArrayKeys[0] != "dependencies" {
		t.Errorf("PSQuery ArrayKeys 应为 [dependencies], 实际: %v", q.ArrayKeys)
	}
}

// TestServices_Command_EmptyResultArrayWrapper 验证 PSQuery.Run 统一提供
// ConvertTo-Json -InputObject @() 外壳，确保空查询结果返回 JSON "[]" 而非 "null"。
func TestServices_Command_EmptyResultArrayWrapper(t *testing.T) {
	p := servicesParams{Name: "NonExistentService", Status: "all", StartType: "all"}
	q := servicesSpec.buildQuery(p)
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json -InputObject @(") {
		t.Errorf("完整命令应包含 ConvertTo-Json -InputObject @() 外壳\n完整命令: %s", full)
	}
}
