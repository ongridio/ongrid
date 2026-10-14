package chatruntime

import (
	"context"
	"fmt"
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
	rt.ReplaceToolSource("mcp__test__", nil, nil)
	if _, err := old.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatal("removed tool executed")
	}
	fresh := &fakeTool{name: "mcp__test__search", schema: `{"type":"object"}`}
	rt.ReplaceToolSource("mcp__test__", []basetool.BaseTool{fresh}, []basetool.BaseTool{fresh})
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
				rt.ReplaceToolSource("mcp__test__", []basetool.BaseTool{tool}, []basetool.BaseTool{tool})
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
