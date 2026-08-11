//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

// TestDeriveValidSet 验证 EnumMapping slice 派生 validSet map。
func TestDeriveValidSet(t *testing.T) {
	mappings := []EnumMapping{
		{"closed", "Closed"},
		{"listen", "Listen"},
	}
	got := deriveValidSet(mappings)

	if !got["closed"] {
		t.Errorf("deriveValidSet: 'closed' 应在 validSet 中")
	}
	if !got["listen"] {
		t.Errorf("deriveValidSet: 'listen' 应在 validSet 中")
	}
	if got["unknown"] {
		t.Errorf("deriveValidSet: 'unknown' 不应在 validSet 中")
	}
	if len(got) != 2 {
		t.Errorf("deriveValidSet: len = %d, want 2", len(got))
	}
}

// TestDeriveToPSMap 验证 EnumMapping slice 派生 canonical→PSValue map。
func TestDeriveToPSMap(t *testing.T) {
	mappings := []EnumMapping{
		{"closed", "Closed"},
		{"listen", "Listen"},
	}
	got := deriveToPSMap(mappings)

	if got["closed"] != "Closed" {
		t.Errorf("deriveToPSMap['closed'] = %q, want 'Closed'", got["closed"])
	}
	if got["listen"] != "Listen" {
		t.Errorf("deriveToPSMap['listen'] = %q, want 'Listen'", got["listen"])
	}
	if _, ok := got["unknown"]; ok {
		t.Errorf("deriveToPSMap: 'unknown' 不应在 map 中")
	}
}

