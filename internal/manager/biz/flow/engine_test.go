package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
)

// --- fakes ---------------------------------------------------------------

type fakeRunRepo struct {
	mu    sync.Mutex
	nodes []*model.FlowRunNode
}

func (f *fakeRunRepo) CreateRun(context.Context, *model.FlowRun) error { return nil }
func (f *fakeRunRepo) UpdateRun(context.Context, *model.FlowRun) error { return nil }
func (f *fakeRunRepo) GetRun(context.Context, string) (*model.FlowRun, error) {
	return nil, nil
}
func (f *fakeRunRepo) ListRuns(context.Context, uint64, int) ([]*model.FlowRun, error) {
	return nil, nil
}
func (f *fakeRunRepo) CreateNode(_ context.Context, n *model.FlowRunNode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes = append(f.nodes, n)
	return nil
}
func (f *fakeRunRepo) UpdateNode(context.Context, *model.FlowRunNode) error { return nil }
func (f *fakeRunRepo) ListNodes(context.Context, string) ([]*model.FlowRunNode, error) {
	return nil, nil
}
func (f *fakeRunRepo) SweepStaleRunning(context.Context, string) (int64, error) { return 0, nil }

type fakeAgent struct{ answer string }

func (f fakeAgent) RunAgent(_ context.Context, _, prompt string) (string, error) {
	if f.answer != "" {
		return f.answer, nil
	}
	return "echo: " + prompt, nil
}

type fakeTools struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (f *fakeTools) InvokeTool(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	if f.fail[name] {
		return nil, fmt.Errorf("boom")
	}
	return json.RawMessage(`{"ok":true,"echo":` + string(args) + `}`), nil
}

type fakeNotify struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeNotify) Notify(_ context.Context, _ []uint64, _, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, message)
	return nil
}

func mustGraph(t *testing.T, raw string) *Graph {
	t.Helper()
	g, err := ParseGraph(raw)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	return g
}

// --- graph validation ----------------------------------------------------

