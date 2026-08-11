//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/skill"
)

// TestProcesses_Metadata_Valid 验证 Metadata 通过框架校验且字段正确。
func TestProcesses_Metadata_Valid(t *testing.T) {
	m := (Processes{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.Key != "host_windows_processes" {
		t.Fatalf("unexpected key %q, want %q", m.Key, "host_windows_processes")
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
	if m.Scope != skill.ScopeHost {
		t.Fatalf("want ScopeHost, got %v", m.Scope)
	}
}

// TestProcesses_Metadata_ParamsSchema 验证参数 schema 完整性。
func TestProcesses_Metadata_ParamsSchema(t *testing.T) {
	m := (Processes{}).Metadata()
	params := m.Params
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}

	// name: optional string
	if params[0].Name != "name" || params[0].Required || params[0].Type != "string" {
		t.Fatalf("name param incorrect: %+v", params[0])
	}

	// top_n: int with default 10
	if params[1].Name != "top_n" || params[1].Type != "int" {
		t.Fatalf("top_n param incorrect: %+v", params[1])
	}

	// min_memory_mb: int
	if params[2].Name != "min_memory_mb" || params[2].Type != "int" {
		t.Fatalf("min_memory_mb param incorrect: %+v", params[2])
	}
}

// TestProcesses_Execute_DefaultParams 验证默认参数正确应用。
// TopN 钳制由 skeleton 在 buildQuery 时统一处理（TopN=0 不在 Defaults 改）。
// MinMemoryMB=0 是合法值"不过滤"，Defaults 只处理负值。
func TestProcesses_Execute_DefaultParams(t *testing.T) {
	p := processesParams{}
	processesSpec.Defaults(&p)

	if p.Name != "" {
		t.Errorf("default Name = %q, want empty", p.Name)
	}
	if p.MinMemoryMB != 0 {
		t.Errorf("default MinMemoryMB = %d, want 0", p.MinMemoryMB)
	}
}

// TestProcesses_Execute_InvalidJSON_ReturnsError 验证非法 JSON 返回错误。
func TestProcesses_Execute_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := (Processes{}).Execute(context.Background(),
		json.RawMessage(`{"name": invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestProcesses_Execute_TopNClamp 验证 top_n 边界值钳制（由 skeleton.buildQuery 经 clampInt 处理）。
func TestProcesses_Execute_TopNClamp(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  int
	}{
		{"zero_clamps_to_default", 0, processesSpec.TopNDefault},
		{"negative_clamps_to_default", -1, processesSpec.TopNDefault},
		{"negative_large_clamps_to_default", -100, processesSpec.TopNDefault},
		{"one_passes", 1, 1},
		{"fifty_passes", 50, 50},
		{"max_boundary_passes", processesSpec.TopNMax, processesSpec.TopNMax},
		{"over_max_clamps_to_max", processesSpec.TopNMax + 1, processesSpec.TopNMax},
		{"way_over_clamps_to_max", 9999, processesSpec.TopNMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := processesParams{TopN: tc.input}
			q := processesSpec.buildQuery(p)
			topN, ok := q.Params[1].(PSInt)
			if !ok {
				t.Fatalf("q.Params[1] 应为 PSInt，实际 %T", q.Params[1])
			}
			if int(topN) != tc.want {
				t.Errorf("clampTopN(%d) = %d, want %d", tc.input, int(topN), tc.want)
			}
		})
	}
}

// TestProcesses_Execute_MinMemoryMBClamp 验证 min_memory_mb 负值钳制为 0（在 processesSpec.Defaults 内处理）。
func TestProcesses_Execute_MinMemoryMBClamp(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  int
	}{
		{"zero_stays_zero", 0, 0},
		{"positive_passes", 100, 100},
		{"negative_clamps_to_zero", -1, 0},
		{"negative_large_clamps_to_zero", -9999, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := processesParams{MinMemoryMB: tc.input}
			processesSpec.Defaults(&p)
			if p.MinMemoryMB != tc.want {
				t.Errorf("clampMinMemoryMB(%d) = %d, want %d", tc.input, p.MinMemoryMB, tc.want)
			}
		})
	}
}

// TestProcesses_Command_FilterConditions 验证不同参数组合生成正确的过滤条件。
func TestProcesses_Command_FilterConditions(t *testing.T) {
	cases := []struct {
		name       string
		params     processesParams
		conditions []string // 命令中应包含的条件片段
		exclusions []string // 命令中不应包含的片段
	}{
		{
			name:       "no_filters",
			params:     processesParams{Name: "", TopN: 10, MinMemoryMB: 0},
			conditions: []string{"$true"},
			exclusions: []string{"$_.Name -eq", "$_.WorkingSet64 / 1MB -gt"},
		},
		{
			name:       "name_only_filter",
			params:     processesParams{Name: "chrome", TopN: 10, MinMemoryMB: 0},
			conditions: []string{"$_.Name -eq 'chrome'"},
			exclusions: []string{"$_.WorkingSet64 / 1MB -gt"},
		},
		{
			name:       "min_memory_only_filter",
			params:     processesParams{Name: "", TopN: 10, MinMemoryMB: 100},
			conditions: []string{"$_.WorkingSet64 / 1MB -gt 100"},
			exclusions: []string{"$_.Name -eq"},
		},
		{
			name:       "both_filters",
			params:     processesParams{Name: "chrome", TopN: 10, MinMemoryMB: 100},
			conditions: []string{"$_.Name -eq 'chrome'", "$_.WorkingSet64 / 1MB -gt 100", "-and"},
			exclusions: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := processesSpec.buildQuery(tc.params)
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

// TestProcesses_Command_TemplateIsConst 验证命令模板是 const string
// （通过检查渲染中包含固定的 PowerShell cmdlet 名称和输出字段）。
func TestProcesses_Command_TemplateIsConst(t *testing.T) {
	p := processesParams{Name: "chrome", TopN: 10, MinMemoryMB: 100}
	q := processesSpec.buildQuery(p)
	rendered := q.render()

	mustContain := []string{
		"Get-Process",
		"Where-Object",
		"Sort-Object",
		"CPU -Descending",
		"Select-Object",
		"-First",
		// computed property 输出字段（snake_case）
		"name",
		"id",
		"cpu_seconds",
		"working_set_mb",
		"path",
	}
	for _, s := range mustContain {
		if !strings.Contains(rendered, s) {
			t.Errorf("渲染中缺少固定元素 %q\n渲染: %s", s, rendered)
		}
	}

	// ConvertTo-Json 外壳在 buildFullCmd 中
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json") {
		t.Errorf("完整命令应包含 ConvertTo-Json\n完整命令: %s", full)
	}
	if !strings.Contains(full, "-Depth 4") {
		t.Errorf("完整命令应包含 -Depth 4\n完整命令: %s", full)
	}
	if !strings.Contains(full, "-Compress") {
		t.Errorf("完整命令应包含 -Compress\n完整命令: %s", full)
	}
}

// TestProcesses_Command_EmptyResultArrayWrapper 验证 PSQuery.Run 统一提供
// ConvertTo-Json -InputObject @() 外壳，确保空查询结果返回 JSON "[]" 而非 "null"。
func TestProcesses_Command_EmptyResultArrayWrapper(t *testing.T) {
	p := processesParams{Name: "NonExistentProcess", TopN: 10, MinMemoryMB: 0}
	q := processesSpec.buildQuery(p)
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json -InputObject @(") {
		t.Errorf("完整命令应包含 ConvertTo-Json -InputObject @() 外壳\n完整命令: %s", full)
	}
}

// TestProcesses_Command_TopNFormattedAsInteger 验证 top_n 通过 PSInt 渲染为
// 裸整数而非引号字符串（Go 类型系统安全保证）。
func TestProcesses_Command_TopNFormattedAsInteger(t *testing.T) {
	cases := []int{1, 10, 50, 100}
	for _, topN := range cases {
		p := processesParams{TopN: topN}
		q := processesSpec.buildQuery(p)
		rendered := q.render()
		expected := "Select-Object -First " + strconv.Itoa(topN)
		if !strings.Contains(rendered, expected) {
			t.Errorf("top_n=%d: 渲染应包含 %q\n渲染: %s", topN, expected, rendered)
		}
	}
}

// TestProcesses_Command_OutputFieldsSnakeCase 验证输出字段为 snake_case computed properties。
func TestProcesses_Command_OutputFieldsSnakeCase(t *testing.T) {
	p := processesParams{TopN: 10}
	q := processesSpec.buildQuery(p)
	rendered := q.render()

	fields := []string{
		"Name='name'",
		"Name='id'",
		"Name='cpu_seconds'",
		"Name='working_set_mb'",
		"Name='path'",
	}
	for _, f := range fields {
		if !strings.Contains(rendered, f) {
			t.Errorf("渲染中缺少 computed property %q\n渲染: %s", f, rendered)
		}
	}
}
