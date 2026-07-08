package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// stubBagProvider is a hand-rolled DeferredToolBagProvider used only by
// the ToolSearch test. We deliberately avoid using *ToolBag here so the
// test exercises the public interface, not the concrete struct.
type stubBagProvider struct {
	all      []basetool.BaseTool
	deferred []basetool.BaseTool
}

func (s *stubBagProvider) AllTools() []basetool.BaseTool      { return s.all }
func (s *stubBagProvider) DeferredTools() []basetool.BaseTool { return s.deferred }

// decodeToolSearchResp unwraps the JSON envelope ToolSearch returns.
func decodeToolSearchResp(t *testing.T, raw string) toolSearchResponse {
	t.Helper()
	var resp toolSearchResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode response: %v (raw=%s)", err, raw)
	}
	return resp
}

// TestToolSearch_SelectExactName confirms `select:foo` returns foo's
// full schema and nothing else.
func TestToolSearch_SelectExactName(t *testing.T) {
	a := newStub("host_find_large_files", "list large files on host")
	b := newStub("host_du_summary", "summarise disk usage")
	c := newStub("host_stat_file", "stat a single file")
	bag := &stubBagProvider{all: []basetool.BaseTool{a, b, c}, deferred: []basetool.BaseTool{a, b, c}}
	ts := NewToolSearchTool(bag, nil)

	out, err := ts.InvokableRun(context.Background(), `{"query":"select:host_du_summary"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	if len(resp.Tools) != 1 {
		t.Fatalf("got %d tools, want 1: %s", len(resp.Tools), out)
	}
	if resp.Tools[0].Name != "host_du_summary" {
		t.Errorf("tool name = %s, want host_du_summary", resp.Tools[0].Name)
	}
	if !strings.Contains(string(resp.Tools[0].Parameters), "properties") {
		t.Errorf("expected full parameters block, got: %s", resp.Tools[0].Parameters)
	}
}

// TestToolSearch_SelectMultiple tests the CSV form select:a,b.
func TestToolSearch_SelectMultiple(t *testing.T) {
	a := newStub("host_find_large_files", "")
	b := newStub("host_du_summary", "")
	c := newStub("host_stat_file", "")
	bag := &stubBagProvider{all: []basetool.BaseTool{a, b, c}}
	ts := NewToolSearchTool(bag, nil)

	out, err := ts.InvokableRun(context.Background(), `{"query":"select:host_find_large_files,host_stat_file"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	if len(resp.Tools) != 2 {
		t.Fatalf("got %d tools, want 2: %s", len(resp.Tools), out)
	}
	got := []string{resp.Tools[0].Name, resp.Tools[1].Name}
	wantSet := map[string]bool{"host_find_large_files": true, "host_stat_file": true}
	for _, n := range got {
		if !wantSet[n] {
			t.Errorf("unexpected tool %s in result", n)
		}
	}
}

// TestToolSearch_KeywordMatch tests substring keyword matching across
// name + description + when_to_use.
func TestToolSearch_KeywordMatch(t *testing.T) {
	a := newStub("host_find_large_files", "find large disk consumers on a host")
	b := newStub("query_promql", "evaluate a PromQL expression")
	c := newStub("host_stat_file", "stat a single filesystem entry")
	bag := &stubBagProvider{all: []basetool.BaseTool{a, b, c}}
	ts := NewToolSearchTool(bag, nil)

	out, err := ts.InvokableRun(context.Background(), `{"query":"file"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	// Both host_find_large_files and host_stat_file mention "file" in name; query_promql doesn't.
	names := make(map[string]bool, len(resp.Tools))
	for _, x := range resp.Tools {
		names[x.Name] = true
	}
	if !names["host_find_large_files"] {
		t.Errorf("expected host_find_large_files in matches: %v", resp.Tools)
	}
	if !names["host_stat_file"] {
		t.Errorf("expected host_stat_file in matches: %v", resp.Tools)
	}
	if names["query_promql"] {
		t.Errorf("query_promql should NOT match 'file' query")
	}
}

func TestToolSearch_KeywordMatchUsesWhenToUse(t *testing.T) {
	trace := &stubTool{
		name:        "query_traceql",
		description: "query Tempo spans",
		whenToUse:   "When the user provides specific trace IDs or asks for span chains.",
		class:       "read",
		params:      `{"type":"object","properties":{"query":{"type":"string"}}}`,
	}
	bag := &stubBagProvider{all: []basetool.BaseTool{
		newStub("query_knowledge", "search runbooks"),
		trace,
	}}
	ts := NewToolSearchTool(bag, nil)

	out, err := ts.InvokableRun(context.Background(), `{"query":"trace ids"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "query_traceql" {
		t.Fatalf("trace-id capability should resolve to query_traceql, got %+v", resp.Tools)
	}
}

// TestToolSearch_NoMatchReturnsEmpty confirms the empty case is a
// well-formed empty array, not an error.
func TestToolSearch_NoMatchReturnsEmpty(t *testing.T) {
	bag := &stubBagProvider{all: []basetool.BaseTool{newStub("query_promql", "PromQL")}}
	ts := NewToolSearchTool(bag, nil)
	out, err := ts.InvokableRun(context.Background(), `{"query":"nonexistent_planet_finder"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	if resp.Tools == nil || len(resp.Tools) != 0 {
		t.Errorf("expected empty tools array, got %v", resp.Tools)
	}
}

// TestToolSearch_SelectUnknownReturnsEmpty confirms select:DOES_NOT_EXIST
// produces an empty result, not an error.
func TestToolSearch_SelectUnknownReturnsEmpty(t *testing.T) {
	bag := &stubBagProvider{all: []basetool.BaseTool{newStub("query_promql", "")}}
	ts := NewToolSearchTool(bag, nil)
	out, err := ts.InvokableRun(context.Background(), `{"query":"select:DOES_NOT_EXIST"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	if len(resp.Tools) != 0 {
		t.Errorf("expected empty result for unknown name, got %v", resp.Tools)
	}
}

// TestToolSearch_MaxResultsClamp tests the [1, 20] clamp on max_results.
func TestToolSearch_MaxResultsClamp(t *testing.T) {
	all := make([]basetool.BaseTool, 0, 30)
	for i := 0; i < 30; i++ {
		all = append(all, newStub("file_tool_"+string(rune('a'+i)), "file related"))
	}
	bag := &stubBagProvider{all: all}
	ts := NewToolSearchTool(bag, nil)

	cases := []struct {
		name       string
		args       string
		wantAtMost int
	}{
		{"default-cap", `{"query":"file"}`, 5},                     // default 5
		{"explicit-2", `{"query":"file","max_results":2}`, 2},      // explicit
		{"clamp-high", `{"query":"file","max_results":99}`, 20},    // clamped to 20
		{"clamp-zero", `{"query":"file","max_results":0}`, 5},      // 0 → default 5
		{"clamp-negative", `{"query":"file","max_results":-3}`, 5}, // negative → default 5
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ts.InvokableRun(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("InvokableRun: %v", err)
			}
			resp := decodeToolSearchResp(t, out)
			if len(resp.Tools) > tc.wantAtMost {
				t.Errorf("got %d tools, want at most %d", len(resp.Tools), tc.wantAtMost)
			}
		})
	}
}

// TestToolSearch_EmptyQueryRejected confirms an empty query string is
// rejected with an error (clear signal to the LLM that it forgot the
// argument).
func TestToolSearch_EmptyQueryRejected(t *testing.T) {
	bag := &stubBagProvider{all: []basetool.BaseTool{newStub("a", "")}}
	ts := NewToolSearchTool(bag, nil)
	_, err := ts.InvokableRun(context.Background(), `{"query":""}`)
	if err == nil {
		t.Errorf("expected error for empty query, got nil")
	}
}

// TestToolSearch_InfoReadClass confirms the metadata advertises Class=read
// (the tool is pure-read; no decorator should ever flag it as write).
func TestToolSearch_InfoReadClass(t *testing.T) {
	bag := &stubBagProvider{}
	ts := NewToolSearchTool(bag, nil)
	info, err := ts.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolSearchToolName {
		t.Errorf("Name = %s, want %s", info.Name, ToolSearchToolName)
	}
	if info.Class != "read" {
		t.Errorf("Class = %s, want read", info.Class)
	}
	// The schema must declare a `query` parameter (string) — we check
	// minimally so prompt-engineering tweaks to the description don't
	// break the test.
	var schema map[string]any
	if err := json.Unmarshal(info.Parameters, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Errorf("schema missing 'query' property: %v", props)
	}
}

// TestToolSearch_KeywordMultiTokenAllMustMatch confirms multi-token
// queries require ALL tokens to match (so "find files" filters more
// strictly than just "find").
func TestToolSearch_KeywordMultiTokenAllMustMatch(t *testing.T) {
	a := newStub("host_find_large_files", "find large files on a host")
	b := newStub("find_outlier_edges", "find ranking outliers among edges")
	bag := &stubBagProvider{all: []basetool.BaseTool{a, b}}
	ts := NewToolSearchTool(bag, nil)

	out, err := ts.InvokableRun(context.Background(), `{"query":"find files"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	resp := decodeToolSearchResp(t, out)
	names := make(map[string]bool, len(resp.Tools))
	for _, x := range resp.Tools {
		names[x.Name] = true
	}
	if !names["host_find_large_files"] {
		t.Errorf("expected host_find_large_files (matches both tokens)")
	}
	if names["find_outlier_edges"] {
		t.Errorf("find_outlier_edges should not match (no 'files' token)")
	}
}
