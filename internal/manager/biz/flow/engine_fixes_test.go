package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
)

// TestEngineConcurrentSetVarsNoRace fans a trigger out to many `set` nodes
// that run concurrently and each write a distinct variable, then a tool
// node downstream reads it back. Before the fix, execSet mutated the shared
// rc.Vars OUTSIDE the engine lock — `go test -race` flagged the concurrent
// map write (and prod could panic with "concurrent map writes"). Now var
// writes are applied by the engine under its lock, so this is race-free.
func TestEngineConcurrentSetVarsNoRace(t *testing.T) {
	const n = 12
	var nodes, edges strings.Builder
	nodes.WriteString(`{"id":"t","type":"trigger.manual"}`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&nodes, `,{"id":"s%d","type":"set","config":{"name":"k%d","value":"v%d"}}`, i, i, i)
		fmt.Fprintf(&nodes, `,{"id":"r%d","type":"tool","config":{"tool":"echo","args":{"got":"{{vars.k%d}}"}}}`, i, i)
		fmt.Fprintf(&edges, `{"id":"te%d","source":"t","target":"s%d"},`, i, i)
		fmt.Fprintf(&edges, `{"id":"se%d","source":"s%d","target":"r%d"}`, i, i, i)
		if i < n-1 {
			edges.WriteString(",")
		}
	}
	g := mustGraph(t, `{"nodes":[`+nodes.String()+`],"edges":[`+edges.String()+`]}`)

	tools := &fakeTools{}
	eng := NewEngine(Executors{Tools: tools}, &fakeRunRepo{}, nil)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "race", TriggerJSON: "{}"}, g, "")
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("Execute = %s, %v", status, err)
	}
	if len(tools.calls) != n {
		t.Fatalf("tool calls = %d, want %d", len(tools.calls), n)
	}
}

// TestEngineConditionReadsContextNoRace runs a condition while sibling `set`
// branches write vars. The trigger value itself contains a template, so the
// condition resolves {{vars.k}} again during evaluation. Before the fix the
// executor got the shared RunContext and that read raced the engine's
// locked writes (`go test -race`; "concurrent map read and map write" in
// prod, which recover cannot catch).
func TestEngineConditionReadsContextNoRace(t *testing.T) {
	const n = 12
	var nodes, edges strings.Builder
	nodes.WriteString(`{"id":"t","type":"trigger.manual"},` +
		`{"id":"s","type":"set","config":{"name":"k","value":"v"}},` +
		`{"id":"c","type":"condition","config":{"expr":"{{trigger.msg}} == \"v\""}}`)
	edges.WriteString(`{"id":"ts","source":"t","target":"s"},{"id":"sc","source":"s","target":"c"}`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&nodes, `,{"id":"s%d","type":"set","config":{"name":"k%d","value":"v%d"}}`, i, i, i)
		fmt.Fprintf(&edges, `,{"id":"te%d","source":"t","target":"s%d"}`, i, i)
	}
	g := mustGraph(t, `{"nodes":[`+nodes.String()+`],"edges":[`+edges.String()+`]}`)

	eng := NewEngine(Executors{}, &fakeRunRepo{}, nil)
	for i := 0; i < 20; i++ {
		run := &model.FlowRun{ID: "cond-race", TriggerJSON: `{"msg":"{{vars.k}}"}`}
		status, err := eng.Execute(context.Background(), run, g, "")
		if err != nil || status != model.RunStatusSucceeded {
			t.Fatalf("Execute = %s, %v", status, err)
		}
	}
}

