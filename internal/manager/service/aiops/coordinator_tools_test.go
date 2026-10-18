package aiops

import (
	"strings"
	"testing"
)

func TestBasePromptAllowsLightweightCoordinatorReads(t *testing.T) {
	prompt := BasePrompt()
	for _, want := range []string{"本轮可见能力", "when_to_use", "单一数据源查询", "已知文件删除", "host_bash", "query_traceql"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing lightweight-read guidance %q", want)
		}
	}
	for _, want := range []string{"ToolSearch", "实时对象或数据源标识", "先用对应注册工具", "不要先查 KB"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing registration/progressive routing guidance %q", want)
		}
	}
	for _, bad := range []string{"单域也必须派", "只要用户的问题落入任一专家域", "所有工具都需要通过 AgentTool"} {
		if strings.Contains(prompt, bad) {
			t.Fatalf("base prompt still contains old dispatch-only guidance %q", bad)
		}
	}
}

func TestBasePromptRoutesSimpleTraceQueriesDirectly(t *testing.T) {
	prompt := BasePrompt()
	for _, want := range []string{
		"调工具前先分类",
		"trace/span/trace_id/慢 trace/错误 trace/TraceQL",
		"query_traceql",
		"query_alert_rules",
		"query_change_events",
		"grep_source",
		"不要为了确认某个数据源是否可用",
		"不要为了确认数据源存在先查设备或拓扑",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing trace direct-routing guidance %q", want)
		}
	}
	for _, want := range []string{"按指标排序", "目录/文件/进程/日志等主机归因", "rank_hosts(by=\"disk\")"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing filesystem delegation guidance %q", want)
		}
	}
}

func TestBasePromptKeepsDefaultAssistantAsOwner(t *testing.T) {
	prompt := BasePrompt()
	for _, want := range []string{
		"根因、影响面、处置建议",
		"综合体检、风险评估、优先级、报告、remediation plan",
		"需要更深证据时可调用 `AgentTool`",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing complex delegation guidance %q", want)
		}
	}
}

func TestDefaultCoordinatorUsesGraphLoopBudget(t *testing.T) {
	if DefaultCoordinatorMaxTurns != 12 {
		t.Fatalf("default coordinator MaxTurns = %d, want 12", DefaultCoordinatorMaxTurns)
	}
}

func TestBasePromptRequiresMetricCatalogBeforeAlertDraft(t *testing.T) {
	prompt := BasePrompt()
	for _, want := range []string{"analyze_database_status", "list_metric_catalog", "draft_config_change", "apply_config_change"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing %q", want)
		}
	}
	if !strings.Contains(prompt, "list_metric_catalog 一次") || !strings.Contains(prompt, "draft_config_change") {
		t.Fatalf("base prompt should require list_metric_catalog before metric alert draft")
	}
	if !strings.Contains(prompt, "不调 list_database_sources") {
		t.Fatalf("base prompt should forbid list_database_sources during alert-rule creation")
	}
	if !strings.Contains(prompt, "catalog 有可用指标后") || !strings.Contains(prompt, "catalog 为空/不可用时停止说明缺失") {
		t.Fatalf("base prompt should require a usable metric catalog before metric alert draft")
	}
	if !strings.Contains(prompt, "catalog 为空/不可用时说明缺失") {
		t.Fatalf("base prompt should stop when the metric catalog is unavailable")
	}
	if !strings.Contains(prompt, "禁止只输出文字草案") || !strings.Contains(prompt, "config_draft/draft_hash") {
		t.Fatalf("base prompt should forbid plain-text alert drafts without config_draft")
	}
	if !strings.Contains(prompt, "config_validation_failed") || !strings.Contains(prompt, "validation.issues") {
		t.Fatalf("base prompt should require repairing validation failed drafts")
	}
	if !strings.Contains(prompt, "原始 payload/draft_hash") {
		t.Fatalf("base prompt should require applying the exact config_draft payload/hash")
	}
	if !strings.Contains(prompt, "具体 rule kind 与表达式规范交给工具 schema 和后端 compiler") {
		t.Fatalf("base prompt should delegate detailed alert semantics to schema/compiler")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
