package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// kbStubTool stands in for query_knowledge: it records the arguments it was
// handed and answers with a scripted result list.
type kbStubTool struct {
	name     string
	body     string
	argsJSON string
	calls    int
}

func (t *kbStubTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: t.name, Description: "fake", Class: "read"}, nil
}

func (t *kbStubTool) InvokableRun(_ context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	t.calls++
	t.argsJSON = argsJSON
	return t.body, nil
}

// kbResult renders the tool's wire shape for one hit at the given score.
func kbResult(score float64) string {
	return fmt.Sprintf(`{"items":[{"title":"DNS 排障","preview":"先看解析器","score":%v}]}`, score)
}

// TestPrologueKBLookup_SendsArgumentsTheToolDeclares — the prologue sent "top_k"
// and "min_score", neither of which is in the tool's schema, so json.Unmarshal
// dropped both and the call silently took the tool's own default count.
func TestPrologueKBLookup_SendsArgumentsTheToolDeclares(t *testing.T) {
	tool := &kbStubTool{name: "query_knowledge", body: kbResult(0.9)}

	_ = (&Runtime{}).prologueKBLookup(context.Background(), []basetool.BaseTool{tool}, "dns 解析失败")

	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tool.argsJSON), &args); err != nil {
		t.Fatalf("args %q: %v", tool.argsJSON, err)
	}
	if args["query"] != "dns 解析失败" {
		t.Errorf("query arg = %v", args["query"])
	}
	if args["max_results"] != float64(3) {
		t.Errorf("max_results arg = %v, want 3", args["max_results"])
	}
	for _, undeclared := range []string{"top_k", "min_score"} {
		if _, ok := args[undeclared]; ok {
			t.Errorf("arg %q is not in the tool schema and must not be sent", undeclared)
		}
	}
}

// TestPrologueKBLookup_InjectsOnlyAboveTheRelevanceBar — the 0.6 bar reads a
// similarity. The hybrid searcher used to overwrite every score with an RRF rank
// value of ~0.02, which made this return "" for every query and silently
// disabled the playbook injection.
func TestPrologueKBLookup_InjectsOnlyAboveTheRelevanceBar(t *testing.T) {
	for _, tc := range []struct {
		name  string
		score float64
		want  bool
	}{
		{name: "cosine above the bar", score: 0.82, want: true},
		{name: "cosine below the bar", score: 0.41, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &kbStubTool{name: "query_knowledge", body: kbResult(tc.score)}

			got := (&Runtime{}).prologueKBLookup(context.Background(), []basetool.BaseTool{tool}, "dns 解析失败")

			if tc.want && got == "" {
				t.Fatal("a relevant top hit must produce a KB block")
			}
			if !tc.want && got != "" {
				t.Fatalf("an irrelevant top hit must produce nothing, got %q", got)
			}
		})
	}
}

// TestPrologueKBLookup_IgnoresANonKnowledgeBag — the prologue must not fire
// unless the worker actually carries the tool.
func TestPrologueKBLookup_IgnoresANonKnowledgeBag(t *testing.T) {
	tool := &kbStubTool{name: "query_promql", body: kbResult(0.9)}

	got := (&Runtime{}).prologueKBLookup(context.Background(), []basetool.BaseTool{tool}, "dns 解析失败")

	if got != "" {
		t.Fatalf("got %q, want no KB block", got)
	}
	if tool.calls != 0 {
		t.Fatalf("unrelated tool was called %d time(s)", tool.calls)
	}
}
