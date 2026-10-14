package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestRuntimeRemovedOrReplacedToolRejectsStaleReference(t *testing.T) {
	rt, err := NewRuntime(Config{Sessions: newMemSessions(nil), ChatModel: newScriptedChatModel(), ToolBag: []basetool.BaseTool{&fakeTool{name: "mcp__test__search", schema: `{"type":"object"}`}}})
	if err != nil {
		t.Fatal(err)
	}
	old := rt.toolBagSnapshot()[0]
	if err := rt.ReplaceToolSource("mcp__test__", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := old.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatal("removed tool executed")
	}
	fresh := &fakeTool{name: "mcp__test__search", schema: `{"type":"object"}`}
	if err := rt.ReplaceToolSource("mcp__test__", []basetool.BaseTool{fresh}, []basetool.BaseTool{fresh}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatal("old generation became executable again")
	}
	if _, err := rt.toolBagSnapshot()[0].InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConcurrentToolRefreshAndSnapshot(t *testing.T) {
	rt, err := NewRuntime(Config{Sessions: newMemSessions(nil), ChatModel: newScriptedChatModel()})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tool := &fakeTool{name: "mcp__test__search", schema: `{"type":"object"}`}
				if err := rt.ReplaceToolSource("mcp__test__", []basetool.BaseTool{tool}, []basetool.BaseTool{tool}); err != nil {
					t.Error(err)
					return
				}
				for _, entry := range rt.toolBagSnapshot() {
					// 并发替换允许明确拒绝旧引用，不能数据竞争或执行已撤销版本。
					_, err := entry.InvokableRun(context.Background(), `{}`)
					if err != nil {
						continue
					}
				}
			}
		}()
	}
	wg.Wait()
}

type countingSearchTool struct{ calls atomic.Int32 }

func (t *countingSearchTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: basetool.ToolWebSearch, Description: "search", Class: "read", Parameters: []byte(`{"type":"object","properties":{"query":{"type":"string"}}}`)}, nil
}
func (t *countingSearchTool) InvokableRun(context.Context, string, ...basetool.InvokeOption) (string, error) {
	t.calls.Add(1)
	return `{"result":"found"}`, nil
}

func TestRuntimeWebSearchToggleControlsActualDispatch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			tool := &countingSearchTool{}
			modelStub := newScriptedChatModel(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "tc-search", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"test"}`}}}}, &schema.Message{Role: schema.Assistant, Content: "done"})
			rt, err := NewRuntime(Config{Sessions: newMemSessions(&model.Session{ID: "s1", UserID: 7}), ChatModel: modelStub, ToolBag: []basetool.BaseTool{tool}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rt.Handle(context.Background(), &Request{SessionID: "s1", UserID: 7, Role: "admin", UserText: "search", WebSearchEnabled: enabled}); err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if enabled {
				want = 1
			}
			if tool.calls.Load() != want {
				t.Fatalf("calls=%d want=%d", tool.calls.Load(), want)
			}
			// 即使持有旧 schema 或直接调用工具引用，也不能越过执行边界。
			if _, err := rt.toolBagSnapshot()[0].InvokableRun(context.Background(), `{}`); err == nil {
				t.Fatal("missing execution gate")
			}
		})
	}
}

func TestWorkerInheritsWebSearchGate(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("background=%t/enabled=%t", background, enabled), func(t *testing.T) {
				tool := &countingSearchTool{}
				scripted := newScriptedChatModel(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "worker-search", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"test"}`}}}}, &schema.Message{Role: schema.Assistant, Content: "done"})
				store, parent := newSeededRuntimeStore(t, 7)
				rt := newRuntimeWithStore(t, "network", scripted, []basetool.BaseTool{tool}, store)
				ctx := basetool.WithWebSearchAllowed(context.Background(), enabled)
				done := make(chan struct{}, 1)
				_, err := rt.SpawnWorker(ctx, SpawnRequest{AgentName: "network", ParentSession: parent, Prompt: "search", Background: background, ParentEmit: func(ev Event) {
					if ev.Type == EventTaskNotification {
						done <- struct{}{}
					}
				}})
				if err != nil {
					t.Fatal(err)
				}
				if background {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("worker did not finish")
					}
				}
				want := int32(0)
				if enabled {
					want = 1
				}
				if tool.calls.Load() != want {
					t.Fatalf("calls=%d want=%d", tool.calls.Load(), want)
				}
			})
		}
	}
}

