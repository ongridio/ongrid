package main

import (
	"slices"
	"strings"
	"testing"
)

// TestCoordinatorRosterHasCodeTools guards the regression where the read-code
// tools (HLD-012) were registered in the runtime toolbag but missing from the
// chat coordinator's curated whitelist — so the coordinator told users it had
// no way to read code. The coordinator can ONLY call tools in this list
// (filterToolsForAgent enforces it), so the code tools must be present here.
func TestCoordinatorRosterHasCodeTools(t *testing.T) {
	for _, want := range []string{"list_repo_sources", "read_source", "grep_source"} {
		if !slices.Contains(coordinatorToolNames, want) {
			t.Errorf("coordinator roster missing code tool %q (have %v)", want, coordinatorToolNames)
		}
	}
	// The lookup/triage baseline must also stay (don't accidentally drop it).
	for _, want := range []string{"query_knowledge", "query_devices", "list_database_sources", "analyze_database_status", "list_metric_catalog"} {
		if !slices.Contains(coordinatorToolNames, want) {
			t.Errorf("coordinator roster missing baseline tool %q", want)
		}
	}
}

func TestBasePromptRequiresMetricCatalogBeforeAlertDraft(t *testing.T) {
	prompt := ongridBasePrompt()
	for _, want := range []string{"analyze_database_status", "list_metric_catalog", "draft_config_change", "数据库告警", "custommetrics"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("base prompt missing %q", want)
		}
	}
	if !strings.Contains(prompt, "先 list_metric_catalog") || !strings.Contains(prompt, "draft_config_change") {
		t.Fatalf("base prompt should require list_metric_catalog before metric alert draft")
	}
	if !strings.Contains(prompt, "数据库告警也先 list_metric_catalog") {
		t.Fatalf("base prompt should route database alert drafts through list_metric_catalog first")
	}
	if !strings.Contains(prompt, "不调 list_database_sources") {
		t.Fatalf("base prompt should forbid list_database_sources during alert-rule creation")
	}
	if !strings.Contains(prompt, "最多一次") {
		t.Fatalf("base prompt should forbid repeated metric catalog discovery in one draft flow")
	}
	if !strings.Contains(prompt, "PromQL vector matching") ||
		!strings.Contains(prompt, "counter/rate/increase 用 sum by") ||
		!strings.Contains(prompt, "gauge/容量/当前值用 max by") {
		t.Fatalf("base prompt should require explicit vector matching for composed metric_raw PromQL")
	}
	if !strings.Contains(prompt, "用户未指定时不要复制 sample label 的具体值") ||
		!strings.Contains(prompt, "让所有采集源各自独立评估") {
		t.Fatalf("base prompt should forbid hard-coding sample ongrid_source when user did not specify a source")
	}
	if !strings.Contains(prompt, "MongoDB 连接使用率必须用") ||
		!strings.Contains(prompt, `conn_type="current"`) ||
		!strings.Contains(prompt, `conn_type="active"`) {
		t.Fatalf("base prompt should force MongoDB connection usage to current/(current+available)")
	}
	if !strings.Contains(prompt, "禁止只输出文字草案") ||
		!strings.Contains(prompt, "config_draft/draft_hash") {
		t.Fatalf("base prompt should forbid plain-text alert drafts without config_draft")
	}
	if !strings.Contains(prompt, "metric_burn_rate 的 sli 必须") ||
		!strings.Contains(prompt, "$window") {
		t.Fatalf("base prompt should require burn-rate SLI to use $window")
	}
	if !strings.Contains(prompt, "trace_latency / trace_error_rate 缺少 service 时不能创建") ||
		!strings.Contains(prompt, "traces_spanmetrics_*") {
		t.Fatalf("base prompt should require trace service before drafting trace alerts")
	}
	if !strings.Contains(prompt, "用户已经明确给出 service 时") ||
		!strings.Contains(prompt, "必须直接调用 draft_config_change") {
		t.Fatalf("base prompt should require direct draft_config_change when trace service is explicit")
	}
	if !strings.Contains(prompt, "创建服务 ongrid-manager p95 延迟超过 500ms 的告警") ||
		!strings.Contains(prompt, "spec.service=ongrid-manager") {
		t.Fatalf("base prompt should include a concrete trace_latency drafting example")
	}
}
