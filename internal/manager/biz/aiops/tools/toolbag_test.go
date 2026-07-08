package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// stubTool is a minimal BaseTool implementation for ToolBag tests. We
// avoid pulling the production tools (host_load, query_promql, ...) so
// the test only depends on the bag's partitioning logic, not on the
// concrete tool wiring.
type stubTool struct {
	name        string
	description string
	whenToUse   string
	class       string
	params      string
}

func (s *stubTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	params := s.params
	if params == "" {
		params = `{"type":"object"}`
	}
	return &basetool.ToolInfo{
		Name:        s.name,
		Description: s.description,
		WhenToUse:   s.whenToUse,
		Parameters:  json.RawMessage(params),
		Class:       s.class,
	}, nil
}

func (s *stubTool) InvokableRun(_ context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	return `{"ok":true,"by":"` + s.name + `"}`, nil
}

func newStub(name, desc string) *stubTool {
	return &stubTool{name: name, description: desc, whenToUse: "use when " + name, class: "read", params: `{"type":"object","properties":{"x":{"type":"string"}}}`}
}

// TestToolBag_BelowThreshold confirms "deferral off" behaviour: every
// tool in the input slice surfaces with its full schema and there's no
// redactedTool wrapper anywhere in the SchemasForLLM output.
func TestToolBag_BelowThreshold(t *testing.T) {
	tools := []basetool.BaseTool{
		newStub("get_host_load", "live host metrics"),
		newStub("query_promql", "PromQL query"),
		newStub("rank_edges", "rank edges by metric"),
	}
	bag := NewToolBag(tools, 30)
	if bag.IsDeferring() {
		t.Fatalf("expected deferral OFF for 3 tools under threshold 30")
	}
	got := bag.SchemasForLLM()
	if len(got) != 3 {
		t.Fatalf("SchemasForLLM len=%d want 3", len(got))
	}
	for _, x := range got {
		if _, ok := x.(redactedTool); ok {
			t.Errorf("unexpected redactedTool wrapper in below-threshold output")
		}
	}
}

// TestToolBag_OverThresholdSplits confirms "deferral on": exactly the
// core tier gets full schemas, specialty tier gets redactedTool, and
// SchemasForLLM length matches input.
func TestToolBag_OverThresholdSplits(t *testing.T) {
	// Build a 35-tool slice with realistic names so tierByName can
	// classify them. We pad with synthetic names that fall through to
	// the "specialty" default.
	in := []basetool.BaseTool{
		newStub("get_host_load", ""),
		newStub("get_host_processes", ""),
		newStub("query_promql", ""),
		newStub("query_logql", ""),
		newStub("query_traceql", ""),
		newStub("query_devices", ""),
		newStub("get_topology", ""),
		newStub("query_incidents", ""),
		newStub("get_edge_summary", ""),
		newStub("correlate_incident", ""),
		newStub("AgentTool", ""),
		newStub("SendMessage", ""),
		newStub("TaskStop", ""),
		// 13 core entries.
		newStub("rank_edges", ""),
		newStub("find_outlier_edges", ""),
		newStub("get_incident_detail", ""),
		newStub("query_alert_rules", ""),
		newStub("host_find_large_files", ""),
		newStub("host_du_summary", ""),
		newStub("host_stat_file", ""),
		newStub("host_restart_service", ""),
		// 8 specialty entries → 21 so far.
	}
	for i := 0; i < 14; i++ {
		in = append(in, newStub("synthetic_pack_tool_"+string(rune('a'+i)), "synthetic"))
	}
	if len(in) != 35 {
		t.Fatalf("setup error: have %d tools want 35", len(in))
	}

	bag := NewToolBag(in, 30)
	if !bag.IsDeferring() {
		t.Fatalf("expected deferral ON for 35 tools over threshold 30")
	}

	all := bag.AllTools()
	if len(all) != 35 {
		t.Errorf("AllTools len=%d want 35", len(all))
	}

	deferred := bag.DeferredTools()
	// 8 known specialty + 14 unknown → 22 deferred.
	if len(deferred) != 22 {
		t.Errorf("DeferredTools len=%d want 22", len(deferred))
	}

	got := bag.SchemasForLLM()
	if len(got) != 35 {
		t.Fatalf("SchemasForLLM len=%d want 35", len(got))
	}

	coreCount, redactedCount := 0, 0
	for _, x := range got {
		if _, ok := x.(redactedTool); ok {
			redactedCount++
		} else {
			coreCount++
		}
	}
	if coreCount != 13 {
		t.Errorf("core (full schema) tools=%d want 13", coreCount)
	}
	if redactedCount != 22 {
		t.Errorf("redacted tools=%d want 22", redactedCount)
	}
}

