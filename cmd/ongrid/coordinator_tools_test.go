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
