package imformat

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const sample = `# Incident summary

**Root cause:** connection pool exhausted after [deploy](https://example.com/r/42?a=1&b=2).

- API errors increased
- Database reached **100%** connections

| Service | Impact |
| --- | --- |
| checkout | 27% |

` + "```sql\nSELECT * FROM requests WHERE status > 499;\n```"

func TestPlain_StripsPresentationButKeepsStructure(t *testing.T) {
	got := Plain(sample)
	for _, want := range []string{
		"Incident summary",
		"Root cause: connection pool exhausted after deploy.",
		"• API errors increased",
		"Service | Impact",
		"SELECT * FROM requests",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Plain output missing %q:\n%s", want, got)
		}
	}
	for _, marker := range []string{"**", "```", "[deploy]("} {
		if strings.Contains(got, marker) {
			t.Errorf("Plain output retained %q:\n%s", marker, got)
		}
	}
}

func TestSlack_UsesMrkdwnDialect(t *testing.T) {
	got := Slack(sample)
	for _, want := range []string{
		"*Incident summary*",
		"*Root cause:*",
		"<https://example.com/r/42?a=1&amp;b=2|deploy>",
		"• API errors increased",
		"```Service | Impact",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Slack output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**Root cause:**") {
		t.Errorf("Slack output retained GFM bold markers:\n%s", got)
	}
}

func TestSlackSections_RespectsBlockKitLimit(t *testing.T) {
	input := strings.Repeat("paragraph text\n\n", 500)
	sections := SlackSections(input)
	if len(sections) < 2 {
		t.Fatalf("expected multiple sections, got %d", len(sections))
	}
	for i, section := range sections {
		if got := utf8.RuneCountInString(section); got > slackSectionMaxRunes {
			t.Errorf("section %d has %d runes", i, got)
		}
	}
}

func TestTelegramHTML_UsesSupportedTagsAndEscapesInput(t *testing.T) {
	got := TelegramHTML(sample + "\n\n<script>alert(1)</script> 2 < 3")
	for _, want := range []string{
		"<b>Incident summary</b>",
		"<b>Root cause:</b>",
		`<a href="https://example.com/r/42?a=1&amp;b=2">deploy</a>`,
		"<pre>Service | Impact",
		"2 &lt; 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Telegram output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("Telegram output passed raw HTML through:\n%s", got)
	}
}

func TestRawHTML_IsNeverPassedToProviderMarkup(t *testing.T) {
	input := "<script>alert(1)</script>\n\n<div>diagnostic</div>"
	for name, got := range map[string]string{
		"plain":    Plain(input),
		"slack":    Slack(input),
		"telegram": TelegramHTML(input),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(got, "<script>") || strings.Contains(got, "<div>") {
				t.Fatalf("raw HTML survived: %s", got)
			}
		})
	}
}

func TestUnsafeLinks_AreRenderedAsLabelsOnly(t *testing.T) {
	input := `[run](javascript:alert(1)) [open](https://example.com)`
	for name, got := range map[string]string{
		"slack":    Slack(input),
		"telegram": TelegramHTML(input),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(got, "javascript:") {
				t.Fatalf("unsafe link survived: %s", got)
			}
			if !strings.Contains(got, "run") || !strings.Contains(got, "https://example.com") {
				t.Fatalf("safe content missing: %s", got)
			}
		})
	}
}

func TestTaskLists_KeepCheckboxState(t *testing.T) {
	input := "- [x] evidence collected\n- [ ] rollback approved"
	for name, got := range map[string]string{
		"plain":    Plain(input),
		"slack":    Slack(input),
		"telegram": TelegramHTML(input),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(got, "☑ evidence collected") || !strings.Contains(got, "☐ rollback approved") {
				t.Fatalf("checkbox state missing: %s", got)
			}
		})
	}
}

func TestTelegramHTML_BoundsLongMessagesWithoutBrokenTags(t *testing.T) {
	input := "**" + strings.Repeat("数据库连接已耗尽。", 800) + "**"
	got := TelegramHTML(input)
	if count := utf8.RuneCountInString(got); count > telegramMaxRunes {
		t.Fatalf("message has %d runes, max %d", count, telegramMaxRunes)
	}
	if strings.Count(got, "<b>") != strings.Count(got, "</b>") {
		t.Fatalf("unbalanced tags: %s", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated message missing marker: %s", got)
	}
}

func FuzzRenderers_NeverPanicOrExceedTelegramLimit(f *testing.F) {
	for _, seed := range []string{
		"plain text",
		"# heading\n\n**bold** [link](https://example.com)",
		"<script>alert(1)</script>",
		"```go\nfunc main() {}\n```",
		"| a | b |\n|---|---|\n| 1 | 2 |",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		plain := Plain(input)
		slack := Slack(input)
		telegram := TelegramHTML(input)
		if !utf8.ValidString(plain) || !utf8.ValidString(slack) || !utf8.ValidString(telegram) {
			t.Fatal("renderer produced invalid UTF-8")
		}
		if got := utf8.RuneCountInString(telegram); got > telegramMaxRunes {
			t.Fatalf("telegram output has %d runes", got)
		}
	})
}