func TestToolSearchKeepsTurnSchemaAcrossRefresh(t *testing.T) {
	old := &fakeTool{name: "mcp__test__search", schema: `{"type":"object","properties":{"old_param":{"type":"string"}}}`}
	bag := aiopstools.NewToolBag([]basetool.BaseTool{old}, 0)
	rt, err := NewRuntime(Config{Sessions: newMemSessions(nil), ChatModel: newScriptedChatModel(), ToolBag: bag.SchemasForLLM()})
	if err != nil {
		t.Fatal(err)
	}
	rt.SetToolBag(bag)
	before := rt.toolBagSnapshot()
	fresh := &fakeTool{name: old.name, schema: `{"type":"object","properties":{"new_param":{"type":"string"}}}`}
	if err := rt.ReplaceToolSource("mcp__test__", []basetool.BaseTool{fresh}, []basetool.BaseTool{fresh}); err != nil {
		t.Fatal(err)
	}
	search := aiopstools.NewToolSearchTool(bag, nil)
	for _, tc := range []struct {
		view []basetool.BaseTool
		want string
	}{{before, "old_param"}, {rt.toolBagSnapshot(), "new_param"}} {
		result, err := search.InvokableRun(basetool.WithFilteredTools(context.Background(), tc.view), `{"query":"select:mcp__test__search"}`)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, tc.want) {
			t.Fatalf("want %s, got %s", tc.want, result)
		}
	}
	if _, err := before[0].InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatal("old executable survived refresh")
	}
	// An empty child view must clear the inherited parent view.
	ctx := basetool.WithFilteredTools(context.Background(), rt.toolBagSnapshot())
	result, err := search.InvokableRun(basetool.WithFilteredTools(ctx, nil), `{"query":"select:mcp__test__search"}`)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tools) != 0 {
		t.Fatalf("empty view leaked parent schemas: %s", result)
	}
}

func TestInvalidToolSourceUpdatePreservesExistingViews(t *testing.T) {
	old := &fakeTool{name: "mcp__test__old", schema: `{"type":"object"}`}
	other := &fakeTool{name: "mcp__other__tool", schema: `{"type":"object"}`}
	for _, tc := range []struct {
		name          string
		raw, executed []basetool.BaseTool
	}{
		{"duplicate", []basetool.BaseTool{old, old}, []basetool.BaseTool{old, old}},
		{"outside source", []basetool.BaseTool{other}, []basetool.BaseTool{other}},
		{"missing execution", []basetool.BaseTool{old}, nil},
		{"missing schema", nil, []basetool.BaseTool{old}},
		{"nil entry", []basetool.BaseTool{nil}, []basetool.BaseTool{nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bag := aiopstools.NewToolBag([]basetool.BaseTool{old}, 30)
			rt, err := NewRuntime(Config{Sessions: newMemSessions(nil), ChatModel: newScriptedChatModel(), ToolBag: bag.AllTools()})
			if err != nil {
				t.Fatal(err)
			}
			rt.SetToolBag(bag)
			ref := rt.toolBagSnapshot()[0]
			if err := rt.ReplaceToolSource("mcp__test__", tc.raw, tc.executed); err == nil {
				t.Fatal("invalid update accepted")
			}
			if _, err := ref.InvokableRun(context.Background(), `{}`); err != nil {
				t.Fatalf("existing executable revoked: %v", err)
			}
			all := bag.AllTools()
			info, err := all[0].Info(context.Background())
			if err != nil || len(all) != 1 || info.Name != old.name {
				t.Fatal("catalog changed after rejected update")
			}
		})
	}
	if _, err := NewRuntime(Config{Sessions: newMemSessions(nil), ChatModel: newScriptedChatModel(), ToolBag: []basetool.BaseTool{old, old}}); err == nil {
		t.Fatal("startup accepted duplicate tools")
	}
}
