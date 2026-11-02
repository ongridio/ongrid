package dingtalk

import (
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/imformat"
	"strings"
	"testing"
)

func TestLongReplyPagesFitProvider(t *testing.T) {
	input := strings.Repeat("中文 & **result** `code`\n\n", 3000) + "END_OF_REPORT"
	pages := (senderAdapter{}).SplitText(input)
	if len(pages) < 2 {
		t.Fatal("expected multiple pages")
	}
	var out strings.Builder
	for _, page := range pages {
		if len(imformat.DingTalk(page)) > 16000 {
			t.Fatal("page exceeds budget")
		}
		out.WriteString(imformat.Plain(page))
	}
	if strings.Count(out.String(), "中文") != 3000 || !strings.HasSuffix(out.String(), "END_OF_REPORT") {
		t.Fatal("content lost")
	}
}
