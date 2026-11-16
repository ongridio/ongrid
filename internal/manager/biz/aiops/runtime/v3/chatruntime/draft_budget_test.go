package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

type pendingDraftTool struct{ captureApplyTool }

func (t *pendingDraftTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "draft_config_change", Class: "read", Parameters: json.RawMessage(`{"type":"object"}`)}, nil
}

func TestRuntime_DraftBudgetPreservesConfirmation(t *testing.T) {
	for _, limit := range []int{2, 8} {
		t.Run(fmt.Sprintf("iterations=%d", limit), func(t *testing.T) {
			sess := &aiopsmodel.Session{ID: "draft-budget", UserID: 7}
			store := newMemSessions(sess)
			const hash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			const payload = `{"action":"create","draft_id":"pending-first","rule":{"name":"CPU high","kind":"metric_raw","scope_type":"host","spec":{"expr":"up == 0"}}}`
			draft := &pendingDraftTool{captureApplyTool: captureApplyTool{resp: `{"kind":"config_draft","domain":"alert_rule","action":"create","summary":"CPU high","scope":{"type":"host","label":"主机级"},"draft_hash":"` + hash + `","payload":` + payload + `}`}}
			apply := &captureApplyTool{resp: `{"kind":"config_apply_result","status":"applied","resource_id":95}`}
			scripted := newScriptedChatModel(
				&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "first", Type: "function", Function: schema.FunctionCall{Name: "draft_config_change", Arguments: `{}`}}}},
				&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "second", Type: "function", Function: schema.FunctionCall{Name: "draft_config_change", Arguments: `{"rule":{"name":"second"}}`}}}},
			)
			rt, err := NewRuntime(Config{Sessions: store, ChatModel: scripted, ToolBag: []basetool.BaseTool{draft, apply}, GraphCfg: graph.Config{MaxIterations: limit}})
			if err != nil {
				t.Fatal(err)
			}
			var events []Event
			reply, err := rt.Handle(context.Background(), &Request{SessionID: sess.ID, UserID: sess.UserID, Role: "admin", UserText: "创建几个主机告警规则", Emit: func(ev Event) { events = append(events, ev) }})
			if err != nil || reply == nil || reply.Message == nil || reply.Message.Content == nil {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
			content := *reply.Message.Content
			for _, want := range []string{"CPU high", "主机级", "尚未应用", "确认", "后续消息"} {
				if !strings.Contains(content, want) {
					t.Fatalf("reply missing %q: %s", want, content)
				}
			}
			if strings.Contains(content, "安全上限") || strings.Contains(content, "draft_hash") || draft.calls.Load() != 1 || apply.calls.Load() != 0 {
				t.Fatalf("reply=%s draft=%d apply=%d", content, draft.calls.Load(), apply.calls.Load())
			}
			last := store.messages[len(store.messages)-1]
			if last.Content == nil || *last.Content != content {
				t.Fatalf("persisted reply differs: %+v", last)
			}
			sawReply := false
			for _, ev := range events {
				if ev.Type == EventAssistant && ev.Assistant != nil && ev.Assistant.Content == content {
					sawReply = true
				}
			}
			if !sawReply {
				t.Fatal("pending draft reply was not emitted")
			}
			modelCalls := scripted.calls.Load()
			_, err = rt.Handle(context.Background(), &Request{SessionID: sess.ID, UserID: sess.UserID, Role: "admin", UserText: "ok"})
			if err != nil || apply.calls.Load() != 1 || scripted.calls.Load() != modelCalls {
				t.Fatalf("confirmation: err=%v apply=%d model=%d", err, apply.calls.Load(), scripted.calls.Load())
			}
			var args struct {
				DraftHash string          `json:"draft_hash"`
				Payload   json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal([]byte(apply.args.Load().(string)), &args); err != nil {
				t.Fatal(err)
			}
			if args.DraftHash != hash || string(args.Payload) != payload {
				t.Fatalf("confirmation did not retain the first draft: %+v", args)
			}
		})
	}
}
