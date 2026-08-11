package flow

import (
	"strings"
	"testing"
)

func TestGenSystemPrompt_WhenMessagingRequested_UsesSharedToolNodes(t *testing.T) {
	prompt := genSystemPrompt([]ToolMeta{
		{Name: "query_promql", Description: "query metrics"},
		{Name: "send_notification", Description: "assistant notification sender"},
		{Name: "send_im_message", Description: "explicit IM group sender"},
	})
	for _, want := range []string{
		"send_notification",
		"send_im_message",
		"设置 → 通知",
		"im_app_id 和 group_id",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("generation prompt missing workflow-notification guidance %q", want)
		}
	}
	if !strings.Contains(prompt, "- send_notification") || !strings.Contains(prompt, "- send_im_message") {
		t.Fatal("messaging tools must be listed as workflow tools")
	}
}

func TestGenSystemPrompt_GuidesSourcePortUsageToConditionOnly(t *testing.T) {
	prompt := genSystemPrompt([]ToolMeta{
		{Name: "query_promql", Description: "query metrics"},
	})
	// The prompt must make it explicit that only condition nodes may carry a
	// sourcePort, and that omitting it is the default for every other node.
	// Without this guidance the model occasionally emits sourcePort:"false"
	// on ordinary tool edges, which ParseGraph rejects with
	// `port "false" not exposed` (issue #185).
	for _, want := range []string{
		"仅 condition 节点使用",
		"只有 condition 节点可以写",
		"其他节点的边必须省略 sourcePort",
		"写错端口",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("generation prompt missing sourcePort-only-for-condition guidance %q", want)
		}
	}
}
