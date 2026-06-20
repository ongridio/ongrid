package callbacks

import (
	"context"
	"strings"
	"testing"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestAlertDraftGuard_ReplacesPlainTextAlertDraftWithoutToolDraft(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建一个 MongoDB 数据库告警规则",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "## 告警规则草案\n规则键 nl_db_mongo\n草案哈希: sha256:fake\n请确认应用。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content != alertDraftGuardBlockedMessage {
		t.Fatalf("content = %q, want guard message", out.Message.Content)
	}
	if strings.Contains(out.Message.Content, "重新发送创建需求") {
		t.Fatalf("guard message should not ask the user to resend the request: %q", out.Message.Content)
	}
}

func TestAlertDraftGuard_ReplacesConfirmableRuleDesignWithoutToolDraft(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建一条 Redis 告警：缓存命中率明显偏低并持续一段时间时提醒我。",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role: schema.Assistant,
		Content: "**规则设计：Redis 缓存命中率偏低**（Warning 级别）\n\n" +
			"| 项目 | 内容 |\n|------|------|\n" +
			"| **规则 Key** | `redis_cache_hit_rate_low` |\n" +
			"| **触发条件** | 最近 5 分钟缓存命中率 < 90%，且持续 5 分钟 |\n" +
			"| **PromQL** | `100 * rate(redis_keyspace_hits_total[5m]) / (rate(redis_keyspace_hits_total[5m]) + rate(redis_keyspace_misses_total[5m])) < 90` |\n\n" +
			"需要确认创建吗？",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content != alertDraftGuardBlockedMessage {
		t.Fatalf("content = %q, want guard message", out.Message.Content)
	}
}

func TestAlertDraftGuard_InstallsForRuleRequestWithoutAlertWord(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "配置 SLO burn rate 规则，1 小时窗口超过 14.4 倍就触发",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "config_draft 已准备好，draft_hash: sha256:fake，请确认后调用 apply_config_change。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content != alertDraftGuardBlockedMessage {
		t.Fatalf("content = %q, want guard message", out.Message.Content)
	}
}

func TestAlertDraftGuard_AllowsAlertDraftAfterSuccessfulDraftTool(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建一个 MongoDB 数据库告警规则",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	guard.OnEnd(context.Background(), toolRunInfo(draftConfigChangeToolName), &einotool.CallbackOutput{
		Response: `{"kind":"config_draft","draft_hash":"sha256:real"}`,
	})
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "告警规则草案已创建，草案哈希: sha256:real，请确认应用。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content != "告警规则草案已创建，草案哈希: sha256:real，请确认应用。" {
		t.Fatalf("content = %q, want unchanged real draft text", out.Message.Content)
	}
}

func TestAlertDraftGuard_DoesNotBlockPendingDraftToolCall(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建一条主机 CPU 和内存告警",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "告警规则草案已生成，等待 draft_config_change 返回 config_draft。",
		ToolCalls: []schema.ToolCall{
			{
				ID:       "call_draft",
				Type:     "function",
				Function: schema.FunctionCall{Name: draftConfigChangeToolName},
			},
		},
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content == alertDraftGuardBlockedMessage {
		t.Fatalf("content was blocked before draft tool executed")
	}
	if !strings.Contains(out.Message.Content, "等待 draft_config_change") {
		t.Fatalf("content = %q, want unchanged pending draft text", out.Message.Content)
	}
}

func TestAlertDraftGuard_HidesSampleSourceMentionsForUnscopedMetricDraft(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建一个 MySQL 连接使用率告警，不限定数据库源",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	guard.OnEnd(context.Background(), toolRunInfo(draftConfigChangeToolName), &einotool.CallbackOutput{
		Response: `{"kind":"config_draft","draft_hash":"sha256:real","payload":{"rule":{"kind":"metric_raw","spec":{"expr":"max by (device_id, ongrid_source) (mysql_global_status_threads_connected) > 75"}}}}`,
	})
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "预览数据：当前 db:mysql-test 实例连接使用率约 66%，但规则覆盖所有 source。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if strings.Contains(out.Message.Content, "db:mysql-test") {
		t.Fatalf("content = %q, want sample source hidden", out.Message.Content)
	}
	if !strings.Contains(out.Message.Content, "某个数据库采集源") {
		t.Fatalf("content = %q, want generic source wording", out.Message.Content)
	}
}

func TestAlertDraftGuard_KeepsExplicitSourceMentionsForScopedMetricDraft(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "为 db:mysql-test 创建 MySQL 连接使用率告警",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	guard.OnEnd(context.Background(), toolRunInfo(draftConfigChangeToolName), &einotool.CallbackOutput{
		Response: `{"kind":"config_draft","draft_hash":"sha256:real","payload":{"rule":{"kind":"metric_raw","spec":{"source_explicit":true,"expr":"mysql_global_status_threads_connected{ongrid_source=\"db:mysql-test\"} > 75"}}}}`,
	})
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "预览数据：当前 db:mysql-test 实例连接使用率约 66%。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if !strings.Contains(out.Message.Content, "db:mysql-test") {
		t.Fatalf("content = %q, want explicit source preserved", out.Message.Content)
	}
}

func TestAlertDraftGuard_DoesNotInstallForNonCreateAlertQuestion(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "现在有哪些告警规则",
	})
	if guard != nil {
		t.Fatalf("guard = %#v, want nil", guard)
	}
}

func chatModelRunInfo() *einocallbacks.RunInfo {
	return &einocallbacks.RunInfo{Name: "ChatModel", Component: components.ComponentOfChatModel}
}

func toolRunInfo(name string) *einocallbacks.RunInfo {
	return &einocallbacks.RunInfo{Name: name, Component: components.ComponentOfTool}
}
