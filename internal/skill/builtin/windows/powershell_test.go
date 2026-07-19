//go:build windows

package windows

import (
	"strings"
	"testing"
)

// TestPsQuote_NormalString_ReturnsQuotedLiteral 验证正常字符串被单引号包裹。
func TestPsQuote_NormalString_ReturnsQuotedLiteral(t *testing.T) {
	cases := []string{"System", "Application", "Error", "Warning"}
	for _, s := range cases {
		got := psQuote(s)
		want := "'" + s + "'"
		if got != want {
			t.Errorf("psQuote(%q) = %q, want %q", s, got, want)
		}
	}
}

// TestPsQuote_EmptyString_ReturnsEmptyQuotes 验证空字符串返回 ''。
func TestPsQuote_EmptyString_ReturnsEmptyQuotes(t *testing.T) {
	got := psQuote("")
	want := "''"
	if got != want {
		t.Errorf("psQuote(\"\") = %q, want %q", got, want)
	}
}

// TestPsQuote_SingleQuote_DoubledEscape 验证单引号被转义为两个单引号。
func TestPsQuote_SingleQuote_DoubledEscape(t *testing.T) {
	got := psQuote("O'Brien")
	want := "'O''Brien'"
	if got != want {
		t.Errorf("psQuote(%q) = %q, want %q", "O'Brien", got, want)
	}
}

// TestPsQuote_AdversarialInjection_DoesNotEscape 验证注入 payload 无法逃逸单引号上下文。
// 不变量：
//  1. 输出始终以 ' 开头、' 结尾
//  2. 输入中每个 ' 在输出中成对出现（转义为 ''）
//  3. 总 ' 数 = 原始 ' 数 * 2 + 2（首尾包裹）
func TestPsQuote_AdversarialInjection_DoesNotEscape(t *testing.T) {
	payloads := []string{
		`'; Remove-Item C:\ -Force; '`,           // 典型注入
		`a$(whoami)`,                              // 子表达式插值
		`"; net user /add h; "`,                   // 双引号注入
		`'; Invoke-Expression 'bar'; '`,           // IEX 注入
		`O'Brien`,                                 // 合法但有单引号
		`正常中文`,                                  // UTF-8 多字节
		"`command`",                               // 反引号
		"line1\nline2",                            // 换行符
		"\u201cquote\u201d",                       // Unicode 引号
		"$var",                                    // 变量插值
		"';Start-Process calc;'#",                 // 注释符注入
		strings.Repeat("';", 100),                 // 超长重复注入
		strings.Repeat("A", 10000),                // 超长字符串
		"\x00\x01\x02",                            // 控制字符
		"'; whoami; '",                            // 命令分隔
		"${{ malicious }}",                        // 大括号插值
	}

	for _, p := range payloads {
		quoted := psQuote(p)

		// 不变量 1：首尾必须是单引号
		if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
			t.Errorf("psQuote(%q) = %q, 缺少首尾单引号包裹", p, quoted)
			continue
		}

		// 不变量 2：内部单引号必须成对（转义为 ''）
		origCount := strings.Count(p, "'")
		totalCount := strings.Count(quoted, "'")
		expectedTotal := origCount*2 + 2 // 内部每个 ' → 2 个，外加首尾 2 个
		if totalCount != expectedTotal {
			t.Errorf("psQuote(%q) = %q, 单引号数量错误：原 %d 现 %d 期望 %d",
				p, quoted, origCount, totalCount, expectedTotal)
		}

		// 不变量 3：去除外层包裹后，内部每个 ' 必须成对出现
		inner := quoted[1 : len(quoted)-1]
		if strings.Contains(inner, "'") {
			// 检查是否存在落单的 '（未成对转义）
			for i := 0; i < len(inner); i++ {
				if inner[i] == '\'' {
					if i+1 >= len(inner) || inner[i+1] != '\'' {
						t.Errorf("psQuote(%q) = %q, 内部位置 %d 有落单单引号（未转义）",
							p, quoted, i)
						break
					}
					i++ // 跳过下一个 '
				}
			}
		}
	}
}

// TestPsQuote_RoundTrip_PreservesOriginal 验证 psQuote 输出经 PowerShell
// 单引号语义还原后等于原输入（数学保证的逆向验证）。
func TestPsQuote_RoundTrip_PreservesOriginal(t *testing.T) {
	payloads := []string{
		"System",
		"",
		"O'Brien",
		`'; Remove-Item C:\ -Force; '`,
		"正常中文",
		"line1\nline2",
	}

	for _, p := range payloads {
		quoted := psQuote(p)
		// 模拟 PowerShell 单引号字符串还原：去掉首尾 '，内部 '' → '
		restored := strings.ReplaceAll(quoted[1:len(quoted)-1], "''", "'")
		if restored != p {
			t.Errorf("psQuote round-trip 失败：原 %q → psQuote %q → 还原 %q",
				p, quoted, restored)
		}
	}
}
