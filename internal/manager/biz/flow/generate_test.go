package flow

import (
	"strings"
	"testing"
)

func TestGenSystemPrompt_WhenAlertDeliveryRequested_UsesNotifyNode(t *testing.T) {
	prompt := genSystemPrompt([]ToolMeta{
		{Name: "query_promql", Description: "query metrics"},
		{Name: "send_im_message", Description: "legacy chat sender"},
	})
	for _, want := range []string{
		"发送单向通知",
		"设置 → 通知",
		"不要创建 send_im_message 工具节点",
		"双向 IM 机器人只用于接收并回复会话",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("generation prompt missing workflow-notification guidance %q", want)
		}
	}
	if strings.Contains(prompt, "- send_im_message") {
		t.Fatal("legacy chat sender must not be listed as a workflow tool")
	}
}