// TestEngineFanOutNodePanicRecovered proves a panic inside a fan-out node's
// executor is recovered and fails the run, rather than crashing the whole
// manager process. The Execute-level recover only guards the main
// goroutine; this needs the per-goroutine recover in activate().
func TestEngineFanOutNodePanicRecovered(t *testing.T) {
	RegisterNode(&NodeSpec{
		Type:     "test_panic",
		Kind:     KindData,
		Category: "data",
		Execute: func(_ context.Context, _ Executors, _ map[string]any, _ *RunContext) (NodeResult, error) {
			panic("boom in node")
		},
	})
	defer delete(nodeRegistry, "test_panic")

	g := mustGraph(t, `{
		"nodes":[{"id":"t","type":"trigger.manual"},{"id":"p","type":"test_panic"}],
		"edges":[{"id":"e1","source":"t","target":"p"}]
	}`)
	eng := NewEngine(Executors{}, &fakeRunRepo{}, nil)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "panic", TriggerJSON: "{}"}, g, "")
	if status != model.RunStatusFailed || err == nil {
		t.Fatalf("want failed run, got %s, %v", status, err)
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error should mention panic, got %v", err)
	}
}

// TestEngineEntryTypeScoping verifies entryType only starts matching trigger
// nodes: an alert-scoped run must not fire the manual branch and vice versa.
func TestEngineEntryTypeScoping(t *testing.T) {
	g := mustGraph(t, `{
		"nodes":[
			{"id":"tm","type":"trigger.manual"},
			{"id":"ta","type":"trigger.alert_fired"},
			{"id":"man","type":"tool","config":{"tool":"manual_tool","args":{}}},
			{"id":"alr","type":"tool","config":{"tool":"alert_tool","args":{}}}
		],
		"edges":[
			{"id":"e1","source":"tm","target":"man"},
			{"id":"e2","source":"ta","target":"alr"}
		]
	}`)
	tools := &fakeTools{}
	eng := NewEngine(Executors{Tools: tools}, &fakeRunRepo{}, nil)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "ent", TriggerJSON: "{}"}, g, NodeTriggerAlert)
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("Execute = %s, %v", status, err)
	}
	if len(tools.calls) != 1 || tools.calls[0] != "alert_tool" {
		t.Fatalf("alert-scoped run fired %v, want [alert_tool] only", tools.calls)
	}
}

// TestTestNodeBlocksSideEffects proves the test-run path refuses node types
// that cause real external side effects (a notify node delivers a real
// message), so the editor's "test this node" can't surprise the user.
func TestTestNodeBlocksSideEffects(t *testing.T) {
	uc := NewUsecase(nil, &fakeRunRepo{}, NewEngine(Executors{}, &fakeRunRepo{}, nil), nil)
	_, err := uc.TestNode(context.Background(), 0, NodeNotify,
		[]byte(`{"channel_ids":[1],"title":"hi","message":"hello"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "test-run") {
		t.Fatalf("notify test-run should be blocked, got %v", err)
	}
	// A read-class node (set) is still test-runnable.
	if _, err := uc.TestNode(context.Background(), 0, NodeSet,
		[]byte(`{"name":"x","value":"1"}`), nil); err != nil {
		t.Fatalf("set node test-run should succeed, got %v", err)
	}
}

// TestEvalConditionQuotedOperator guards the quote/brace-aware operator
// scan: an operator substring inside a quoted literal must NOT be mistaken
// for the comparison operator.
func TestEvalConditionQuotedOperator(t *testing.T) {
	rc := &RunContext{
		Trigger: map[string]any{"msg": "a==b", "tag": "has == inside"},
		Nodes:   map[string]any{},
		Vars:    map[string]any{},
	}
	cases := []struct {
		expr string
		want bool
	}{
		// real == operator; RHS literal itself contains ==
		{`{{trigger.msg}} == "a==b"`, true},
		{`{{trigger.msg}} == "x==y"`, false},
		// contains is the real op even though the RHS literal holds ==
		{`{{trigger.tag}} contains "=="`, true},
	}
	for _, c := range cases {
		got, err := rc.EvalCondition(c.expr)
		if err != nil || got != c.want {
			t.Errorf("EvalCondition(%q) = %v, %v; want %v", c.expr, got, err, c.want)
		}
	}
}
