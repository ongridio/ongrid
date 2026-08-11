package logs

import (
	"strings"
	"testing"
)

// TestYamlDoubleQuote 验证 YAML 双引号标量转义函数的安全性。
// 覆盖正常值 + 注入 payload + 边界值。
func TestYamlDoubleQuote(t *testing.T) {
	cases := []struct {
		name  string
		input any
		check func(output string) bool // true = pass
	}{
		{
			name:  "正常字符串含引号",
			input: `hello"world`,
			check: func(out string) bool {
				// 内部引号必须被反斜杠转义（\"），使整个值是一个 YAML 双引号标量
				// 即：输出形如 "hello\"world"，内部 " 前必有 \
				return strings.HasPrefix(out, `"`) &&
					strings.HasSuffix(out, `"`) &&
					strings.Contains(out, `\"`) &&
					len(out) == len(`"hello\"world"`)
			},
		},
		{
			name:  "含换行注入 payload",
			input: "evil\n  malicious: injected",
			check: func(out string) bool {
				// 实际换行必须被转义为字面 \n（不能产生真换行）
				return !strings.Contains(out, "\nmalicious") && strings.Contains(out, `\n`)
			},
		},
		{
			name:  "反斜杠转义",
			input: `path\to\file`,
			check: func(out string) bool {
				return strings.Contains(out, `\\`)
			},
		},
		{
			name:  "uint64 类型（EdgeID）",
			input: uint64(42),
			check: func(out string) bool {
				return out == `"42"`
			},
		},
		{
			name:  "空字符串",
			input: "",
			check: func(out string) bool {
				return out == `""`
			},
		},
		{
			name:  "正常 URL",
			input: "https://manager.example.com/push",
			check: func(out string) bool {
				return strings.HasPrefix(out, `"`) && strings.HasSuffix(out, `"`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output := yamlDoubleQuote(tc.input)
			if !tc.check(output) {
				t.Errorf("yamlDoubleQuote(%v) = %q — check failed", tc.input, output)
			}
		})
	}
}

// TestYamlKey 验证 YAML mapping key 的危险字符清理。
// 防止 label key 含冒号 / 换行 / 引号导致 YAML 结构注入。
func TestYamlKey(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // 不含危险字符的期望输出
	}{
		{"正常 key", "service", "service"},
		{"含冒号", "evil:injected", "evil_injected"},
		{"含换行", "key\nmalicious", "key_malicious"},
		{"含双引号", `a"b`, "a_b"},
		{"含单引号", "a'b", "a_b"},
		{"空字符串", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output := yamlKey(tc.input)
			// 验证输出不含任何危险字符
			for _, dangerous := range []string{":", "\n", "\"", "'"} {
				if strings.Contains(output, dangerous) {
					t.Errorf("yamlKey(%q) = %q — still contains dangerous char %q", tc.input, output, dangerous)
				}
			}
		})
	}
}
