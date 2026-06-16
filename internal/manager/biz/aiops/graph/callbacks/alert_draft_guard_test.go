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

func TestAlertDraftGuard_ExplainsMissingMetricInsteadOfGenericDraftBlock(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建一个 custom_app_queue_depth 队列积压告警",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	guard.OnEnd(context.Background(), toolRunInfo(listMetricCatalogToolName), &einotool.CallbackOutput{
		Response: `{"status":"empty","query":"payments 队列 custom_app_queue_depth 深度超过 100","instruction":"No currently scraped metric matched this query/selector."}`,
	})
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "## 告警规则草案\n规则键 nl_custom\n请确认应用。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content == alertDraftGuardBlockedMessage {
		t.Fatalf("content = generic guard message, want missing metric explanation")
	}
	for _, want := range []string{"当前指标目录未匹配", "custom_app_queue_depth", "没有生成可应用的 config_draft"} {
		if !strings.Contains(out.Message.Content, want) {
			t.Fatalf("content = %q, want %q", out.Message.Content, want)
		}
	}
}

func TestAlertDraftGuard_AsksForTraceServiceWhenMissing(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建链路 p95 延迟超过 500ms 持续 10 分钟的告警",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "## 告警规则草案\n规则键 trace_latency\n请确认应用。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content != traceServiceRequiredMessage {
		t.Fatalf("content = %q, want trace service prompt", out.Message.Content)
	}
}

func TestAlertDraftGuard_DoesNotAskForTraceServiceWhenExplicit(t *testing.T) {
	guard := NewAlertDraftGuardHandler(AlertDraftGuardDeps{
		UserText: "创建服务 ongrid-manager 的链路 p95 延迟超过 500ms 持续 10 分钟的告警",
	})
	if guard == nil {
		t.Fatal("guard = nil")
	}
	out := &einomodel.CallbackOutput{Message: &schema.Message{
		Role:    schema.Assistant,
		Content: "## 告警规则草案\n规则键 trace_latency\n请确认应用。",
	}}

	guard.OnEnd(context.Background(), chatModelRunInfo(), out)

	if out.Message.Content == traceServiceRequiredMessage {
		t.Fatalf("content = trace service prompt, want non-missing-service guard path")
	}
}

func chatModelRunInfo() *einocallbacks.RunInfo {
	return &einocallbacks.RunInfo{Name: "ChatModel", Component: components.ComponentOfChatModel}
}

func toolRunInfo(name string) *einocallbacks.RunInfo {
	return &einocallbacks.RunInfo{Name: name, Component: components.ComponentOfTool}
}
