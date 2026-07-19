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

// TestHotfix_Metadata_Valid 验证 Metadata 通过框架校验且字段正确。
func TestHotfix_Metadata_Valid(t *testing.T) {
	m := (Hotfix{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.Key != "host_windows_hotfix" {
		t.Fatalf("unexpected key %q, want %q", m.Key, "host_windows_hotfix")
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
	if m.Scope != skill.ScopeHost {
		t.Fatalf("want ScopeHost, got %v", m.Scope)
	}
}

// TestHotfix_Metadata_ParamsSchema 验证参数 schema 完整性。
func TestHotfix_Metadata_ParamsSchema(t *testing.T) {
	m := (Hotfix{}).Metadata()
	params := m.Params
	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(params))
	}

	// top_n: int, default 20
	if params[0].Name != "top_n" || params[0].Type != "int" {
		t.Fatalf("top_n param incorrect: %+v", params[0])
	}

	// since_days: int, optional
	if params[1].Name != "since_days" || params[1].Type != "int" {
		t.Fatalf("since_days param incorrect: %+v", params[1])
	}
}

// TestHotfix_Execute_DefaultParams 验证默认参数正确应用。
// TopN 钳制由 skeleton 在 buildQuery 时统一处理（TopN=0 不在 Defaults 改）。
// SinceDays=0 是合法值"不过滤"，Defaults 只处理负值和超上限。
func TestHotfix_Execute_DefaultParams(t *testing.T) {
	p := hotfixParams{}
	hotfixSpec.Defaults(&p)

	if p.SinceDays != 0 {
		t.Errorf("default SinceDays = %d, want 0（不过滤）", p.SinceDays)
	}
}

// TestHotfix_Execute_InvalidJSON_ReturnsError 验证非法 JSON 返回错误。
func TestHotfix_Execute_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := (Hotfix{}).Execute(context.Background(),
		json.RawMessage(`{"top_n": invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestHotfix_Execute_TopNClamp 验证 top_n 边界值钳制（由 skeleton.buildQuery 经 clampInt 处理）。
func TestHotfix_Execute_TopNClamp(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  int
	}{
		{"zero_clamps_to_default", 0, hotfixSpec.TopNDefault},
		{"negative_clamps_to_default", -1, hotfixSpec.TopNDefault},
		{"one_passes", 1, 1},
		{"twenty_passes", 20, 20},
		{"max_boundary_passes", hotfixSpec.TopNMax, hotfixSpec.TopNMax},
		{"over_max_clamps_to_max", hotfixSpec.TopNMax + 1, hotfixSpec.TopNMax},
		{"way_over_clamps_to_max", 99999, hotfixSpec.TopNMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := hotfixParams{TopN: tc.input}
			q := hotfixSpec.buildQuery(p)
			topN, ok := q.Params[1].(PSInt)
			if !ok {
				t.Fatalf("q.Params[1] 应为 PSInt，实际 %T", q.Params[1])
			}
			if int(topN) != tc.want {
				t.Errorf("TopN(%d) = %d, want %d", tc.input, int(topN), tc.want)
			}
		})
	}
}

// TestHotfix_Execute_SinceDaysClamp 验证 since_days 边界值钳制（在 hotfixSpec.Defaults 内处理）。
func TestHotfix_Execute_SinceDaysClamp(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  int
	}{
		{"zero_passes_no_filter", 0, 0},
		{"negative_clamps_to_zero", -1, 0},
		{"one_passes", 1, 1},
		{"thirty_passes", 30, 30},
		{"max_boundary_passes", hotfixSinceDaysMax, hotfixSinceDaysMax},
		{"over_max_clamps_to_max", hotfixSinceDaysMax + 1, hotfixSinceDaysMax},
		{"way_over_clamps_to_max", 99999, hotfixSinceDaysMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := hotfixParams{SinceDays: tc.input}
			hotfixSpec.Defaults(&p)
			if p.SinceDays != tc.want {
				t.Errorf("SinceDays(%d) = %d, want %d", tc.input, p.SinceDays, tc.want)
			}
		})
	}
}

// TestHotfix_Command_FilterConditions 验证 since_days 参数生成正确的过滤条件。
func TestHotfix_Command_FilterConditions(t *testing.T) {
	cases := []struct {
		name       string
		params     hotfixParams
		conditions []string // 命令中应包含的条件片段
		exclusions []string // 命令中不应包含的片段
	}{
		{
			name:       "no_since_days_filter",
			params:     hotfixParams{TopN: 20},
			conditions: []string{"$true"},
			exclusions: []string{"InstalledOn -gt", "AddDays"},
		},
		{
			name:       "since_days_30_filter",
			params:     hotfixParams{TopN: 20, SinceDays: 30},
			conditions: []string{"InstalledOn -gt", "AddDays(-30)"},
			exclusions: []string{},
		},
		{
			name:       "since_days_7_filter",
			params:     hotfixParams{TopN: 20, SinceDays: 7},
			conditions: []string{"AddDays(-7)"},
			exclusions: []string{"AddDays(-30)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hotfixSpec.Defaults(&tc.params)
			q := hotfixSpec.buildQuery(tc.params)
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

// TestHotfix_Command_TemplateIsConst 验证命令模板是 const string
// （通过检查渲染中包含固定的 PowerShell cmdlet 名称和输出字段）。
func TestHotfix_Command_TemplateIsConst(t *testing.T) {
	p := hotfixParams{TopN: 20}
	q := hotfixSpec.buildQuery(p)
	rendered := q.render()

	mustContain := []string{
		"Get-HotFix",
		"Where-Object",
		"Sort-Object",
		"InstalledOn",
		"-Descending",
		"Select-Object",
		"-First",
		// computed property 输出字段（snake_case）
		"hotfix_id",
		"description",
		"installed_on",
		"source",
		"installed_by",
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

// TestHotfix_Command_EmptyResultArrayWrapper 验证 PSQuery.Run 统一提供
// ConvertTo-Json -InputObject @() 外壳。
func TestHotfix_Command_EmptyResultArrayWrapper(t *testing.T) {
	p := hotfixParams{SinceDays: 99999}
	hotfixSpec.Defaults(&p)
	q := hotfixSpec.buildQuery(p)
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json -InputObject @(") {
		t.Errorf("完整命令应包含 ConvertTo-Json -InputObject @() 外壳\n完整命令: %s", full)
	}
}

// TestHotfix_Command_TopNFormattedAsInteger 验证 top_n 通过 PSInt 渲染为
// 裸整数而非引号字符串（Go 类型系统安全保证）。
func TestHotfix_Command_TopNFormattedAsInteger(t *testing.T) {
	cases := []int{1, 10, 20, 50, 200}
	for _, topN := range cases {
		p := hotfixParams{TopN: topN}
		q := hotfixSpec.buildQuery(p)
		rendered := q.render()
		expected := "Select-Object -First " + strconv.Itoa(topN)
		if !strings.Contains(rendered, expected) {
			t.Errorf("top_n=%d: 渲染应包含 %q\n渲染: %s", topN, expected, rendered)
		}
	}
}

// TestHotfix_Command_OutputFieldsSnakeCase 验证输出字段为 snake_case computed properties。
func TestHotfix_Command_OutputFieldsSnakeCase(t *testing.T) {
	p := hotfixParams{TopN: 20}
	q := hotfixSpec.buildQuery(p)
	rendered := q.render()

	fields := []string{
		"Name='hotfix_id'",
		"Name='description'",
		"Name='installed_on'",
		"Name='source'",
		"Name='installed_by'",
	}
	for _, f := range fields {
		if !strings.Contains(rendered, f) {
			t.Errorf("渲染中缺少 computed property %q\n渲染: %s", f, rendered)
		}
	}
}

// TestHotfix_Command_InstalledOnToStringISO8601 验证 InstalledOn 经 .ToString('o')
// 转为 ISO 8601 字符串（避免 ConvertTo-Json 序列化 DateTime 问题）。
func TestHotfix_Command_InstalledOnToStringISO8601(t *testing.T) {
	p := hotfixParams{TopN: 20}
	q := hotfixSpec.buildQuery(p)
	rendered := q.render()

	if !strings.Contains(rendered, ".ToString('o')") {
		t.Errorf("渲染中缺少 .ToString('o')（ISO 8601 序列化保证）\n渲染: %s", rendered)
	}
}

// TestHotfix_Command_InstalledOnNullGuard 验证 InstalledOn 有 null 保护
// （部分 hotfix 的 InstalledOn 为 null，直接 .ToString() 会抛错）。
func TestHotfix_Command_InstalledOnNullGuard(t *testing.T) {
	p := hotfixParams{TopN: 20}
	q := hotfixSpec.buildQuery(p)
	rendered := q.render()

	// 必须有 null 检查（$null -ne $_.InstalledOn 或类似模式）
	if !strings.Contains(rendered, "$null") {
		t.Errorf("渲染中缺少 InstalledOn null 保护（ErrorActionPreference=Stop 下 null.ToString() 会致命错误）\n渲染: %s", rendered)
	}
}