// TestToolBag_RedactedSchemaShape confirms a redacted tool's Info()
// returns an empty-properties schema with a hint pointing at
// ToolSearch, and that InvokableRun still delegates to the inner tool.
func TestToolBag_RedactedSchemaShape(t *testing.T) {
	inner := newStub("host_find_large_files", "list large files")
	r := redactedTool{inner: inner}
	info, err := r.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "host_find_large_files" {
		t.Errorf("name=%s want host_find_large_files", info.Name)
	}
	if !strings.Contains(string(info.Parameters), `"properties":{}`) {
		t.Errorf("expected empty properties block, got: %s", info.Parameters)
	}
	if !strings.Contains(string(info.Parameters), "ToolSearch") {
		t.Errorf("expected ToolSearch hint in redacted schema: %s", info.Parameters)
	}
	out, err := r.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, `"by":"host_find_large_files"`) {
		t.Errorf("delegate did not reach inner tool: %s", out)
	}
}

// TestToolBag_WithExtraAddsToCore confirms the extras slot lands in
// the core tier with a full schema, even when deferral is on.
func TestToolBag_WithExtraAddsToCore(t *testing.T) {
	// Above threshold so deferral is active.
	in := make([]basetool.BaseTool, 0, 32)
	for i := 0; i < 32; i++ {
		in = append(in, newStub("pack_"+string(rune('a'+i)), "pack tool"))
	}
	bag := NewToolBag(in, 30)
	if !bag.IsDeferring() {
		t.Fatalf("expected deferral ON")
	}
	extra := newStub("ToolSearch", "deferred schema fetch")
	bag = bag.WithExtra(extra)

	got := bag.SchemasForLLM()
	// Find ToolSearch in the LLM-facing slice; it must be the
	// unredacted variant (not a redactedTool).
	var found basetool.BaseTool
	for _, x := range got {
		info, _ := x.Info(context.Background())
		if info != nil && info.Name == "ToolSearch" {
			found = x
		}
	}
	if found == nil {
		t.Fatalf("ToolSearch not present in SchemasForLLM")
	}
	if _, isRedacted := found.(redactedTool); isRedacted {
		t.Errorf("ToolSearch should NOT be redacted in extras slot")
	}

	// AllTools should also include it.
	gotAll := bag.AllTools()
	if !containsToolName(t, gotAll, "ToolSearch") {
		t.Errorf("AllTools missing ToolSearch")
	}
}

// TestToolBag_UnknownTierDefaultsSpecialty confirms a tool whose name
// is not in tierByName (e.g. a future marketplace tool) ends up in the
// specialty (deferred) bucket when deferral is on.
func TestToolBag_UnknownTierDefaultsSpecialty(t *testing.T) {
	in := make([]basetool.BaseTool, 0, 32)
	in = append(in, newStub("query_promql", "")) // core
	for i := 0; i < 31; i++ {
		in = append(in, newStub("unknown_tool_"+string(rune('a'+i)), "marketplace add"))
	}
	bag := NewToolBag(in, 30)
	if !bag.IsDeferring() {
		t.Fatalf("expected deferral ON")
	}
	deferred := bag.DeferredTools()
	if len(deferred) != 31 {
		t.Errorf("expected 31 unknown tools to land in specialty, got %d", len(deferred))
	}
	// query_promql should be the only core tool.
	got := bag.SchemasForLLM()
	coreCount := 0
	for _, x := range got {
		if _, ok := x.(redactedTool); !ok {
			coreCount++
		}
	}
	if coreCount != 1 {
		t.Errorf("expected 1 core tool, got %d", coreCount)
	}
}

