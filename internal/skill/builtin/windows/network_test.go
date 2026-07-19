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

// TestNetwork_Metadata_Valid 验证 Metadata 通过框架校验且字段正确。
func TestNetwork_Metadata_Valid(t *testing.T) {
	m := (Network{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.Key != "host_windows_network" {
		t.Fatalf("unexpected key %q, want %q", m.Key, "host_windows_network")
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
	if m.Scope != skill.ScopeHost {
		t.Fatalf("want ScopeHost, got %v", m.Scope)
	}
}

// TestNetwork_Metadata_ParamsSchema 验证参数 schema 完整性。
func TestNetwork_Metadata_ParamsSchema(t *testing.T) {
	m := (Network{}).Metadata()
	params := m.Params
	if len(params) != 5 {
		t.Fatalf("expected 5 params, got %d", len(params))
	}

	// state: enum with default "all"
	if params[0].Name != "state" || params[0].Type != "enum" {
		t.Fatalf("state param incorrect: %+v", params[0])
	}

	// local_port: int
	if params[1].Name != "local_port" || params[1].Type != "int" {
		t.Fatalf("local_port param incorrect: %+v", params[1])
	}

	// remote_address: string
	if params[2].Name != "remote_address" || params[2].Type != "string" {
		t.Fatalf("remote_address param incorrect: %+v", params[2])
	}

	// remote_port: int
	if params[3].Name != "remote_port" || params[3].Type != "int" {
		t.Fatalf("remote_port param incorrect: %+v", params[3])
	}

	// top_n: int
	if params[4].Name != "top_n" || params[4].Type != "int" {
		t.Fatalf("top_n param incorrect: %+v", params[4])
	}
}

// TestNetwork_Execute_DefaultParams 验证默认参数正确应用。
// TopN 钳制由 skeleton 在 buildQuery 时统一处理，Defaults 只负责 State 默认值。
func TestNetwork_Execute_DefaultParams(t *testing.T) {
	p := networkParams{}
	networkSpec.Defaults(&p)

	if p.State != "all" {
		t.Errorf("default State = %q, want %q", p.State, "all")
	}
}

// TestNetwork_Execute_AllEnumValuesPassValidation 验证 "all" 元选项通过 enum 校验。
// Regression：CRITICAL bug — networkStateValid["all"] 派生不含 "all"，
// 若不在 ValidateEnums 特判会导致默认参数执行失败。
func TestNetwork_Execute_AllEnumValuesPassValidation(t *testing.T) {
	cases := []string{"all", "closed", "listen", "established", "time_wait"}
	for _, state := range cases {
		p := networkParams{State: state}
		if err := networkSpec.ValidateEnums(p); err != nil {
			t.Errorf("ValidateEnums(state=%q) 不应报错，实际: %v", state, err)
		}
	}
}

// TestNetwork_Execute_InvalidJSON_ReturnsError 验证非法 JSON 返回错误。
func TestNetwork_Execute_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := (Network{}).Execute(context.Background(),
		json.RawMessage(`{"state": invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestNetwork_Execute_InvalidState_ReturnsError 验证非法 state 返回错误。
func TestNetwork_Execute_InvalidState_ReturnsError(t *testing.T) {
	_, err := (Network{}).Execute(context.Background(),
		json.RawMessage(`{"state": "MALICIOUS"}`))
	if err == nil {
		t.Fatal("expected error for invalid state")
	}
}

// TestNetwork_Execute_TopNClamp 验证 top_n 边界值钳制（由 skeleton.buildQuery 经 clampInt 处理）。
func TestNetwork_Execute_TopNClamp(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  int
	}{
		{"zero_clamps_to_default", 0, networkSpec.TopNDefault},
		{"negative_clamps_to_default", -1, networkSpec.TopNDefault},
		{"one_passes", 1, 1},
		{"fifty_passes", 50, 50},
		{"max_boundary_passes", networkSpec.TopNMax, networkSpec.TopNMax},
		{"over_max_clamps_to_max", networkSpec.TopNMax + 1, networkSpec.TopNMax},
		{"way_over_clamps_to_max", 99999, networkSpec.TopNMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := networkParams{TopN: tc.input}
			q := networkSpec.buildQuery(p)
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

// TestNetwork_Command_FilterConditions 验证不同参数组合生成正确的过滤条件。
func TestNetwork_Command_FilterConditions(t *testing.T) {
	cases := []struct {
		name       string
		params     networkParams
		conditions []string // 命令中应包含的条件片段
		exclusions []string // 命令中不应包含的片段
	}{
		{
			name:       "no_filters",
			params:     networkParams{TopN: 50},
			conditions: []string{"$true"},
			exclusions: []string{"$_.State -eq", "$_.LocalPort -eq", "$_.RemoteAddress -eq", "$_.RemotePort -eq"},
		},
		{
			name:       "state_only_filter",
			params:     networkParams{State: "listen", TopN: 50},
			conditions: []string{"$_.State -eq 'Listen'"},
			exclusions: []string{"$_.LocalPort -eq", "$_.RemoteAddress -eq"},
		},
		{
			name:       "local_port_only_filter",
			params:     networkParams{LocalPort: 8080, TopN: 50},
			conditions: []string{"$_.LocalPort -eq 8080"},
			exclusions: []string{"$_.State -eq", "$_.RemoteAddress -eq"},
		},
		{
			name:       "remote_address_only_filter",
			params:     networkParams{RemoteAddress: "192.168.1.1", TopN: 50},
			conditions: []string{"$_.RemoteAddress -eq '192.168.1.1'"},
			exclusions: []string{"$_.State -eq", "$_.LocalPort -eq"},
		},
		{
			name:       "remote_port_only_filter",
			params:     networkParams{RemotePort: 443, TopN: 50},
			conditions: []string{"$_.RemotePort -eq 443"},
			exclusions: []string{"$_.State -eq", "$_.LocalPort -eq"},
		},
		{
			name:   "all_filters",
			params: networkParams{State: "established", LocalPort: 80, RemoteAddress: "10.0.0.1", RemotePort: 443, TopN: 50},
			conditions: []string{
				"$_.State -eq 'Established'",
				"$_.LocalPort -eq 80",
				"$_.RemoteAddress -eq '10.0.0.1'",
				"$_.RemotePort -eq 443",
				"-and",
			},
			exclusions: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			networkSpec.Defaults(&tc.params)
			q := networkSpec.buildQuery(tc.params)
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

// TestNetwork_Command_TemplateIsConst 验证命令模板是 const string
// （通过检查渲染中包含固定的 PowerShell cmdlet 名称和输出字段）。
func TestNetwork_Command_TemplateIsConst(t *testing.T) {
	p := networkParams{State: "established", TopN: 50}
	networkSpec.Defaults(&p)
	q := networkSpec.buildQuery(p)
	rendered := q.render()

	mustContain := []string{
		"Get-NetTCPConnection",
		"Where-Object",
		"Select-Object",
		"-First",
		// computed property 输出字段（snake_case）
		"local_address",
		"local_port",
		"remote_address",
		"remote_port",
		"state",
		"owning_process",
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

// TestNetwork_Command_EmptyResultArrayWrapper 验证 PSQuery.Run 统一提供
// ConvertTo-Json -InputObject @() 外壳，确保空查询结果返回 JSON "[]" 而非 "null"。
func TestNetwork_Command_EmptyResultArrayWrapper(t *testing.T) {
	p := networkParams{State: "listen", LocalPort: 99999, TopN: 50}
	networkSpec.Defaults(&p)
	q := networkSpec.buildQuery(p)
	full := q.buildFullCmd()
	if !strings.Contains(full, "ConvertTo-Json -InputObject @(") {
		t.Errorf("完整命令应包含 ConvertTo-Json -InputObject @() 外壳\n完整命令: %s", full)
	}
}

// TestNetwork_Command_TopNFormattedAsInteger 验证 top_n 通过 PSInt 渲染为
// 裸整数而非引号字符串（Go 类型系统安全保证）。
func TestNetwork_Command_TopNFormattedAsInteger(t *testing.T) {
	cases := []int{1, 10, 50, 100, 500}
	for _, topN := range cases {
		p := networkParams{TopN: topN}
		q := networkSpec.buildQuery(p)
		rendered := q.render()
		expected := "Select-Object -First " + strconv.Itoa(topN)
		if !strings.Contains(rendered, expected) {
			t.Errorf("top_n=%d: 渲染应包含 %q\n渲染: %s", topN, expected, rendered)
		}
	}
}

// TestNetwork_Command_OutputFieldsSnakeCase 验证输出字段为 snake_case computed properties。
func TestNetwork_Command_OutputFieldsSnakeCase(t *testing.T) {
	p := networkParams{TopN: 50}
	q := networkSpec.buildQuery(p)
	rendered := q.render()

	fields := []string{
		"Name='local_address'",
		"Name='local_port'",
		"Name='remote_address'",
		"Name='remote_port'",
		"Name='state'",
		"Name='owning_process'",
	}
	for _, f := range fields {
		if !strings.Contains(rendered, f) {
			t.Errorf("渲染中缺少 computed property %q\n渲染: %s", f, rendered)
		}
	}
}

// TestNetwork_Command_StateToString 验证 State 属性经 .ToString() 确保枚举值序列化为字符串。
func TestNetwork_Command_StateToString(t *testing.T) {
	p := networkParams{State: "listen", TopN: 50}
	q := networkSpec.buildQuery(p)
	rendered := q.render()

	if !strings.Contains(rendered, "$_.State.ToString()") {
		t.Errorf("渲染中缺少 $_.State.ToString()（枚举值序列化保证）\n渲染: %s", rendered)
	}
}