// TestDeriveCanonicalList 验证 EnumMapping slice 派生 canonical 名列表。
func TestDeriveCanonicalList(t *testing.T) {
	mappings := []EnumMapping{
		{"closed", "Closed"},
		{"listen", "Listen"},
		{"established", "Established"},
	}
	got := deriveCanonicalList(mappings)

	if len(got) != 3 {
		t.Fatalf("deriveCanonicalList: len = %d, want 3", len(got))
	}
	want := []string{"closed", "listen", "established"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("deriveCanonicalList[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestClampInt 验证 clampInt 钳制行为。
func TestClampInt(t *testing.T) {
	cases := []struct {
		name    string
		v       int
		def     int
		max     int
		want    int
	}{
		{"zero uses default", 0, 50, 500, 50},
		{"negative uses default", -1, 50, 500, 50},
		{"positive under max passes through", 100, 50, 500, 100},
		{"exactly max passes through", 500, 50, 500, 500},
		{"over max clamped to max", 501, 50, 500, 500},
		{"one passes through", 1, 50, 500, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampInt(tc.v, tc.def, tc.max)
			if got != tc.want {
				t.Errorf("clampInt(%d, %d, %d) = %d, want %d",
					tc.v, tc.def, tc.max, got, tc.want)
			}
		})
	}
}

// TestJoinFilterConditions 验证 filter 条件 join + $true fallback。
func TestJoinFilterConditions(t *testing.T) {
	// 空 → $true
	if got := joinFilterConditions(nil); got != "$true" {
		t.Errorf("joinFilterConditions(nil) = %q, want '$true'", got)
	}
	if got := joinFilterConditions([]string{}); got != "$true" {
		t.Errorf("joinFilterConditions([]) = %q, want '$true'", got)
	}

	// 单条件 → 原样
	single := []string{"$_.State -eq 'Closed'"}
	if got := joinFilterConditions(single); got != "$_.State -eq 'Closed'" {
		t.Errorf("joinFilterConditions(single) = %q, want %q", got, single[0])
	}

	// 多条件 → " -and " join
	multi := []string{"$_.State -eq 'Closed'", "$_.LocalPort -eq 80"}
	got := joinFilterConditions(multi)
	want := "$_.State -eq 'Closed' -and $_.LocalPort -eq 80"
	if got != want {
		t.Errorf("joinFilterConditions(multi) = %q, want %q", got, want)
	}
}

// testSpecParams 是 TestSkillSpec_Run 用的测试 params 类型。
type testSpecParams struct {
	Name  string
	TopN  int
	State string
}

// TestSkillSpec_Run_NoTopN_DelegatesToPSQuery 验证无 TopN 的 SkillSpec.buildQuery
// 生成正确的 PSQuery（只含 filter，不含 topN）。
func TestSkillSpec_Run_NoTopN_DelegatesToPSQuery(t *testing.T) {
	spec := SkillSpec[testSpecParams]{
		Metadata: skill.Metadata{Key: "test_skill"},
		Timeout:  5 * time.Second,
		Template: `Get-Test | Where-Object { %s }`,
		Depth:    4,
		Defaults: func(p *testSpecParams) {
			if p.State == "" {
				p.State = "all"
			}
		},
		BuildFilter: func(p testSpecParams) string {
			if p.Name == "" {
				return "$true"
			}
			return `$_.Name -eq ` + psQuote(p.Name)
		},
	}

	p := testSpecParams{Name: "foo", State: "all"}
	q := spec.buildQuery(p)

	// 不变量：无 TopN 时 PSQuery.Params 只含 filter
	if len(q.Params) != 1 {
		t.Fatalf("Params len = %d, want 1 (filter only, no topN)", len(q.Params))
	}
	if _, ok := q.Params[0].(PSRaw); !ok {
		t.Errorf("Params[0] 应为 PSRaw，实际 %T", q.Params[0])
	}
	// 渲染应含 BuildFilter 输出
	rendered := q.render()
	if !strings.Contains(rendered, `$_.Name -eq 'foo'`) {
		t.Errorf("渲染应含 filter 输出\n渲染: %s", rendered)
	}
}

// TestSkillSpec_Run_InvalidJSON_ReturnsDecodeError 验证 JSON 解析失败返回错误。
func TestSkillSpec_Run_InvalidJSON_ReturnsDecodeError(t *testing.T) {
	spec := SkillSpec[testSpecParams]{
		Metadata: skill.Metadata{Key: "test_skill"},
		Timeout:  5 * time.Second,
		Template: `Get-Test`,
		Depth:    4,
		BuildFilter: func(p testSpecParams) string { return "$true" },
	}

	_, err := spec.Run(context.Background(), json.RawMessage(`{invalid json`))
	if err == nil {
		t.Fatal("期望返回 decode error，实际 nil")
	}
	if !strings.Contains(err.Error(), "decode params") {
		t.Errorf("错误应包含 'decode params'，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "test_skill") {
		t.Errorf("错误应包含 skill key 'test_skill'，实际: %v", err)
	}
}

// TestSkillSpec_Run_ValidateEnumsCalled 验证 enum 校验函数被调用。
func TestSkillSpec_Run_ValidateEnumsCalled(t *testing.T) {
	validateCalled := false
	spec := SkillSpec[testSpecParams]{
		Metadata: skill.Metadata{Key: "test_skill"},
		Timeout:  5 * time.Second,
		Template: `Get-Test | Where-Object { %s }`,
		Depth:    4,
		Defaults: func(p *testSpecParams) {
			if p.State == "" {
				p.State = "all"
			}
		},
		BuildFilter: func(p testSpecParams) string { return "$true" },
		ValidateEnums: func(p testSpecParams) error {
			validateCalled = true
			return errors.New("forced validation error")
		},
	}

	_, err := spec.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("期望 ValidateEnums 返回错误")
	}
	if !validateCalled {
		t.Error("ValidateEnums 未被调用")
	}
	if !strings.Contains(err.Error(), "forced validation error") {
		t.Errorf("错误应传播 ValidateEnums 的错误，实际: %v", err)
	}
}

// TestSkillSpec_BuildQuery_NoTopN 验证无 TopN 时 PSQuery.Params 只含 filter。
func TestSkillSpec_BuildQuery_NoTopN(t *testing.T) {
	spec := SkillSpec[testSpecParams]{
		Template: `Get-Test | Where-Object { %s }`,
		Depth:    4,
		Defaults: func(p *testSpecParams) {
			if p.State == "" {
				p.State = "all"
			}
		},
		BuildFilter: func(p testSpecParams) string {
			return `$_.Name -eq ` + psQuote("foo")
		},
	}

	p := testSpecParams{Name: "foo", State: "all"}
	q := spec.buildQuery(p)

	if len(q.Params) != 1 {
		t.Fatalf("Params len = %d, want 1 (filter only)", len(q.Params))
	}
	if _, ok := q.Params[0].(PSRaw); !ok {
		t.Errorf("Params[0] 应为 PSRaw，实际 %T", q.Params[0])
	}
}

// TestSkillSpec_BuildQuery_WithTopN 验证有 TopN 时 PSQuery.Params 含 filter + topN。
func TestSkillSpec_BuildQuery_WithTopN(t *testing.T) {
	spec := SkillSpec[testSpecParams]{
		Template:    `Get-Test | Where-Object { %s } | Select-Object -First %d`,
		Depth:       4,
		BuildFilter: func(p testSpecParams) string { return "$true" },
		ExtractTopN: func(p testSpecParams) int { return p.TopN },
		TopNDefault: 50,
		TopNMax:     500,
	}

	cases := []struct {
		name    string
		topN    int
		wantTop int
	}{
		{"zero → default", 0, 50},
		{"negative → default", -1, 50},
		{"normal passthrough", 100, 100},
		{"over max clamped", 501, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testSpecParams{TopN: tc.topN}
			q := spec.buildQuery(p)

			if len(q.Params) != 2 {
				t.Fatalf("Params len = %d, want 2 (filter + topN)", len(q.Params))
			}
			topN, ok := q.Params[1].(PSInt)
			if !ok {
				t.Fatalf("Params[1] 应为 PSInt，实际 %T", q.Params[1])
			}
			if int(topN) != tc.wantTop {
				t.Errorf("topN = %d, want %d", topN, tc.wantTop)
			}
		})
	}
}

// TestSkillSpec_BuildQuery_ArrayKeys 验证 ArrayKeys 正确传递。
func TestSkillSpec_BuildQuery_ArrayKeys(t *testing.T) {
	spec := SkillSpec[testSpecParams]{
		Template:    `Get-Test | Where-Object { %s }`,
		Depth:       4,
		ArrayKeys:   []string{"dependencies"},
		BuildFilter: func(p testSpecParams) string { return "$true" },
	}

	p := testSpecParams{}
	q := spec.buildQuery(p)

	if len(q.ArrayKeys) != 1 || q.ArrayKeys[0] != "dependencies" {
		t.Errorf("ArrayKeys = %v, want ['dependencies']", q.ArrayKeys)
	}
}