// TestToolBag_AppendBucketsByTier confirms post-construction Append
// honours the tier classification.
func TestToolBag_AppendBucketsByTier(t *testing.T) {
	// Above threshold to trigger partition.
	in := make([]basetool.BaseTool, 0, 32)
	for i := 0; i < 32; i++ {
		in = append(in, newStub("pack_"+string(rune('a'+i)), ""))
	}
	bag := NewToolBag(in, 30)
	if !bag.IsDeferring() {
		t.Fatalf("expected deferral ON")
	}
	beforeDeferred := len(bag.DeferredTools())

	bag.Append(newStub("host_find_large_files", "")) // specialty
	bag.Append(newStub("query_promql", ""))          // core

	if got := len(bag.DeferredTools()); got != beforeDeferred+1 {
		t.Errorf("expected +1 deferred, got %d (was %d)", got, beforeDeferred)
	}
	// query_promql should now appear unredacted in SchemasForLLM.
	llm := bag.SchemasForLLM()
	for _, x := range llm {
		info, _ := x.Info(context.Background())
		if info != nil && info.Name == "query_promql" {
			if _, isRedacted := x.(redactedTool); isRedacted {
				t.Errorf("query_promql should be core (full schema)")
			}
		}
	}
}

func TestCoreToolNames_UsesRegistrationTier(t *testing.T) {
	in := []basetool.BaseTool{
		newStub("query_devices", ""),
		newStub("query_traceql", ""),
		newStub("query_knowledge", ""),
		newStub("read_source", ""),
		newStub("host_find_large_files", ""),
		newStub("query_devices", "duplicate"),
	}
	got := CoreToolNames(in)
	for _, want := range []string{"query_devices", "query_traceql", "query_knowledge", "read_source"} {
		if !containsNameString(got, want) {
			t.Errorf("CoreToolNames missing %q: %v", want, got)
		}
	}
	if containsNameString(got, "host_find_large_files") {
		t.Errorf("CoreToolNames should not include specialty tool: %v", got)
	}
	if countNameString(got, "query_devices") != 1 {
		t.Errorf("CoreToolNames should de-duplicate query_devices, got %v", got)
	}
}

// TestToolBag_NilSafety guards against panics on a nil receiver — used
// by the runtime when the bag isn't wired in test harnesses.
func TestToolBag_NilSafety(t *testing.T) {
	var bag *ToolBag
	if got := bag.SchemasForLLM(); got != nil {
		t.Errorf("nil bag SchemasForLLM should return nil, got %v", got)
	}
	if got := bag.AllTools(); got != nil {
		t.Errorf("nil bag AllTools should return nil, got %v", got)
	}
	if got := bag.DeferredTools(); got != nil {
		t.Errorf("nil bag DeferredTools should return nil, got %v", got)
	}
	if bag.IsDeferring() {
		t.Errorf("nil bag should not be deferring")
	}
	if bag.WithExtra(newStub("x", "")) != nil {
		t.Errorf("nil bag WithExtra should return nil, got non-nil")
	}
}

// containsToolName checks whether any tool in the slice has the given
// Name. Helper to keep the assertions readable.
func containsToolName(t *testing.T, tools []basetool.BaseTool, name string) bool {
	t.Helper()
	for _, x := range tools {
		info, err := x.Info(context.Background())
		if err != nil || info == nil {
			continue
		}
		if info.Name == name {
			return true
		}
	}
	return false
}

func containsNameString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countNameString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}
