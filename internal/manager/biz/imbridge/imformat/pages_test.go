package imformat

import (
	"strings"
	"testing"
)

func TestMarkdownPagesShortReplyRetainsRichText(t *testing.T) {
	input := "# Report\n\n**bold** [link](https://example.com)"
	pages := MarkdownPages(input, func(s string) bool { return len(s) <= 1000 })
	if len(pages) != 1 || pages[0] != input {
		t.Fatalf("changed short rich text: %#v", pages)
	}
}
func TestMarkdownPagesLongReplyPreservesContent(t *testing.T) {
	input := strings.Repeat("中文 <x> & `code`\n\n", 300) + "[report](https://example.com/end)"
	pages := MarkdownPages(input, func(s string) bool { return len(s) <= 1000 })
	if len(pages) < 2 {
		t.Fatal("expected pages")
	}
	var body strings.Builder
	for _, p := range pages {
		if len(p) > 1000 {
			t.Fatal("over budget")
		}
		body.WriteString(Plain(p))
	}
	got := body.String()
	if strings.Count(got, "中文") != 300 || !strings.Contains(got, "https://example.com/end") {
		t.Fatalf("content lost: %s", got)
	}
	for _, p := range pages {
		if strings.Contains(TelegramHTML(p), "…") {
			t.Fatal("Telegram page was truncated")
		}
	}
}