func TestGraphValidateRejectsCycle(t *testing.T) {
	_, err := ParseGraph(`{
		"nodes":[{"id":"t","type":"trigger.manual"},{"id":"a","type":"set","config":{"name":"x","value":"1"}},{"id":"b","type":"set","config":{"name":"y","value":"2"}}],
		"edges":[{"id":"e1","source":"t","target":"a"},{"id":"e2","source":"a","target":"b"},{"id":"e3","source":"b","target":"a"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestGraphValidateRejectsBadPort(t *testing.T) {
	_, err := ParseGraph(`{
		"nodes":[{"id":"t","type":"trigger.manual"},{"id":"a","type":"set"}],
		"edges":[{"id":"e1","source":"t","sourcePort":"true","target":"a"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("want port error, got %v", err)
	}
}

func TestGraphValidateRejectsEdgeIntoTrigger(t *testing.T) {
	_, err := ParseGraph(`{
		"nodes":[{"id":"t","type":"trigger.manual"},{"id":"a","type":"set"}],
		"edges":[{"id":"e1","source":"a","target":"t"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "trigger") {
		t.Fatalf("want trigger error, got %v", err)
	}
}

// --- expressions ---------------------------------------------------------

func TestResolveStringNativeAndMixed(t *testing.T) {
	rc := &RunContext{
		Trigger: map[string]any{"severity": "critical", "count": float64(3)},
		Nodes:   map[string]any{"diag": map[string]any{"structured": map[string]any{"root_cause": "oom"}}},
		Vars:    map[string]any{},
	}
	// whole-string template → native type
	v, err := rc.ResolveString("{{trigger.count}}")
	if err != nil || v != float64(3) {
		t.Fatalf("native resolve = %v, %v", v, err)
	}
	// mixed template → string
	v, err = rc.ResolveString("cause={{nodes.diag.output.structured.root_cause}} sev={{trigger.severity}}")
	if err != nil || v != "cause=oom sev=critical" {
		t.Fatalf("mixed resolve = %v, %v", v, err)
	}
	// missing node → error
	if _, err = rc.ResolveString("{{nodes.nope.output.x}}"); err == nil {
		t.Fatal("want error for missing node")
	}
}

func TestEvalCondition(t *testing.T) {
	rc := &RunContext{
		Trigger: map[string]any{"severity": "critical", "n": float64(5)},
		Nodes:   map[string]any{},
		Vars:    map[string]any{},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`{{trigger.severity}} == "critical"`, true},
		{`{{trigger.severity}} != "critical"`, false},
		{`{{trigger.n}} >= 5`, true},
		{`{{trigger.n}} < 5`, false},
		{`{{trigger.severity}} contains "crit"`, true},
		{`{{trigger.severity}}`, true},
	}
	for _, c := range cases {
		got, err := rc.EvalCondition(c.expr)
		if err != nil || got != c.want {
			t.Errorf("EvalCondition(%q) = %v, %v; want %v", c.expr, got, err, c.want)
		}
	}
}

// --- engine --------------------------------------------------------------

const branchGraph = `{
	"nodes":[
		{"id":"t","type":"trigger.manual"},
		{"id":"diag","type":"agent","config":{"persona":"default","instruction":"diagnose {{trigger.host}}","output_schema":{"type":"object"}}},
		{"id":"cond","type":"condition","config":{"expr":"{{nodes.diag.output.structured.severity}} == \"critical\""}},
		{"id":"fix","type":"tool","config":{"tool":"restart_service","args":{"service":"{{nodes.diag.output.structured.service}}"}}},
		{"id":"notify_ok","type":"notify","config":{"channel_ids":[1],"title":"done","message":"fixed {{nodes.diag.output.structured.service}}"}},
		{"id":"notify_low","type":"notify","config":{"channel_ids":[1],"title":"info","message":"low severity"}}
	],
	"edges":[
		{"id":"e1","source":"t","target":"diag"},
		{"id":"e2","source":"diag","target":"cond"},
		{"id":"e3","source":"cond","sourcePort":"true","target":"fix"},
		{"id":"e4","source":"cond","sourcePort":"false","target":"notify_low"},
		{"id":"e5","source":"fix","target":"notify_ok"}
	]
}`

func TestEngineBranchTrue(t *testing.T) {
	tools := &fakeTools{}
	notif := &fakeNotify{}
	eng := NewEngine(Executors{
		Agent:  fakeAgent{answer: `{"severity":"critical","service":"nginx"}`},
		Tools:  tools,
		Notify: notif,
	}, &fakeRunRepo{}, nil)

	run := &model.FlowRun{ID: "r1", TriggerJSON: `{"host":"vm-1"}`}
	status, err := eng.Execute(context.Background(), run, mustGraph(t, branchGraph))
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("Execute = %s, %v", status, err)
	}
	if len(tools.calls) != 1 || tools.calls[0] != "restart_service" {
		t.Fatalf("tool calls = %v, want [restart_service]", tools.calls)
	}
	if len(notif.msgs) != 1 || notif.msgs[0] != "fixed nginx" {
		t.Fatalf("notify msgs = %v", notif.msgs)
	}
}

func TestEngineBranchFalse(t *testing.T) {
	tools := &fakeTools{}
	notif := &fakeNotify{}
	eng := NewEngine(Executors{
		Agent:  fakeAgent{answer: `{"severity":"low","service":"nginx"}`},
		Tools:  tools,
		Notify: notif,
	}, &fakeRunRepo{}, nil)

	run := &model.FlowRun{ID: "r2", TriggerJSON: `{"host":"vm-1"}`}
	status, err := eng.Execute(context.Background(), run, mustGraph(t, branchGraph))
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("Execute = %s, %v", status, err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("false branch must not call tools, got %v", tools.calls)
	}
	if len(notif.msgs) != 1 || notif.msgs[0] != "low severity" {
		t.Fatalf("notify msgs = %v", notif.msgs)
	}
}

func TestEngineUnhandledErrorFailsRun(t *testing.T) {
	tools := &fakeTools{fail: map[string]bool{"bash": true}}
	eng := NewEngine(Executors{Tools: tools}, &fakeRunRepo{}, nil)
	g := mustGraph(t, `{
		"nodes":[{"id":"t","type":"trigger.manual"},{"id":"x","type":"tool","config":{"tool":"bash","args":{}}}],
		"edges":[{"id":"e1","source":"t","target":"x"}]
	}`)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "r3", TriggerJSON: "{}"}, g)
	if status != model.RunStatusFailed || err == nil {
		t.Fatalf("want failed run, got %s, %v", status, err)
	}
}

func TestEngineErrorPortHandlesFailure(t *testing.T) {
	tools := &fakeTools{fail: map[string]bool{"bash": true}}
	notif := &fakeNotify{}
	eng := NewEngine(Executors{Tools: tools, Notify: notif}, &fakeRunRepo{}, nil)
	g := mustGraph(t, `{
		"nodes":[
			{"id":"t","type":"trigger.manual"},
			{"id":"x","type":"tool","config":{"tool":"bash","args":{}}},
			{"id":"alarm","type":"notify","config":{"channel_ids":[1],"title":"err","message":"node failed: {{nodes.x.output.error}}"}}
		],
		"edges":[
			{"id":"e1","source":"t","target":"x"},
			{"id":"e2","source":"x","sourcePort":"error","target":"alarm"}
		]
	}`)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "r4", TriggerJSON: "{}"}, g)
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("handled error should succeed run, got %s, %v", status, err)
	}
	if len(notif.msgs) != 1 || !strings.Contains(notif.msgs[0], "boom") {
		t.Fatalf("error handler msgs = %v", notif.msgs)
	}
}

func TestEngineFanOutRunsBothBranches(t *testing.T) {
	tools := &fakeTools{}
	eng := NewEngine(Executors{Tools: tools}, &fakeRunRepo{}, nil)
	g := mustGraph(t, `{
		"nodes":[
			{"id":"t","type":"trigger.manual"},
			{"id":"a","type":"tool","config":{"tool":"t_a","args":{}}},
			{"id":"b","type":"tool","config":{"tool":"t_b","args":{}}}
		],
		"edges":[
			{"id":"e1","source":"t","target":"a"},
			{"id":"e2","source":"t","target":"b"}
		]
	}`)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "r5", TriggerJSON: "{}"}, g)
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("Execute = %s, %v", status, err)
	}
	if len(tools.calls) != 2 {
		t.Fatalf("fan-out calls = %v, want both", tools.calls)
	}
}

func TestEngineExecuteOnceOnDiamond(t *testing.T) {
	// t → a, t → b, a → c, b → c: c must run exactly once (OR-join).
	tools := &fakeTools{}
	eng := NewEngine(Executors{Tools: tools}, &fakeRunRepo{}, nil)
	g := mustGraph(t, `{
		"nodes":[
			{"id":"t","type":"trigger.manual"},
			{"id":"a","type":"set","config":{"name":"a","value":"1"}},
			{"id":"b","type":"set","config":{"name":"b","value":"2"}},
			{"id":"c","type":"tool","config":{"tool":"join","args":{}}}
		],
		"edges":[
			{"id":"e1","source":"t","target":"a"},
			{"id":"e2","source":"t","target":"b"},
			{"id":"e3","source":"a","target":"c"},
			{"id":"e4","source":"b","target":"c"}
		]
	}`)
	status, err := eng.Execute(context.Background(), &model.FlowRun{ID: "r6", TriggerJSON: "{}"}, g)
	if err != nil || status != model.RunStatusSucceeded {
		t.Fatalf("Execute = %s, %v", status, err)
	}
	if len(tools.calls) != 1 {
		t.Fatalf("diamond join ran %d times, want 1", len(tools.calls))
	}
}
